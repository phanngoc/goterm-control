package coord

import (
	"strings"
	"testing"
)

func TestParseGitHubRepoAcceptsTheShapesPeoplePaste(t *testing.T) {
	for in, want := range map[string]string{
		"phanngoc/shopee-deals":                           "phanngoc/shopee-deals",
		"https://github.com/phanngoc/rumba-puzzle":        "phanngoc/rumba-puzzle",
		"https://github.com/phanngoc/rumba-puzzle.git":    "phanngoc/rumba-puzzle",
		"https://github.com/phanngoc/rumba-puzzle/pull/1": "phanngoc/rumba-puzzle",
		"git@github.com:phanngoc/queens.git":              "phanngoc/queens",
		"github.com/phanngoc/tank-battle-90/":             "phanngoc/tank-battle-90",
	} {
		got, err := ParseGitHubRepo(in)
		if err != nil || got != want {
			t.Errorf("%q → %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "shopee", "https://gitlab.com/a/b", "a/b c", "https://github.com/onlyowner", "gitlab.com/x/y", "bitbucket.org/a/b"} {
		if _, err := ParseGitHubRepo(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// A channel links many repos and exactly one is primary: the first, until
// another is made primary; removing the primary promotes the next.
func TestAChannelHasManyReposAndOnePrimary(t *testing.T) {
	db := testDB(t)
	c, err := db.CreateProject("arcade", "games", "bomclaw", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	platform, err := db.AddRepo(c.ID, "phanngoc/arcade-platform", "platform", "main", "", "bomclaw", false)
	if err != nil || platform.Role != RepoPrimary {
		t.Fatalf("first repo is not primary: %+v %v", platform, err)
	}
	if _, err := db.AddRepo(c.ID, "https://github.com/phanngoc/rumba-puzzle", "", "", "game", "bomclaw", false); err != nil {
		t.Fatal(err)
	}
	queens, err := db.AddRepo(c.ID, "git@github.com:phanngoc/queens.git", "", "", "", "owner", true)
	if err != nil || queens.Role != RepoPrimary {
		t.Fatalf("explicit primary: %+v %v", queens, err)
	}
	repos, _ := db.ListRepos(c.ID)
	primaries := 0
	for _, r := range repos {
		if r.Role == RepoPrimary {
			primaries++
		}
	}
	if len(repos) != 3 || primaries != 1 || repos[0].Repo != "phanngoc/queens" {
		t.Fatalf("repos: %+v", repos)
	}
	// Re-linking updates rather than duplicating, and keeps what it is not told.
	again, err := db.AddRepo(c.ID, "PHANNGOC/arcade-platform", "", "", "the platform", "bomclaw", false)
	if err != nil || again.ID != platform.ID || again.Path != "platform" || again.Label != "the platform" {
		t.Fatalf("relink: %+v %v", again, err)
	}
	if _, err := db.SetPrimaryRepo(c.ID, "phanngoc/rumba-puzzle"); err != nil {
		t.Fatal(err)
	}
	if err := db.RemoveRepo(c.ID, "phanngoc/rumba-puzzle"); err != nil {
		t.Fatal(err)
	}
	repos, _ = db.ListRepos(c.ID)
	if len(repos) != 2 || repos[0].Role != RepoPrimary || repos[0].Repo != "phanngoc/arcade-platform" {
		t.Fatalf("after removing the primary, the oldest was not promoted: %+v", repos)
	}
	brief := db.RepoBrief(c.ID)
	if !strings.Contains(brief, "**chính** `phanngoc/arcade-platform`") || !strings.Contains(brief, "`platform`") || !strings.Contains(brief, "phụ `phanngoc/queens`") {
		t.Errorf("brief:\n%s", brief)
	}
	if db.RepoBrief("ch_nothing") != "" {
		t.Error("a channel with no repos has a brief")
	}
}
