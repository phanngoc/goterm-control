package coord

import (
	"os"
	"testing"
)

func testHints() []ProjectHint {
	home, _ := os.UserHomeDir()
	return []ProjectHint{
		{ChannelID: "ch_shopee-deals", Name: "shopee-deals", Paths: []string{home + "/goterm-projects/shopee-deals", home + "/goterm-workspace/shopee-deals"}},
		{ChannelID: "ch_arcade", Name: "arcade", Paths: []string{home + "/goterm-projects/arcade", home + "/goterm-workspace/games", home + "/goterm-workspace/games/tank-battle"}},
		{ChannelID: "ch_phim-ngan-25d", Name: "phim-ngan-25d", Paths: []string{home + "/goterm-projects/phim-ngan-25d"}},
	}
}

func TestGuessProjectFromWhereTheTurnWorked(t *testing.T) {
	h := testHints()
	got := GuessProject(h, "làm trang chi tiết từng món có biểu đồ giá",
		[]string{`{"command":"cd ~/goterm-workspace/shopee-deals && npm test"}`, `{"file_path":"/x/y.md"}`})
	if got != "ch_shopee-deals" {
		t.Fatalf("got %q", got)
	}
	// The deepest folder wins: tank-battle is arcade's, through games/.
	if got := GuessProject(h, "", []string{`{"command":"cd ~/goterm-workspace/games/tank-battle && git status"}`}); got != "ch_arcade" {
		t.Fatalf("nested repo: %q", got)
	}
}

func TestGuessProjectFromTheRequestAlone(t *testing.T) {
	h := testHints()
	// Named, and nothing points elsewhere: +2 is below the bar on its own…
	if got := GuessProject(h, "shopee deal có gì hot?", nil); got != "" {
		t.Fatalf("a mention alone moved the turn: %q", got)
	}
	// …but named and worked in is clear.
	if got := GuessProject(h, "shopee deal có gì hot?", []string{"curl https://deal.bomclaw.org", `cd ~/goterm-workspace/shopee-deals`}); got != "ch_shopee-deals" {
		t.Fatalf("got %q", got)
	}
	// Diacritics: "phim ngắn" names phim-ngan-25d (25d is too short to need).
	if !namesProject(fold("làm phim ngắn về FPT"), "phim-ngan-25d") {
		t.Fatal("phim ngắn should name phim-ngan-25d")
	}
}

func TestGuessProjectRefusesWhenUnclear(t *testing.T) {
	h := testHints()
	if got := GuessProject(h, "tóm tắt tin hôm nay", []string{"ls /tmp"}); got != "" {
		t.Fatalf("nothing project-like: %q", got)
	}
	// One command in each: a tie is not a guess.
	tie := []string{"cd ~/goterm-workspace/shopee-deals", "cd ~/goterm-projects/arcade"}
	if got := GuessProject(h, "", tie); got != "" {
		t.Fatalf("tie: %q", got)
	}
	// A sibling folder that only starts with the name is not the project.
	if got := GuessProject(h, "", []string{"cd ~/goterm-workspace/shopee-deals-settings && ls", "cd ~/goterm-workspace/shopee-deals-settings"}); got != "" {
		t.Fatalf("prefix folder matched: %q", got)
	}
}

// Live projects are found; an archived one is not.
func TestProjectHintsListsLiveProjects(t *testing.T) {
	db := testDB(t)
	a, _ := db.CreateProject("shopee-deals", "", "owner", t.TempDir(), nil)
	if _, err := db.CreateChannel("", "general", ChannelPublic, "", "system", nil); err != nil {
		t.Fatal(err)
	}
	h, err := db.ProjectHints()
	if err != nil || len(h) != 1 || h[0].ChannelID != a.ID || h[0].Paths[0] != a.Workspace {
		t.Fatalf("hints = %+v, %v", h, err)
	}
}
