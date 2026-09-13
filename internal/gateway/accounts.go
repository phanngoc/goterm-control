package gateway

import (
	"encoding/json"
	"fmt"

	"github.com/ngocp/goterm-control/internal/credentials"
)

// Choosing which login a conversation runs on.
//
// The pool already rotates sessions across logins and already takes a pinned
// name (credentials.Pool.Pick), and a session already records the one it used
// (session.SetAccount). What was missing was any way to say which — the choice
// was the pool's alone.
//
// It is a per-SESSION choice and cannot be anything else: both CLIs keep their
// conversation store inside the credential directory, so moving a live session
// to another login does not switch accounts, it loses the conversation. So the
// answer to "use Tâm's account" is a new session, and this says so rather than
// appearing to switch and quietly starting over.

// AccountInfo is one login, and what the pool knows about it.
type AccountInfo struct {
	credentials.Status
	Current bool `json:"current"` // the account this chat's active session is on
}

type accountsListParams struct {
	ChatID int64 `json:"chat_id,omitempty"`
}

func handleAccountsList(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p accountsListParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if deps.Accounts == nil || deps.Accounts.Empty() {
		// Not an error: most installs run on the ambient credentials, and an
		// empty list is the honest description of that.
		return json.Marshal([]AccountInfo{})
	}
	var current string
	if deps.Sessions != nil {
		if sess := deps.Sessions.Get(p.ChatID); sess != nil {
			current = sess.GetAccount()
		}
	}
	statuses := deps.Accounts.Status()
	out := make([]AccountInfo, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, AccountInfo{Status: s, Current: s.Name == current})
	}
	return json.Marshal(out)
}

type useAccountParams struct {
	ChatID  int64  `json:"chat_id"`
	Account string `json:"account"`
}

// handleAccountsUse starts a new session pinned to a login.
//
// Deliberately a new session rather than a change to the current one. The CLI
// stores its conversation inside the credential directory, so repointing a
// session that already has one would hand the next turn an id its new account
// has never heard of — the conversation would restart anyway, only without
// anyone having said so.
func handleAccountsUse(deps Deps, params json.RawMessage) (json.RawMessage, error) {
	var p useAccountParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Account == "" {
		return nil, fmt.Errorf("account is required")
	}
	if deps.Accounts == nil || deps.Accounts.Empty() {
		return nil, fmt.Errorf("no credential pool configured — add accounts.pool to this agent's config")
	}
	known := false
	for _, name := range deps.Accounts.Names() {
		if name == p.Account {
			known = true
			break
		}
	}
	if !known {
		return nil, fmt.Errorf("unknown account %q; this agent has %v", p.Account, deps.Accounts.Names())
	}

	sess, err := deps.Sessions.NewSession(p.ChatID)
	if err != nil {
		return nil, fmt.Errorf("start a session on %s: %w", p.Account, err)
	}
	sess.SetAccount(p.Account)
	deps.Sessions.MarkDirty()
	return json.Marshal(map[string]any{
		"session_id": sess.ID,
		"account":    p.Account,
		"note":       "new session — an account is part of a conversation, not a setting on one",
	})
}
