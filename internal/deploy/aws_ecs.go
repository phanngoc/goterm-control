package deploy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudfront"
	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	ecrtypes "github.com/aws/aws-sdk-go-v2/service/ecr/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"

	"github.com/ngocp/goterm-control/internal/connectors"
)

// An app with a server on AWS: its image in ECR, run on Fargate by an ECS
// Express Mode service. Express Mode is AWS's successor to App Runner (which
// takes no new customers): one call gives the service, an Application Load
// Balancer shared by the account's Express services, HTTPS on
// https://<id>.ecs.<region>.on.aws, health checks and auto scaling, in the
// region's default VPC. A custom domain is a CloudFront distribution in front,
// with the same ACM certificate and DNS steps as a static site.

var lookPath = exec.LookPath

const (
	// ecsCluster holds every bomclaw app.
	ecsCluster = "bomclaw"
	// ecsExecRole lets the tasks pull from ECR and write their logs.
	ecsExecRole = "bomclaw-ecs-task-execution"
	// ecsInfraRole lets ECS build the load balancer, security groups and
	// scaling for an Express service.
	ecsInfraRole = "bomclaw-ecs-infrastructure"
)

// Managed CloudFront policies for a dynamic origin.
const (
	cachingDisabled           = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
	allViewerExceptHostHeader = "b689b0a8-53d0-40ab-baf2-68738e2966ac"
)

func (a *AWS) serviceARN(ctx context.Context, region, name string) (string, error) {
	acct, err := a.accountID(ctx)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("arn:aws:ecs:%s:%s:service/%s/%s", region, acct, ecsCluster, name), nil
}

// appService is the project's ECS service, nil when there is none (or it was
// deleted).
func (a *AWS) appService(ctx context.Context, c *ecs.Client, name string) (*ecstypes.Service, error) {
	out, err := c.DescribeServices(ctx, &ecs.DescribeServicesInput{Cluster: aws.String(ecsCluster), Services: []string{name},
		Include: []ecstypes.ServiceField{ecstypes.ServiceFieldTags}})
	if err != nil {
		var nf *ecstypes.ClusterNotFoundException
		if errors.As(err, &nf) {
			return nil, nil
		}
		return nil, fmt.Errorf("ecs: %w", err)
	}
	for i := range out.Services {
		if aws.ToString(out.Services[i].Status) != "INACTIVE" {
			return &out.Services[i], nil
		}
	}
	return nil, nil
}

// endpoint is the Express service's public address.
func endpoint(x *ecstypes.ECSExpressGatewayService) string {
	if x == nil {
		return ""
	}
	for i := len(x.ActiveConfigurations) - 1; i >= 0; i-- {
		for _, p := range x.ActiveConfigurations[i].IngressPaths {
			if e := aws.ToString(p.Endpoint); e != "" && p.AccessType != ecstypes.AccessTypePrivate {
				if !strings.HasPrefix(e, "http") {
					e = "https://" + e
				}
				return strings.TrimRight(e, "/")
			}
		}
	}
	return ""
}

func expressImage(x *ecstypes.ECSExpressGatewayService) string {
	if x == nil {
		return ""
	}
	for i := len(x.ActiveConfigurations) - 1; i >= 0; i-- {
		if pc := x.ActiveConfigurations[i].PrimaryContainer; pc != nil {
			return aws.ToString(pc.Image)
		}
	}
	return ""
}

// appStatus fills st from the project's ECS Express service.
func (a *AWS) appStatus(ctx context.Context, region, name string, st *ProdStatus) {
	cfg, err := a.regional(ctx, region)
	if err != nil {
		st.Error = err.Error()
		return
	}
	c := ecs.NewFromConfig(cfg)
	svc, err := a.appService(ctx, c, name)
	if err != nil {
		st.Error = err.Error()
		return
	}
	if svc == nil {
		return
	}
	st.Deployed = true
	if aws.ToString(svc.Status) == "DRAINING" {
		st.Error = "ECS: the service is being deleted"
	}
	for _, d := range svc.Deployments {
		if aws.ToString(d.Status) == "PRIMARY" {
			if d.UpdatedAt != nil {
				st.DeployedAt = d.UpdatedAt.UTC().Format(time.RFC3339)
			}
			if d.RolloutState == ecstypes.DeploymentRolloutStateFailed {
				st.Error = "ECS: the last rollout failed — " + aws.ToString(d.RolloutStateReason)
			} else if d.RolloutState == ecstypes.DeploymentRolloutStateInProgress {
				st.Services = append(st.Services, "ECS: rolling out")
			}
		}
	}
	if svc.RunningCount == 0 && st.Error == "" {
		st.Services = append(st.Services, "ECS: 0 tasks running")
	}
	x, err := c.DescribeExpressGatewayService(ctx, &ecs.DescribeExpressGatewayServiceInput{ServiceArn: svc.ServiceArn})
	if err == nil {
		st.WorkersDev = endpoint(x.Service)
		st.URL = st.WorkersDev
		img := expressImage(x.Service)
		if i := strings.LastIndex(img, ":"); i >= 0 {
			tag := img[i+1:]
			st.Version = &ProdVersion{ID: tag, Commit: commitIn(tag), By: "bomclaw"}
		}
	}
	if st.Version != nil {
		for _, t := range svc.Tags {
			if aws.ToString(t.Key) == "bomclaw:message" {
				st.Version.Message = aws.ToString(t.Value)
			}
		}
	}
	// A custom domain lives on the CloudFront distribution in front.
	if d, err := a.findDistribution(ctx, name, false); err == nil && d != nil && d.Aliases != nil {
		st.Domains = d.Aliases.Items
	}
}

// pushImage builds the image here, for the architecture Fargate will run it
// on, and pushes it to the account's ECR.
func (a *AWS) pushImage(ctx context.Context, cfg aws.Config, docker string, r Resolved, tag, platform string, step func(string, ...any), log io.Writer) (string, error) {
	ec := ecr.NewFromConfig(cfg)
	repo := "bomclaw/" + r.Name
	if _, err := ec.CreateRepository(ctx, &ecr.CreateRepositoryInput{RepositoryName: &repo}); err != nil {
		var exists *ecrtypes.RepositoryAlreadyExistsException
		if !errors.As(err, &exists) {
			return "", fmt.Errorf("ecr repository: %w", err)
		}
	}
	auth, err := ec.GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil || len(auth.AuthorizationData) == 0 {
		return "", fmt.Errorf("ecr login: %v", err)
	}
	raw, _ := base64.StdEncoding.DecodeString(aws.ToString(auth.AuthorizationData[0].AuthorizationToken))
	user, pass, _ := strings.Cut(string(raw), ":")
	registry := strings.TrimPrefix(aws.ToString(auth.AuthorizationData[0].ProxyEndpoint), "https://")
	login := exec.CommandContext(ctx, docker, "login", "--username", user, "--password-stdin", registry)
	login.Stdin = strings.NewReader(pass)
	if out, err := login.CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker login %s: %v %s", registry, err, strings.TrimSpace(string(out)))
	}
	image := registry + "/" + repo + ":" + tag
	step("docker build %s (%s)", image, platform)
	if err := ExecRunner(ctx, r.WorkDir, nil, log, docker, "build", "--platform", platform, "-f", r.DockerPath, "-t", image, r.WorkDir); err != nil {
		return "", fmt.Errorf("docker build failed: %w", err)
	}
	step("docker push %s", image)
	if err := ExecRunner(ctx, r.WorkDir, nil, log, docker, "push", image); err != nil {
		return "", fmt.Errorf("docker push failed: %w", err)
	}
	return image, nil
}

// fargateArch is the architecture to build for: this machine's, so an Apple
// Silicon Mac builds natively instead of emulating x86.
func fargateArch() (ecstypes.ExpressCpuArchitecture, string) {
	if runtime.GOARCH == "arm64" {
		return ecstypes.ExpressCpuArchitectureArm64, "linux/arm64"
	}
	return ecstypes.ExpressCpuArchitectureX8664, "linux/amd64"
}

func (a *AWS) deployApp(ctx context.Context, r Resolved, opt Options, log io.Writer) (*Result, error) {
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
		return nil, fmt.Errorf("docker is not installed — an AWS app runs an image built here (brew install --cask docker)")
	}
	port := r.Port
	if port == 0 {
		port = 8080
	}
	region := a.region(r)
	g := Git(ctx, r.ProjectDir)
	tag := imageLabel(g)
	if tag == "" {
		tag = fmt.Sprintf("t%d", time.Now().Unix())
	}
	arch, platform := fargateArch()
	step("ecs: image from %s, port %d, region %s, tag %s", r.DockerPath, port, region, tag)
	if opt.DryRun {
		step("dry run: stopping before docker build")
		return res, nil
	}
	cfg, err := a.regional(ctx, region)
	if err != nil {
		return nil, err
	}
	image, err := a.pushImage(ctx, cfg, docker, r, tag, platform, step, log)
	if err != nil {
		return nil, err
	}

	ic := iam.NewFromConfig(cfg)
	execARN, err := ensureServiceRole(ctx, ic, ecsExecRole, "ecs-tasks.amazonaws.com",
		"arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy", "Lets bomclaw's ECS tasks pull from ECR and write logs", step)
	if err != nil {
		return nil, err
	}
	infraARN, err := ensureServiceRole(ctx, ic, ecsInfraRole, "ecs.amazonaws.com",
		"arn:aws:iam::aws:policy/service-role/AmazonECSInfrastructureRoleforExpressGatewayServices",
		"Lets ECS Express Mode build bomclaw apps' load balancer and scaling", step)
	if err != nil {
		return nil, err
	}

	c := ecs.NewFromConfig(cfg)
	if _, err := c.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(ecsCluster),
		Tags: []ecstypes.Tag{{Key: aws.String("bomclaw:managed"), Value: aws.String("true")}}}); err != nil {
		return nil, fmt.Errorf("ecs cluster: %w", err)
	}
	var env []ecstypes.KeyValuePair
	for _, v := range vars {
		env = append(env, ecstypes.KeyValuePair{Name: aws.String(v.Name), Value: aws.String(v.Value)})
	}
	if len(env) > 0 {
		step("env: %d value(s) from the project's connectors → the service's environment", len(env))
	}
	container := &ecstypes.ExpressGatewayContainer{Image: &image, ContainerPort: aws.Int32(int32(port)), Environment: env}
	health := aws.String("/")
	scaling := &ecstypes.ExpressGatewayScalingTarget{MinTaskCount: aws.Int32(1), MaxTaskCount: aws.Int32(2),
		AutoScalingMetric: ecstypes.ExpressGatewayServiceScalingMetricAverageCPUUtilization, AutoScalingTargetValue: aws.Int32(60)}
	message := ""
	if g != nil {
		message = tagValue(deployMessage(g))
	}

	existing, err := a.appService(ctx, c, r.Name)
	if err != nil {
		return nil, err
	}
	var arn string
	if existing != nil && aws.ToString(existing.Status) == "DRAINING" {
		return nil, fmt.Errorf("the ECS service %s is still being deleted — publish again in a few minutes", r.Name)
	}
	if existing == nil {
		tags := []ecstypes.Tag{{Key: aws.String("bomclaw:project"), Value: &r.Name}}
		if message != "" {
			tags = append(tags, ecstypes.Tag{Key: aws.String("bomclaw:message"), Value: &message})
		}
		// Roles created a moment ago are not usable at once (IAM is
		// eventually consistent): retry the create for a little while.
		var out *ecs.CreateExpressGatewayServiceOutput
		err = waitFor(ctx, 6*time.Second, 2*time.Minute, func() (bool, error) {
			out, err = c.CreateExpressGatewayService(ctx, &ecs.CreateExpressGatewayServiceInput{
				ServiceName: &r.Name, Cluster: aws.String(ecsCluster),
				InfrastructureRoleArn: &infraARN, ExecutionRoleArn: &execARN,
				PrimaryContainer: container, CpuArchitecture: arch, Cpu: aws.String("256"), Memory: aws.String("512"),
				HealthCheckPath: health, ScalingTarget: scaling, Tags: tags,
			})
			if err != nil && isRolePropagation(err) {
				return false, nil
			}
			return err == nil, err
		})
		if err != nil {
			return nil, fmt.Errorf("create ECS Express service: %w", err)
		}
		arn = aws.ToString(out.Service.ServiceArn)
		step("ecs: created Express service %s (Fargate %s, 0.25 vCPU / 512 MB, 1–2 tasks)", r.Name, strings.ToLower(string(arch)))
	} else {
		arn = aws.ToString(existing.ServiceArn)
		if _, err := c.UpdateExpressGatewayService(ctx, &ecs.UpdateExpressGatewayServiceInput{ServiceArn: &arn,
			PrimaryContainer: container, CpuArchitecture: arch, ExecutionRoleArn: &execARN, HealthCheckPath: health}); err != nil {
			return nil, fmt.Errorf("update ECS Express service: %w", err)
		}
		if message != "" {
			_, _ = c.TagResource(ctx, &ecs.TagResourceInput{ResourceArn: &arn,
				Tags: []ecstypes.Tag{{Key: aws.String("bomclaw:message"), Value: &message}}})
		}
		step("ecs: rolling out %s", tag)
	}

	step("ecs: waiting for the rollout (the first one builds a load balancer: several minutes)")
	err = waitFor(ctx, 15*time.Second, 20*time.Minute, func() (bool, error) {
		svc, err := a.appService(ctx, c, r.Name)
		if err != nil || svc == nil {
			return false, err
		}
		for _, d := range svc.Deployments {
			if aws.ToString(d.Status) != "PRIMARY" {
				continue
			}
			switch d.RolloutState {
			case ecstypes.DeploymentRolloutStateCompleted:
				return true, nil
			case ecstypes.DeploymentRolloutStateFailed:
				why := aws.ToString(d.RolloutStateReason)
				if len(svc.Events) > 0 {
					why += " — " + aws.ToString(svc.Events[0].Message)
				}
				return false, fmt.Errorf("ECS could not start the app (does the image listen on port %d and answer / with 200?): %s", port, why)
			}
		}
		return false, nil
	})
	if errors.Is(err, errTimeout) {
		step("ecs: still rolling out — status will show when it is live")
		err = nil
	}
	if err != nil {
		return nil, err
	}
	if x, err := c.DescribeExpressGatewayService(ctx, &ecs.DescribeExpressGatewayServiceInput{ServiceArn: &arn}); err == nil {
		res.URL = endpoint(x.Service)
	}
	if r.Domain != "" && res.URL != "" {
		if a.frontApp(ctx, r, strings.TrimPrefix(res.URL, "https://"), step) {
			res.URL = "https://" + r.Domain
		}
	}
	step("live: %s", res.URL)
	return res, nil
}

func isRolePropagation(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "role") && (strings.Contains(s, "assume") || strings.Contains(s, "not authorized") ||
		strings.Contains(s, "invalid") || strings.Contains(s, "unable"))
}

// ensureServiceRole finds or creates a role the AWS service may assume, with
// one managed policy.
func ensureServiceRole(ctx context.Context, c *iam.Client, name, principal, policy, desc string, step func(string, ...any)) (string, error) {
	got, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(name)})
	if err == nil {
		return aws.ToString(got.Role.Arn), nil
	}
	var missing *iamtypes.NoSuchEntityException
	if !errors.As(err, &missing) {
		return "", fmt.Errorf("iam: %w", err)
	}
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"` + principal + `"},"Action":"sts:AssumeRole"}]}`
	out, err := c.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(name), AssumeRolePolicyDocument: aws.String(trust),
		Description: aws.String(desc)})
	if err != nil {
		return "", fmt.Errorf("create role %s: %w", name, err)
	}
	if _, err := c.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(name), PolicyArn: aws.String(policy)}); err != nil {
		return "", fmt.Errorf("attach %s: %w", policy, err)
	}
	step("iam: created role %s", name)
	return aws.ToString(out.Role.Arn), nil
}

// frontApp puts a CloudFront distribution in front of the app's endpoint so
// it can answer on the project's domain. It reports whether the domain is
// attached.
func (a *AWS) frontApp(ctx context.Context, r Resolved, origin string, step func(string, ...any)) bool {
	gcfg, err := a.regional(ctx, globalRegion)
	if err != nil {
		step("domain: %v", err)
		return false
	}
	cf := cloudfront.NewFromConfig(gcfg)
	d, err := a.findDistribution(ctx, r.Name, true)
	if err != nil {
		step("domain: %v", err)
		return false
	}
	var id, domain string
	if d == nil {
		out, err := cf.CreateDistribution(ctx, &cloudfront.CreateDistributionInput{DistributionConfig: appDistConfig(r.Name, origin)})
		if err != nil {
			step("domain: create CloudFront distribution: %v", err)
			return false
		}
		id, domain = aws.ToString(out.Distribution.Id), aws.ToString(out.Distribution.DomainName)
		step("cloudfront: created %s in front of %s", id, origin)
	} else {
		id, domain = aws.ToString(d.Id), aws.ToString(d.DomainName)
	}
	distMu.Lock()
	distCache = nil
	distMu.Unlock()
	return a.attachCloudFrontDomain(ctx, cf, id, domain, r.Domain, step)
}

// appDistConfig passes everything through to the app: no caching, every
// method, the viewer's headers and cookies (but the origin's own Host, which
// its certificate and routing need).
func appDistConfig(name, origin string) *cftypes.DistributionConfig {
	id := "app-" + name
	all := []cftypes.Method{cftypes.MethodGet, cftypes.MethodHead, cftypes.MethodOptions, cftypes.MethodPut,
		cftypes.MethodPost, cftypes.MethodPatch, cftypes.MethodDelete}
	cached := []cftypes.Method{cftypes.MethodGet, cftypes.MethodHead}
	return &cftypes.DistributionConfig{
		CallerReference: aws.String(fmt.Sprintf("bomclaw-%s-%d", name, time.Now().UnixNano())),
		Comment:         aws.String(distComment(name)),
		Enabled:         aws.Bool(true),
		HttpVersion:     cftypes.HttpVersionHttp2and3,
		PriceClass:      cftypes.PriceClassPriceClass200,
		Origins: &cftypes.Origins{Quantity: aws.Int32(1), Items: []cftypes.Origin{{
			Id: aws.String(id), DomainName: aws.String(origin),
			CustomOriginConfig: &cftypes.CustomOriginConfig{HTTPPort: aws.Int32(80), HTTPSPort: aws.Int32(443),
				OriginProtocolPolicy: cftypes.OriginProtocolPolicyHttpsOnly,
				OriginSslProtocols:   &cftypes.OriginSslProtocols{Quantity: aws.Int32(1), Items: []cftypes.SslProtocol{cftypes.SslProtocolTLSv12}}},
		}}},
		DefaultCacheBehavior: &cftypes.DefaultCacheBehavior{
			TargetOriginId:        aws.String(id),
			ViewerProtocolPolicy:  cftypes.ViewerProtocolPolicyRedirectToHttps,
			CachePolicyId:         aws.String(cachingDisabled),
			OriginRequestPolicyId: aws.String(allViewerExceptHostHeader),
			Compress:              aws.Bool(true),
			AllowedMethods: &cftypes.AllowedMethods{Quantity: aws.Int32(int32(len(all))), Items: all,
				CachedMethods: &cftypes.CachedMethods{Quantity: aws.Int32(2), Items: cached}},
		},
		ViewerCertificate: &cftypes.ViewerCertificate{CloudFrontDefaultCertificate: aws.Bool(true)},
	}
}
