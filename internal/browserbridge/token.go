package browserbridge

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// TokenFile is where a generated pairing token lives, under the data dir.
const TokenFile = "browser-bridge.token"

// Token is the pairing secret and where it came from.
type Token struct {
	Value   string
	Path    string // the file it was read from or written to; "" when configured
	Created bool   // true the first time, so the gateway can tell the user
}

// LoadOrCreateToken returns the pairing token: the configured one if set,
// otherwise the one stored in dataDir, generating and storing a fresh one the
// first time. Generation, not a config field, is the default because a
// hand-typed token in config.yaml ends up in git and chat logs; the file is
// owner-readable only and never leaves the machine except by the user
// pasting it into the extension.
func LoadOrCreateToken(configured, dataDir string) (Token, error) {
	if t := strings.TrimSpace(configured); t != "" {
		return Token{Value: t}, nil
	}
	path := filepath.Join(dataDir, TokenFile)
	if b, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return Token{Value: t, Path: path}, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, fmt.Errorf("generate token: %w", err)
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return Token{}, fmt.Errorf("create data dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		return Token{}, fmt.Errorf("store token: %w", err)
	}
	return Token{Value: value, Path: path, Created: true}, nil
}
