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
