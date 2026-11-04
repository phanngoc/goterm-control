package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/plugins"
)

// `bomclaw plugins` is the agent's own handle on its toolkit, and a person's
// view of it. The listing is computed by the same plugins.Load a turn uses,
// from the same two folders, so "why is that plugin not doing anything" is
// answered by looking: its status and the reason sit on its row.

const pluginsUsage = `Usage: bomclaw plugins <command> [flags]

  list                       Every plugin and skill, its status, and why
  show <name>                One plugin in full: contributions, config, reasons
  prompt                     The block the agent receives on its next turn
  new <name>                 Scaffold a plugin in this agent's workspace
  install <git-url|folder>   Fetch a plugin (--ref, --path, --name, --replace, --trust)
  update <name>              Re-fetch an installed plugin from its source (--trust)
  uninstall <name>           Remove a plugin and forget its switches and settings
  migrate                    Move skill folders from skills/ into plugins/ (also runs at gateway start)
  enable|disable <name>      Turn a plugin (or a bare skill) on or off
  trust|untrust <name>       Let a project's plugin run commands/MCP, or stop it
  config <name> [k=v ...]    Show settings, or set them (k= clears one)

Flags: --workspace <dir> (the agent's; default $BOMCLAW_AGENT_WORKSPACE, then config)
       --project <dir>   (the turn's folder; default the current directory)
       --json            (list/show as JSON)

Changes apply from the agent's next turn; nothing restarts.`

func runPlugins(args []string) {
	fs := flag.NewFlagSet("plugins", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "Path to config file")
	workspace := fs.String("workspace", "", "The agent's workspace")
	project := fs.String("project", "", "The folder a turn runs in")
	asJSON := fs.Bool("json", false, "Print JSON")
	ref := fs.String("ref", "", "install: branch or tag")
	subPath := fs.String("path", "", "install: the plugin's folder inside the repository")
	asName := fs.String("name", "", "install: install under this name")
	replace := fs.Bool("replace", false, "install: overwrite an existing plugin of that name")
	trust := fs.Bool("trust", false, "install/update: let its commands, env and MCP servers run")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, pluginsUsage) }

	sub, rest := "list", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}
	// Positional arguments may come before or after the flags.
	var pos []string
	for len(rest) > 0 {
		fs.Parse(rest)
		rest = fs.Args()
		if len(rest) > 0 {
			pos, rest = append(pos, rest[0]), rest[1:]
		}
	}

	ws := pluginWorkspace(*workspace, *configPath)
	turn := *project
	if turn == "" {
		turn, _ = os.Getwd()
	}
	need := func(n int) {
		if len(pos) < n {
			fs.Usage()
			os.Exit(1)
		}
	}

	switch sub {
	case "list", "ls":
		pluginList(plugins.Load(ws, turn), *asJSON)
	case "show":
		need(1)
		pluginShow(plugins.Load(ws, turn), pos[0], *asJSON)
	case "prompt":
		fmt.Print(plugins.Load(ws, turn).Prompt())
	case "new":
		need(1)
		dir, err := plugins.Scaffold(ws, pos[0])
		exitIf(err)
		fmt.Printf("created %s\nEdit %s, then `bomclaw plugins show %s`.\n", dir, plugins.File, pos[0])
	case "install":
		need(1)
		inst, err := plugins.Install(ws, plugins.Source{URL: pos[0], Ref: *ref, Path: *subPath},
			plugins.InstallOptions{Name: *asName, Replace: *replace, Trust: *trust})
		exitIf(err)
		reportInstall(inst, "installed")
	case "update":
		need(1)
		inst, err := plugins.Update(ws, pos[0], *trust)
		exitIf(err)
		reportInstall(inst, "updated")
	case "uninstall", "remove", "rm":
		need(1)
		exitIf(plugins.Uninstall(ws, pos[0]))
		fmt.Printf("%s uninstalled — from the next turn\n", pos[0])
	case "migrate":
		moved, skipped, err := plugins.MigrateSkills(ws)
		exitIf(err)
		for _, m := range moved {
			tag := ""
			if m.Bundled {
				tag = " (bundled manifest)"
			}
			fmt.Printf("moved %s → %s%s\n", m.Name, m.To, tag)
		}
		for _, s := range skipped {
			fmt.Printf("left in place: %s\n", s)
		}
		if len(moved) == 0 && len(skipped) == 0 {
			fmt.Println("nothing to migrate — every skill is already a plugin")
		}
	case "enable", "disable":
		need(1)
		updateState(ws, func(st *plugins.State) { st.SetEnabled(pos[0], sub == "enable") })
		fmt.Printf("%s %sd — from the next turn\n", pos[0], sub)
	case "trust", "untrust":
		need(1)
		updateState(ws, func(st *plugins.State) { st.SetTrusted(pos[0], sub == "trust") })
		fmt.Printf("%s %sed — from the next turn\n", pos[0], sub)
	case "config":
		need(1)
		if len(pos) == 1 {
			pluginConfig(plugins.Load(ws, turn), pos[0])
			return
		}
		updateState(ws, func(st *plugins.State) {
			for _, kv := range pos[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					exitIf(fmt.Errorf("%q is not key=value", kv))
				}
				st.SetConfig(pos[0], k, v)
			}
		})
		pluginConfig(plugins.Load(ws, turn), pos[0])
	case "help", "-h", "--help":
		fs.Usage()
	default:
		fs.Usage()
		os.Exit(1)
	}
}

// pluginWorkspace finds the agent's workspace: the flag, then the variable
// every turn carries, then the config file.
func pluginWorkspace(flagValue, configPath string) string {
	if flagValue != "" {
		return flagValue
	}
	if ws := os.Getenv(plugins.WorkspaceEnv); ws != "" {
		return ws
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		exitIf(fmt.Errorf("%v\nPass --workspace <dir>, or run this from the agent's own directory", err))
	}
	return cfg.Claude.Workspace
}

func updateState(ws string, change func(*plugins.State)) {
	st, err := plugins.LoadState(ws)
	exitIf(err)
	change(&st)
	exitIf(st.Save(ws))
}

func exitIf(err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "plugins: %v\n", err)
		os.Exit(1)
	}
}

func pluginList(k *plugins.Kit, asJSON bool) {
	if asJSON {
		b, _ := json.MarshalIndent(k, "", "  ")
		fmt.Println(string(b))
		return
	}
	if len(k.Plugins) == 0 {
		fmt.Println("no plugins or skills — `bomclaw plugins new <name>` starts one")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tKIND\tSCOPE\tSTATUS\tBRINGS / WHY NOT")
	for _, p := range k.Plugins {
		note := brings(p)
		if len(p.Reasons) > 0 && p.Status != plugins.Active {
			note = strings.Join(p.Reasons, "; ")
		}
		status := string(p.Status)
		if p.Untrusted {
			status += " (untrusted)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", p.Name, p.Kind, p.Scope, status, truncate(note, 90))
	}
	w.Flush()
	for _, e := range k.Errors {
		fmt.Fprintf(os.Stderr, "warning: %s\n", e)
	}
}

// brings summarises a plugin's contributions in one cell.
func brings(p *plugins.Plugin) string {
	var parts []string
	if n := len(p.SkillList); n > 0 {
		parts = append(parts, countOf(n, "skill"))
	}
	if n := len(p.Commands); n > 0 {
		parts = append(parts, countOf(n, "command"))
	}
	if n := len(p.Servers); n > 0 {
		parts = append(parts, countOf(n, "MCP server"))
	}
	if p.PromptText != "" {
		parts = append(parts, "prompt")
	}
	if len(parts) == 0 {
		return oneLine(p.Description)
	}
	return strings.Join(parts, ", ")
}

func countOf(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func pluginShow(k *plugins.Kit, name string, asJSON bool) {
	p := k.Find(name)
	if p == nil {
		exitIf(fmt.Errorf("no plugin or skill named %q here", name))
	}
	if asJSON {
		b, _ := json.MarshalIndent(p, "", "  ")
		fmt.Println(string(b))
		return
	}
	fmt.Printf("%s  %s · %s · %s", p.Name, p.Kind, p.Scope, p.Status)
	if p.Version != "" {
		fmt.Printf(" · v%s", p.Version)
	}
	fmt.Printf("\n%s\n%s\n", p.Dir, orDash(p.Description))
	for _, r := range p.Reasons {
		fmt.Printf("  ! %s\n", r)
	}
	if p.Untrusted {
		fmt.Printf("  ! %s plugin: its commands, env and MCP servers are off until `bomclaw plugins trust %s`\n", p.Scope, p.Name)
	}
	section := func(title string, lines []string) {
		if len(lines) > 0 {
			fmt.Printf("\n%s:\n  %s\n", title, strings.Join(lines, "\n  "))
		}
	}
	var sk []string
	for _, s := range p.SkillList {
		sk = append(sk, s.Name+" — "+s.Path)
	}
	section("skills", sk)
	section("commands", p.Commands)
	var srv []string
	for _, n := range sortedNames(p.Servers) {
		s := p.Servers[n]
		if s.URL != "" {
			srv = append(srv, n+" → "+s.URL)
		} else {
			srv = append(srv, n+" → "+strings.Join(append([]string{s.Command}, s.Args...), " "))
		}
	}
	section("mcp", srv)
	section("provides", p.Provides)
	section("injects", append(append([]string{}, p.Inject.Required...), optionalNames(p.Inject.Optional)...))
	if p.PromptText != "" {
		fmt.Printf("\nprompt:\n  %s\n", strings.ReplaceAll(p.PromptText, "\n", "\n  "))
	}
	if len(p.Config) > 0 {
		fmt.Println()
		pluginConfig(k, name)
	}
}

func pluginConfig(k *plugins.Kit, name string) {
	p := k.Find(name)
	if p == nil {
		exitIf(fmt.Errorf("no plugin named %q here", name))
	}
	if len(p.Config) == 0 {
		fmt.Printf("%s has no settings\n", name)
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "SETTING\tVALUE\tENV\tABOUT")
	for _, key := range sortedNames(p.Config) {
		f := p.Config[key]
		v, set := p.Values[key]
		switch {
		case !set && f.Required:
			v = "(required, unset)"
		case !set:
			v = "-"
		case f.Secret:
			v = "(set, secret)"
		}
		about := f.Description
		if len(f.Enum) > 0 {
			about = strings.TrimSpace(about + " one of " + strings.Join(f.Enum, "|"))
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", key, v, plugins.ConfigEnvName(p.Name, key), about)
	}
	w.Flush()
}

func optionalNames(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = s + " (optional)"
	}
	return out
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func reportInstall(inst *plugins.Installed, verb string) {
	at := inst.Source.URL
	if inst.Source.Commit != "" {
		at += " @ " + inst.Source.Commit[:min(12, len(inst.Source.Commit))]
	}
	fmt.Printf("%s %s from %s\n  %s\n", verb, inst.Name, at, inst.Dir)
	for _, s := range inst.Skipped {
		fmt.Printf("  skipped %s (symlinks and special files are never installed)\n", s)
	}
	if inst.Trusted {
		fmt.Println("  trusted: its commands, env and MCP servers run from the next turn")
	} else {
		fmt.Printf("  NOT trusted: skills and prompt apply now; read it, then `bomclaw plugins trust %s`\n", inst.Name)
	}
}
