package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// Every config written before the guard existed has no fs_guard section; it
// must get the guard, not lose it, so an unset switch reads as on.
func TestTheFSGuardIsOnUnlessSwitchedOff(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		on   bool
	}{
		{"allowed_user_ids: [1]\n", true},
		{"fs_guard: {allow: [\"~/Downloads\"]}\n", true},
		{"fs_guard: {enabled: true}\n", true},
		{"fs_guard: {enabled: false}\n", false},
	} {
		var s SecurityConfig
		if err := yaml.Unmarshal([]byte(tc.yaml), &s); err != nil {
			t.Fatal(err)
		}
		if got := s.FSGuard.On(); got != tc.on {
			t.Errorf("%q: On() = %v, want %v", tc.yaml, got, tc.on)
		}
	}
}
