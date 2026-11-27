package deploy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apprunner"
	artypes "github.com/aws/aws-sdk-go-v2/service/apprunner/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/ngocp/goterm-control/internal/connectors"
)

// An app with a server on AWS: its image in ECR, run by App Runner.

var lookPath = exec.LookPath

// runnerRole is the role App Runner uses to pull from the account's ECR.
const runnerRole = "bomclaw-apprunner-ecr-access"

var (
	svcMu    sync.Mutex
	svcAt    = map[string]time.Time{}
	svcCache = map[string][]artypes.ServiceSummary{}
)

// services lists App Runner services in region, briefly cached.
func (a *AWS) services(ctx context.Context, region string, fresh bool) ([]artypes.ServiceSummary, error) {
	svcMu.Lock()
	if hit, ok := svcCache[region]; ok && !fresh && time.Since(svcAt[region]) < 30*time.Second {
		svcMu.Unlock()
		return hit, nil
	}
	svcMu.Unlock()
	cfg, err := a.regional(ctx, region)
	if err != nil {
		return nil, err
	}
	c := apprunner.NewFromConfig(cfg)
	var out []artypes.ServiceSummary
	var next *string
	for {
		page, err := c.ListServices(ctx, &apprunner.ListServicesInput{NextToken: next})
		if err != nil {
			return nil, fmt.Errorf("app runner: %w", err)
		}
		out = append(out, page.ServiceSummaryList...)
		if page.NextToken == nil {
			break
		}
		next = page.NextToken
	}
	svcMu.Lock()
	svcCache[region], svcAt[region] = out, time.Now()
	svcMu.Unlock()
	return out, nil
}

func (a *AWS) findService(ctx context.Context, region, name string, fresh bool) (*artypes.ServiceSummary, error) {
	all, err := a.services(ctx, region, fresh)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if aws.ToString(all[i].ServiceName) == name {
			return &all[i], nil
		}
	}
	return nil, nil
}

// runnerStatus fills st from the project's App Runner service.
func (a *AWS) runnerStatus(ctx context.Context, region, name string, st *ProdStatus) {
	s, err := a.findService(ctx, region, name, false)
	if err != nil {
		st.Error = err.Error()
		return
	}
	if s == nil {
		return
	}
	switch s.Status {
	case artypes.ServiceStatusCreateFailed, artypes.ServiceStatusDeleteFailed:
		st.Error = "App Runner: " + strings.ToLower(strings.ReplaceAll(string(s.Status), "_", " "))
		return
	case artypes.ServiceStatusDeleted:
		return
	}
	st.Deployed = true
	if s.Status == artypes.ServiceStatusPaused {
		st.Error = "App Runner: service paused"
	}
	st.WorkersDev = "https://" + aws.ToString(s.ServiceUrl)
	st.URL = st.WorkersDev
	if s.UpdatedAt != nil {
		st.DeployedAt = s.UpdatedAt.UTC().Format(time.RFC3339)
	}
	cfg, err := a.regional(ctx, region)
	if err != nil {
		return
	}
	c := apprunner.NewFromConfig(cfg)
	if d, err := c.DescribeService(ctx, &apprunner.DescribeServiceInput{ServiceArn: s.ServiceArn}); err == nil && d.Service != nil {
		if sc := d.Service.SourceConfiguration; sc != nil && sc.ImageRepository != nil {
			img := aws.ToString(sc.ImageRepository.ImageIdentifier)
			if i := strings.LastIndex(img, ":"); i >= 0 {
				tag := img[i+1:]
				st.Version = &ProdVersion{ID: tag, Commit: commitIn(tag), By: "bomclaw"}
			}
		}
	}
	if doms, err := c.DescribeCustomDomains(ctx, &apprunner.DescribeCustomDomainsInput{ServiceArn: s.ServiceArn}); err == nil {
		for _, d := range doms.CustomDomains {
			if d.Status == artypes.CustomDomainAssociationStatusActive {
				st.Domains = append(st.Domains, aws.ToString(d.DomainName))
			}
		}
	}
}

func (a *AWS) deployRunner(ctx context.Context, r Resolved, opt Options, log io.Writer) (*Result, error) {
	step := func(format string, x ...any) { fmt.Fprintf(log, "▸ "+format+"\n", x...) }
	res := &Result{DryRun: opt.DryRun}
	vars := projectConnectors(r, step)
	if r.Build != "" {
		step("build: %s", r.Build)
		if err := ExecRunner(ctx, r.WorkDir, connectors.BuildEnv(vars), log, "sh", "-c", r.Build); err != nil {
			return nil, fmt.Errorf("build failed: %w", err)
		}
	}
	if _, err := os.Stat(r.DockerPath); err != nil {
		return nil, fmt.Errorf("deploy.dockerfile: %w", err)
	}
	docker, err := lookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("docker is not installed — App Runner runs an image built here (brew install --cask docker)")
	}
	port := r.Port
	if port == 0 {
		port = 8080
	}
	region := a.region(r)
	tag := imageLabel(Git(ctx, r.ProjectDir))
	if tag == "" {
		tag = fmt.Sprintf("t%d", time.Now().Unix())
	}
	step("app runner: image from %s, port %d, region %s, tag %s", r.DockerPath, port, region, tag)
	if opt.DryRun {
		step("dry run: stopping before docker build")
		return res, nil
	}
	cfg, err := a.regional(ctx, region)
	if err != nil {
		return nil, err
	}

	// The image, in the account's ECR.
	ec := ecr.NewFromConfig(cfg)
	repo := "bomclaw/" + r.Name
	if _, err := ec.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: &repo}); err != nil {
		var exists *ecrtypes.RepositoryAlreadyExistsException
		if !errors.As(err, &exists) {
			return nil, fmt.Errorf("ecr repository: %w", err)
		}
	}
	auth, err := ec.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil || len(auth.AuthorizationData) == 0 {
		return nil, fmt.Errorf("ecr login: %v", err)
	}
	raw, _ := base64.StdEncoding.DecodeString(aws.ToString(auth.AuthorizationData[0].AuthorizationToken))
	user, pass, _ := strings.Cut(string(raw), ":")
	registry := strings.TrimPrefix(aws.ToString(auth.AuthorizationData[0].ProxyEndpoint), "https://")
	login := exec.CommandContext(ctx, docker, "login", "--username", user, "--password-stdin", registry)
	login.Stdin = strings.NewReader(pass)
	if out, err := login.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker login %s: %v %s", registry, err, strings.TrimSpace(string(out)))
	}
	image := registry + "/" + repo + ":" + tag
	step("docker build %s", image)
	// App Runner runs x86_64; an Apple Silicon build must say so.
	if err := ExecRunner(ctx, r.WorkDir, nil, log, docker, "build", "--platform", "linux/amd64", "-f", r.DockerPath, "-t", image, r.WorkDir); err != nil {
		return nil, fmt.Errorf("docker build failed: %w", err)
	}
	step("docker push %s", image)
	if err := ExecRunner(ctx, r.WorkDir, nil, log, docker, "push", image); err != nil {
		return nil, fmt.Errorf("docker push failed: %w", err)
	}

	roleARN, err := ensureRunnerRole(ctx, iam.NewFromConfig(cfg), step)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, v := range vars {
		env[v.Name] = v.Value
	}
	if len(env) > 0 {
		step("env: %d value(s) from the project's connectors → the service's environment", len(env))
	}
	src := &artypes.SourceConfiguration{
		AuthenticationConfiguration: &artypes.AuthenticationConfiguration{AccessRoleArn: &roleARN},
		AutoDeploymentsEnabled:      aws.Bool(false),
		ImageRepository: &artypes.ImageRepository{
			ImageIdentifier: &image, ImageRepositoryType: artypes.ImageRepositoryTypeEcr,
			ImageConfiguration: &artypes.ImageConfiguration{Port: aws.String(fmt.Sprint(port)), RuntimeEnvironmentVariables: env},
		},
	}
	c := apprunner.NewFromConfig(cfg)
	existing, err := a.findService(ctx, region, r.Name, true)
	if err != nil {
		return nil, err
	}
	var arn string
	if existing == nil {
		// A role created a moment ago is not usable at once (IAM is
		// eventually consistent): retry the create for a little while.
		var out *apprunner.CreateServiceOutput
		err = waitFor(ctx, 5*time.Second, 90*time.Second, func() (bool, error) {
			out, err = c.CreateService(ctx, &apprunner.CreateServiceInput{
				ServiceName: &r.Name, SourceConfiguration: src,
				InstanceConfiguration: &artypes.InstanceConfiguration{Cpu: aws.String("0.25 vCPU"), Memory: aws.String("0.5 GB")},
				Tags:                  []artypes.Tag{{Key: aws.String("bomclaw:project"), Value: &r.Name}},
			})
			if err != nil && strings.Contains(err.Error(), "role") {
				return false, nil
			}
			return err == nil, err
		})
		if err != nil {
			return nil, fmt.Errorf("create App Runner service: %w", err)
		}
		arn = aws.ToString(out.Service.ServiceArn)
		step("app runner: created %s", r.Name)
	} else {
		arn = aws.ToString(existing.ServiceArn)
		if _, err := c.UpdateService(ctx, &apprunner.UpdateServiceInput{ServiceArn: &arn, SourceConfiguration: src}); err != nil {
			return nil, fmt.Errorf("update App Runner service: %w", err)
		}
		step("app runner: rolling out %s", tag)
	}

	var svc *artypes.Service
	step("app runner: waiting for the rollout (several minutes)")
	err = waitFor(ctx, 10*time.Second, 15*time.Minute, func() (bool, error) {
		d, err := c.DescribeService(ctx, &apprunner.DescribeServiceInput{ServiceArn: &arn})
		if err != nil {
			return false, err
		}
		svc = d.Service
		switch svc.Status {
		case artypes.ServiceStatusRunning:
			return true, nil
		case artypes.ServiceStatusCreateFailed:
			return false, fmt.Errorf("App Runner could not start the service — check the image listens on port %d (deploy.port)", port)
		}
		return false, nil
	})
	if errors.Is(err, errTimeout) {
		step("app runner: still rolling out — status will show when it is live")
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if svc != nil {
		res.URL = "https://" + aws.ToString(svc.ServiceUrl)
	}
	if r.Domain != "" {
		if a.attachRunnerDomain(ctx, c, arn, r.Domain, step) {
			res.URL = "https://" + r.Domain
		}
	}
	svcMu.Lock()
	delete(svcCache, region)
	svcMu.Unlock()
	step("live: %s", res.URL)
	return res, nil
}

func ensureRunnerRole(ctx context.Context, c *iam.Client, step func(string, ...any)) (string, error) {
	got, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(runnerRole)})
	if err == nil {
		return aws.ToString(got.Role.Arn), nil
	}
	var missing *iamtypes.NoSuchEntityException
	if !errors.As(err, &missing) {
		return "", fmt.Errorf("iam: %w", err)
	}
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"build.apprunner.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	out, err := c.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(runnerRole), AssumeRolePolicyDocument: aws.String(trust),
		Description: aws.String("Lets App Runner pull bomclaw images from ECR")})
	if err != nil {
		return "", fmt.Errorf("create role %s: %w", runnerRole, err)
	}
	if _, err := c.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(runnerRole),
		PolicyArn: aws.String("arn:aws:iam::aws:policy/service-role/AWSAppRunnerServicePolicyForECRAccess")}); err != nil {
		return "", fmt.Errorf("attach ECR policy: %w", err)
	}
	step("iam: created role %s", runnerRole)
	return aws.ToString(out.Role.Arn), nil
}

// attachRunnerDomain associates the domain and writes App Runner's records.
func (a *AWS) attachRunnerDomain(ctx context.Context, c *apprunner.Client, arn, domain string, step func(string, ...any)) bool {
	if _, err := c.AssociateCustomDomain(ctx, &apprunner.AssociateCustomDomainInput{ServiceArn: &arn, DomainName: &domain,
		EnableWWWSubdomain: aws.Bool(false)}); err != nil && !strings.Contains(strings.ToLower(err.Error()), "already") {
		step("domain %s: %v", domain, err)
		return false
	}
	var recs []DNSRecord
	active := false
	_ = waitFor(ctx, 3*time.Second, 45*time.Second, func() (bool, error) {
		d, err := c.DescribeCustomDomains(ctx, &apprunner.DescribeCustomDomainsInput{ServiceArn: &arn})
		if err != nil {
			return false, err
		}
		recs = recs[:0]
		for _, cd := range d.CustomDomains {
			if aws.ToString(cd.DomainName) != domain {
				continue
			}
			active = cd.Status == artypes.CustomDomainAssociationStatusActive
			recs = append(recs, DNSRecord{Type: "CNAME", Name: domain, Content: aws.ToString(d.DNSTarget)})
			for _, v := range cd.CertificateValidationRecords {
				recs = append(recs, DNSRecord{Type: aws.ToString(v.Type), Name: aws.ToString(v.Name), Content: aws.ToString(v.Value)})
			}
		}
		return len(recs) > 1 || active, nil
	})
	pointDNS(ctx, recs, step)
	if !active {
		step("domain %s: associated — App Runner serves it once its certificate is validated (minutes to hours)", domain)
	}
	return active
}
