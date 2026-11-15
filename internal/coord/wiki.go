package coord

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Every project has a wiki: documents — markdown, HTML, plain text, and the
// pictures they show — that people and agents keep, read in the dashboard and
// open as hosted pages.
//
// The pages are rows in the shared database, owned by the project (its room).
// They used to be files in <project>/wiki/, which meant a wiki existed only on
// the disk of whoever had that folder, could not be listed or searched with
// the rest of the project, and was overwritten by a stray `git checkout`.
// Rows are read the same way by every agent and the dashboard, and a save
// that races another is caught instead of silently lost.
//
// A wiki that still has files from before is imported the first time it is
// opened. The files are left where they were; the database is the wiki now.

// WikiFolder is where a project kept its wiki before the database did.
const WikiFolder = "wiki"

// wikiMaxBytes bounds one page. A long report is ~100 KB; a page past 2 MB is
// something else (a data dump, a bundle) and belongs in artifacts.
const wikiMaxBytes = 2 << 20

// WikiPage is one document in a project's wiki.
type WikiPage struct {
	Path      string `json:"path"`   // slash-separated, e.g. design/gdd.md
	Title     string `json:"title"`  // first heading or <title>, else the file name
	Format    string `json:"format"` // md | html | txt | json | csv | other
	Bytes     int64  `json:"bytes"`
	Mtime     string `json:"mtime"` // when it last changed; the save-conflict token
	UpdatedBy string `json:"updated_by,omitempty"`
}

// wikiExts are the formats a page may be saved as. Pictures are allowed so a
// page can show one from its own wiki; they are listed as format "other".
var wikiExts = map[string]string{
	".md": "md", ".markdown": "md", ".html": "html", ".htm": "html",
	".txt": "txt", ".json": "json", ".csv": "csv", ".svg": "other",
	".png": "other", ".jpg": "other", ".jpeg": "other", ".gif": "other", ".webp": "other",
}

var wikiSchema = []string{
	`CREATE TABLE IF NOT EXISTS wiki_pages (
		channel_id TEXT NOT NULL,
		path       TEXT NOT NULL,
		format     TEXT NOT NULL,
		title      TEXT NOT NULL DEFAULT '',
		body       BLOB NOT NULL,
		bytes      INTEGER NOT NULL,
		created_by TEXT NOT NULL DEFAULT '',
		updated_by TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (channel_id, path)
	) STRICT`,
	`CREATE INDEX IF NOT EXISTS idx_wiki_pages_recent ON wiki_pages(channel_id, updated_at)`,
}

// SharedWikiRoot is where rooms without a project folder kept their wiki
// files, read once to import them. A variable so tests use a temp folder.
var SharedWikiRoot = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "goterm-shared", "wiki")
}

var safeChannelID = regexp.MustCompile(`^[A-Za-z0-9_.@-]+$`)

// cleanWikiPath checks a page path: relative, no "..", no hidden segments.
func cleanWikiPath(rel string) (string, error) {
	rel = strings.Trim(strings.TrimSpace(filepath.ToSlash(rel)), "/")
	if rel == "" || rel == "." {
		return "", fmt.Errorf("coord: a page path is required")
	}
	if strings.HasPrefix(strings.TrimSpace(filepath.ToSlash(rel)), "/") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("coord: %q must be relative to the wiki", rel)
	}
	segs := strings.Split(rel, "/")
	if len(segs) > 4 {
		return "", fmt.Errorf("coord: %q is nested too deep", rel)
	}
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") || strings.ContainsAny(seg, "\\\x00") {
			return "", fmt.Errorf("coord: %q is not a wiki page path", rel)
		}
	}
	return rel, nil
}

// checkWikiPath is cleanWikiPath for a path as typed, so "/etc/passwd.md"
// is refused rather than quietly made relative.
func checkWikiPath(rel string) (string, error) {
	r := strings.TrimSpace(filepath.ToSlash(rel))
	if strings.HasPrefix(r, "/") || filepath.IsAbs(r) {
		return "", fmt.Errorf("coord: %q must be relative to the wiki", rel)
	}
	return cleanWikiPath(r)
}

// WikiFormat names a page's format from its extension.
func WikiFormat(name string) string {
	if f, ok := wikiExts[strings.ToLower(path.Ext(name))]; ok {
		return f
	}
	return "other"
}

// wikiOwner checks the room exists; a wiki belongs to one.
func (db *DB) wikiOwner(channelID string) (*Channel, error) {
	if channelID == "" {
		return nil, fmt.Errorf("coord: a wiki belongs to a project — channel is required")
	}
	return db.GetChannel(channelID)
}

// ListWiki is every page in a project's wiki, newest first.
func (db *DB) ListWiki(channelID string) ([]WikiPage, error) {
	if _, err := db.wikiOwner(channelID); err != nil {
		return nil, err
	}
	db.importWikiFiles(channelID)
	rows, err := db.conn.Query(`SELECT path, title, format, bytes, updated_at, updated_by
		FROM wiki_pages WHERE channel_id = ? ORDER BY updated_at DESC LIMIT 1000`, channelID)
	if err != nil {
		return nil, fmt.Errorf("coord: list wiki: %w", err)
	}
	defer rows.Close()
	pages := []WikiPage{}
	for rows.Next() {
		var p WikiPage
		if err := rows.Scan(&p.Path, &p.Title, &p.Format, &p.Bytes, &p.Mtime, &p.UpdatedBy); err != nil {
			return nil, err
		}
		pages = append(pages, p)
	}
	return pages, rows.Err()
}

var (
	mdHeading = regexp.MustCompile(`(?m)^#{1,2}\s+(.+?)\s*#*\s*$`)
	htmlTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	htmlH1    = regexp.MustCompile(`(?is)<h1[^>]*>(.*?)</h1>`)
	htmlTag   = regexp.MustCompile(`<[^>]+>`)
)

// wikiTitle reads a page's title from its first 8 KB.
func wikiTitle(body []byte, format, name string) string {
	base := path.Base(name)
	fallback := strings.TrimSuffix(base, path.Ext(base))
	if format != "md" && format != "html" {
		return fallback
	}
	head := string(body)
	if len(head) > 8<<10 {
		head = head[:8<<10]
	}
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

// ErrWikiNotFound is a page that does not exist.
var ErrWikiNotFound = errors.New("coord: no such wiki page")

// ReadWikiBytes returns one page's raw bytes and when it last changed.
func (db *DB) ReadWikiBytes(channelID, rel string) (body []byte, mtime string, err error) {
	if _, err := db.wikiOwner(channelID); err != nil {
		return nil, "", err
	}
	rel, err = checkWikiPath(rel)
	if err != nil {
		return nil, "", err
	}
	db.importWikiFiles(channelID)
	err = db.conn.QueryRow(`SELECT body, updated_at FROM wiki_pages WHERE channel_id = ? AND path = ?`,
		channelID, rel).Scan(&body, &mtime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", fmt.Errorf("%w: %s", ErrWikiNotFound, rel)
	}
	return body, mtime, err
}

// ReadWiki returns one page as text and when it last changed.
func (db *DB) ReadWiki(channelID, rel string) (body, mtime string, err error) {
	b, m, err := db.ReadWikiBytes(channelID, rel)
	return string(b), m, err
}

// ErrWikiConflict is a save refused because the page changed since it was
// opened — an agent writes to the same wiki.
var ErrWikiConflict = errors.New("conflict: the page changed since it was opened")

// WriteWiki saves a page. baseMtime, when set, is the mtime the editor opened
// it at; a page that has moved on since is not overwritten. by is who saved.
func (db *DB) WriteWiki(channelID, rel, body, baseMtime, by string) (mtime string, err error) {
	if _, err := db.wikiOwner(channelID); err != nil {
		return "", err
	}
	rel, err = checkWikiPath(rel)
	if err != nil {
		return "", err
	}
	if _, ok := wikiExts[strings.ToLower(path.Ext(rel))]; !ok {
		return "", fmt.Errorf("coord: a wiki page is .md, .html, .txt, .json, .csv, .svg or a picture — not %q", path.Ext(rel))
	}
	if len(body) > wikiMaxBytes {
		return "", fmt.Errorf("coord: page is %d bytes, over the %d a page may be", len(body), wikiMaxBytes)
	}
	db.importWikiFiles(channelID)
	return db.putWiki(channelID, rel, []byte(body), baseMtime, by, time.Now())
}

func (db *DB) putWiki(channelID, rel string, body []byte, baseMtime, by string, at time.Time) (string, error) {
	format := WikiFormat(rel)
	title := wikiTitle(body, format, rel)
	tx, err := db.conn.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var cur string
	err = tx.QueryRow(`SELECT updated_at FROM wiki_pages WHERE channel_id = ? AND path = ?`, channelID, rel).Scan(&cur)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if baseMtime != "" && exists && cur != baseMtime {
		return "", ErrWikiConflict
	}
	now := ts(at)
	if exists && now <= cur {
		// Two saves inside one clock tick must still give two tokens, or a
		// conflict between them could not be seen.
		now = ts(parseTS(cur).Add(time.Nanosecond))
	}
	if exists {
		_, err = tx.Exec(`UPDATE wiki_pages SET format = ?, title = ?, body = ?, bytes = ?, updated_by = ?, updated_at = ?
			WHERE channel_id = ? AND path = ?`, format, title, body, len(body), by, now, channelID, rel)
	} else {
		_, err = tx.Exec(`INSERT INTO wiki_pages (channel_id, path, format, title, body, bytes, created_by, updated_by, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, channelID, rel, format, title, body, len(body), by, by, now, now)
	}
	if err != nil {
		return "", fmt.Errorf("coord: save wiki %s: %w", rel, err)
	}
	return now, tx.Commit()
}

// DeleteWiki removes one page.
func (db *DB) DeleteWiki(channelID, rel string) error {
	if _, err := db.wikiOwner(channelID); err != nil {
		return err
	}
	rel, err := checkWikiPath(rel)
	if err != nil {
		return err
	}
	db.importWikiFiles(channelID)
	res, err := db.conn.Exec(`DELETE FROM wiki_pages WHERE channel_id = ? AND path = ?`, channelID, rel)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrWikiNotFound, rel)
	}
	return nil
}

// RenameWiki moves a page, keeping it a page.
func (db *DB) RenameWiki(channelID, from, to string) error {
	if _, err := db.wikiOwner(channelID); err != nil {
		return err
	}
	from, err := checkWikiPath(from)
	if err != nil {
		return err
	}
	to, err = checkWikiPath(to)
	if err != nil {
		return err
	}
	if _, ok := wikiExts[strings.ToLower(path.Ext(to))]; !ok {
		return fmt.Errorf("coord: %q is not a wiki page name", to)
	}
	db.importWikiFiles(channelID)
	var n int
	if err := db.conn.QueryRow(`SELECT count(*) FROM wiki_pages WHERE channel_id = ? AND path = ?`, channelID, to).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("coord: %s already exists", to)
	}
	res, err := db.conn.Exec(`UPDATE wiki_pages SET path = ?, format = ?, updated_at = ? WHERE channel_id = ? AND path = ?`,
		to, WikiFormat(to), ts(time.Now()), channelID, from)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: %s", ErrWikiNotFound, from)
	}
	return nil
}

// importWikiFiles brings a wiki that lived in files into the database, once
// per project. Best effort: a file that cannot be read is skipped, and a page
// already in the database is never overwritten by its older file.
func (db *DB) importWikiFiles(channelID string) {
	key := "wiki_imported:" + channelID
	var done string
	if db.conn.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&done) == nil {
		return
	}
	defer db.conn.Exec(`INSERT INTO meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING`, key, ts(time.Now()))

	c, err := db.GetChannel(channelID)
	if err != nil {
		return
	}
	var dirs []string
	if c.Workspace != "" {
		dirs = append(dirs, filepath.Join(c.Workspace, WikiFolder))
	}
	if safeChannelID.MatchString(c.ID) && !strings.HasPrefix(c.ID, ".") {
		dirs = append(dirs, filepath.Join(SharedWikiRoot(), c.ID))
	}
	for _, root := range dirs {
		if real, err := filepath.EvalSymlinks(root); err == nil {
			root = real
		} else {
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if p != root && strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if d.IsDir() || d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return nil
			}
			rel, err = cleanWikiPath(rel)
			if err != nil {
				return nil
			}
			if _, ok := wikiExts[strings.ToLower(path.Ext(rel))]; !ok {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.Size() > wikiMaxBytes {
				return nil
			}
			var n int
			if db.conn.QueryRow(`SELECT count(*) FROM wiki_pages WHERE channel_id = ? AND path = ?`, channelID, rel).Scan(&n); n > 0 {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			_, _ = db.putWiki(channelID, rel, b, "", "import", info.ModTime())
			return nil
		})
	}
}
