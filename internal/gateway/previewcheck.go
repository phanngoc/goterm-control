package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Setting a preview up is the agent's job (the `preview` skill), and an agent
// that writes bomclaw.json by hand gets the base path wrong often enough that
// "it works" has to be checked, not believed. This file is what
// `bomclaw preview init` and `bomclaw preview check` stand on: a guess at the
// recipe from the files a project already has, and a dry run of exactly what
// the dashboard's Preview button will run.

// ReadPreviewSpec reads bomclaw.json in a project folder. nil, nil: none.
func ReadPreviewSpec(projectDir string) (*PreviewSpec, error) {
	raw, err := os.ReadFile(filepath.Join(projectDir, PreviewManifest))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return ParsePreviewManifest(raw)
}

// WritePreviewSpec sets bomclaw.json's "preview" and keeps every other key the
// file already has: the manifest is the project's, preview is one tenant.
func WritePreviewSpec(projectDir string, spec *PreviewSpec) error {
	path := filepath.Join(projectDir, PreviewManifest)
	doc := map[string]json.RawMessage{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return fmt.Errorf("%s is not a JSON object, not touching it: %w", PreviewManifest, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	doc["preview"] = b
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if _, err := ParsePreviewManifest(out); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// DetectPreview guesses a preview from what is in the folder, for the stacks
// whose recipe is certain. Anything else returns nil and a reason, and the
// agent writes the file from the skill's table instead of trusting a guess.
func DetectPreview(projectDir string) (*PreviewSpec, string) {
	for _, sub := range []string{"", "web", "app", "frontend", "site"} {
		dir := filepath.Join(projectDir, sub)
		if spec, why := detectNode(dir); spec != nil {
			spec.Cwd = sub
			return spec, why
		}
	}
	for _, sub := range []string{"", "board", "public", "site", "dist", "docs", "web"} {
		if isFile(filepath.Join(projectDir, sub, "index.html")) && !isFile(filepath.Join(projectDir, sub, "package.json")) {
			static := sub
			if static == "" {
				static = "."
			}
			return &PreviewSpec{Static: static}, "index.html in " + static + ", no build step: served as files"
		}
	}
	if isFile(filepath.Join(projectDir, "package.json")) {
		return nil, "package.json has no dev server this knows (vite, next, wrangler): write bomclaw.json from the preview skill's table"
	}
	return nil, "nothing to preview yet: no package.json and no index.html. Run this again once the project has something to look at"
}

func detectNode(dir string) (*PreviewSpec, string) {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil, ""
	}
	var pkg struct {
		Scripts         map[string]string `json:"scripts"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if json.Unmarshal(raw, &pkg) != nil {
		return nil, ""
	}
	has := func(name string) bool {
		_, a := pkg.Dependencies[name]
		_, b := pkg.DevDependencies[name]
		return a || b
	}
	install, run := "npm install", "npm run dev --"
	switch {
	case isFile(filepath.Join(dir, "pnpm-lock.yaml")):
		install, run = "pnpm install", "pnpm run dev"
	case isFile(filepath.Join(dir, "yarn.lock")):
		install, run = "yarn install", "yarn dev"
	}
	dev := pkg.Scripts["dev"]
	switch {
	case has("vite") && strings.Contains(dev, "vite"):
		return &PreviewSpec{
			Setup:   install,
			Command: run + " --host $HOST --port $PORT --strictPort --base $BASE_PATH",
			Base:    "keep",
		}, "Vite dev server, told its base path"
	case has("next"):
		return &PreviewSpec{
			Setup:   install,
			Command: "npx next dev -H $HOST -p $PORT",
			Base:    "keep",
		}, "Next.js: also needs basePath from $BASE_PATH in next.config (see the preview skill), or every asset 404s"
	case has("wrangler"):
		return &PreviewSpec{
			Setup:   install,
			Command: "npx wrangler dev --ip $HOST --port $PORT",
			Base:    "strip",
		}, "Cloudflare Worker under wrangler dev: it answers at /, so the prefix is stripped; links in its pages must be relative"
	}
	return nil, ""
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// PreviewCheck is the outcome of a dry run.
type PreviewCheck struct {
	OK       bool     `json:"ok"`
	Kind     string   `json:"kind"`
	URL      string   `json:"url,omitempty"` // what was fetched
	Status   int      `json:"status,omitempty"`
	Problems []string `json:"problems,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	Logs     string   `json:"logs,omitempty"` // the server's output, on failure
}

func (c *PreviewCheck) fail(format string, a ...any) {
	c.OK = false
	c.Problems = append(c.Problems, fmt.Sprintf(format, a...))
}

// CheckPreview runs a project's preview the way the dashboard would, fetches
// its page through the base path, and stops it again. projectDir is the
// project root; channelID decides $BASE_PATH.
func CheckPreview(ctx context.Context, projectDir, channelID string, spec *PreviewSpec, wait time.Duration) PreviewCheck {
	if spec.Command == "" {
		return checkStatic(projectDir, spec)
	}
	res := PreviewCheck{OK: true, Kind: "server"}
	dir, err := insideProject(projectDir, spec.Cwd)
	if err != nil {
		res.fail("%v", err)
		return res
	}
	port, err := freePort()
	if err != nil {
		res.fail("no free port: %v", err)
		return res
	}
	cmd, script := PreviewCommand(spec, dir, channelID, port)
	logs := &ringLog{max: 16 << 10}
	cmd.Stdout, cmd.Stderr = logs, logs
	res.Notes = append(res.Notes, fmt.Sprintf("ran: %s (cwd %s, PORT=%d, BASE_PATH=%s)", script, dir, port, PreviewBasePath(channelID)))
	if err := cmd.Start(); err != nil {
		res.fail("could not start: %v", err)
		return res
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	defer func() {
		killGroup(cmd.Process, false)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			killGroup(cmd.Process, true)
			<-done
		}
	}()

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(wait)
	up := false
	for !up && time.Now().Before(deadline) {
		select {
		case <-done:
			res.fail("the command exited before anything listened on $PORT (exit %d)", cmd.ProcessState.ExitCode())
			res.Logs = logs.String()
			return res
		case <-ctx.Done():
			res.fail("cancelled")
			return res
		case <-time.After(300 * time.Millisecond):
		}
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			up = true
		}
	}
	if !up {
		res.fail("nothing listened on 127.0.0.1:$PORT within %s: does the command pass $HOST and $PORT to the server?", wait)
		res.Logs = logs.String()
		return res
	}

	// What the proxy will ask for: the base path kept, or stripped to /.
	path := PreviewBasePath(channelID)
	if spec.Base == "strip" {
		path = "/"
	}
	res.URL = "http://" + addr + path
	status, body, ctype, err := fetchPage(ctx, res.URL, 30*time.Second)
	res.Status = status
	switch {
	case err != nil:
		res.fail("GET %s: %v", res.URL, err)
	case status >= 400:
		hint := ""
		if spec.Base != "strip" {
			hint = ` (with "base": "keep" the server must answer under $BASE_PATH: pass it the base, or switch to "strip")`
		}
		res.fail("GET %s answered %d%s", res.URL, status, hint)
	case strings.Contains(ctype, "html"):
		for _, ref := range badRefs(body, spec.Base, PreviewBasePath(channelID)) {
			res.fail("the page asks for %s, which goes to the dashboard instead of the app: make it relative or prefix it with $BASE_PATH", ref)
		}
	}
	if !res.OK {
		res.Logs = logs.String()
	}
	return res
}

func checkStatic(projectDir string, spec *PreviewSpec) PreviewCheck {
	res := PreviewCheck{OK: true, Kind: "static"}
	dir, err := insideProject(projectDir, spec.Static)
	if err != nil {
		res.fail("%v", err)
		return res
	}
	index := filepath.Join(dir, "index.html")
	body, err := os.ReadFile(index)
	if err != nil {
		res.fail("static %q has no index.html: the panel would show a file listing, or nothing", spec.Static)
		return res
	}
	res.URL = index
	for _, ref := range badRefs(string(body), "strip", "") {
		res.fail("index.html asks for %s: a static preview is served under /project/<channel>/, so links must be relative", ref)
	}
	return res
}

// insideProject resolves rel under the project folder and refuses a way out:
// the dashboard resolves cwd and static the same way, and would refuse too.
func insideProject(projectDir, rel string) (string, error) {
	root, err := filepath.Abs(projectDir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("%q is outside the project folder", rel)
	}
	st, err := os.Stat(full)
	if err != nil {
		return "", fmt.Errorf("%q does not exist in the project", rel)
	}
	if !st.IsDir() {
		return "", fmt.Errorf("%q is a file, not a folder", rel)
	}
	return full, nil
}

func fetchPage(ctx context.Context, u string, timeout time.Duration) (int, string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return 0, "", "", err
	}
	req.Header.Set("Accept", "text/html,*/*")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, "", "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(b), resp.Header.Get("Content-Type"), nil
}

var refAttr = regexp.MustCompile(`(?i)\b(?:src|href|action)\s*=\s*["'](/[^"'\s]*)["']`)

// badRefs lists the root-absolute URLs in a page that would escape its base:
// with "strip" any "/x" does, with "keep" any "/x" not under the base does.
// "//host/x" is another origin and fine.
func badRefs(html, base, prefix string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range refAttr.FindAllStringSubmatch(html, -1) {
		ref := m[1]
		if strings.HasPrefix(ref, "//") || seen[ref] {
			continue
		}
		if base != "strip" && prefix != "" && strings.HasPrefix(ref, prefix) {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
		if len(out) == 5 {
			break
		}
	}
	return out
}
