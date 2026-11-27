//go:build live

package deploy

// Live tests publish to the real providers with this machine's credentials.
// They create billable resources, so they only build with the "live" tag:
//
//	go test -tags live -run TestLiveAWSStatic ./internal/deploy/ -v -timeout 40m
//
// Each leaves its resources up so the result can be looked at; tear down with
// the commands the test prints.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ecs"
)

func liveSite(t *testing.T, name, body string) Resolved {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "dist", "assets"), 0o755)
	os.WriteFile(filepath.Join(dir, "dist", "index.html"), []byte(body), 0o644)
	os.WriteFile(filepath.Join(dir, "dist", "assets", "app.js"), []byte("console.log(1)"), 0o644)
	s := &Spec{Static: "dist", SPA: true, Name: name}
	return s.Resolve(dir, "ch_"+name)
}

// fetchUntil polls url until its body contains want (CloudFront takes a few
// minutes to reach every edge).
func fetchUntil(t *testing.T, url, want string, limit time.Duration) {
	deadline := time.Now().Add(limit)
	var last string
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			last = resp.Status + " " + string(b)
			if resp.StatusCode == 200 && strings.Contains(string(b), want) {
				return
			}
		} else {
			last = err.Error()
		}
		time.Sleep(15 * time.Second)
	}
	t.Fatalf("%s never served %q; last: %.200s", url, want, last)
}

func TestLiveAWSStatic(t *testing.T) {
	name := firstNonEmpty(os.Getenv("LIVE_NAME"), "bomclaw-smoketest")
	a := &AWS{Region: firstNonEmpty(os.Getenv("AWS_REGION"), "ap-southeast-1")}
	if ok, hint := a.Connected(); !ok {
		t.Skip(hint)
	}
	marker := "smoke " + time.Now().Format(time.RFC3339)
	r := liveSite(t, name, "<h1>"+marker+"</h1>")
	if err := a.Validate(&r.Spec); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	res, err := a.Deploy(ctx, r, Options{}, io.MultiWriter(&log, os.Stdout))
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	t.Logf("URL %s", res.URL)
	fetchUntil(t, res.URL, marker, 20*time.Minute)
	fetchUntil(t, res.URL+"/some/client/route", marker, 2*time.Minute) // SPA fallback

	st := a.Status(ctx, r)
	t.Logf("status deployed=%v url=%s version=%+v account=%+v err=%s", st.Deployed, st.URL, st.Version, st.Account, st.Error)
	if !st.Deployed || st.URL == "" {
		t.Errorf("status does not see the deploy: %+v", st)
	}
	m := a.Metrics(ctx, 24*time.Hour)
	t.Logf("metrics %+v", m)
}

// An app with a server: a tiny Dockerfile goes through docker build, ECR and
// an ECS Express service, and the service URL must answer with it.
func TestLiveAWSApp(t *testing.T) {
	name := firstNonEmpty(os.Getenv("LIVE_NAME"), "bomclaw-smoketest-app")
	a := &AWS{Region: firstNonEmpty(os.Getenv("AWS_REGION"), "ap-southeast-1")}
	if ok, hint := a.Connected(); !ok {
		t.Skip(hint)
	}
	if _, err := lookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	marker := "app " + time.Now().Format(time.RFC3339)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>"+marker+"</h1>"), 0o644)
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM nginx:alpine\n"+
		"RUN sed -i 's/listen  *80;/listen 8080;/' /etc/nginx/conf.d/default.conf\n"+
		"COPY index.html /usr/share/nginx/html/index.html\nEXPOSE 8080\n"), 0o644)
	s := &Spec{Dockerfile: "Dockerfile", Port: 8080, Name: name}
	if err := a.Validate(s); err != nil {
		t.Fatal(err)
	}
	r := s.Resolve(dir, "ch_"+name)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Minute)
	defer cancel()
	res, err := a.Deploy(ctx, r, Options{}, os.Stdout)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	t.Logf("URL %s", res.URL)
	fetchUntil(t, res.URL, marker, 10*time.Minute)
	st := a.Status(ctx, r)
	t.Logf("status deployed=%v url=%s version=%+v err=%s", st.Deployed, st.URL, st.Version, st.Error)
	if !st.Deployed || st.URL == "" {
		t.Errorf("status does not see the deploy: %+v", st)
	}
}

// TestLiveAWSAppTeardown deletes the smoke-test app's Express service (and
// with it the load balancer ECS built for it). The aws CLI before 2.31 has
// no command for it.
func TestLiveAWSAppTeardown(t *testing.T) {
	name := firstNonEmpty(os.Getenv("LIVE_NAME"), "bomclaw-smoketest-app")
	a := &AWS{Region: firstNonEmpty(os.Getenv("AWS_REGION"), "ap-southeast-1")}
	ctx := context.Background()
	arn, err := a.serviceARN(ctx, a.Region, name)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := a.regional(ctx, a.Region)
	c := ecs.NewFromConfig(cfg)
	if _, err := c.DeleteExpressGatewayService(ctx, &ecs.DeleteExpressGatewayServiceInput{ServiceArn: &arn}); err != nil {
		t.Logf("delete: %v", err)
	}
	err = waitFor(ctx, 15*time.Second, 15*time.Minute, func() (bool, error) {
		svc, err := a.appService(ctx, c, name)
		return svc == nil, err
	})
	if err != nil {
		t.Fatalf("still there: %v", err)
	}
	t.Log("deleted")
}
