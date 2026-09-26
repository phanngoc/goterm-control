package gateway

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
)

// Serving a project's own files back, so the thing the work produced can be
// looked at.
//
// A project that builds a board, a report, a page — the dashboard could browse
// its files and show the source, and that is not the same as seeing it. The
// artifact viewer does open an HTML artifact, but a single file opened from a
// blob has no folder around it: a board that fetches ../data/latest.json gets
// nothing, and shows an empty page or its own "no data" fallback. The result of
// the work was only visible to whoever could reach the machine and run a static
// server by hand.
//
// So the gateway serves the folder. The page's own relative requests resolve
// the way they do on disk, which is the whole difference between reading a file
// and seeing a result.
//
// The boundary is the one every other project-file caller already uses:
// coord.ProjectFilePath refuses absolute paths and anything that leaves the
// folder once symlinks are resolved, and it is the guard the escape tests are
// written against. Nothing here re-implements that check, because two path
// guards are two chances to get it wrong.

// ProjectPrefix is the route this is mounted at.
const ProjectPrefix = "/project/"

// ProjectHandler serves files out of a project's folder: /project/<channel>/<path>.
//
// A directory serves its index.html when it has one — that is what makes
// /project/ch_x/board/ open a board rather than a listing — and otherwise a
// listing, which the fallback in a board's own loader uses to find the newest
// dated file.
func ProjectHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if deps.Coord == nil {
			http.Error(w, "no coordination database", http.StatusServiceUnavailable)
			return
		}
		channelID, rel, ok := splitProjectPath(r.URL.Path)
		if !ok {
			http.Error(w, "usage: "+ProjectPrefix+"<channel-id>/<path>", http.StatusBadRequest)
			return
		}

		full, err := deps.Coord.ProjectFilePath(channelID, rel)
		if err != nil {
			// The same message a person would get from the CLI: it says which
			// of the two refusals happened, and neither of them is a 404.
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		info, err := os.Stat(full)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if info.IsDir() {
			if index, err := deps.Coord.ProjectFilePath(channelID, path.Join(rel, "index.html")); err == nil {
				if st, err := os.Stat(index); err == nil && !st.IsDir() {
					http.ServeFile(w, r, index)
					return
				}
			}
			// No index: a listing, which is what a board's own fallback walks
			// to find the newest dated file. http.FileServer would need its own
			// root and its own escaping rules, so the listing is written here
			// against the path that has already been checked.
			serveListing(w, r, full, channelID, rel)
			return
		}
		http.ServeFile(w, r, full)
	}
}

// splitProjectPath takes <prefix><channel>/<rest> apart. A bare channel with no
// path is the project's own root.
func splitProjectPath(urlPath string) (channelID, rel string, ok bool) {
	rest := strings.TrimPrefix(urlPath, ProjectPrefix)
	if rest == urlPath || rest == "" {
		return "", "", false
	}
	channelID, rel, found := strings.Cut(rest, "/")
	if channelID == "" {
		return "", "", false
	}
	if !found || rel == "" {
		rel = "."
	}
	return channelID, rel, true
}

// serveListing writes the plain directory index a fallback loader can read.
func serveListing(w http.ResponseWriter, r *http.Request, dir, channelID, rel string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	base := ProjectPrefix + channelID + "/"
	if rel != "." {
		base += strings.TrimSuffix(rel, "/") + "/"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, "<!doctype html><meta charset=utf-8><title>%s</title><ul>", htmlEscape(rel))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			name += "/"
		}
		fmt.Fprintf(w, `<li><a href="%s%s">%s</a></li>`, base, htmlEscape(name), htmlEscape(name))
	}
	fmt.Fprint(w, "</ul>")
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
