package deploy

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// Bucket names are global: the account id makes a project's unique, and
// the whole stays within S3's 63 characters.
func TestAWSBucketNamesAreUniqueAndShortEnough(t *testing.T) {
	if got := bucketName("rebds-danang", "123456789012"); got != "bomclaw-rebds-danang-123456789012" {
		t.Errorf("bucket %q", got)
	}
	long := bucketName(strings.Repeat("a", 63), "123456789012")
	if len(long) > 63 || !strings.HasSuffix(long, "-123456789012") || strings.Contains(long, "--") {
		t.Errorf("long bucket %q (%d)", long, len(long))
	}
}

func TestAWSRefusesWhatItCannotHost(t *testing.T) {
	a := &AWS{}
	for _, s := range []*Spec{
		{Main: "w.js"}, {Framework: "nextjs"}, {},
		{Static: "dist", Dockerfile: "Dockerfile"},
		{Static: "dist", Database: &Database{Engine: "sqlite", File: "a.db"}},
	} {
		if a.Validate(s) == nil {
			t.Errorf("accepted %+v", s)
		}
	}
	for _, s := range []*Spec{{Static: "dist"}, {Dockerfile: "Dockerfile"}} {
		if err := a.Validate(s); err != nil {
			t.Errorf("refused %+v: %v", s, err)
		}
	}
	if got := awsServices(Resolved{Spec: Spec{Static: "dist", Domain: "x.bomclaw.org"}}); strings.Join(got, ",") != "S3,CloudFront,ACM" {
		t.Errorf("static services %v", got)
	}
}

// Tag values have a small alphabet; a commit subject is cut down to it.
func TestAWSTagValuesAreSanitised(t *testing.T) {
	if got := tagValue("bomclaw git abc1234: fix (header) & \"quotes\""); got != "bomclaw git abc1234: fix header quotes" {
		t.Errorf("tag %q", got)
	}
	if got := commitIn(tagValue(deployMessage(&GitVersion{Short: "abc1234", Subject: "x"}))); got != "abc1234" {
		t.Errorf("commit after tagging %q", got)
	}
}

func TestAWSStaticCaching(t *testing.T) {
	if cacheControl("index.html") != "no-cache" || cacheControl("a/app.js") == "no-cache" {
		t.Error("HTML must be revalidated, assets cached")
	}
	if contentType("a/app.css") != "text/css; charset=utf-8" || contentType("x.unknownext") != "application/octet-stream" {
		t.Errorf("types %q %q", contentType("a/app.css"), contentType("x.unknownext"))
	}
}

// The app's address comes from the newest active configuration's public
// ingress path, with or without a scheme.
func TestAWSExpressEndpointAndImage(t *testing.T) {
	x := &ecstypes.ECSExpressGatewayService{ActiveConfigurations: []ecstypes.ExpressGatewayServiceConfiguration{
		{IngressPaths: []ecstypes.IngressPathSummary{{AccessType: ecstypes.AccessTypePublic, Endpoint: aws.String("old.ecs.ap-southeast-1.on.aws")}},
			PrimaryContainer: &ecstypes.ExpressGatewayContainer{Image: aws.String("r/app:git-aaaaaaa")}},
		{IngressPaths: []ecstypes.IngressPathSummary{{AccessType: ecstypes.AccessTypePublic, Endpoint: aws.String("new.ecs.ap-southeast-1.on.aws/")}},
			PrimaryContainer: &ecstypes.ExpressGatewayContainer{Image: aws.String("r/app:git-bbbbbbb")}},
	}}
	if got := endpoint(x); got != "https://new.ecs.ap-southeast-1.on.aws" {
		t.Errorf("endpoint %q", got)
	}
	if got := expressImage(x); got != "r/app:git-bbbbbbb" {
		t.Errorf("image %q", got)
	}
	if endpoint(nil) != "" || expressImage(nil) != "" {
		t.Error("nil service")
	}
}

// CloudWatch names ALB metrics by ARN suffixes.
func TestAWSALBDimensions(t *testing.T) {
	tg := "arn:aws:elasticloadbalancing:ap-southeast-1:1:targetgroup/ecs-x/abc"
	lb := "arn:aws:elasticloadbalancing:ap-southeast-1:1:loadbalancer/app/ecs-express/def"
	if got := arnSuffix(tg, "targetgroup/"); got != "targetgroup/ecs-x/abc" {
		t.Errorf("tg %q", got)
	}
	if got := arnSuffix(lb, "loadbalancer/"); got != "app/ecs-express/def" {
		t.Errorf("lb %q", got)
	}
}

// A domain in front of an app must not cache, must pass every method, and
// must keep the origin's Host.
func TestAWSAppDistributionPassesThrough(t *testing.T) {
	c := appDistConfig("shop", "x.ecs.ap-southeast-1.on.aws")
	b := c.DefaultCacheBehavior
	if aws.ToString(b.CachePolicyId) != cachingDisabled || aws.ToString(b.OriginRequestPolicyId) != allViewerExceptHostHeader {
		t.Error("policies")
	}
	if aws.ToInt32(b.AllowedMethods.Quantity) != 7 || aws.ToString(c.Comment) != distComment("shop") {
		t.Errorf("methods %d comment %q", aws.ToInt32(b.AllowedMethods.Quantity), aws.ToString(c.Comment))
	}
	if c.Origins.Items[0].CustomOriginConfig.OriginProtocolPolicy != "https-only" {
		t.Error("origin must be reached over HTTPS")
	}
}
