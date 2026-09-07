package browserbridge

import (
	"os"
	"path/filepath"
	"runtime"
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
	fi, err := os.Stat(first.Path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	// Windows does not carry POSIX mode bits — os.Chmod there only toggles the
	// read-only attribute, so a file written 0600 reads back 0666 and this
	// assertion cannot hold. The token still is not world-readable: it lives
	// under the user's profile, whose ACL already excludes other standard
	// users. Restricting it further would mean an explicit ACL, not a mode.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file should be owner-only (0600), got %v", fi.Mode())
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
