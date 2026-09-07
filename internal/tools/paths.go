package tools

import (
	"os"
	"path/filepath"
)

// homeDir returns the user's home directory.
//
// os.UserHomeDir is used rather than reading $HOME directly because Windows
// does not set HOME — there the home directory comes from USERPROFILE, which
// is what UserHomeDir reads. On Unix UserHomeDir still returns $HOME, so
// behaviour there is unchanged.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

// tempFile returns a path for a scratch file in the OS temp directory.
// /tmp does not exist on Windows; os.TempDir resolves to %TEMP% there.
func tempFile(name string) string {
	return filepath.Join(os.TempDir(), name)
}
