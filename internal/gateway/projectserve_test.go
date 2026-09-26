package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/coord"
)

func projectServeDeps(t *testing.T) (Deps, *coord.Channel) {
	t.Helper()
	db := testCoordDB(t)
	p, err := db.CreateProject("radar", "", "owner", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p.Workspace, "board"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p.Workspace, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(p.Workspace, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("board/index.html", `<h1>radar</h1><script>fetch('../data/latest.json')</script>`)
	write("data/latest.json", `{"vn_index":1785.11}`)
	write("data/2026-09-25.json", `{"vn_index":1775.09}`)
	return Deps{Coord: db}, p
}

func get(t *testing.T, deps Deps, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	ProjectHandler(deps)(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// The whole point: a board opens with its data beside it. A single file served
// from a blob has no folder around it, so its own fetch for ../data/latest.json
// gets nothing and the page shows empty.
func TestAProjectsBoardOpensWithItsDataBesideIt(t *testing.T) {
	deps, p := projectServeDeps(t)

	// A directory serves its index, so /board/ is a board and not a listing.
	w := get(t, deps, ProjectPrefix+p.ID+"/board/")
	if w.Code != http.StatusOK {
		t.Fatalf("board: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "<h1>radar</h1>") {
		t.Fatalf("board did not render: %s", w.Body.String())
	}

	// And the relative fetch the page makes resolves, which is the difference
	// between reading a file and seeing a result.
	w = get(t, deps, ProjectPrefix+p.ID+"/data/latest.json")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "1785.11") {
		t.Fatalf("the board's own data request failed: %d %s", w.Code, w.Body.String())
	}

	// A listing where there is no index: a board's fallback walks this to find
	// the newest dated file.
	w = get(t, deps, ProjectPrefix+p.ID+"/data/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "2026-09-25.json") {
		t.Fatalf("no listing for the fallback to read: %d %s", w.Code, w.Body.String())
	}
}

// The guard is coord's, not a second one written here — two path checks are two
// chances to get it wrong. These pin that it is actually reached.
func TestServingAProjectCannotEscapeItsFolder(t *testing.T) {
	deps, p := projectServeDeps(t)
	outside := filepath.Join(filepath.Dir(p.Workspace), "secret.txt")
	if err := os.WriteFile(outside, []byte("không được đọc"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, bad := range []string{
		ProjectPrefix + p.ID + "/../secret.txt",
		ProjectPrefix + p.ID + "/board/../../secret.txt",
		ProjectPrefix + p.ID + "//etc/passwd",
	} {
		w := get(t, deps, bad)
		if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "không được đọc") {
			t.Fatalf("%s served a file from outside the project folder", bad)
		}
	}

	// A symlink pointing out is the case a string check misses.
	if err := os.Symlink(outside, filepath.Join(p.Workspace, "link.txt")); err == nil {
		w := get(t, deps, ProjectPrefix+p.ID+"/link.txt")
		if w.Code == http.StatusOK && strings.Contains(w.Body.String(), "không được đọc") {
			t.Fatal("a symlink out of the project folder was followed")
		}
	}
}

// A room is not a project and has nothing to serve; a missing file is a 404
// rather than a refusal, because they are different facts.
func TestServingSaysWhichKindOfNoItIs(t *testing.T) {
	deps, p := projectServeDeps(t)

	if w := get(t, deps, ProjectPrefix+coord.GeneralChannelID+"/anything"); w.Code != http.StatusForbidden {
		t.Errorf("a room with no folder answered %d", w.Code)
	}
	if w := get(t, deps, ProjectPrefix+p.ID+"/khong-co-file.txt"); w.Code != http.StatusNotFound {
		t.Errorf("a missing file answered %d, want 404", w.Code)
	}
	if w := get(t, deps, ProjectPrefix); w.Code != http.StatusBadRequest {
		t.Errorf("a request naming no project answered %d", w.Code)
	}
}

// Opening a project has to answer "show me the thing", not "what files are
// here". The result is one folder deep and named differently in every project;
// finding it by guessing is what the landing page removes.
func TestOpeningAProjectLeadsToItsResult(t *testing.T) {
	deps, p := projectServeDeps(t)
	if err := os.WriteFile(filepath.Join(p.Workspace, "README.md"),
		[]byte("# radar\n\n## Chạy\n\n    python3 scripts/fetch.py\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	w := get(t, deps, ProjectPrefix+p.ID+"/")
	if w.Code != http.StatusOK {
		t.Fatalf("landing: %d", w.Code)
	}
	body := w.Body.String()

	// Where the result is — board/index.html, found by convention rather than
	// by the person guessing.
	if !strings.Contains(body, ProjectPrefix+p.ID+"/board/index.html") {
		t.Fatalf("the page does not lead to the result:\n%s", body)
	}
	// How it is run.
	if !strings.Contains(body, "python3 scripts/fetch.py") {
		t.Errorf("the running instructions are not shown")
	}
	// And a way to run it.
	if !strings.Contains(body, `name=run`) {
		t.Errorf("no way to run it from here")
	}
	// The files are still reachable, underneath.
	if !strings.Contains(body, ProjectPrefix+p.ID+"/data/") {
		t.Errorf("the file list is gone")
	}
}

// A project that produced a report and no page is ordinary. It must say so
// rather than link to nothing.
func TestAProjectWithNoPageSaysSo(t *testing.T) {
	deps, p := projectServeDeps(t)
	if err := os.RemoveAll(filepath.Join(p.Workspace, "board")); err != nil {
		t.Fatal(err)
	}
	body := get(t, deps, ProjectPrefix+p.ID+"/").Body.String()
	if strings.Contains(body, "Mở kết quả") {
		t.Fatal("offered to open a result that does not exist")
	}
	if !strings.Contains(body, "chưa có trang kết quả") {
		t.Errorf("did not say why there is nothing to open:\n%s", body)
	}
}

// "Run it" opens a task rather than executing anything here. The gateway has no
// workspace, no credentials and nowhere to put the output; an agent has all
// three, and asking an agent is the move the whole system is built on.
func TestRunningAProjectAsksAnAgentRatherThanExecuting(t *testing.T) {
	deps, p := projectServeDeps(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, ProjectPrefix+p.ID+"/?run=1", nil)
	ProjectHandler(deps)(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("run: %d %s", w.Code, w.Body.String())
	}

	tasks, err := deps.Coord.ListTasks(coord.TaskFilter{ChannelID: p.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("opened %d tasks, want 1", len(tasks))
	}
	got := tasks[0]
	if got.ChannelID != p.ID {
		t.Error("the task landed outside the project it is meant to refresh")
	}
	if got.Acceptance == "" {
		t.Error("the task has no bar, so none of the goal loop applies to it")
	}
	if !strings.Contains(w.Body.String(), got.ID) {
		t.Error("the page does not say which task was opened, so there is nothing to follow")
	}

	// A plain GET must not open one: a reload is not a request to run again.
	get(t, deps, ProjectPrefix+p.ID+"/")
	if again, _ := deps.Coord.ListTasks(coord.TaskFilter{ChannelID: p.ID, Limit: 10}); len(again) != 1 {
		t.Fatalf("reloading the page opened another task (%d total)", len(again))
	}
}
