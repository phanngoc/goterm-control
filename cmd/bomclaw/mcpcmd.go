package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/deploy"
	"github.com/ngocp/goterm-control/internal/gateway"
	"github.com/ngocp/goterm-control/internal/mcpserver"
	"github.com/ngocp/goterm-control/internal/secrets"
)

// `bomclaw mcp` is bomclaw's own MCP server, on stdio. The bundled "publish"
// plugin starts it inside every agent turn, so publishing a project is a tool
// in the model's tool list — the same thing the dashboard's Publish button
// does, through the same EnvManager, announced in the same room.
//
// stdout is the protocol. Nothing else may be printed to it.

func runMCP(args []string) {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(os.Stderr, `Usage: bomclaw mcp

Serves bomclaw's tools over MCP (stdio), for an agent's CLI:
  publish_status   where a project's PROD is: provider, account, plan, live URL
  publish_setup    declare how a project publishes (bomclaw.json "deploy")
  publish          build and publish a project to PROD, and say so in its room

The project is the "project" argument, else $BOMCLAW_PROJECT,
$BOMCLAW_TASK_CHANNEL, or the project whose folder the agent is in.`)
		return
	}
	db, err := coord.Open(coord.DefaultPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()
	deploy.Lookup = secrets.New(secrets.PathBeside(db.Path())).Lookup

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &mcpserver.Server{Name: "bomclaw", Version: "1", Tools: publishTools(db, gateway.NewEnvManager(db, nil))}
	if err := srv.Serve(ctx, os.Stdin, os.Stdout); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "mcp: %v\n", err)
	}
}

var projectArg = map[string]any{
	"type":        "string",
	"description": "Project room: ch_<name> or <name>. Optional — defaults to the project you are working in.",
}

func publishTools(db *coord.DB, envs *gateway.EnvManager) []mcpserver.Tool {
	agent := strings.TrimSpace(os.Getenv("BOMCLAW_AGENT_ID"))
	return []mcpserver.Tool{
		{
			Name: "publish_status",
			Description: "Where a project's PROD (public site) stands: provider, whether it is connected, account, plan, " +
				"services, live URL and last publish — and whether publishing is set up. Changes nothing.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{"project": projectArg}},
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				c, err := mcpProject(db, raw)
				if err != nil {
					return "", err
				}
				return describeStatus(envs.Status(ctx, c.ID)), nil
			},
		},
		{
			Name: "publish_setup",
			Description: "Declare how a project publishes to PROD (writes \"deploy\" into its bomclaw.json). Needed once, " +
				"before the first publish. A site already live on Cloudflare under another name is adopted when " +
				"publish_status found it — its Worker name and domain are kept unless you pass others.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"project":  projectArg,
				"static":   map[string]any{"type": "string", "description": "Folder of built files (dist, build, out, public…). Guessed when omitted."},
				"build":    map[string]any{"type": "string", "description": "Command that builds it, e.g. npm run build"},
				"domain":   map[string]any{"type": "string", "description": "Hostname on one of the account's zones, e.g. app.bomclaw.org"},
				"name":     map[string]any{"type": "string", "description": "Worker name; default from the room"},
				"spa":      map[string]any{"type": "boolean", "description": "Serve index.html for unknown paths (client-side router)"},
				"database": map[string]any{"type": "string", "description": "Local SQLite file to host as D1, relative to the project"},
				"main":     map[string]any{"type": "string", "description": "Optional Worker script (an API in front of the files)"},
				"replace":  map[string]any{"type": "boolean", "description": "Replace a declaration that already exists"},
			}},
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				c, err := mcpProject(db, raw)
				if err != nil {
					return "", err
				}
				var a struct {
					Static, Build, Domain, Name, Database, Main string
					SPA, Replace                                bool
				}
				_ = json.Unmarshal(raw, &a)
				if old, err := deploy.Read(c.Workspace); err == nil && old != nil && !a.Replace {
					return "", fmt.Errorf("%s already publishes (bomclaw.json has \"deploy\"); pass replace=true to change it", c.ID)
				}
				// Adopt a site that is live without a declaration.
				st := envs.Status(ctx, c.ID)
				if st.Prod.DiscoveredFrom != "" {
					if a.Name == "" {
						a.Name = st.Prod.Name
					}
					if a.Domain == "" && len(st.Prod.Domains) > 0 {
						a.Domain = st.Prod.Domains[0]
					}
				}
				s := &deploy.Spec{Target: deploy.DefaultTarget, Static: a.Static, Build: a.Build, Domain: a.Domain,
					Name: a.Name, SPA: a.SPA, Main: a.Main}
				if s.Static == "" && s.Main == "" {
					if s.Static = guessStatic(c.Workspace); s.Static == "" {
						return "", fmt.Errorf("no built folder found (dist/, build/, out/, public/ with an index.html) — build first, or pass static")
					}
				}
				if a.Database != "" {
					s.Database = &deploy.Database{Engine: "sqlite", File: a.Database}
				}
				raw2, _ := json.Marshal(map[string]any{"deploy": s})
				if _, err := deploy.Parse(raw2); err != nil {
					return "", err
				}
				if err := deploy.Write(c.Workspace, s); err != nil {
					return "", err
				}
				return fmt.Sprintf("Wrote \"deploy\" to %s.\n\n%s", filepath.Join(c.Workspace, deploy.Manifest),
					describeStatus(envs.Status(ctx, c.ID))), nil
			},
		},
		{
			Name: "publish",
			Description: "Build and publish a project to PROD — the dashboard's Publish button. Runs its build, uploads, " +
				"attaches its domain, and announces the result in the project's room. Takes minutes; answers when done " +
				"with the live URL and the end of the log. with_database REPLACES the PROD tables with the local file: " +
				"only when the person asked for it. dry_run builds and checks without publishing.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"project":       projectArg,
				"dry_run":       map[string]any{"type": "boolean", "description": "Build and check only; nothing is published"},
				"with_database": map[string]any{"type": "boolean", "description": "Also upload the local database — replaces PROD data"},
			}},
			Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
				c, err := mcpProject(db, raw)
				if err != nil {
					return "", err
				}
				var a struct {
					DryRun       bool `json:"dry_run"`
					WithDatabase bool `json:"with_database"`
				}
				_ = json.Unmarshal(raw, &a)
				by := coord.Member{Kind: coord.MemberAgent, ID: agent}
				if agent == "" {
					by = coord.Member{Kind: coord.MemberAgent, ID: "bomclaw"}
				}
				if _, err := envs.DeployBy(c.ID, deploy.Options{DryRun: a.DryRun, WithDB: a.WithDatabase}, by); err != nil {
					return "", err
				}
				wctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
				defer cancel()
				v, err := envs.Wait(wctx, c.ID)
				tail := lastLines(envs.Logs(c.ID), 30)
				if err != nil {
					return tail, err
				}
				if v.Error != "" {
					return tail, fmt.Errorf("publish failed: %s", v.Error)
				}
				switch {
				case a.DryRun:
					return "Dry run passed — nothing was published.\n\n" + tail, nil
				case v.Result != nil && v.Result.URL != "":
					return fmt.Sprintf("Published %s → %s (announced in the room).\n\n%s", c.ID, v.Result.URL, tail), nil
				}
				return "Published (announced in the room).\n\n" + tail, nil
			},
		},
	}
}

// mcpProject finds the project a tool call is about.
func mcpProject(db *coord.DB, raw json.RawMessage) (*coord.Channel, error) {
	var a struct {
		Project string `json:"project"`
	}
	_ = json.Unmarshal(raw, &a)
	id := strings.TrimSpace(a.Project)
	for _, env := range []string{"BOMCLAW_PROJECT", "BOMCLAW_TASK_CHANNEL"} {
		if id == "" {
			id = strings.TrimSpace(os.Getenv(env))
		}
	}
	if id != "" {
		c, err := db.GetChannel(id)
		if err != nil && !strings.HasPrefix(id, "ch_") {
			c, err = db.GetChannel("ch_" + id)
		}
		if err != nil {
			if all, lerr := db.ListChannels("", ""); lerr == nil {
				for i := range all {
					if strings.EqualFold(all[i].Name, strings.TrimPrefix(id, "#")) {
						c, err = &all[i], nil
						break
					}
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("no project room %q", id)
		}
		if c.Workspace == "" {
			return nil, fmt.Errorf("%s is a room with no project folder, so it has nothing to publish", c.ID)
		}
		return c, nil
	}
	// The project whose folder we are standing in.
	wd, _ := os.Getwd()
	if all, err := db.ListChannels("", ""); err == nil {
		best := -1
		for i, c := range all {
			if c.Workspace == "" {
				continue
			}
			ws := filepath.Clean(c.Workspace)
			if (wd == ws || strings.HasPrefix(wd, ws+string(filepath.Separator))) && (best < 0 || len(ws) > len(all[best].Workspace)) {
				best = i
			}
		}
		if best >= 0 {
			return &all[best], nil
		}
	}
	return nil, fmt.Errorf("which project? pass project (ch_<name>) — not inside a project folder")
}

// describeStatus is env.status in the words a model acts on.
func describeStatus(st gateway.EnvStatus) string {
	var b strings.Builder
	p := st.Prod
	fmt.Fprintf(&b, "Project %s (%s)\n", st.ChannelID, st.ProjectDir)
	provider := orDefault(p.Title, orDefault(p.Provider, "Cloudflare"))
	if !p.Connected {
		fmt.Fprintf(&b, "%s: NOT connected — %s\n", provider, p.Error)
		return b.String()
	}
	fmt.Fprintf(&b, "%s: connected", provider)
	if a := p.Account; a != nil {
		if a.Name != "" {
			fmt.Fprintf(&b, " · account %s", a.Name)
		}
		if a.Plan != "" {
			fmt.Fprintf(&b, " · Workers plan %s", a.Plan)
		}
		for _, z := range a.Zones {
			fmt.Fprintf(&b, " · zone %s on %s", z.Zone, z.Plan)
		}
	}
	b.WriteString("\n")
	if len(p.Services) > 0 {
		fmt.Fprintf(&b, "Services: %s\n", strings.Join(p.Services, ", "))
	}
	switch {
	case p.Deployed:
		fmt.Fprintf(&b, "PROD: live at %s (Worker %s, published %s)\n", p.URL, p.Name, p.DeployedAt)
	case p.Error != "":
		fmt.Fprintf(&b, "PROD: error — %s\n", p.Error)
	default:
		b.WriteString("PROD: not published yet\n")
	}
	switch {
	case st.Declared:
		b.WriteString("Publishing is set up (bomclaw.json \"deploy\") — call publish.\n")
	case p.DiscoveredFrom != "":
		fmt.Fprintf(&b, "Live but NOT set up for publishing from here (matched by %s). Call publish_setup to adopt it, then publish.\n", p.DiscoveredFrom)
	default:
		b.WriteString("Publishing is NOT set up — call publish_setup (static folder, build command, domain), then publish.\n")
	}
	if p.WantDomain != "" && !contains(p.Domains, p.WantDomain) {
		fmt.Fprintf(&b, "Domain %s is attached on the next publish.\n", p.WantDomain)
	}
	if st.Error != "" {
		fmt.Fprintf(&b, "Note: %s\n", st.Error)
	}
	return b.String()
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{"…"}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n")
}
