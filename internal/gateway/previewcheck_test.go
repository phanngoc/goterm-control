package gateway

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectKnowsTheStacksItIsSureOf(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  func(*PreviewSpec) bool
	}{
		{"vite in web/", map[string]string{
			"web/package.json": `{"scripts":{"dev":"vite"},"devDependencies":{"vite":"^5"}}`,
		}, func(s *PreviewSpec) bool {
			return s.Cwd == "web" && strings.Contains(s.Command, "--base $BASE_PATH") && s.Setup == "npm install"
		}},
		{"vite with pnpm", map[string]string{
			"package.json":   `{"scripts":{"dev":"vite"},"devDependencies":{"vite":"^5"}}`,
			"pnpm-lock.yaml": "",
		}, func(s *PreviewSpec) bool {
			return s.Setup == "pnpm install" && strings.HasPrefix(s.Command, "pnpm run dev")
		}},
		{"wrangler strips", map[string]string{
			"package.json": `{"devDependencies":{"wrangler":"^3"}}`,
		}, func(s *PreviewSpec) bool { return s.Base == "strip" && strings.Contains(s.Command, "wrangler dev") }},
		{"static board", map[string]string{
			"board/index.html": "<html></html>",
		}, func(s *PreviewSpec) bool { return s.Static == "board" && s.Command == "" }},
	}
	for _, c := range cases {
		dir := t.TempDir()
		for p, b := range c.files {
			write(t, filepath.Join(dir, p), b)
		}
		got, why := DetectPreview(dir)
		if got == nil || !c.want(got) {
			t.Errorf("%s: got %+v (%s)", c.name, got, why)
		}
	}
	// A folder with only a brief has nothing to show, and says so.
	dir := t.TempDir()
	write(t, filepath.Join(dir, "AGENTS.md"), "# x")
	if got, why := DetectPreview(dir); got != nil || !strings.Contains(why, "nothing to preview") {
		t.Errorf("empty project: %+v %q", got, why)
	}
}

func TestWriteKeepsTheManifestsOtherKeys(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, PreviewManifest), `{"name":"x","preview":{"static":"old"}}`)
	if err := WritePreviewSpec(dir, &PreviewSpec{Static: "board"}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, PreviewManifest))
	if !strings.Contains(string(raw), `"name": "x"`) {
		t.Errorf("other keys lost: %s", raw)
	}
	spec, err := ReadPreviewSpec(dir)
	if err != nil || spec == nil || spec.Static != "board" {
		t.Errorf("read back %+v %v", spec, err)
	}
	if err := WritePreviewSpec(dir, &PreviewSpec{}); err == nil {
		t.Error("an empty preview was written")
	}
}

func TestStaticCheckCatchesRootAbsoluteLinks(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "board/index.html"), `<script src="app.js"></script><a href="//cdn.x/y">`)
	if r := CheckPreview(t.Context(), dir, "ch_x", &PreviewSpec{Static: "board"}, time.Second); !r.OK {
		t.Errorf("relative page failed: %v", r.Problems)
	}
	write(t, filepath.Join(dir, "board/index.html"), `<link href="/style.css">`)
	if r := CheckPreview(t.Context(), dir, "ch_x", &PreviewSpec{Static: "board"}, time.Second); r.OK {
		t.Error("a /style.css link passed")
	}
	if r := CheckPreview(t.Context(), dir, "ch_x", &PreviewSpec{Static: "../.."}, time.Second); r.OK {
		t.Error("a static folder outside the project passed")
	}
}

func TestBadRefsRespectsTheBase(t *testing.T) {
	page := `<script src="/preview/ch_x/@vite/client"></script><img src="/logo.png"><a href="//o.org/">`
	if got := badRefs(page, "keep", "/preview/ch_x/"); len(got) != 1 || got[0] != "/logo.png" {
		t.Errorf("keep: %v", got)
	}
	if got := badRefs(page, "strip", "/preview/ch_x/"); len(got) != 2 {
		t.Errorf("strip: %v", got)
	}
}

// TestHelperServe is not a test: it is the dev server the check below starts,
// run from this test binary so it needs nothing installed (python3 on the
// macOS runners never listened within 20s). It serves the folder it is run in,
// at /, on $HOST:$PORT, the way a server that ignores the base would.
func TestHelperServe(t *testing.T) {
	if os.Getenv("BOMCLAW_TEST_SERVE") == "" {
		t.Skip("helper process for TestServerCheckRunsWhatTheButtonRuns")
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(os.Getenv("HOST"), os.Getenv("PORT")))
	if err != nil {
		t.Fatal(err)
	}
	_ = http.Serve(ln, http.FileServer(http.Dir(".")))
}

// The check is only worth trusting if it runs a real server the way the
// dashboard does: the helper answers at /, so "strip" passes and "keep" — the
// server never heard of the base — fails with a 404.
func TestServerCheckRunsWhatTheButtonRuns(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell preview commands are POSIX")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, filepath.Join(dir, "site/index.html"), `<script src="app.js"></script>`)
	spec := &PreviewSpec{
		Command: "exec '" + self + "' -test.run='^TestHelperServe$'",
		Cwd:     "site", Base: "strip",
		Env: map[string]string{"BOMCLAW_TEST_SERVE": "1"},
	}
	if r := CheckPreview(t.Context(), dir, "ch_x", spec, 20*time.Second); !r.OK {
		t.Fatalf("strip failed: %v\n%s", r.Problems, r.Logs)
	}
	spec.Base = "keep"
	r := CheckPreview(t.Context(), dir, "ch_x", spec, 20*time.Second)
	if r.OK || r.Status != 404 {
		t.Errorf("keep against a server that ignores the base passed: %+v", r)
	}
	dead := &PreviewSpec{Command: "echo no server; exit 3"}
	if r := CheckPreview(t.Context(), dir, "ch_x", dead, 5*time.Second); r.OK || !strings.Contains(r.Logs, "no server") {
		t.Errorf("an exiting command passed or lost its output: %+v", r)
	}
}
