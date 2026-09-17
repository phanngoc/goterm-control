package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/skills"
)

func hubFixture(t *testing.T) (Deps, string, string) {
	t.Helper()
	db := testCoordDB(t)
	wsA, wsB := t.TempDir(), t.TempDir()
	for _, a := range []coord.Agent{
		{ID: "bomclaw", DisplayName: "bomclaw", Workspace: wsA, Provider: "claude"},
		{ID: "bomclaw3", DisplayName: "bomclaw3", Workspace: wsB, Provider: "opencode"},
	} {
		if err := db.RegisterAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := skills.EnsureDefaults(wsA); err != nil {
		t.Fatal(err)
	}
	if _, err := skills.EnsureDefaults(wsB); err != nil {
		t.Fatal(err)
	}
	return Deps{Coord: db}, wsA, wsB
}

func call(t *testing.T, fn func(Deps, json.RawMessage) (json.RawMessage, error), deps Deps, params any) json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(params)
	out, err := fn(deps, raw)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	return out
}

func TestHubListsEveryAgentsToolkit(t *testing.T) {
	deps, _, _ := hubFixture(t)
	var rows []AgentSkills
	if err := json.Unmarshal(call(t, handleSkillsList, deps, map[string]any{}), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d agents", len(rows))
	}
	for _, r := range rows {
		if len(r.Skills) == 0 {
			t.Fatalf("%s has no skills although it was seeded", r.AgentID)
		}
		for _, s := range r.Skills {
			if !s.Bundled {
				t.Errorf("%s: %q not marked as a shipped default", r.AgentID, s.Name)
			}
			if s.Edited {
				t.Errorf("%s: %q marked edited before anyone touched it", r.AgentID, s.Name)
			}
		}
	}
}

// The flag the whole per-agent design exists for: it marks where one agent has
// learned something its peers have not.
func TestAnEditedCopyIsMarkedAsEdited(t *testing.T) {
	deps, wsA, _ := hubFixture(t)
	if err := skills.Install(wsA, "browser",
		[]byte("---\nname: browser\ndescription: 'bản tôi tự sửa'\n---\nmẹo riêng\n")); err != nil {
		t.Fatal(err)
	}
	var rows []AgentSkills
	json.Unmarshal(call(t, handleSkillsList, deps, map[string]any{}), &rows)
	for _, r := range rows {
		for _, s := range r.Skills {
			if s.Name != "browser" {
				continue
			}
			want := r.AgentID == "bomclaw"
			if s.Edited != want {
				t.Errorf("%s browser edited=%v, want %v", r.AgentID, s.Edited, want)
			}
		}
	}
}

func TestRemovingLeavesItOfferedBack(t *testing.T) {
	deps, _, _ := hubFixture(t)
	call(t, handleSkillRemove, deps, map[string]any{"agent_id": "bomclaw", "name": "browser"})

	var rows []AgentSkills
	json.Unmarshal(call(t, handleSkillsList, deps, map[string]any{}), &rows)
	for _, r := range rows {
		if r.AgentID != "bomclaw" {
			continue
		}
		if !contains(r.Missing, "browser") {
			t.Fatalf("a removed default is not offered back: missing=%v", r.Missing)
		}
		for _, s := range r.Skills {
			if s.Name == "browser" {
				t.Fatal("still listed after removal")
			}
		}
	}
	// Removing one agent's copy must not touch its peer's.
	for _, r := range rows {
		if r.AgentID == "bomclaw3" && !hasSkill(r.Skills, "browser") {
			t.Fatal("removing one agent's copy took the other agent's too")
		}
	}

	// An empty body means "the shipped one" — how a default is put back.
	call(t, handleSkillInstall, deps, map[string]any{"agent_id": "bomclaw", "name": "browser"})
	json.Unmarshal(call(t, handleSkillsList, deps, map[string]any{}), &rows)
	for _, r := range rows {
		if r.AgentID == "bomclaw" && !hasSkill(r.Skills, "browser") {
			t.Fatal("the default did not come back")
		}
	}
}

func TestCopySpreadsAnImprovement(t *testing.T) {
	deps, wsA, wsB := hubFixture(t)
	if err := skills.Install(wsA, "browser",
		[]byte("---\nname: browser\ndescription: d\n---\nmẹo mới học được\n")); err != nil {
		t.Fatal(err)
	}
	call(t, handleSkillCopy, deps, map[string]any{"from": "bomclaw", "agent_id": "bomclaw3", "name": "browser"})

	got, err := skills.Read(wsB, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "mẹo mới học được") {
		t.Fatal("the improvement did not reach the other agent — without this,\n" +
			"three agents improve alone and every lesson dies where it was learned")
	}
	if src, _ := skills.Read(wsA, "browser"); !strings.Contains(string(src), "mẹo mới học được") {
		t.Error("copying took it away from the agent that wrote it")
	}
}

func TestAnUnknownAgentIsNamedRatherThanIgnored(t *testing.T) {
	deps, _, _ := hubFixture(t)
	raw, _ := json.Marshal(map[string]any{"agent_id": "nobody", "name": "browser"})
	if _, err := handleSkillRemove(deps, raw); err == nil {
		t.Fatal("removing a skill from an agent that does not exist reported success")
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func hasSkill(xs []HubSkill, name string) bool {
	for _, x := range xs {
		if x.Name == name {
			return true
		}
	}
	return false
}
