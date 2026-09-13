package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/credentials"
	"github.com/ngocp/goterm-control/internal/session"
)

func poolOf(t *testing.T, names ...string) *credentials.Pool {
	t.Helper()
	accts := make([]credentials.Account, 0, len(names))
	for _, n := range names {
		accts = append(accts, credentials.Account{Name: n, Provider: "claude", ConfigDir: "/tmp/" + n})
	}
	p, err := credentials.NewPool("claude", accts, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Most installs run on one ambient login. An empty list is the honest
// description of that, not an error the dashboard has to special-case.
func TestNoPoolListsNothingRatherThanFailing(t *testing.T) {
	raw, err := handleAccountsList(Deps{Sessions: session.NewManager(nil)}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("an install with no pool should not error: %v", err)
	}
	var out []AccountInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("expected an empty list, got %v", out)
	}
}

func TestUsingAnAccountStartsANewSession(t *testing.T) {
	deps := Deps{Sessions: session.NewManager(nil), Accounts: poolOf(t, "default", "tam")}

	// A conversation already under way on the default login.
	existing := deps.Sessions.Get(0)
	existing.SetSessionID("claude-session-1")

	raw, err := handleAccountsUse(deps, json.RawMessage(`{"chat_id":0,"account":"tam"}`))
	if err != nil {
		t.Fatalf("accounts.use: %v", err)
	}
	var out struct {
		SessionID string `json:"session_id"`
		Account   string `json:"account"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Account != "tam" {
		t.Fatalf("account: %q", out.Account)
	}
	// A NEW session, not the old one repointed: the CLI keeps its conversation
	// inside the credential directory, so repointing would hand the next turn
	// an id the new account has never seen.
	if out.SessionID == existing.ID {
		t.Fatal("the live session was repointed instead of a new one being started")
	}
	fresh := deps.Sessions.GetByID(out.SessionID)
	if fresh == nil {
		t.Fatal("the new session was not registered")
	}
	if fresh.GetAccount() != "tam" {
		t.Fatalf("the new session is not pinned: %q", fresh.GetAccount())
	}
	if fresh.GetSessionID() != "" {
		t.Fatal("a fresh session must not carry the old provider session id")
	}
	// And the old conversation is untouched, not silently lost.
	if existing.GetSessionID() != "claude-session-1" {
		t.Fatal("the previous session was modified")
	}
}

func TestUnknownAccountIsRefusedByName(t *testing.T) {
	deps := Deps{Sessions: session.NewManager(nil), Accounts: poolOf(t, "default", "tam")}
	_, err := handleAccountsUse(deps, json.RawMessage(`{"chat_id":0,"account":"nope"}`))
	if err == nil {
		t.Fatal("an unknown account was accepted")
	}
	// The message has to say what there is, or the caller cannot act on it.
	for _, want := range []string{"nope", "default", "tam"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestUsingAnAccountWithNoPoolSaysWhy(t *testing.T) {
	deps := Deps{Sessions: session.NewManager(nil)}
	_, err := handleAccountsUse(deps, json.RawMessage(`{"chat_id":0,"account":"tam"}`))
	if err == nil || !strings.Contains(err.Error(), "accounts.pool") {
		t.Fatalf("should point at the config that is missing, got %v", err)
	}
}

// The list marks the login the current conversation is on, so the screen can
// show it without a second round trip.
func TestListMarksTheCurrentAccount(t *testing.T) {
	deps := Deps{Sessions: session.NewManager(nil), Accounts: poolOf(t, "default", "tam")}
	deps.Sessions.Get(0).SetAccount("tam")

	raw, err := handleAccountsList(deps, json.RawMessage(`{"chat_id":0}`))
	if err != nil {
		t.Fatal(err)
	}
	var out []AccountInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	var current []string
	for _, a := range out {
		if a.Current {
			current = append(current, a.Name)
		}
	}
	if len(current) != 1 || current[0] != "tam" {
		t.Fatalf("expected tam marked current, got %v", current)
	}
}
