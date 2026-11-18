package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"

	"github.com/ngocp/goterm-control/internal/connectors"
	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/mcpserver"
	"github.com/ngocp/goterm-control/internal/secrets"
)

// `bomclaw connectors` is the dashboard's Connectors panel from the shell, and
// `bomclaw mcp connectors` the same as MCP tools — so connecting Stripe to a
// project is one sentence in chat. Both run the operations below; values are
// never printed back.

func runConnectors(args []string) {
	if len(args) == 0 {
		connectorsUsage()
		os.Exit(1)
	}
	cmd, rest := args[0], args[1:]
	// exec takes everything after "--" as the command, untouched.
	var tail []string
	for i, a := range rest {
		if a == "--" {
			rest, tail = rest[:i], rest[i+1:]
			break
		}
	}
	fs := flag.NewFlagSet("connectors "+cmd, flag.ExitOnError)
	dbPath := dbFlag(fs)
	project := fs.String("project", "", "project room (ch_<name>); default: the project you are in")
	stdin := fs.Bool("stdin", false, "connect: read KEY=VALUE lines from stdin (keeps values out of the command line)")
	pos, _ := parseAll(fs, rest)
	db, err := coord.Open(*dbPath)
	if err != nil {
		sfail("connectors: %v", err)
	}
	defer db.Close()
	ops := newConnectorOps(db)
	ctx := context.Background()
	need := func(n int, usage string) {
		if len(pos) < n {
			sfail("usage: bomclaw connectors %s", usage)
		}
	}
	projectArgJSON := func() json.RawMessage {
		b, _ := json.Marshal(map[string]string{"project": *project})
		return b
	}
	out := func(s string, err error) {
		if s != "" {
			fmt.Println(strings.TrimRight(s, "\n"))
		}
		if err != nil {
			sfail("%v", err)
		}
	}

	switch cmd {
	case "list", "ls", "status":
		out(ops.list(ctx, projectArgJSON(), ""))
	case "guide", "how":
		need(1, "guide <connector>")
		out(ops.guide(pos[0], projectArgJSON()))
	case "connect", "set":
		need(1, "connect <connector> KEY=VALUE … (or --stdin)")
		pairs := pos[1:]
		if *stdin {
			pairs = append(pairs, readPairs(os.Stdin)...)
		}
		values := map[string]string{}
		for _, p := range pairs {
			k, v, ok := strings.Cut(p, "=")
			if !ok || strings.TrimSpace(v) == "" {
				sfail("connect: %q is not KEY=VALUE", redact(p))
			}
			values[strings.TrimSpace(k)] = v
		}
		out(ops.connect(ctx, pos[0], values, *project))
	case "verify", "check":
		need(1, "verify <connector>")
		out(ops.verify(ctx, pos[0]))
	case "enable", "on":
		need(1, "enable <connector> [--project ch_x]")
		out(ops.enable(ctx, pos[0], projectArgJSON(), true))
	case "disable", "off":
		need(1, "disable <connector> [--project ch_x]")
		out(ops.enable(ctx, pos[0], projectArgJSON(), false))
	case "disconnect", "unset":
		need(1, "disconnect <connector>")
		out(ops.disconnect(pos[0]))
	case "exec", "run":
		if len(tail) == 0 {
			sfail("usage: bomclaw connectors exec [--project ch_x] -- <command> [args…]")
		}
		c, err := mcpProject(db, projectArgJSON())
		if err != nil {
			sfail("%v", err)
		}
		vars, missing, err := connectors.ProjectEnv(c.Workspace, ops.store.Lookup)
		if err != nil {
			sfail("%v", err)
		}
		for id, why := range missing {
			fmt.Fprintf(os.Stderr, "connectors: %s not connected (%s)\n", id, why)
		}
		bin, err := exec.LookPath(tail[0])
		if err != nil {
			sfail("%v", err)
		}
		env := append(os.Environ(), connectors.ServerEnv(vars)...)
		if err := syscall.Exec(bin, tail, env); err != nil {
			sfail("exec: %v", err)
		}
	default:
		connectorsUsage()
		os.Exit(1)
	}
}

func readPairs(r io.Reader) []string {
	var out []string
	b, _ := io.ReadAll(io.LimitReader(r, 1<<20))
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func connectorsUsage() {
	fmt.Fprintln(os.Stderr, `Usage: bomclaw connectors <command>

What a project can plug into (Stripe, Resend, Gmail, OpenAI…), like Lovable's
Connectors: connect once for the machine, enable on a project, and its code
gets the keys as environment variables — in Preview and in PROD.

  list [--project ch_x]                   catalog, connected or not, enabled on the project
  guide <connector>                       variables and how to call it from code
  connect <connector> KEY=VALUE …         store keys (or --stdin), then check them;
                                          a Google connector answers with a sign-in link
  verify <connector>                      ask the service whether the keys work
  enable <connector> [--project ch_x]     give the project its keys (bomclaw.json "connectors")
  disable <connector> [--project ch_x]    stop giving them
  disconnect <connector>                  delete the stored keys (every project loses them)
  exec [--project ch_x] -- <cmd> [args…]  run a command with the project's connector values

Same store as the dashboard (secrets.yaml); values are never printed.`)
}

// ── operations, shared by the CLI and the MCP tools ───────────────────────

type connectorOps struct {
	db    *coord.DB
	store *secrets.Store
	by    string
}

func newConnectorOps(db *coord.DB) *connectorOps {
	by := "cli"
	if a := os.Getenv("BOMCLAW_AGENT_ID"); a != "" {
		by = a
	}
	return &connectorOps{db: db, store: secrets.New(secrets.PathBeside(db.Path())), by: by}
}

// projectDir is the project a call is about, "" (no error) when there is none
// and the call can do without one.
func (o *connectorOps) projectDir(raw json.RawMessage, required bool) (*coord.Channel, error) {
	c, err := mcpProject(o.db, raw)
	if err != nil && required {
		return nil, err
	}
	if err != nil {
		return nil, nil
	}
	return c, nil
}

func state(v connectors.View) string {
	switch {
	case v.Ready:
		return "connected"
	case v.Connected:
		return "keys stored, but " + strings.Join(v.Requires, ", ") + " is not set up"
	case v.Auth == connectors.AuthOAuth:
		return "not connected (Google sign-in)"
	}
	return "not connected"
}

func (o *connectorOps) list(_ context.Context, raw json.RawMessage, category string) (string, error) {
	c, _ := o.projectDir(raw, false)
	dir := ""
	if c != nil {
		dir = c.Workspace
	}
	views, err := connectors.Views(o.store, dir)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if c != nil {
		var on []string
		for _, v := range views {
			if v.Enabled {
				on = append(on, v.ID)
			}
		}
		fmt.Fprintf(&b, "Project %s — enabled: %s\n", c.ID, orDefault(strings.Join(on, ", "), "none"))
		if _, missing, _ := connectors.ProjectEnv(dir, o.store.Lookup); len(missing) > 0 {
			for _, id := range sortedKeys(missing) {
				fmt.Fprintf(&b, "  ! %s is enabled but gives nothing yet: %s\n", id, missing[id])
			}
		}
		b.WriteString("\n")
	}
	for _, cat := range connectors.Categories {
		if category != "" && category != cat.ID {
			continue
		}
		fmt.Fprintf(&b, "%s — %s\n", cat.Title, cat.Blurb)
		for _, v := range views {
			if v.Category != cat.ID || v.Hidden {
				continue
			}
			mark := " "
			if v.Enabled {
				mark = "✓"
			}
			fmt.Fprintf(&b, " %s %-22s %-34s %s\n", mark, v.ID, v.Short, state(v))
		}
	}
	b.WriteString("\n✓ = enabled on the project. connector_guide <id> says how to use one in code.\n")
	return b.String(), nil
}

func (o *connectorOps) guide(id string, raw json.RawMessage) (string, error) {
	c, ok := connectors.ByID(id)
	if !ok {
		return "", fmt.Errorf("no connector %q (known: %s)", id, strings.Join(connectors.IDs(), ", "))
	}
	var b strings.Builder
	b.WriteString(connectors.Guide(c))
	views, _ := connectors.Views(o.store, "")
	for _, v := range views {
		if v.ID == c.ID {
			fmt.Fprintf(&b, "\nState: %s", state(v))
		}
	}
	if p, _ := o.projectDir(raw, false); p != nil {
		on, _ := connectors.Enabled(p.Workspace)
		en := "NOT enabled"
		for _, x := range on {
			if x == c.ID {
				en = "enabled"
			}
		}
		fmt.Fprintf(&b, " · %s on %s", en, p.ID)
	}
	b.WriteString("\n\n" + connectors.Rules + "\n")
	return b.String(), nil
}

func (o *connectorOps) connect(ctx context.Context, id string, values map[string]string, project string) (string, error) {
	c, ok := connectors.ByID(id)
	if !ok {
		return "", fmt.Errorf("no connector %q (known: %s)", id, strings.Join(connectors.IDs(), ", "))
	}
	if c.Auth == connectors.AuthOAuth {
		if len(values) > 0 {
			return "", fmt.Errorf("%s connects by signing in with Google, not with values — call this without values for the link", c.Title)
		}
		return o.signInLink(c, project)
	}
	if len(values) == 0 {
		var need []string
		for _, f := range c.Fields {
			s := f.Env
			if f.Required {
				s += " (required)"
			}
			need = append(need, s)
		}
		return "", fmt.Errorf("%s needs: %s. Ask the person for them (create at %s)", c.Title, strings.Join(need, ", "), c.Docs)
	}
	if err := o.store.Set(c.ID, values, o.by); err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Stored %d value(s) for %s (machine-wide; enable it on a project to use it).", len(values), c.Title)
	if c.Hidden {
		msg = fmt.Sprintf("Stored %d value(s) for %s — the Google connectors can sign in now (connector_connect id=gmail, google-sheets…).", len(values), c.Title)
	}
	detail, err := connectors.Verify(ctx, c.ID, o.store.Lookup)
	if err != nil {
		return msg, fmt.Errorf("check FAILED: %v — the values are stored; ask for corrected ones", err)
	}
	return msg + "\nCheck OK: " + detail, nil
}

func (o *connectorOps) signInLink(c *connectors.Connector, project string) (string, error) {
	if o.store.Lookup("GOOGLE_CLIENT_ID") == "" || o.store.Lookup("GOOGLE_CLIENT_SECRET") == "" {
		return "", fmt.Errorf("%s signs in through the person's own Google OAuth client, which is not set up. Ask for "+
			"GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET (Google Cloud → APIs & Services → Credentials → OAuth client ID, "+
			"type Web application, redirect URI <dashboard>%s; enable the API for %s), then call connector_connect "+
			"id=google-oauth with them", c.Title, connectors.CallbackPath, c.Title)
	}
	q := url.Values{"id": {c.ID}}
	if project != "" {
		q.Set("project", project)
	}
	path := connectors.StartPath + "?" + q.Encode()
	origin := connectors.Origin(o.store.Path())
	if origin == "" {
		return fmt.Sprintf("Send the person to the dashboard: Connectors → %s → \"Kết nối với Google\" (or open %s on the dashboard's address).", c.Title, path), nil
	}
	return fmt.Sprintf("Give the person this link to sign in with Google (it needs their dashboard login): %s%s\n"+
		"When they say it is done, call connector_verify id=%s.", origin, path, c.ID), nil
}

func (o *connectorOps) verify(ctx context.Context, id string) (string, error) {
	c, ok := connectors.ByID(id)
	if !ok {
		return "", fmt.Errorf("no connector %q", id)
	}
	detail, err := connectors.Verify(ctx, c.ID, o.store.Lookup)
	if err != nil {
		return "", fmt.Errorf("%s: FAIL — %v", c.Title, err)
	}
	return c.Title + ": OK — " + detail, nil
}

func (o *connectorOps) enable(ctx context.Context, id string, raw json.RawMessage, on bool) (string, error) {
	p, err := o.projectDir(raw, true)
	if err != nil {
		return "", err
	}
	c, ok := connectors.ByID(id)
	if !ok {
		return "", fmt.Errorf("no connector %q (known: %s)", id, strings.Join(connectors.IDs(), ", "))
	}
	changed, err := connectors.SetEnabled(p.Workspace, c.ID, on)
	if err != nil {
		return "", err
	}
	verb := map[bool]string{true: "enabled on", false: "disabled on"}[on]
	if !changed {
		return fmt.Sprintf("%s was already %s %s.", c.Title, strings.TrimSuffix(verb, " on"), p.ID), nil
	}
	msg := fmt.Sprintf("%s %s %s (bomclaw.json \"connectors\"). An open Preview restarts with the new keys by itself; PROD gets them on the next publish.", c.Title, verb, p.ID)
	if on {
		views, _ := connectors.Views(o.store, "")
		for _, v := range views {
			if v.ID == c.ID && !v.Ready {
				msg += "\nNOT connected yet (" + state(v) + ") — connect it (connector_connect) before the code can use it."
			}
		}
		msg += "\nVariables: "
		var names []string
		for _, f := range c.Envs() {
			names = append(names, f.Env)
		}
		msg += strings.Join(names, ", ") + ". connector_guide id=" + c.ID + " for how to call it."
	}
	return msg, nil
}

func (o *connectorOps) disconnect(id string) (string, error) {
	c, ok := connectors.ByID(id)
	if !ok {
		return "", fmt.Errorf("no connector %q", id)
	}
	if err := o.store.Unset(c.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Removed %s's stored keys. Projects that enable it get nothing from it until it is connected again (a value in .env still counts).", c.Title), nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ── MCP ───────────────────────────────────────────────────────────────────

func connectorTools(db *coord.DB) []mcpserver.Tool {
	o := newConnectorOps(db)
	idArg := map[string]any{"type": "string", "description": "Connector id, e.g. stripe, resend, gmail, openai (connectors_list shows all)"}
	obj := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}
	type toolArgs struct {
		ID       string            `json:"id"`
		Project  string            `json:"project"`
		Category string            `json:"category"`
		Values   map[string]string `json:"values"`
	}
	parse := func(raw json.RawMessage) (a toolArgs) {
		_ = json.Unmarshal(raw, &a)
		return a
	}
	return []mcpserver.Tool{
		{
			Name: "connectors_list",
			Description: "The connectors a project can plug into (Stripe, PayPal, Resend, Telegram, Gmail, Google Sheets, Notion, " +
				"OpenAI, Google Maps…), which are connected on this machine, and which are enabled on the project. Changes nothing.",
			Schema: obj(map[string]any{"project": projectArg,
				"category": map[string]any{"type": "string", "enum": []string{"grow", "sell", "communicate", "work", "ai"}}}),
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				return o.list(ctx, raw, parse(raw).Category)
			},
		},
		{
			Name: "connector_guide",
			Description: "How to use a connector in the project's code: its environment variables (secret or public), " +
				"a working request, and where the code must run. Read it before writing code that calls the service.",
			Schema: obj(map[string]any{"id": idArg, "project": projectArg}, "id"),
			Run: func(_ context.Context, raw json.RawMessage) (string, error) {
				return o.guide(parse(raw).ID, raw)
			},
		},
		{
			Name: "connector_connect",
			Description: "Connect a service for this machine with keys the person gave you in this conversation " +
				"(values: {\"STRIPE_SECRET_KEY\": \"sk_test_…\"}); they are stored, never echoed, and checked against the service. " +
				"For Google connectors (gmail, google-drive, google-sheets, google-calendar, google-search-console) call it " +
				"WITHOUT values: it answers with a sign-in link for the person. Never invent or guess a key.",
			Schema: obj(map[string]any{"id": idArg, "project": projectArg,
				"values": map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"},
					"description": "ENV_NAME → value, as the person gave them"}}, "id"),
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				a := parse(raw)
				return o.connect(ctx, a.ID, a.Values, a.Project)
			},
		},
		{
			Name:        "connector_verify",
			Description: "Ask the service whether a connector's stored keys work, and what they reach (account, mode, workspace).",
			Schema:      obj(map[string]any{"id": idArg}, "id"),
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				return o.verify(ctx, parse(raw).ID)
			},
		},
		{
			Name: "connector_enable",
			Description: "Enable a connector on a project: its code gets the keys as environment variables in Preview and " +
				"on publish (bomclaw.json \"connectors\"). Connect it first, or right after.",
			Schema: obj(map[string]any{"id": idArg, "project": projectArg}, "id"),
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				return o.enable(ctx, parse(raw).ID, raw, true)
			},
		},
		{
			Name:        "connector_disable",
			Description: "Stop giving a project a connector's keys. The keys stay stored for other projects.",
			Schema:      obj(map[string]any{"id": idArg, "project": projectArg}, "id"),
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				return o.enable(ctx, parse(raw).ID, raw, false)
			},
		},
		{
			Name: "connector_disconnect",
			Description: "Delete a connector's stored keys from this machine — every project using it loses it. " +
				"Only when the person asked to remove or revoke it.",
			Schema: obj(map[string]any{"id": idArg}, "id"),
			Run: func(_ context.Context, raw json.RawMessage) (string, error) {
				return o.disconnect(parse(raw).ID)
			},
		},
	}
}
