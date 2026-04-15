package config

import (
	"strings"
	"testing"
)

func TestDefaultSystemPromptFor(t *testing.T) {
	cases := []struct {
		goos     string
		contains []string
		absent   []string
	}{
		{
			"darwin",
			[]string{"macOS", "osascript", "screencapture"},
			[]string{"gnome-screenshot", "powershell.exe"},
		},
		{
			"linux",
			[]string{"Linux", "scrot"},
			[]string{"osascript", "powershell.exe"},
		},
		{
			"windows",
			[]string{"WSL", "powershell.exe"},
			[]string{"osascript", "scrot"},
		},
	}
	for _, tc := range cases {
		got := defaultSystemPromptFor(tc.goos)
		for _, s := range tc.contains {
			if !strings.Contains(got, s) {
				t.Errorf("%s: expected prompt to contain %q", tc.goos, s)
			}
		}
		for _, s := range tc.absent {
			if strings.Contains(got, s) {
				t.Errorf("%s: expected prompt NOT to contain %q", tc.goos, s)
			}
		}
	}
}

func TestDefaultSystemPromptFor_UnknownOS(t *testing.T) {
	got := defaultSystemPromptFor("freebsd")
	// Should fallback to linux
	if !strings.Contains(got, "Linux") {
		t.Error("unknown OS should fallback to linux addendum")
	}
}

func TestDefaultSystemPromptFor_ContainsBase(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		got := defaultSystemPromptFor(goos)
		if !strings.Contains(got, "Operating guidelines:") {
			t.Errorf("%s: missing base prompt content", goos)
		}
		if !strings.Contains(got, "goterm-workspace") {
			t.Errorf("%s: missing workspace rules", goos)
		}
	}
}
