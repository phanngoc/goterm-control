package coord

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Looking inside a project's folder.
//
// The folder is the point of a project — it is where the work lands — and until
// now the only way to see what was in it was a terminal. That is fine for the
// person who set the machine up and useless for anyone else, including for
// checking whether an agent actually wrote the thing it said it wrote.
//
// Everything here is confined to one project's workspace. The guard is not
// decoration: a path arrives from a browser, and "../../.ssh/id_rsa" is the
// first thing anyone tries.

// MaxProjectFileBytes is how much of a file travels to the browser. Enough for
// source and reports; past it the reader is told to open the file itself
// rather than handed a silent half.
const MaxProjectFileBytes = 512 << 10

// ProjectEntry is one name in a project folder.
type ProjectEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"` // relative to the workspace, forward slashes
	Dir   bool   `json:"dir"`
	Bytes int64  `json:"bytes"`
	Mtime string `json:"mtime"`
}

// projectPath resolves a workspace-relative path and refuses anything that
// leaves the workspace — through "..", through an absolute path, or through a
// symlink pointing out.
func (db *DB) projectPath(channelID, rel string) (string, error) {
	c, err := db.GetChannel(channelID)
	if err != nil {
		return "", err
	}
	if c.Workspace == "" {
		return "", fmt.Errorf("coord: %s is a room, not a project — it has no folder", c.Name)
	}
	// An absolute path is refused rather than quietly reinterpreted. Join would
	// treat "/tmp/x" as "<workspace>/tmp/x" — safe, but surprising: the caller
	// asked for one thing and got a nested directory they did not mean to
	// create. A test wrote /tmp/victim.txt into a project this way.
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("coord: %q must be relative to the project folder", rel)
	}
	root, err := filepath.EvalSymlinks(c.Workspace)
	if err != nil {
		root = filepath.Clean(c.Workspace)
	}
	full := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))

	// Compare after resolving symlinks: a link inside the folder pointing at
	// /etc would otherwise pass a string check. A path that does not exist yet
	// — a file about to be created, a rename's target — is resolved through
	// its nearest existing ancestor, or "link-to-etc/new.txt" would slip by.
	full = resolveExisting(full)
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("coord: %q is outside the project folder", rel)
	}
	return full, nil
}

// resolveExisting resolves symlinks in the longest existing prefix of p and
// re-attaches the rest.
func resolveExisting(p string) string {
	rest := ""
	cur := p
	for {
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			if rest == "" {
				return resolved
			}
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// ProjectFilePath resolves one path inside a project to somewhere on disk, or
// refuses it.
//
// Exported so the gateway can serve a project's own files back — a board, a
// report, a page an agent built. Reading the bytes through ReadProjectFile
// would not do: that one truncates, flags binaries and returns a string,
// because it exists to show a file to a person in a text pane. Serving needs
// the file itself, with its own type and length, so the page's own relative
// requests for its JSON and its images resolve the way they do on disk.
//
// The refusal is the same one every other caller gets: absolute paths, and
// anything that escapes the folder after symlinks are resolved.
func (db *DB) ProjectFilePath(channelID, rel string) (string, error) {
	return db.projectPath(channelID, rel)
}

// ProjectFiles lists one directory inside a project, directories first.
func (db *DB) ProjectFiles(channelID, rel string) ([]ProjectEntry, error) {
	full, err := db.projectPath(channelID, rel)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(full)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rel, err)
	}
	out := make([]ProjectEntry, 0, len(items))
	for _, it := range items {
		info, err := it.Info()
		if err != nil {
			continue // vanished between listing and stat; not worth failing the page
		}
		e := ProjectEntry{
			Name: it.Name(),
			Path: strings.TrimPrefix(filepath.ToSlash(filepath.Join(rel, it.Name())), "/"),
			Dir:  it.IsDir(),
		}
		if !e.Dir {
			e.Bytes = info.Size()
		}
		e.Mtime = info.ModTime().UTC().Format("2006-01-02T15:04:05Z")
		out = append(out, e)
	}
	// Directories first, then by name: the shape people expect, and it puts
	// the parts of a project above its loose files.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// WriteProjectFile replaces a file inside a project, creating it if it is not
// there yet.
//
// Confined like the reads, and atomic for the same reason the brief is: agents
// have this folder open and one reading mid-save would get half a file and act
// on it.
//
// It refuses to overwrite a binary file. The browser could not show you one, so
// a save that lands on it is a save made blind — most likely a path typed by
// hand, and the outcome is a destroyed asset with no undo.
func (db *DB) WriteProjectFile(channelID, rel, body string) error {
	if strings.TrimSpace(rel) == "" {
		return fmt.Errorf("coord: which file?")
	}
	full, err := db.projectPath(channelID, rel)
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(full); err == nil {
		if info.IsDir() {
			return fmt.Errorf("coord: %s is a directory", rel)
		}
		mode = info.Mode()
		if err := db.checkEditable(channelID, rel, full); err != nil {
			return err
		}
		if _, _, binary, err := db.ReadProjectFile(channelID, rel); err == nil && binary {
			return fmt.Errorf("coord: %s is a binary file — editing it here would destroy it", rel)
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(full), ".edit-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	_ = os.Chmod(tmp.Name(), mode)
	if err := os.Rename(tmp.Name(), full); err != nil {
		return fmt.Errorf("replace %s: %w", rel, err)
	}
	return nil
}

// ReadProjectFile returns a file's text, whether it was cut, and whether it is
// binary — which is reported rather than rendered, because a browser showing a
// PNG as mojibake is worse than one saying it cannot.
func (db *DB) ReadProjectFile(channelID, rel string) (body string, truncated, binary bool, err error) {
	full, err := db.projectPath(channelID, rel)
	if err != nil {
		return "", false, false, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", false, false, fmt.Errorf("read %s: %w", rel, err)
	}
	if info.IsDir() {
		return "", false, false, fmt.Errorf("coord: %s is a directory", rel)
	}
	f, err := os.Open(full)
	if err != nil {
		return "", false, false, fmt.Errorf("read %s: %w", rel, err)
	}
	defer f.Close()

	buf := make([]byte, MaxProjectFileBytes)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "", false, false, nil // empty file reads as EOF
	}
	data := buf[:n]
	// A NUL in the first block is the cheap, reliable tell. Text files do not
	// contain them; every binary format this would otherwise mangle does.
	if bytes.IndexByte(data, 0) >= 0 {
		return "", false, true, nil
	}
	return string(data), info.Size() > int64(n), false, nil
}

// ProjectFileMtime is a file's modification time, precise enough to tell two
// saves in the same second apart. The editor sends back the one it opened, so
// a save can refuse to overwrite what an agent wrote in the meantime.
func (db *DB) ProjectFileMtime(channelID, rel string) (string, error) {
	full, err := db.projectPath(channelID, rel)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	return info.ModTime().UTC().Format(time.RFC3339Nano), nil
}

// A file bigger than what reaches the browser was shown cut, so a save of it
// would drop everything past the cut. Refused here and not only in the page,
// because a page is one client and this is every client.
func (db *DB) checkEditable(channelID, rel, full string) error {
	info, err := os.Stat(full)
	if err != nil {
		return nil // new file
	}
	if !info.IsDir() && info.Size() > MaxProjectFileBytes {
		return fmt.Errorf("coord: %s is larger than %d KB — too large to edit here", rel, MaxProjectFileBytes>>10)
	}
	return nil
}

// entryPath resolves rel to the directory entry itself, without following a
// symlink in its last component. Delete and rename act on names: deleting a
// link called "current" that points at "v2/" must remove the link, not v2.
func (db *DB) entryPath(channelID, rel string) (string, error) {
	clean := path.Clean(filepath.ToSlash(rel))
	base := path.Base(clean)
	if clean == "." || clean == "/" || base == ".." {
		return "", fmt.Errorf("coord: %q names no file", rel)
	}
	dir := path.Dir(clean)
	if dir == "." {
		dir = ""
	}
	parent, err := db.projectPath(channelID, dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, base), nil
}

// MakeProjectDir creates a folder (and its parents) inside a project.
func (db *DB) MakeProjectDir(channelID, rel string) error {
	if strings.TrimSpace(rel) == "" {
		return fmt.Errorf("coord: which folder?")
	}
	full, err := db.projectPath(channelID, rel)
	if err != nil {
		return err
	}
	if info, err := os.Stat(full); err == nil {
		if info.IsDir() {
			return fmt.Errorf("coord: %s already exists", rel)
		}
		return fmt.Errorf("coord: %s is a file", rel)
	}
	return os.MkdirAll(full, 0o755)
}

// RenameProjectPath moves a file or folder within one project. It will not
// overwrite: a rename that lands on an existing name destroys that file, and
// nothing about a rename says the person meant to.
func (db *DB) RenameProjectPath(channelID, from, to string) error {
	if strings.TrimSpace(from) == "" || strings.TrimSpace(to) == "" {
		return fmt.Errorf("coord: rename needs a source and a target")
	}
	src, err := db.entryPath(channelID, from)
	if err != nil {
		return err
	}
	dst, err := db.entryPath(channelID, to)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(src); err != nil {
		return fmt.Errorf("rename %s: %w", from, err)
	}
	if _, err := os.Lstat(dst); err == nil {
		return fmt.Errorf("coord: %s already exists", to)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("rename %s: %w", from, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("rename %s: %w", from, err)
	}
	return nil
}

// DeleteProjectPath removes a file, or a folder with everything in it. The
// project folder itself is refused: that is deleting the project, which is
// not something a file tree's delete button should be able to do.
func (db *DB) DeleteProjectPath(channelID, rel string) error {
	if strings.TrimSpace(rel) == "" {
		return fmt.Errorf("coord: which file?")
	}
	full, err := db.entryPath(channelID, rel)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(full); err != nil {
		return fmt.Errorf("delete %s: %w", rel, err)
	}
	return os.RemoveAll(full)
}

// Finding things in a project: every file by name (the editor's Cmd+P) and
// every line that matches (Cmd+Shift+F).
//
// Both walk the folder on each call rather than keeping an index: agents write
// here constantly, and a stale index answers "no such file" about the file an
// agent created a minute ago. A project is hundreds of files, not millions,
// and the caps below keep an unusual one from holding the page.

// skipDirs are folders nobody means when they search their project: tool
// caches and dependency trees, often larger than the project itself.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true, ".venv": true, "venv": true,
	".mypy_cache": true, ".pytest_cache": true, ".next": true, ".cache": true, "target": true,
}

const (
	// MaxIndexedFiles caps the name index.
	MaxIndexedFiles = 20000
	// MaxSearchMatches caps one content search.
	MaxSearchMatches = 1000
	// maxSearchFileBytes skips big files — generated JSON, logs, dumps — which
	// would dominate both the time and the results.
	maxSearchFileBytes = 1 << 20
)

// walkProject calls fn for each regular file under the project, skipping
// skipDirs and never following a symlink out (WalkDir does not follow them).
// fn returns false to stop.
func (db *DB) walkProject(channelID string, fn func(rel, full string, size int64) bool) error {
	root, err := db.projectPath(channelID, "")
	if err != nil {
		return err
	}
	stop := errors.New("stop")
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, keep the rest
		}
		if d.IsDir() {
			if p != root && skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if !fn(filepath.ToSlash(rel), p, info.Size()) {
			return stop
		}
		return nil
	})
	if errors.Is(err, stop) {
		return nil
	}
	return err
}

// ProjectFileIndex lists every file in a project by path, and whether the
// list was cut at MaxIndexedFiles.
func (db *DB) ProjectFileIndex(channelID string) (paths []string, truncated bool, err error) {
	err = db.walkProject(channelID, func(rel, _ string, _ int64) bool {
		if len(paths) >= MaxIndexedFiles {
			truncated = true
			return false
		}
		paths = append(paths, rel)
		return true
	})
	return paths, truncated, err
}

// SearchMatch is one line that matched.
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"` // 1-based
	Col  int    `json:"col"`  // 1-based, in characters
	Len  int    `json:"len"`  // match length in characters
	Text string `json:"text"` // the line, cut to a readable length
	At   int    `json:"at"`   // where the match starts in Text, in characters
}

// SearchOptions shapes a content search.
type SearchOptions struct {
	CaseSensitive bool
	Regex         bool
	WholeWord     bool
}

// SearchProject finds lines matching query in the project's text files.
// Binary files and files over 1 MB are skipped; the result says how many.
func (db *DB) SearchProject(channelID, query string, opt SearchOptions) (matches []SearchMatch, skipped int, truncated bool, err error) {
	if query == "" {
		return nil, 0, false, fmt.Errorf("coord: search for what?")
	}
	pattern := query
	if !opt.Regex {
		pattern = regexp.QuoteMeta(query)
	}
	if opt.WholeWord {
		pattern = `\b(?:` + pattern + `)\b`
	}
	if !opt.CaseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, 0, false, fmt.Errorf("coord: bad pattern: %w", err)
	}

	err = db.walkProject(channelID, func(rel, full string, size int64) bool {
		if size > maxSearchFileBytes {
			skipped++
			return true
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return true
		}
		if bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			return true // binary
		}
		for i, line := range strings.Split(string(data), "\n") {
			loc := re.FindStringIndex(line)
			if loc == nil || loc[0] == loc[1] {
				continue
			}
			if len(matches) >= MaxSearchMatches {
				truncated = true
				return false
			}
			matches = append(matches, searchMatch(rel, i+1, strings.TrimRight(line, "\r"), loc))
		}
		return true
	})
	return matches, skipped, truncated, err
}

// searchMatch cuts a long line around its match — a minified file is one line
// of a megabyte — and reports positions in characters, which is what an
// editor counts.
func searchMatch(rel string, line int, text string, loc []int) SearchMatch {
	const before, maxRunes = 40, 240
	col := utf8.RuneCountInString(text[:loc[0]]) + 1
	n := utf8.RuneCountInString(text[loc[0]:loc[1]])
	runes := []rune(text)
	start := 0
	if col-1 > before {
		start = col - 1 - before
	}
	end := min(len(runes), start+maxRunes)
	shown := string(runes[start:end])
	at := col - 1 - start
	if start > 0 {
		shown = "…" + shown
		at++
	}
	if end < len(runes) {
		shown += "…"
	}
	return SearchMatch{Path: rel, Line: line, Col: col, Len: n, Text: shown, At: at}
}
