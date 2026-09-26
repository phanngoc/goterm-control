package gateway

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/ngocp/goterm-control/internal/coord"
)

// The first screen of a project: what it built, how to run it, and a way to
// run it.
//
// Opening a project used to give a directory listing. That is the right answer
// to "what files are here" and the wrong answer to every question a person
// actually arrives with — which is some form of "show me the thing". The result
// was one folder deep, named differently in every project, and findable only by
// guessing.
//
// So the folder's root answers three questions instead: where the result is,
// how the project is run, and how to run it now. The file listing stays,
// underneath, because sometimes the question really is about the files.

// entryCandidates are where a project's result tends to live, in the order
// worth trying. Convention rather than configuration: a project that has to
// declare its entry point is a project whose entry point goes stale, and every
// one of these is a name somebody chose without being asked to.
var entryCandidates = []string{
	"index.html",
	"board/index.html",
	"dist/index.html",
	"public/index.html",
	"site/index.html",
	"build/index.html",
	"out/index.html",
}

// guideCandidates are where the running instructions tend to be written.
// AGENTS.md is last: it is the brief the agents were given, which describes the
// work rather than how to run what came of it.
var guideCandidates = []string{"README.md", "readme.md", "RUN.md", "HOWTO.md", "AGENTS.md"}

// projectEntry is the page a project's result lives on, or "" when it has none
// — a project that produced a report and no page is an ordinary thing.
func projectEntry(db *coord.DB, channelID string) string {
	for _, rel := range entryCandidates {
		full, err := db.ProjectFilePath(channelID, rel)
		if err != nil {
			continue
		}
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			return rel
		}
	}
	return ""
}

// projectGuide is the first guide file that exists, with its name.
func projectGuide(db *coord.DB, channelID string) (name, body string) {
	for _, rel := range guideCandidates {
		full, err := db.ProjectFilePath(channelID, rel)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		return rel, string(b)
	}
	return "", ""
}

// runProject opens a task asking an agent to run the project and refresh what
// it produced.
//
// A task rather than a command executed here. The gateway has no business
// running a project's scripts: it has no workspace, no credentials, no timeout
// that means anything, and nowhere to put the output. An agent has all four,
// and "ask an agent" is the one move this whole system is built around — the
// button is a shortcut to the thing a person would otherwise type.
func runProject(deps Deps, channelID string) (*coord.Task, error) {
	c, err := deps.Coord.GetChannel(channelID)
	if err != nil {
		return nil, err
	}
	guide, _ := projectGuide(deps.Coord, channelID)
	where := "the project's own files"
	if guide != "" {
		where = guide
	}
	return deps.Coord.CreateTask(coord.NewTask{
		ChannelID: channelID,
		CreatedBy: coord.OwnerUserID,
		Title:     "Chạy lại " + c.Name + " và cập nhật kết quả",
		Body: fmt.Sprintf("Somebody asked for this from the project's own page, so they are "+
			"waiting to look at the result.\n\nRun the project the way %s says to run it, "+
			"refresh whatever it produces, and say what changed. If a step fails, say which one "+
			"and what it said — a half-refreshed result that looks finished is worse than an "+
			"honest failure.", where),
		Acceptance: fmt.Sprintf("1) Chạy đúng các bước trong %s, không bỏ bước nào; "+
			"2) Kết quả trong thư mục dự án được cập nhật và mở được; "+
			"3) Nói rõ cái gì đã đổi, hoặc bước nào hỏng và hỏng thế nào", where),
	})
}

// serveLanding writes the project's front page.
func serveLanding(w http.ResponseWriter, deps Deps, channelID string, justStarted *coord.Task) {
	c, err := deps.Coord.GetChannel(channelID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	entry := projectEntry(deps.Coord, channelID)
	guideName, guide := projectGuide(deps.Coord, channelID)
	base := ProjectPrefix + channelID + "/"

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, landingHead, htmlEscape(c.Name))
	fmt.Fprintf(w, `<h1>%s</h1>`, htmlEscape(c.Name))
	if c.Purpose != "" {
		fmt.Fprintf(w, `<p class=muted>%s</p>`, htmlEscape(c.Purpose))
	}

	if justStarted != nil {
		fmt.Fprintf(w, `<p class=note>Đã giao cho một agent chạy lại — task <code>%s</code>. `+
			`Tải lại trang này sau khi nó xong để xem kết quả mới.</p>`, htmlEscape(justStarted.ID))
	}

	fmt.Fprint(w, `<div class=row>`)
	if entry != "" {
		fmt.Fprintf(w, `<a class="btn primary" href="%s%s">Mở kết quả →</a>`,
			base, htmlEscape(entry))
	} else {
		fmt.Fprint(w, `<span class=muted>Dự án này chưa có trang kết quả nào `+
			`(index.html, board/, dist/ …) — xem tệp bên dưới.</span>`)
	}
	fmt.Fprint(w, `<form method=post><button class=btn name=run value=1>Chạy lại</button></form>`)
	fmt.Fprint(w, `</div>`)

	if guide != "" {
		fmt.Fprintf(w, `<h2>Cách chạy <span class=muted>· %s</span></h2><pre>%s</pre>`,
			htmlEscape(guideName), htmlEscape(guide))
	}

	fmt.Fprint(w, `<h2>Tệp</h2><ul class=files>`)
	if entries, err := os.ReadDir(c.Workspace); err == nil {
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
	}
	fmt.Fprintf(w, `</ul><footer class=muted>%s</footer></body>`, htmlEscape(c.Workspace))
}

// landingPath is where "run it again" posts back to, so the button needs no
// path of its own that a real file could collide with.
func isRunRequest(r *http.Request) bool {
	return r.Method == http.MethodPost && (r.FormValue("run") == "1" || r.URL.Query().Get("run") == "1")
}

func landingRel(rel string) bool { return rel == "." || rel == "" || rel == "/" }

var _ = path.Join // kept for the entry candidates' documentation of shape

const landingHead = `<!doctype html><meta charset=utf-8><title>%s</title><style>
:root{color-scheme:dark}
body{margin:0;padding:40px 24px;background:#0c1420;color:#eef4fc;
     font:15px/1.6 system-ui,-apple-system,sans-serif;max-width:860px;margin:auto}
h1{font-size:28px;margin:0 0 4px}
h2{font-size:15px;text-transform:uppercase;letter-spacing:1.5px;color:#a7b8cc;margin:32px 0 10px;font-weight:600}
.muted{color:#a7b8cc}
.row{display:flex;gap:12px;align-items:center;margin:24px 0;flex-wrap:wrap}
.btn{display:inline-block;border:1px solid #2c3b4e;background:#1e3044;color:inherit;
     border-radius:8px;padding:10px 16px;font:inherit;cursor:pointer;text-decoration:none}
.btn:hover{border-color:#61dfb1}
.primary{background:#61dfb1;color:#08131d;border-color:#61dfb1;font-weight:600}
.note{border:1px solid #f4cf78;color:#f4cf78;border-radius:8px;padding:10px 14px}
pre{background:#131f2e;border:1px solid #2c3b4e;border-radius:10px;padding:16px;
    overflow:auto;white-space:pre-wrap;font-size:13px}
ul.files{list-style:none;padding:0;columns:2;font-size:14px}
ul.files a{color:#92c5ff;text-decoration:none}
ul.files a:hover{text-decoration:underline}
footer{margin-top:32px;font-size:12px;font-family:ui-monospace,monospace}
code{color:#f4cf78}
</style><body>`
