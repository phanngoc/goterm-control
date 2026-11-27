package deploy

import (
	"strings"
	"testing"
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
