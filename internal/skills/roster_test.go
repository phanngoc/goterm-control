package skills

import (
	"strings"
	"testing"
)

func peerWith(t *testing.T, id string, names ...string) Peer {
	t.Helper()
	ws := t.TempDir()
	for _, n := range names {
		if err := Install(ws, n,
			[]byte("---\nname: "+n+"\ndescription: 'Làm việc "+n+". Use when: cần "+n+
				". NOT for: mọi thứ khác, và một câu dài lê thê nữa để kiểm phần cắt.'\n---\nthân\n")); err != nil {
			t.Fatal(err)
		}
	}
	return Peer{ID: id, Provider: "claude", Model: "sonnet", Workspace: ws, Online: true}
}

// The thing the old roster could not do. An agent choosing who to hand work to
// had three names and three backend labels — so it chose by guessing.
func TestRosterSaysWhatEachPeerCanActuallyDo(t *testing.T) {
	got := Roster([]Peer{
		peerWith(t, "bomclaw2", "database"),
		peerWith(t, "bomclaw3", "frontend"),
	})
	for _, want := range []string{"bomclaw2", "bomclaw3", "database", "frontend"} {
		if !strings.Contains(got, want) {
			t.Errorf("roster never mentions %q:\n%s", want, got)
		}
	}
}

// The old roster was prose in a config file, written once. By the time a third
// agent existed it was wrong, and agent 1 said out loud it had no idea who
// bomclaw3 was. This one is read from the peer's own folder every time.
func TestRosterFollowsThePeersActualToolkit(t *testing.T) {
	p := peerWith(t, "bomclaw3", "frontend")
	if !strings.Contains(Roster([]Peer{p}), "frontend") {
		t.Fatal("setup")
	}
	if err := Install(p.Workspace, "geospatial",
		[]byte("---\nname: geospatial\ndescription: 'Dữ liệu bản đồ.'\n---\n")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(Roster([]Peer{p}), "geospatial") {
		t.Fatal("a skill the peer gained is invisible to the agent deciding whom to ask")
	}
}

// A peer with an unreadable or empty workspace is still a peer worth naming.
func TestAPeerWithNoSkillsIsStillListed(t *testing.T) {
	got := Roster([]Peer{{ID: "bomclaw2", Provider: "codex", Online: false}})
	if !strings.Contains(got, "bomclaw2") {
		t.Fatalf("dropped a peer for having no skills:\n%s", got)
	}
	if !strings.Contains(got, "offline") {
		t.Errorf("does not say it is offline:\n%s", got)
	}
}

func TestNoPeersMeansNoHeading(t *testing.T) {
	if got := Roster(nil); got != "" {
		t.Errorf("got %q", got)
	}
}

// A roster is for choosing between agents, not for reading their manuals — and
// it is paid on every prompt that names the peers at all.
func TestOnePeerCannotFloodTheRoster(t *testing.T) {
	many := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, "skill-"+string(rune('a'+i)))
	}
	p := peerWith(t, "bomclaw2", many...)
	got := Roster([]Peer{p})
	if n := strings.Count(got, "\n  - "); n > MaxSkillsPerPeer {
		t.Errorf("%d skills in the roster, max %d", n, MaxSkillsPerPeer)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "  - ") && len([]rune(line)) > MaxPeerSkillRunes+40 {
			t.Errorf("a roster line is %d runes: %q", len([]rune(line)), line)
		}
	}
}

// The caveats matter to the agent holding the skill, not to the one deciding
// whom to ask.
func TestTheRosterKeepsWhatItIsAndDropsTheCaveats(t *testing.T) {
	p := peerWith(t, "bomclaw2", "database")
	got := Roster([]Peer{p})
	if !strings.Contains(got, "Làm việc database.") {
		t.Errorf("lost what the skill is:\n%s", got)
	}
	if strings.Contains(got, "NOT for") {
		t.Errorf("carried the caveats into a list meant for choosing:\n%s", got)
	}
}
