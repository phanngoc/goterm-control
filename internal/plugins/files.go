package plugins

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Browsing a plugin's folder: what it is made of, before deciding to trust
// it — the manifest, the scripts that would run before every tool call, the
// skill text the agent will read. Read-only on purpose: editing someone's
// plugin from a browser is how you end up trusting code you did not read.

// MaxFileBytes is how much of one file is shown.
const MaxFileBytes = 512 << 10

// maxTreeEntries bounds one listing. A plugin is a handful of files; past
// this it is vendored dependencies, and the listing says it stopped.
const maxTreeEntries = 2000

// Entry is one file or folder in a plugin.
type Entry struct {
	Path  string `json:"path"` // relative to the plugin folder, forward slashes
	Name  string `json:"name"`
	Dir   bool   `json:"dir"`
	Bytes int64  `json:"bytes,omitempty"`
	Mode  string `json:"mode,omitempty"` // "x" for an executable file
	// Link is set for a symlink, which is listed but never followed: a link
	// can point anywhere on the disk.
	Link string `json:"link,omitempty"`
}

// Tree lists every file and folder in a plugin, directories first at each
// level. .git is skipped — history is not what someone deciding to trust a
// plugin is looking at.
func Tree(dir string) (entries []Entry, truncated bool, err error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, false, err
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil // an unreadable corner is skipped, not fatal
		}
		if path == root {
			return nil
		}
		if d.Name() == ".git" && d.IsDir() {
			return filepath.SkipDir
		}
		if len(entries) >= maxTreeEntries {
			truncated = true
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(root, path)
		e := Entry{Path: filepath.ToSlash(rel), Name: d.Name(), Dir: d.IsDir()}
		if d.Type()&fs.ModeSymlink != 0 {
			e.Dir = false
			if target, err := os.Readlink(path); err == nil {
				e.Link = target
			}
		} else if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				e.Bytes = info.Size()
				if runnable(d.Name(), info.Mode()) {
					e.Mode = "x"
				}
			}
		}
		entries = append(entries, e)
		return nil
	})
	sortTree(entries)
	return entries, truncated, err
}

// sortTree orders a flat listing the way a tree reads: each folder followed
// by its contents, folders before files at every level.
func sortTree(entries []Entry) {
	key := func(e Entry) string {
		parts := strings.Split(e.Path, "/")
		var b strings.Builder
		for i, p := range parts {
			// Every ancestor is a folder ("0"); the last part is a folder or a
			// file ("1"), so files sort after folders that share their level.
			if i < len(parts)-1 || e.Dir {
				b.WriteString("0")
			} else {
				b.WriteString("1")
			}
			b.WriteString(strings.ToLower(p))
			b.WriteString("\x00")
		}
		return b.String()
	}
	sort.SliceStable(entries, func(i, j int) bool { return key(entries[i]) < key(entries[j]) })
}

// ReadFile returns one file of a plugin, confined to the plugin's folder:
// no "..", no absolute paths, and no symlink that resolves outside it.
func ReadFile(dir, rel string) (body string, truncated, binary bool, err error) {
	full, err := confine(dir, rel)
	if err != nil {
		return "", false, false, err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", false, false, err
	}
	if info.IsDir() {
		return "", false, false, fmt.Errorf("plugins: %s is a folder", rel)
	}
	f, err := os.Open(full)
	if err != nil {
		return "", false, false, err
	}
	defer f.Close()
	buf := make([]byte, MaxFileBytes)
	n, _ := f.Read(buf)
	data := buf[:n]
	// A NUL in the first block is the reliable tell of a binary file, which
	// is reported rather than shown as mojibake.
	if bytes.IndexByte(data, 0) >= 0 {
		return "", false, true, nil
	}
	return string(data), info.Size() > int64(n), false, nil
}

func confine(dir, rel string) (string, error) {
	rel = filepath.FromSlash(strings.TrimPrefix(rel, "/"))
	if rel == "" || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("plugins: %q is not a path inside the plugin", rel)
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	if r, err := filepath.Rel(root, real); err != nil || !filepath.IsLocal(r) {
		return "", fmt.Errorf("plugins: %s leads outside the plugin", rel)
	}
	return real, nil
}
