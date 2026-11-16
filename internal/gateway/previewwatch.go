package gateway

import (
	"bufio"
	"bytes"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// When did the project's files last change?
//
// An agent edits the project and the Preview beside it keeps showing the old
// app: the page was loaded before the edit and nothing told it to load again,
// and a server that reads its code once at start (a Python or Go app) keeps
// running the old code even when the page is reloaded. Lovable's preview
// follows the edit; this is what lets ours: the status carries when the
// project last changed, and the panel reloads — or restarts the server first,
// when what changed is the server's own code.

// serverCode are the files a running server loads once and does not reread:
// a change to one of these needs a restart, not a reload. JavaScript and
// TypeScript are not here — in a project with a dev server they are the
// frontend, which the dev server or a reload already picks up.
var serverCode = map[string]bool{
	".py": true, ".go": true, ".rb": true, ".php": true, ".java": true, ".kt": true,
	".rs": true, ".ex": true, ".exs": true, ".cs": true, ".scala": true, ".clj": true,
}

// hotReloading recognises a command whose server follows its own files
// (HMR, --reload, a watcher). Restarting one of those would only throw away
// its state.
var hotReloading = regexp.MustCompile(`(?i)\b(vite|next\s+dev|nuxt|astro\s+dev|webpack\s+serve|webpack-dev-server|remix\s+dev|svelte-kit\s+dev|nodemon|--reload|--watch|watchexec|air\b|uvicorn\s.*--reload|flask\s.*--debug|bun\s+--hot|deno\s.*--watch)`)

// skipDirs are never walked when the project is not a git repository.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "__pycache__": true, ".venv": true, "venv": true,
	".next": true, ".nuxt": true, ".cache": true, "dist": true, "build": true, "out": true,
	"target": true, ".turbo": true, ".svelte-kit": true,
}

const (
	changeTTL   = 2 * time.Second
	maxWalked   = 20000
	listTimeout = 3 * time.Second
)

type changeStamp struct {
	at          time.Time // when this was measured
	any, server time.Time
}

var (
	changeMu    sync.Mutex
	changeCache = map[string]changeStamp{}
)

// projectChanged reports the newest modification time among the project's
// own files, and among its server code. Files git ignores (renders, builds,
// caches) do not count: they change because the app ran, not because someone
// edited it. Measured at most once per changeTTL per folder.
func projectChanged(dir string) (anyAt, serverAt time.Time) {
	if dir == "" {
		return
	}
	changeMu.Lock()
	if c, ok := changeCache[dir]; ok && time.Since(c.at) < changeTTL {
		changeMu.Unlock()
		return c.any, c.server
	}
	changeMu.Unlock()

	note := func(path string, mt time.Time) {
		if mt.After(anyAt) {
			anyAt = mt
		}
		if serverCode[strings.ToLower(filepath.Ext(path))] && mt.After(serverAt) {
			serverAt = mt
		}
	}
	if files, ok := gitFiles(dir); ok {
		for _, rel := range files {
			if info, err := os.Stat(filepath.Join(dir, rel)); err == nil && !info.IsDir() {
				note(rel, info.ModTime())
			}
		}
	} else {
		n := 0
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if p != dir && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if n++; n > maxWalked {
				return filepath.SkipAll
			}
			if info, err := d.Info(); err == nil {
				note(p, info.ModTime())
			}
			return nil
		})
	}
	changeMu.Lock()
	changeCache[dir] = changeStamp{at: time.Now(), any: anyAt, server: serverAt}
	changeMu.Unlock()
	return
}

// gitFiles lists the project's tracked and untracked-but-not-ignored files,
// relative to dir. ok is false when dir is not a work tree (or git fails).
func gitFiles(dir string) ([]string, bool) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "ls-files", "-z", "--cached", "--others", "--exclude-standard").Output()
	if err != nil {
		return nil, false
	}
	var files []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, 0); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	for sc.Scan() {
		if s := sc.Text(); s != "" {
			files = append(files, s)
		}
	}
	return files, true
}
