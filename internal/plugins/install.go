package plugins

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Installing a plugin from somewhere else: a git repository, or a folder on
// this machine.
//
// What arrives is someone else's code, so an installed plugin is its own
// scope (ScopeInstalled): its skills and prompt apply at once, but its
// commands, environment and MCP servers stay off until the owner trusts it —
// the same rule as a plugin that came with a repository. Installing or
// updating also WITHDRAWS any trust the name had: trust was given to the code
// that was there, not to whatever is fetched under the same name next.

// SourceFile records where an installed plugin came from, inside its folder.
const SourceFile = ".source.yaml"

// Install limits. A plugin is a manifest, some scripts and some instructions;
// anything past these is a repository that wants to be a dependency the
// plugin fetches, not something copied into every agent's workspace.
const (
	MaxInstallBytes = 64 << 20
	MaxInstallFiles = 5000
	cloneTimeout    = 2 * time.Minute
)

// Source is where a plugin is installed from.
type Source struct {
	// URL is a git URL (https://, ssh://, git@host:path) or a local folder.
	URL string `yaml:"url" json:"url"`
	// Ref is a branch or tag; empty is the repository's default branch.
	Ref string `yaml:"ref,omitempty" json:"ref,omitempty"`
	// Path is the plugin's folder inside the repository; empty is the root.
	Path string `yaml:"path,omitempty" json:"path,omitempty"`
	// Commit is what was installed, filled by Install.
	Commit      string    `yaml:"commit,omitempty" json:"commit,omitempty"`
	InstalledAt time.Time `yaml:"installed_at" json:"installed_at"`
}

// InstallOptions are the choices a person makes at install time.
type InstallOptions struct {
	// Name overrides the manifest's name for the installed folder.
	Name string
	// Replace allows overwriting an existing plugin of that name.
	Replace bool
	// Trust grants the plugin's code the right to run immediately.
	Trust bool
}

// Installed reports what Install did.
type Installed struct {
	Name     string   `json:"name"`
	Dir      string   `json:"dir"`
	Source   Source   `json:"source"`
	Trusted  bool     `json:"trusted"`
	Skipped  []string `json:"skipped,omitempty"` // symlinks left out
	Replaced bool     `json:"replaced"`
}

// Install fetches a plugin into an agent's workspace.
func Install(workspace string, src Source, opt InstallOptions) (*Installed, error) {
	if workspace == "" {
		return nil, fmt.Errorf("plugins: this agent has no workspace")
	}
	if src.Path != "" && !local(src.Path) {
		return nil, fmt.Errorf("plugins: path %q must stay inside the repository", src.Path)
	}

	fetched, commit, cleanup, err := fetch(src)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	src.Commit = commit

	from := filepath.Join(fetched, src.Path)
	m, err := ParseManifest(from)
	if err != nil {
		return nil, fmt.Errorf("plugins: %s is not a usable plugin: %w", describe(src), err)
	}
	name := m.Name
	if opt.Name != "" {
		name = opt.Name
	}
	if !ValidName(name) {
		return nil, fmt.Errorf("plugins: %q is not usable — lowercase letters, digits, - and _", name)
	}

	root := filepath.Join(workspace, Dir)
	dest := filepath.Join(root, name)
	_, statErr := os.Stat(dest)
	exists := statErr == nil
	if exists && !opt.Replace {
		return nil, fmt.Errorf("plugins: %s already exists (replace it with --replace, or `plugins update %s`)", dest, name)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}

	// Copy beside the destination and swap in with renames, so an agent's turn
	// reading this folder sees the old plugin or the new one, never half of it.
	stage, err := os.MkdirTemp(root, ".install-"+name+"-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	skipped, err := copyTree(from, stage)
	if err != nil {
		return nil, fmt.Errorf("plugins: copy %s: %w", describe(src), err)
	}
	src.InstalledAt = time.Now().UTC().Truncate(time.Second)
	if err := writeSource(stage, src); err != nil {
		return nil, err
	}
	if opt.Name != "" && opt.Name != m.Name {
		if err := renameManifest(stage, opt.Name); err != nil {
			return nil, err
		}
	}
	if err := swapIn(stage, dest, exists); err != nil {
		return nil, err
	}

	st, err := LoadState(workspace)
	if err != nil {
		return nil, err
	}
	st.SetTrusted(name, opt.Trust)
	if err := st.Save(workspace); err != nil {
		return nil, err
	}
	return &Installed{Name: name, Dir: dest, Source: src, Trusted: opt.Trust, Skipped: skipped, Replaced: exists}, nil
}

// Update re-installs a plugin from the source it was installed from.
func Update(workspace, name string, trust bool) (*Installed, error) {
	src, err := ReadSource(filepath.Join(workspace, Dir, name))
	if err != nil {
		return nil, fmt.Errorf("plugins: %s was not installed from a source (%v) — nothing to update from", name, err)
	}
	return Install(workspace, Source{URL: src.URL, Ref: src.Ref, Path: src.Path},
		InstallOptions{Name: name, Replace: true, Trust: trust})
}

// Uninstall removes a plugin folder from an agent's workspace and forgets the
// agent's switches for it. Its settings are forgotten too: a later install
// of the same name may be different code, and secrets meant for this one
// should not silently flow to it.
func Uninstall(workspace, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("plugins: %q is not a plugin name", name)
	}
	dir := filepath.Join(workspace, Dir, name)
	if _, err := os.Stat(filepath.Join(dir, File)); err != nil {
		return fmt.Errorf("plugins: this agent has no plugin %q in %s", name, filepath.Join(workspace, Dir))
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	st, err := LoadState(workspace)
	if err != nil {
		return err
	}
	st.SetTrusted(name, false)
	st.SetEnabled(name, true)
	delete(st.Config, name)
	return st.Save(workspace)
}

// Copy gives one agent's plugin to another, the way skills.Copy does for a
// skill. The receiving agent does not inherit trust: whether to run code is
// decided per agent.
func Copy(fromWorkspace, toWorkspace, name string) (*Installed, error) {
	if fromWorkspace == toWorkspace {
		return nil, fmt.Errorf("plugins: that is the same agent")
	}
	dir := filepath.Join(fromWorkspace, Dir, name)
	if !ValidName(name) || !isDir(dir) {
		return nil, fmt.Errorf("plugins: no plugin %q in %s", name, filepath.Join(fromWorkspace, Dir))
	}
	inst, err := Install(toWorkspace, Source{URL: dir}, InstallOptions{Name: name, Replace: true})
	if err != nil {
		return nil, err
	}
	// Keep the true origin, so `update` on the copy fetches upstream rather
	// than the peer's folder.
	if orig, err := ReadSource(dir); err == nil {
		inst.Source.URL, inst.Source.Ref, inst.Source.Path, inst.Source.Commit = orig.URL, orig.Ref, orig.Path, orig.Commit
		if err := writeSource(inst.Dir, inst.Source); err != nil {
			return nil, err
		}
	}
	return inst, nil
}

// ReadSource returns where an installed plugin came from.
func ReadSource(dir string) (Source, error) {
	var s Source
	data, err := os.ReadFile(filepath.Join(dir, SourceFile))
	if err != nil {
		return s, err
	}
	return s, yaml.Unmarshal(data, &s)
}

func writeSource(dir string, s Source) error {
	data, err := yaml.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, SourceFile), data, 0o644)
}

// fetch makes the source available as a local folder.
func fetch(src Source) (dir, commit string, cleanup func(), err error) {
	cleanup = func() {}
	u := strings.TrimSpace(src.URL)
	if u == "" {
		return "", "", cleanup, fmt.Errorf("plugins: nothing to install from")
	}
	localDir := isDir(u)
	if localDir && src.Ref == "" {
		abs, _ := filepath.Abs(u)
		return abs, "", cleanup, nil
	}
	if !localDir && !gitURL(u) {
		return "", "", cleanup, fmt.Errorf("plugins: %q is neither a folder nor a git URL (https://, ssh://, git@host:repo)", u)
	}
	tmp, err := os.MkdirTemp("", "bomclaw-plugin-*")
	if err != nil {
		return "", "", cleanup, err
	}
	cleanup = func() { os.RemoveAll(tmp) }

	ctx, cancel := context.WithTimeout(context.Background(), cloneTimeout)
	defer cancel()
	// ext:: runs arbitrary commands, and a URL is user input.
	args := []string{"-c", "protocol.ext.allow=never", "clone", "--depth", "1", "--quiet"}
	if src.Ref != "" {
		args = append(args, "--branch", src.Ref)
	}
	args = append(args, "--", u, tmp)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0") // never hang on a password prompt
	if out, err := cmd.CombinedOutput(); err != nil {
		cleanup()
		return "", "", func() {}, fmt.Errorf("plugins: git clone %s: %v: %s", u, err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("git", "-C", tmp, "rev-parse", "HEAD").Output(); err == nil {
		commit = strings.TrimSpace(string(out))
	}
	return tmp, commit, cleanup, nil
}

func gitURL(u string) bool {
	if strings.HasPrefix(u, "-") {
		return false
	}
	for _, p := range []string{"https://", "http://", "ssh://", "git://", "file://"} {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	// scp-like: git@github.com:owner/repo.git
	at, colon := strings.Index(u, "@"), strings.Index(u, ":")
	return at > 0 && colon > at && !strings.Contains(u[:colon], "/")
}

func describe(s Source) string {
	out := s.URL
	if s.Ref != "" {
		out += "@" + s.Ref
	}
	if s.Path != "" {
		out += " (" + s.Path + ")"
	}
	return out
}

// copyTree copies a plugin folder, leaving out .git and every symlink: a
// link can point anywhere on the disk, and following it would install a file
// the manifest never mentioned. Executable bits are kept — bin/ depends on
// them.
func copyTree(from, to string) (skipped []string, err error) {
	var bytes int64
	files := 0
	err = filepath.WalkDir(from, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, _ := filepath.Rel(from, path)
		if rel == "." {
			return nil
		}
		if d.Name() == ".git" || rel == SourceFile {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			skipped = append(skipped, rel)
			return nil
		}
		dest := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		if !d.Type().IsRegular() {
			skipped = append(skipped, rel)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files++
		bytes += info.Size()
		if files > MaxInstallFiles || bytes > MaxInstallBytes {
			return fmt.Errorf("larger than %d files / %d MB", MaxInstallFiles, MaxInstallBytes>>20)
		}
		return copyFile(path, dest, info.Mode().Perm()&0o755|0o644)
	})
	return skipped, err
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// renameManifest rewrites the name field when a plugin is installed under a
// name of the installer's choosing, so the folder and the manifest agree.
func renameManifest(dir, name string) error {
	path := filepath.Join(dir, File)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("plugins: %s is not a mapping", path)
	}
	m := doc.Content[0]
	set := false
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "name" {
			m.Content[i+1].Value, m.Content[i+1].Tag, set = name, "!!str", true
		}
	}
	if !set {
		m.Content = append([]*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "name"},
			{Kind: yaml.ScalarNode, Value: name},
		}, m.Content...)
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// swapIn moves a staged folder into place, replacing what is there.
func swapIn(stage, dest string, exists bool) error {
	if !exists {
		return os.Rename(stage, dest)
	}
	// Dot-prefixed, so Load never mistakes the outgoing copy for a plugin.
	old := filepath.Join(filepath.Dir(dest), fmt.Sprintf(".old-%s-%d", filepath.Base(dest), time.Now().UnixNano()))
	if err := os.Rename(dest, old); err != nil {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		_ = os.Rename(old, dest) // put the previous version back
		return err
	}
	return os.RemoveAll(old)
}
