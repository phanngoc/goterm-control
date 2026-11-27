package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// AWS is the AWS provider. Two shapes, picked by the spec like on Fly.io:
//
//   - "static": the built folder goes to a private S3 bucket, served over
//     HTTPS by a CloudFront distribution (Origin Access Control, so the
//     bucket is never public). https://<id>.cloudfront.net, and a custom
//     domain with an ACM certificate when deploy.domain is set.
//   - "dockerfile": the image is built here with docker, pushed to ECR, and
//     run on Fargate by ECS Express Mode — HTTPS, a managed load balancer,
//     health checks and scaling in one call. https://<id>.ecs.<region>.on.aws.
//
// Every resource is named after the project (bomclaw-<name>…) and tagged, so
// status, metrics and the Infra page find them without a state file. The
// month's cost comes from Cost Explorer. A database is not hosted here yet
// (RDS is a different weight class): such a project stays on Cloudflare D1.
//
// Credentials: AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY from the
// Connections page, or — when none are stored — the machine's own AWS CLI
// profile (~/.aws, AWS_PROFILE), which is how most people already have it.
type AWS struct {
	Region string
	// cfg is built once per provider value.
	once sync.Once
	cfg  aws.Config
	err  error
}

func init() {
	Register("aws", func() Provider { return &AWS{Region: firstNonEmpty(Lookup("AWS_REGION"), Lookup("AWS_DEFAULT_REGION"), "ap-southeast-1")} })
}

// ErrNoAWS says what to set.
var ErrNoAWS = errors.New("AWS is not connected: add an access key on the dashboard (Admin → Connections), " +
	"or `bomclaw secrets set aws AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… AWS_REGION=ap-southeast-1`, or configure the AWS CLI on this machine (aws configure)")

func (a *AWS) Name() string  { return "aws" }
func (a *AWS) Title() string { return "AWS" }

func (a *AWS) Validate(s *Spec) error {
	if s.Main != "" {
		return fmt.Errorf("deploy.main is a Cloudflare Worker script; on AWS an app with a server is deploy.dockerfile (ECS on Fargate)")
	}
	if s.Framework != "" {
		return fmt.Errorf("deploy.framework is a Vercel build; on AWS build here (deploy.build + deploy.static) or ship a Dockerfile")
	}
	if s.Static == "" && s.Dockerfile == "" {
		return fmt.Errorf("on AWS, deploy needs \"static\" (S3 + CloudFront) or \"dockerfile\" (ECS on Fargate)")
	}
	if s.Static != "" && s.Dockerfile != "" {
		return fmt.Errorf("on AWS, deploy has both \"static\" and \"dockerfile\"; keep one (copy the static files in your Dockerfile)")
	}
	if s.Database != nil {
		return fmt.Errorf("deploy.database on AWS (RDS) is not built yet — keep the data on Cloudflare D1 (\"target\": \"cloudflare\") or use a connector (Supabase)")
	}
	return nil
}

// sharedProfile reports whether the machine has an AWS CLI profile.
func sharedProfile() bool {
	if Lookup("AWS_PROFILE") != "" {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, f := range []string{"credentials", "config"} {
		if _, err := os.Stat(filepath.Join(home, ".aws", f)); err == nil {
			return true
		}
	}
	return false
}

func (a *AWS) Connected() (bool, string) {
	if Lookup("AWS_ACCESS_KEY_ID") != "" && Lookup("AWS_SECRET_ACCESS_KEY") != "" {
		return true, ""
	}
	if sharedProfile() {
		return true, ""
	}
	return false, ErrNoAWS.Error()
}

// config is the SDK config: stored keys first, else the default chain.
func (a *AWS) config(ctx context.Context) (aws.Config, error) {
	a.once.Do(func() {
		opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(a.Region)}
		if id, secret := Lookup("AWS_ACCESS_KEY_ID"), Lookup("AWS_SECRET_ACCESS_KEY"); id != "" && secret != "" {
			opts = append(opts, awsconfig.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(id, secret, Lookup("AWS_SESSION_TOKEN"))))
		} else if p := Lookup("AWS_PROFILE"); p != "" {
			opts = append(opts, awsconfig.WithSharedConfigProfile(p))
		}
		a.cfg, a.err = awsconfig.LoadDefaultConfig(context.Background(), opts...)
	})
	return a.cfg, a.err
}

// regional is the config for another region (CloudFront and ACM for it live
// in us-east-1; ECS in the project's region).
func (a *AWS) regional(ctx context.Context, region string) (aws.Config, error) {
	cfg, err := a.config(ctx)
	if err != nil {
		return cfg, err
	}
	c := cfg.Copy()
	if region != "" {
		c.Region = region
	}
	return c, nil
}

const globalRegion = "us-east-1"

// identity is the account id the credentials reach, cached.
var (
	identityMu    sync.Mutex
	identityCache = map[string]string{}
)

func (a *AWS) accountID(ctx context.Context) (string, error) {
	cfg, err := a.config(ctx)
	if err != nil {
		return "", err
	}
	key := Lookup("AWS_ACCESS_KEY_ID") + "|" + Lookup("AWS_PROFILE")
	identityMu.Lock()
	if id, ok := identityCache[key]; ok {
		identityMu.Unlock()
		return id, nil
	}
	identityMu.Unlock()
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("aws: %w", err)
	}
	id := aws.ToString(out.Account)
	identityMu.Lock()
	identityCache[key] = id
	identityMu.Unlock()
	return id, nil
}

// region is where the project runs.
func (a *AWS) region(r Resolved) string { return firstNonEmpty(r.Region, a.Region) }

// Status asks AWS what is live.
func (a *AWS) Status(ctx context.Context, r Resolved) (st ProdStatus) {
	st = ProdStatus{Provider: a.Name(), Title: a.Title(), Declared: true, Name: r.Name, WantDomain: r.Domain,
		Services: awsServices(r)}
	if ok, hint := a.Connected(); !ok {
		st.Error = hint
		return st
	}
	st.Connected = true
	acc := make(chan *Account, 1)
	go func() { acc <- a.Account(ctx) }()
	defer func() { st.Account = <-acc }()
	if r.Static != "" {
		a.staticStatus(ctx, r.Name, &st)
	} else {
		a.appStatus(ctx, a.region(r), r.Name, &st)
	}
	if st.Deployed && r.Domain != "" {
		for _, d := range st.Domains {
			if d == r.Domain {
				st.URL = "https://" + d
			}
		}
	}
	return st
}

func awsServices(r Resolved) []string {
	if r.Dockerfile != "" {
		out := []string{"ECR", "ECS Fargate", "Load Balancer"}
		if r.Domain != "" {
			out = append(out, "CloudFront", "ACM")
		}
		return out
	}
	out := []string{"S3", "CloudFront"}
	if r.Domain != "" {
		out = append(out, "ACM")
	}
	return out
}

// Account names the account: its alias when it has one, else its id.
func (a *AWS) Account(ctx context.Context) *Account {
	if ok, _ := a.Connected(); !ok {
		return nil
	}
	id, err := a.accountID(ctx)
	if err != nil {
		return &Account{PlanHint: err.Error()}
	}
	acc := &Account{Name: id, PlanHint: "AWS tính theo mức dùng — xem chi phí tháng này"}
	if cfg, err := a.config(ctx); err == nil {
		if out, err := iam.NewFromConfig(cfg).ListAccountAliases(ctx, &iam.ListAccountAliasesInput{}); err == nil && len(out.AccountAliases) > 0 {
			acc.Name = out.AccountAliases[0] + " (" + id + ")"
		}
	}
	acc.Name += " · " + a.Region
	return acc
}

// Verify checks the credentials and names the account.
func (a *AWS) Verify(ctx context.Context) (string, error) {
	if ok, hint := a.Connected(); !ok {
		return "", errors.New(hint)
	}
	cfg, err := a.config(ctx)
	if err != nil {
		return "", err
	}
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("the credentials were refused: %v", err)
	}
	msg := fmt.Sprintf("credentials active · account %s · %s · region %s", aws.ToString(out.Account), aws.ToString(out.Arn), a.Region)
	if _, err := lookPath("docker"); err != nil {
		msg += " · docker NOT installed (needed for Dockerfile apps)"
	}
	return msg, nil
}

// Discover: a project deployed by bomclaw to AWS carries its name in its
// resources, so a room's candidates are looked up by that.
func (a *AWS) Discover(ctx context.Context, cands []Candidate) (*ProdStatus, string) {
	if ok, _ := a.Connected(); !ok {
		return nil, ""
	}
	for _, c := range cands {
		st := ProdStatus{Provider: a.Name(), Title: a.Title(), Connected: true, Name: c.Name}
		a.staticStatus(ctx, c.Name, &st)
		if !st.Deployed {
			st = ProdStatus{Provider: a.Name(), Title: a.Title(), Connected: true, Name: c.Name}
			a.appStatus(ctx, a.Region, c.Name, &st)
		}
		if st.Deployed {
			st.Error = ""
			return &st, c.Source
		}
	}
	return nil, ""
}

// Deploy publishes: S3 + CloudFront for a static site, ECR + ECS Express for
// a Dockerfile.
func (a *AWS) Deploy(ctx context.Context, r Resolved, opt Options, log io.Writer) (*Result, error) {
	if opt.WithDB {
		return nil, fmt.Errorf("--with-db: AWS hosts no database in bomclaw yet (RDS is not built)")
	}
	if !opt.DryRun {
		if ok, hint := a.Connected(); !ok {
			return nil, errors.New(hint)
		}
	}
	if r.Static != "" {
		return a.deployStatic(ctx, r, opt, log)
	}
	return a.deployApp(ctx, r, opt, log)
}

// resourceName is a project's name as an AWS resource name: bucket names are
// global and at most 63 characters, so the account id makes them unique.
func bucketName(name, account string) string {
	n := "bomclaw-" + name
	if max := 63 - 1 - len(account); len(n) > max {
		n = strings.TrimRight(n[:max], "-")
	}
	return n + "-" + account
}

var tagUnsafe = regexp.MustCompile(`[^\p{L}\p{Z}\p{N}_.:/=+\-@]+`)

// tagValue makes s a valid AWS tag value (≤ 256, a limited alphabet).
func tagValue(s string) string {
	s = strings.Join(strings.Fields(tagUnsafe.ReplaceAllString(s, " ")), " ")
	if len(s) > 256 {
		s = s[:256]
	}
	return s
}

// waitFor polls until done reports true, ctx ends or the limit passes.
func waitFor(ctx context.Context, every, limit time.Duration, done func() (bool, error)) error {
	deadline := time.Now().Add(limit)
	for {
		ok, err := done()
		if err != nil || ok {
			return err
		}
		if time.Now().After(deadline) {
			return errTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
}

var errTimeout = errors.New("timed out")
