package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
	"github.com/ngocp/goterm-control/internal/deploy"
	"github.com/ngocp/goterm-control/internal/gateway"
	"github.com/ngocp/goterm-control/internal/secrets"
)

// `bomclaw env` is a project's two environments from the agent's shell: DEV
// (this machine — Preview, build, local database) and PROD (Cloudflare). It
// runs in-process, the same code the dashboard's Environments panel calls, so
// an agent can check and publish without a browser.

func runEnv(args []string) {
	if len(args) == 0 {
		envUsage()
		os.Exit(1)
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		fs := flag.NewFlagSet("env status", flag.ExitOnError)
		dbPath := dbFlag(fs)
		asJSON := fs.Bool("json", false, "Print the status as JSON")
		id, _ := parseLeading(fs, rest)
		c := envProject(*dbPath, id)
		envStatus(c, *asJSON)

	case "init":
		fs := flag.NewFlagSet("env init", flag.ExitOnError)
		dbPath := dbFlag(fs)
		static := fs.String("static", "", "Folder of built files to publish (dist, build, public…)")
		build := fs.String("build", "", "Command that builds it (npm run build)")
		cwd := fs.String("cwd", "", "Folder build runs in and static is found, relative to the project")
		domain := fs.String("domain", "", "Hostname on a Cloudflare zone (phim.bomclaw.org); optional")
		name := fs.String("name", "", "Worker name; default from the channel")
		spa := fs.Bool("spa", false, "Serve index.html for unknown paths (client-side router)")
		dbFile := fs.String("database", "", "Local SQLite file to host as Cloudflare D1, relative to the project")
		main := fs.String("main", "", "Optional Worker script (an API in front of the files)")
		target := fs.String("provider", deploy.DefaultTarget, "Where PROD lives: "+strings.Join(deploy.Available(), ", "))
		dockerfile := fs.String("dockerfile", "", "Fly.io: the project's Dockerfile, for an app that runs a server")
		port := fs.Int("port", 0, "Fly.io: the port the image listens on (default 8080)")
		region := fs.String("region", "", "Fly.io: region (default $FLY_REGION or sin)")
		force := fs.Bool("force", false, "Replace a deploy that is already declared")
		id, _ := parseLeading(fs, rest)
		c := envProject(*dbPath, id)
		if old, err := deploy.Read(c.Workspace); err == nil && old != nil && !*force {
			fmt.Fprintf(os.Stderr, "env init: %s already declares a PROD environment; edit bomclaw.json, or pass --force\n", c.ID)
			os.Exit(1)
		}
		s := &deploy.Spec{Target: *target, Static: *static, Build: *build, Cwd: *cwd,
			Domain: *domain, Name: *name, SPA: *spa, Main: *main,
			Dockerfile: *dockerfile, Port: *port, Region: *region}
		if s.Static == "" && s.Main == "" && s.Dockerfile == "" {
			s.Static = guessStatic(c.Workspace)
			if s.Static == "" {
				fmt.Fprintln(os.Stderr, "env init: pass --static <built folder> (no dist/, build/, out/ or public/ with an index.html here)")
				os.Exit(1)
			}
			fmt.Printf("static: %s (guessed)\n", s.Static)
		}
		if *dbFile != "" {
			s.Database = &deploy.Database{Engine: "sqlite", File: *dbFile}
		}
		raw, _ := json.Marshal(map[string]any{"deploy": s})
		if _, err := deploy.Parse(raw); err != nil {
			fmt.Fprintf(os.Stderr, "env init: %v\n", err)
			os.Exit(1)
		}
		if err := deploy.Write(c.Workspace, s); err != nil {
			fmt.Fprintf(os.Stderr, "env init: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("wrote %s/%s\n\n", c.Workspace, deploy.Manifest)
		envStatus(c, false)

	case "deploy":
		fs := flag.NewFlagSet("env deploy", flag.ExitOnError)
		dbPath := dbFlag(fs)
		withDB := fs.Bool("with-db", false, "Also upload the local database — REPLACES the PROD tables")
		dry := fs.Bool("dry-run", false, "Build and print the wrangler config; send nothing to Cloudflare")
		id, _ := parseLeading(fs, rest)
		c := envProject(*dbPath, id)
		spec, err := deploy.Read(c.Workspace)
		if err != nil || spec == nil {
			fmt.Fprintf(os.Stderr, "env deploy: %s declares no PROD environment (bomclaw env init %s --static dist) %v\n", c.ID, c.ID, orEmpty(err))
			os.Exit(1)
		}
		r := spec.Resolve(c.Workspace, c.ID)
		prov, err := deploy.For(spec.Target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "env deploy: %v\n", err)
			os.Exit(1)
		}
		if ok, hint := prov.Connected(); !ok && !*dry {
			fmt.Fprintf(os.Stderr, "env deploy: %s\n", hint)
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		res, err := prov.Deploy(ctx, r, deploy.Options{WithDB: *withDB, DryRun: *dry}, os.Stdout)
		if err != nil {
			fmt.Fprintf(os.Stderr, "env deploy: %v\n", err)
			os.Exit(1)
		}
		if res.DryRun {
			os.RemoveAll(strings.TrimSuffix(res.ConfigPath, "/wrangler.json"))
		} else if res.URL != "" {
			fmt.Printf("\nPROD: %s\n", res.URL)
		}

	default:
		envUsage()
		os.Exit(1)
	}
}

func envStatus(c *coord.Channel, asJSON bool) {
	st := gateway.EnvStatus{ChannelID: c.ID, ProjectDir: c.Workspace}
	if p, err := gateway.ReadPreviewSpec(c.Workspace); err == nil && p != nil {
		st.Dev.Preview = &gateway.PreviewStatus{ChannelID: c.ID, Declared: true}
	}
	spec, err := deploy.Read(c.Workspace)
	if err != nil {
		st.Error = err.Error()
	}
	if spec != nil {
		st.Declared, st.Spec = true, spec
		r := spec.Resolve(c.Workspace, c.ID)
		dev := deploy.Dev(r)
		st.Dev.Build = &dev
		if prov, err := deploy.For(spec.Target); err != nil {
			st.Prod = deploy.ProdStatus{Declared: true, Provider: spec.Target, Error: err.Error()}
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			st.Prod = prov.Status(ctx, r)
			cancel()
		}
	}
	if asJSON {
		b, _ := json.MarshalIndent(st, "", "  ")
		fmt.Println(string(b))
		return
	}

	fmt.Printf("%s  (%s)\n\n", c.ID, c.Workspace)
	fmt.Println("DEV — this machine")
	if st.Dev.Preview != nil {
		fmt.Printf("  preview:  declared — dashboard %s\n", gateway.PreviewBasePath(c.ID))
	} else {
		fmt.Println("  preview:  not declared (bomclaw preview init)")
	}
	if !st.Declared {
		if st.Error != "" {
			fmt.Printf("\n%s\n", st.Error)
		}
		fmt.Println("\nPROD — not declared. Publish to Cloudflare with:\n  bomclaw env init " + c.ID + " --static dist [--build \"npm run build\"] [--domain x.bomclaw.org] [--database data/app.db]")
		return
	}
	b := st.Dev.Build
	if b.StaticDir != "" {
		if b.Built {
			fmt.Printf("  build:    %d files in %s (newest %s)\n", b.Files, b.StaticDir, b.BuiltAt)
		} else {
			fmt.Printf("  build:    %s is empty — run the build\n", b.StaticDir)
		}
	}
	if d := b.Database; d != nil {
		fmt.Printf("  database: %s\n", describeDB(d))
	}

	p := st.Prod
	fmt.Printf("\nPROD — %s %q\n", orDefault(p.Title, p.Provider), p.Name)
	switch {
	case !p.Connected:
		fmt.Printf("  not connected: %s\n", p.Error)
	case p.Error != "":
		fmt.Printf("  error: %s\n", p.Error)
	case !p.Deployed:
		fmt.Println("  not deployed yet (bomclaw env deploy " + c.ID + ")")
	default:
		fmt.Printf("  live:     %s (deployed %s)\n", p.URL, p.DeployedAt)
		if p.WorkersDev != "" && p.WorkersDev != p.URL {
			fmt.Printf("  also:     %s\n", p.WorkersDev)
		}
	}
	if p.WantDomain != "" && !contains(p.Domains, p.WantDomain) {
		fmt.Printf("  domain:   %s — attached on the next deploy\n", p.WantDomain)
	}
	if d := p.Database; d != nil {
		fmt.Printf("  database: %s\n", describeDB(d))
	}
}

func describeDB(d *deploy.DBStatus) string {
	switch {
	case d.Error != "":
		return d.Name + " — " + d.Error
	case !d.Exists && d.Engine == "d1":
		return "D1 " + d.Name + " — not created yet (bomclaw env deploy --with-db)"
	case !d.Exists:
		return d.Name + " — missing"
	}
	return fmt.Sprintf("%s %s — %d tables, %d KB", strings.ToUpper(d.Engine), d.Name, d.Tables, d.Bytes/1024)
}

func guessStatic(dir string) string {
	for _, d := range []string{"dist", "build", "out", "public", "site", "."} {
		if _, err := os.Stat(dir + "/" + d + "/index.html"); err == nil {
			return d
		}
	}
	return ""
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func orEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func envProject(dbPath, id string) *coord.Channel {
	if id == "" {
		id = os.Getenv("BOMCLAW_PROJECT")
	}
	if id == "" {
		envUsage()
		os.Exit(1)
	}
	db := openCoord(dbPath)
	defer db.Close()
	// The Connections page's credentials, ahead of the environment.
	deploy.Lookup = secrets.New(secrets.PathBeside(db.Path())).Lookup
	c, err := db.GetChannel(id)
	if err != nil && !strings.HasPrefix(id, "ch_") {
		c, err = db.GetChannel("ch_" + id)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "env: no channel %s (bomclaw ch list)\n", id)
		os.Exit(1)
	}
	if c.Workspace == "" {
		fmt.Fprintf(os.Stderr, "env: %s is a room with no project folder, so it has no environments\n", c.ID)
		os.Exit(1)
	}
	return c
}

func envUsage() {
	fmt.Fprintln(os.Stderr, `Usage: bomclaw env <command> <channel>

A project's two environments: DEV is this machine (Preview, build, local
SQLite), PROD is a hosting provider, picked by bomclaw.json "deploy.target":
  cloudflare  a Worker serving the built files, optional custom domain,
              optional D1 database (from the local SQLite file)
  fly         Fly.io Machines: the built files behind nginx, or the project's
              own Dockerfile for an app that runs a server; <name>.fly.dev
              plus an optional custom domain

  status <channel> [--json]   DEV and PROD side by side
  init   <channel>            Declare PROD: --static dist [--build "npm run build"]
                              [--domain x.bomclaw.org] [--spa] [--database data/app.db]
                              [--main worker.js] [--name n] [--force]
                              [--provider fly --dockerfile Dockerfile --port 3000 --region sin]
  deploy <channel>            Build and publish PROD. --dry-run sends nothing;
                              --with-db also uploads the local database, which
                              REPLACES the PROD tables — never implied.

Credentials, in the gateway's .env:
  cloudflare  CLOUDFLARE_API_TOKEN, CLOUDFLARE_ACCOUNT_ID
  fly         FLY_API_TOKEN (FLY_ORG, FLY_REGION optional), and flyctl installed
<channel> is ch_<name> or <name>; defaults to $BOMCLAW_PROJECT.`)
}
