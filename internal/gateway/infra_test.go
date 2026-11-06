package gateway

import (
	"encoding/json"
	"testing"
)

// The hub's action door takes only the allow-listed kinds and verbs; anything
// else is refused before a single command runs. (The checks against a live
// snapshot are internal/infra's tests.)
func TestInfraActionRefusesWhatIsNotOnTheList(t *testing.T) {
	deps, _, _ := hubFixture(t)
	for _, p := range []map[string]string{
		{"kind": "shell", "target": "ls", "verb": "run"},
		{"kind": "pm2", "target": "x", "verb": "exec"},
		{"kind": "launchd", "target": "com.apple.Finder", "verb": "delete"},
		{"kind": "pm2", "target": "", "verb": "restart"},
	} {
		raw, _ := json.Marshal(p)
		if _, err := handleInfraAction(deps, raw); err == nil {
			t.Errorf("%v was accepted", p)
		}
	}
}

func TestInfraMethodsAreAnsweredByTheDashboard(t *testing.T) {
	for _, m := range []string{"infra.snapshot", "infra.action", "infra.logs"} {
		if !DetachedMethods[m] {
			t.Errorf("%s would be relayed to an agent — it must keep working while agents restart", m)
		}
	}
}
