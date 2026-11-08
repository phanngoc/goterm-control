package deploy

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// CompatibilityDate pins the Workers runtime behaviour a deploy gets. Fixed,
// not "today": a redeploy of unchanged files should not change behaviour.
const CompatibilityDate = "2026-10-01"

// Wrangler is how wrangler is run: pinned to a major version through npx, so
// nothing has to be installed and an unannounced v5 cannot change a deploy.
var Wrangler = []string{"npx", "--yes", "wrangler@4"}

// Runner runs one command. Swapped in tests.
type Runner func(ctx context.Context, dir string, env []string, out io.Writer, name string, args ...string) error

// ExecRunner runs the command for real, streaming its output.
func ExecRunner(ctx context.Context, dir string, env []string, out io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// Options say what this deploy does.
type Options struct {
	// WithDB uploads the local database too. It replaces the PROD tables,
	// so it is never implied: a site redeploy must not wipe live data.
	WithDB bool
	// DryRun builds and writes the wrangler config, and stops before
	// anything reaches Cloudflare. Needs no credentials.
	DryRun bool
}

// Result is what a deploy produced.
type Result struct {
	URL        string `json:"url,omitempty"`
	ConfigPath string `json:"config_path,omitempty"`
	DBUploaded bool   `json:"db_uploaded"`
	DryRun     bool   `json:"dry_run"`
}

// Cloudflare is the Cloudflare provider: a Worker serving the built files
// (Workers static assets), an optional custom domain on one of the account's
// zones, and D1 for a SQLite database.
type Cloudflare struct {
	Client *Client
	Run    Runner
}

func init() {
	Register("cloudflare", func() Provider { return &Cloudflare{Client: NewClient(CredsFromEnv())} })
}

func (d *Cloudflare) Name() string  { return "cloudflare" }
func (d *Cloudflare) Title() string { return "Cloudflare" }

// Validate: D1 hosts SQLite, so that is the engine Cloudflare takes.
func (d *Cloudflare) Validate(s *Spec) error {
	if s.Dockerfile != "" {
		return fmt.Errorf("deploy.dockerfile: Cloudflare runs Workers, not containers — use \"target\": \"fly\" for an app with a server")
	}
	db := s.Database
	if db == nil {
		return nil
	}
	if db.Engine == "" {
		db.Engine = "sqlite"
	}
	switch db.Engine {
	case "sqlite":
		if db.File == "" {
			return fmt.Errorf("deploy.database.file is the local SQLite file to upload")
		}
		return nil
	case "supabase", "postgres":
		return fmt.Errorf("deploy.database.engine %q is planned (Supabase), not built yet — Cloudflare hosts \"sqlite\" as D1", db.Engine)
	}
	return fmt.Errorf("deploy.database.engine %q: Cloudflare hosts \"sqlite\" (D1)", db.Engine)
}

func (d *Cloudflare) Connected() (bool, string) {
	if d.Client != nil && d.Client.Creds.OK() {
		return true, ""
	}
	return false, ErrNoCreds.Error()
}

// Status asks Cloudflare what is live.
func (d *Cloudflare) Status(ctx context.Context, r Resolved) ProdStatus {
	st := Prod(ctx, d.Client, r)
	st.Provider, st.Title = d.Name(), d.Title()
	return st
}

// Deploy publishes a project's PROD environment, logging each step to log.
func (d *Cloudflare) Deploy(ctx context.Context, r Resolved, opt Options, log io.Writer) (*Result, error) {
	run := d.Run
	if run == nil {
		run = ExecRunner
	}
	creds := Creds{}
	if d.Client != nil {
		creds = d.Client.Creds
	}
	if !opt.DryRun && !creds.OK() {
		return nil, ErrNoCreds
	}
	res := &Result{DryRun: opt.DryRun}
	step := func(format string, a ...any) { fmt.Fprintf(log, "▸ "+format+"\n", a...) }

	if r.Build != "" {
		step("build: %s", r.Build)
		if err := run(ctx, r.WorkDir, nil, log, "sh", "-c", r.Build); err != nil {
			return nil, fmt.Errorf("build failed: %w", err)
		}
	}
	if r.StaticDir != "" {
		n := 0
		_ = walkFiles(r.StaticDir, func(os.FileInfo) { n++ })
		if n == 0 {
			return nil, fmt.Errorf("%s is empty or missing — nothing to publish (run the build, or fix deploy.static)", r.StaticDir)
		}
		step("static: %d files from %s", n, r.StaticDir)
	}

	dbID := ""
	if r.Database != nil && (opt.WithDB || r.MainPath != "") {
		if opt.DryRun {
			dbID = "00000000-0000-0000-0000-000000000000"
			step("database: D1 %q (dry run: not created)", r.DBName)
		} else {
			db, err := d.Client.EnsureD1(ctx, r.DBName)
			if err != nil {
				return nil, fmt.Errorf("database %s: %w", r.DBName, err)
			}
			dbID = db.UUID
			step("database: D1 %q (%s)", r.DBName, db.UUID)
		}
	}

	tmp, err := os.MkdirTemp("", "bomclaw-deploy-")
	if err != nil {
		return nil, err
	}
	if !opt.DryRun {
		defer os.RemoveAll(tmp)
	}
	env := []string{"CLOUDFLARE_API_TOKEN=" + creds.Token, "CLOUDFLARE_ACCOUNT_ID=" + creds.AccountID}

	if opt.WithDB && r.Database != nil {
		dump, err := DumpForD1(r.DBFile)
		if err != nil {
			return nil, err
		}
		sqlPath := filepath.Join(tmp, "import.sql")
		if err := os.WriteFile(sqlPath, dump, 0o600); err != nil {
			return nil, err
		}
		step("database: upload %s → D1 %s (%d KB of SQL; replaces the PROD tables)", r.DBFile, r.DBName, len(dump)/1024)
		if !opt.DryRun {
			args := append(append([]string{}, Wrangler[1:]...), "d1", "execute", r.DBName, "--remote", "--file", sqlPath, "--yes")
			if err := run(ctx, r.ProjectDir, env, log, Wrangler[0], args...); err != nil {
				return nil, fmt.Errorf("database upload failed: %w", err)
			}
			res.DBUploaded = true
		}
	}

	cfg := WranglerConfig(r, dbID)
	cfgPath := filepath.Join(tmp, "wrangler.json")
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
		return nil, err
	}
	res.ConfigPath = cfgPath
	step("config: %s", cfgPath)
	if opt.DryRun {
		fmt.Fprintf(log, "%s\n", b)
		step("dry run: stopping before wrangler deploy")
		return res, nil
	}

	var out bytes.Buffer
	args := append(append([]string{}, Wrangler[1:]...), "deploy", "--config", cfgPath)
	step("wrangler deploy %s", r.Name)
	if err := run(ctx, r.ProjectDir, env, io.MultiWriter(log, &out), Wrangler[0], args...); err != nil {
		return nil, fmt.Errorf("wrangler deploy failed: %w", err)
	}
	res.URL = deployedURL(out.String(), r.Domain)
	if res.URL != "" {
		step("live: %s", res.URL)
	}
	return res, nil
}

// WranglerConfig is the wrangler.json for a deploy. Generated rather than kept
// in the project, so PROD is described in one place (bomclaw.json) and a
// project is not asked to learn wrangler's format.
func WranglerConfig(r Resolved, dbID string) map[string]any {
	cfg := map[string]any{
		"name":               r.Name,
		"compatibility_date": CompatibilityDate,
		"workers_dev":        true,
	}
	if r.MainPath != "" {
		cfg["main"] = r.MainPath
	}
	if r.StaticDir != "" {
		assets := map[string]any{"directory": r.StaticDir}
		if r.SPA {
			assets["not_found_handling"] = "single-page-application"
		}
		if r.MainPath != "" {
			assets["binding"] = "ASSETS"
		}
		cfg["assets"] = assets
	}
	if r.Domain != "" {
		cfg["routes"] = []map[string]any{{"pattern": r.Domain, "custom_domain": true}}
	}
	// A binding is only useful to a script; an assets-only Worker has no
	// code to read it with.
	if r.Database != nil && r.MainPath != "" && dbID != "" {
		cfg["d1_databases"] = []map[string]any{{
			"binding": r.DBBinding, "database_name": r.DBName, "database_id": dbID,
		}}
	}
	return cfg
}

var workersDevURL = regexp.MustCompile(`https://[a-z0-9.-]+\.workers\.dev`)

func deployedURL(out, domain string) string {
	if domain != "" {
		return "https://" + domain
	}
	return workersDevURL.FindString(out)
}

// DumpForD1 turns a SQLite file into SQL that D1 accepts and that REPLACES
// the tables it defines: D1 imports SQL, not .sqlite files, refuses
// BEGIN/COMMIT (the import is already a transaction), and an import on top of
// existing tables would duplicate every row.
func DumpForD1(path string) ([]byte, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("local database: %w", err)
	}
	tables, err := tableNames(path)
	if err != nil {
		return nil, err
	}
	out, err := exec.Command("sqlite3", path, ".dump").Output()
	if err != nil {
		return nil, fmt.Errorf("sqlite3 .dump %s: %w (is the sqlite3 CLI installed?)", path, err)
	}
	return SanitizeDump(out, tables), nil
}

func tableNames(path string) ([]string, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name != '_cf_KV' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("read tables of %s: %w", path, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// SanitizeDump makes a `sqlite3 .dump` importable into D1 as a replacement.
func SanitizeDump(dump []byte, tables []string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "-- bomclaw deploy %s: replaces %d table(s)\n", time.Now().UTC().Format(time.RFC3339), len(tables))
	b.WriteString("PRAGMA defer_foreign_keys = true;\n")
	for _, t := range tables {
		fmt.Fprintf(&b, "DROP TABLE IF EXISTS %q;\n", t)
	}
	sc := bufio.NewScanner(bytes.NewReader(dump))
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	skipping := false
	for sc.Scan() {
		line := sc.Text()
		trim := strings.TrimSpace(line)
		switch {
		case trim == "BEGIN TRANSACTION;", trim == "COMMIT;", trim == "PRAGMA foreign_keys=OFF;":
			continue
		case strings.HasPrefix(trim, "CREATE TABLE _cf_KV"), strings.HasPrefix(trim, `CREATE TABLE "_cf_KV"`):
			skipping = !strings.HasSuffix(trim, ";")
			continue
		case strings.Contains(trim, "sqlite_sequence"):
			// D1 manages it; writing it is refused.
			continue
		}
		if skipping {
			skipping = !strings.HasSuffix(trim, ";")
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.Bytes()
}
