package claude

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/credentials"
	"github.com/ngocp/goterm-control/internal/session"
)

// fakeClaude puts a stand-in `claude` on PATH. A turn under the token named
// `spent` — or under no token at all, which is what a login account runs
// with — fails with failWith; any other token answers with its own name.
// Every call is logged.
func fakeClaude(t *testing.T, failWith string) (log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a sh script")
	}
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	failure := filepath.Join(dir, "failure.json")
	if err := os.WriteFile(failure, []byte(`{"type":"result","is_error":true,"result":"`+failWith+`"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
cat > /dev/null
echo "$CLAUDE_CODE_OAUTH_TOKEN" >> ` + log + `
echo '{"type":"system","subtype":"init","session_id":"sess-1"}'
if [ "$CLAUDE_CODE_OAUTH_TOKEN" = "spent" ] || [ -z "$CLAUDE_CODE_OAUTH_TOKEN" ]; then
  cat ` + failure + `
  exit 1
fi
echo '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"answered by '"$CLAUDE_CODE_OAUTH_TOKEN"'"}]}}'
echo '{"type":"result","is_error":false,"result":"ok"}'
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func tokenClient(t *testing.T, every int, accts ...credentials.Account) (*Client, *credentials.Pool) {
	t.Helper()
	pool, err := credentials.NewPool(credentials.ProviderClaude, accts, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	pool.SetRotateEvery(every)
	c := New("test", nil)
	c.SetWorkspace(t.TempDir())
	c.SetPool(pool)
	return c, pool
}

func turn(t *testing.T, c *Client, sess *session.Session) (string, error) {
	t.Helper()
	var out strings.Builder
	err := c.SendMessage(context.Background(), sess, "m", "hi", "", StreamCallbacks{
		OnText: func(s string) { out.WriteString(s) },
	})
	return out.String(), err
}

// A capped token hands the turn to the next one before anything is shown,
// and the conversation continues there: same session, no error for the user.
func TestCappedTokenFailsOverWithinTheTurn(t *testing.T) {
	calls := fakeClaude(t, "You've hit your session limit · resets 3pm")
	c, pool := tokenClient(t, 5,
		credentials.Account{Name: "a", OAuthToken: "spent"},
		credentials.Account{Name: "b", OAuthToken: "fresh"})
	sess := session.New(1)

	out, err := turn(t, c, sess)
	if err != nil || out != "answered by fresh" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if got, _ := os.ReadFile(calls); string(got) != "spent\nfresh\n" {
		t.Fatalf("calls:\n%s", got)
	}
	if sess.GetSessionID() != "sess-1" {
		t.Fatalf("session = %q", sess.GetSessionID())
	}
	// The next turn goes straight to b: a is benched.
	if out, _ := turn(t, c, sess); out != "answered by fresh" {
		t.Fatalf("second turn: %q", out)
	}
	for _, st := range pool.Status() {
		if st.Name == "a" && st.CoolingHint == "" {
			t.Fatal("the capped token is not cooling down")
		}
	}
}

// With nowhere to go, the original error stands.
func TestFailoverGivesUpWithTheRealError(t *testing.T) {
	fakeClaude(t, "Failed to authenticate: OAuth token revoked")
	c, _ := tokenClient(t, 5, credentials.Account{Name: "a", OAuthToken: "spent"})
	if _, err := turn(t, c, session.New(2)); err == nil || !strings.Contains(err.Error(), "OAuth token revoked") {
		t.Fatalf("err = %v", err)
	}
}

// A login is never failed over: its history lives in its own config dir.
func TestLoginsDoNotFailOver(t *testing.T) {
	calls := fakeClaude(t, "429 rate limit")
	c, _ := tokenClient(t, 5,
		credentials.Account{Name: "login", ConfigDir: t.TempDir()},
		credentials.Account{Name: "b", OAuthToken: "fresh"})
	sess := session.New(3)
	sess.SetAccount("login")
	if _, err := turn(t, c, sess); err == nil {
		t.Fatal("the login's failure was swallowed")
	}
	if got, _ := os.ReadFile(calls); strings.Count(string(got), "\n") != 1 {
		t.Fatalf("a login turn ran more than once:\n%s", got)
	}
}
