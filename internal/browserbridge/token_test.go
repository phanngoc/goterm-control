package browserbridge

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")

	first, err := LoadOrCreateToken("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Created || first.Value == "" || first.Path != filepath.Join(dir, TokenFile) {
		t.Fatalf("first call should generate and store a token, got %+v", first)
	}
	if fi, err := os.Stat(first.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file should be owner-only (0600), got %v err=%v", fi.Mode(), err)
	}

	second, err := LoadOrCreateToken("", dir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Created || second.Value != first.Value {
		t.Fatalf("second call should read the stored token back, got %+v", second)
	}

	configured, err := LoadOrCreateToken("  from-config  ", dir)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Value != "from-config" || configured.Path != "" || configured.Created {
		t.Fatalf("a configured token wins and is not stored, got %+v", configured)
	}
}
