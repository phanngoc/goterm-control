package coord

import (
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
)

// Every room has a wiki: a folder of documents — markdown, HTML, plain text —
// that people and agents keep, read in the dashboard and open as hosted pages.
//
// A project's wiki is the wiki/ folder inside its project folder, so it sits
// beside the code, travels with the repo and is served by /project/ as well.
// A room without a folder (general, a direct conversation) keeps its wiki
// under ~/goterm-shared/wiki/<channel>, which every agent may read.

// WikiFolder is the folder a project's wiki lives in.
const WikiFolder = "wiki"

// wikiMaxBytes bounds one page. A long report is ~100 KB; a page past 2 MB is
// something else (a data dump, a bundle) and belongs in artifacts.
const wikiMaxBytes = 2 << 20

// WikiPage is one document in a room's wiki.
type WikiPage struct {
	Path   string `json:"path"`   // relative to the wiki root, slash-separated
	Title  string `json:"title"`  // first heading or <title>, else the file name
	Format string `json:"format"` // md | html | txt | json | csv | other
	Bytes  int64  `json:"bytes"`
	Mtime  string `json:"mtime"`
}

// wikiExts are the formats a page may be saved as. Serving is not limited to
// these — an image a page links to is served too — but writing is.
var wikiExts = map[string]string{
	".md": "md", ".markdown": "md", ".html": "html", ".htm": "html",
	".txt": "txt", ".json": "json", ".csv": "csv", ".svg": "other",
}

// SharedWikiRoot is where rooms without a project folder keep their wiki.
// A variable so tests keep their wikis in a temp folder.
var SharedWikiRoot = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "goterm-shared", "wiki")
}

var safeChannelID = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)

// WikiDir is a room's wiki folder, created on first use.
func (db *DB) WikiDir(channelID string) (string, error) {
	c, err := db.GetChannel(channelID)
	if err != nil {
		return "", err
	}
	var dir string
	if c.Workspace != "" {
		dir = filepath.Join(c.Workspace, WikiFolder)
	} else {
		if !safeChannelID.MatchString(c.ID) || strings.HasPrefix(c.ID, ".") {
			return "", fmt.Errorf("coord: channel id %q cannot name a folder", c.ID)
		}
		dir = filepath.Join(SharedWikiRoot(), c.ID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("coord: wiki folder: %w", err)
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	return dir, nil
}

// WikiPath resolves a page inside a room's wiki, refusing anything that would
// land outside it: absolute paths, "..", hidden files, links pointing out.
func (db *DB) WikiPath(channelID, rel string) (string, error) {
	root, err := db.WikiDir(channelID)
	if err != nil {
		return "", err
	}
	rel = strings.TrimSpace(filepath.ToSlash(rel))
	if rel == "" || rel == "." {
		return root, nil
	}
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("coord: %q must be relative to the wiki", rel)
	}
	for _, seg := range strings.Split(rel, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("coord: %q is not a wiki page path", rel)
		}
	}
	full := resolveExisting(filepath.Clean(filepath.Join(root, filepath.FromSlash(rel))))
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("coord: %q is outside the wiki", rel)
	}
	return full, nil
}

// WikiFormat names a page's format from its extension.
func WikiFormat(name string) string {
	if f, ok := wikiExts[strings.ToLower(path.Ext(name))]; ok {
		return f
	}
	return "other"
}

// ListWiki is every page in a room's wiki, newest first. Images and other
// files a page links to are listed too, as format "other".
func (db *DB) ListWiki(channelID string) ([]WikiPage, error) {
	root, err := db.WikiDir(channelID)
	if err != nil {
		return nil, err
	}
	pages := []WikiPage{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if p != root && strings.HasPrefix(name, ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if rel, _ := filepath.Rel(root, p); strings.Count(rel, string(filepath.Separator)) >= 3 {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		pg := WikiPage{
			Path:   filepath.ToSlash(rel),
			Format: WikiFormat(name),
			Bytes:  info.Size(),
			Mtime:  info.ModTime().UTC().Format(time.RFC3339Nano),
		}
		pg.Title = wikiTitle(p, pg.Format, name)
		pages = append(pages, pg)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(pages, func(i, j int) bool { return pages[i].Mtime > pages[j].Mtime })
	if len(pages) > 1000 {
		pages = pages[:1000]
	}
	return pages, nil
}

var (
	mdHeading = regexp.MustCompile(`(?m)^#{1,2}\s+(.+?)\s*#*\s*$`)
	htmlTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	htmlH1    = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	htmlTag   = regexp.MustCompile(`<[^>]+>`)
)

// wikiTitle reads a page's title from its first 8 KB.
func wikiTitle(full, format, name string) string {
	fallback := strings.TrimSuffix(name, path.Ext(name))
	if format != "md" && format != "html" {
		return fallback
	}
	f, err := os.Open(full)
	if err != nil {
		return fallback
	}
	defer f.Close()
	buf := make([]byte, 8<<10)
	n, _ := f.Read(buf)
	head := string(buf[:n])
	var m []string
	if format == "md" {
		m = mdHeading.FindStringSubmatch(head)
	} else if m = htmlTitle.FindStringSubmatch(head); m == nil {
		m = htmlH1.FindStringSubmatch(head)
	}
	if m == nil {
		return fallback
	}
	t := strings.Join(strings.Fields(htmlTag.ReplaceAllString(m[1], "")), " ")
	if t == "" {
		return fallback
	}
	if r := []rune(t); len(r) > 120 {
		t = string(r[:119]) + "…"
	}
	return t
}

// ReadWiki returns one page and its mtime.
func (db *DB) ReadWiki(channelID, rel string) (body, mtime string, err error) {
	full, err := db.WikiPath(channelID, rel)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", "", fmt.Errorf("coord: %s: %w", rel, err)
	}
	if info.IsDir() {
		return "", "", fmt.Errorf("coord: %s is a folder", rel)
	}
	if info.Size() > wikiMaxBytes {
		return "", "", fmt.Errorf("coord: %s is %d bytes, over the %d a page may be", rel, info.Size(), wikiMaxBytes)
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return "", "", err
	}
	return string(b), info.ModTime().UTC().Format(time.RFC3339Nano), nil
}

// ErrWikiConflict is a save refused because the page changed since it was
// opened — an agent writes in this folder too.
var ErrWikiConflict = errors.New("conflict: the page changed since it was opened")

// WriteWiki saves a page. baseMtime, when set, is the mtime the editor opened
// it at; a page that has moved on since is not overwritten.
func (db *DB) WriteWiki(channelID, rel, body, baseMtime string) (mtime string, err error) {
	if _, ok := wikiExts[strings.ToLower(path.Ext(rel))]; !ok {
		return "", fmt.Errorf("coord: a wiki page is .md, .html, .txt, .json, .csv or .svg — not %q", path.Ext(rel))
	}
	if len(body) > wikiMaxBytes {
		return "", fmt.Errorf("coord: page is %d bytes, over the %d a page may be", len(body), wikiMaxBytes)
	}
	full, err := db.WikiPath(channelID, rel)
	if err != nil {
		return "", err
	}
	if baseMtime != "" {
		if info, err := os.Stat(full); err == nil && info.ModTime().UTC().Format(time.RFC3339Nano) != baseMtime {
			return "", ErrWikiConflict
		}
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, full); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		return "", err
	}
	return info.ModTime().UTC().Format(time.RFC3339Nano), nil
}

// DeleteWiki removes one page (not a folder).
func (db *DB) DeleteWiki(channelID, rel string) error {
	full, err := db.WikiPath(channelID, rel)
	if err != nil {
		return err
	}
	root, _ := db.WikiDir(channelID)
	if full == root {
		return fmt.Errorf("coord: refusing to delete the wiki itself")
	}
	info, err := os.Stat(full)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("coord: %s is a folder", rel)
	}
	return os.Remove(full)
}

// RenameWiki moves a page, keeping it a page.
func (db *DB) RenameWiki(channelID, from, to string) error {
	if _, ok := wikiExts[strings.ToLower(path.Ext(to))]; !ok {
		return fmt.Errorf("coord: %q is not a wiki page name", to)
	}
	src, err := db.WikiPath(channelID, from)
	if err != nil {
		return err
	}
	dst, err := db.WikiPath(channelID, to)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("coord: %s already exists", to)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}
