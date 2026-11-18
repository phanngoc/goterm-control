package deploy

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/connectors"
)

// A project's connectors reach PROD: the build gets only the public values,
// and the Worker gets every value as a secret — set after it exists.
func TestPublishHandsTheConnectorsToTheWorker(t *testing.T) {
	r := project(t, `{"name":"phim","static":"dist","build":"npm run build","main":"worker.js"}`)
	if _, err := connectors.SetEnabled(r.ProjectDir, "stripe", true); err != nil {
		t.Fatal(err)
	}
	vals := map[string]string{"STRIPE_SECRET_KEY": "sk_test_s3cret", "STRIPE_PUBLISHABLE_KEY": "pk_test_pub"}
	old := Lookup
	Lookup = func(k string) string { return vals[k] }
	defer func() { Lookup = old }()

	f := &fakeRun{}
	var bulk string
	run := func(ctx context.Context, dir string, env []string, out io.Writer, name string, args ...string) error {
		if i := indexOf(args, "bulk"); i >= 0 {
			b, _ := os.ReadFile(args[i+1])
			bulk = string(b)
		}
		return f.run(ctx, dir, env, out, name, args...)
	}
	c, _ := fakeCloudflare(t)
	var log strings.Builder
	if _, err := (&Cloudflare{Client: c, Run: run}).Deploy(context.Background(), r, Options{}, &log); err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	order := []string{}
	for _, c := range f.calls {
		joined := strings.Join(c.args, " ")
		switch {
		case c.name == "sh":
			env := strings.Join(c.env, " ")
			if strings.Contains(env, "sk_test_s3cret") {
				t.Error("the build was given the secret key")
			}
			if !strings.Contains(env, "VITE_STRIPE_PUBLISHABLE_KEY=pk_test_pub") {
				t.Errorf("the build lacks the public key: %s", env)
			}
			order = append(order, "build")
		case strings.Contains(joined, "secret bulk"):
			order = append(order, "secrets")
		case strings.Contains(joined, "deploy --config"):
			order = append(order, "deploy")
		}
	}
	if strings.Join(order, ",") != "build,deploy,secrets" {
		t.Errorf("order = %v", order)
	}
	if !strings.Contains(bulk, `"STRIPE_SECRET_KEY":"sk_test_s3cret"`) {
		t.Errorf("secrets file = %s", bulk)
	}
	if strings.Contains(log.String(), "sk_test_s3cret") {
		t.Error("the log shows a secret value")
	}
}

// A static site has nowhere to keep a secret: it is said, and not sent.
func TestAStaticSiteGetsOnlyPublicValues(t *testing.T) {
	r := project(t, `{"name":"phim","static":"dist","build":"npm run build"}`)
	connectors.SetEnabled(r.ProjectDir, "stripe", true)
	old := Lookup
	Lookup = func(k string) string {
		return map[string]string{"STRIPE_SECRET_KEY": "sk_test_s3cret", "STRIPE_PUBLISHABLE_KEY": "pk"}[k]
	}
	defer func() { Lookup = old }()
	f := &fakeRun{}
	c, _ := fakeCloudflare(t)
	var log strings.Builder
	if _, err := (&Cloudflare{Client: c, Run: f.run}).Deploy(context.Background(), r, Options{}, &log); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if strings.Contains(strings.Join(c.args, " "), "secret bulk") {
			t.Error("secrets sent to an assets-only Worker")
		}
	}
	if !strings.Contains(log.String(), "static site") {
		t.Errorf("log does not say why: %s", log.String())
	}
}

func indexOf(ss []string, s string) int {
	for i, x := range ss {
		if x == s {
			return i
		}
	}
	return -1
}
