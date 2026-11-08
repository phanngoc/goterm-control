package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ngocp/goterm-control/internal/deploy"
	"github.com/ngocp/goterm-control/internal/secrets"
)

// `bomclaw secrets` is the Connections page from the shell — and the way an
// agent stores a credential the owner handed it in chat, so the owner never
// has to open a file. Values are never printed back: a mask and the check.

func runSecrets(args []string) {
	if len(args) == 0 {
		secretsUsage()
		os.Exit(1)
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet("secrets "+cmd, flag.ExitOnError)
	dbPath := dbFlag(fs)
	stdin := fs.Bool("stdin", false, "set: read KEY=VALUE lines from stdin (keeps the value out of the command line)")
	noVerify := fs.Bool("no-verify", false, "set: do not check the credentials after storing them")
	pos, _ := parseAll(fs, rest)
	store := secrets.New(secrets.PathBeside(*dbPath))
	deploy.Lookup = store.Lookup
	by := "cli"
	if a := os.Getenv("BOMCLAW_AGENT_ID"); a != "" {
		by = a
	}

	switch cmd {
	case "list", "ls", "status":
		views, err := store.View()
		if err != nil {
			sfail("secrets: %v", err)
		}
		for _, v := range views {
			state := "not connected"
			if v.Connected {
				state = "connected"
			}
			if v.Planned {
				state += " (stored for later; deploys do not use it yet)"
			}
			fmt.Printf("%s — %s\n", v.Title, state)
			for _, f := range v.Fields {
				val := "—"
				if f.Set {
					val = f.Masked + "  (" + f.Source
					if f.By != "" {
						val += ", by " + f.By
					}
					val += ")"
				}
				req := ""
				if f.Required {
					req = "*"
				}
				fmt.Printf("  %-27s %s\n", f.Env+req, val)
			}
		}
		fmt.Printf("\nstore: %s\n", store.Path())

	case "set":
		if len(pos) < 1 {
			sfail("usage: bomclaw secrets set <service> KEY=VALUE … (or --stdin)")
		}
		service := pos[0]
		pairs := pos[1:]
		if *stdin {
			sc := bufio.NewScanner(os.Stdin)
			for sc.Scan() {
				if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
					pairs = append(pairs, l)
				}
			}
		}
		values := map[string]string{}
		for _, p := range pairs {
			k, v, ok := strings.Cut(p, "=")
			if !ok || strings.TrimSpace(v) == "" {
				sfail("secrets set: %q is not KEY=VALUE", redact(p))
			}
			values[strings.TrimSpace(k)] = v
		}
		if len(values) == 0 {
			sfail("secrets set: nothing to store")
		}
		if err := store.Set(service, values, by); err != nil {
			sfail("secrets set: %v", err)
		}
		fmt.Printf("stored %d value(s) for %s in %s\n", len(values), service, store.Path())
		if !*noVerify {
			verifyAndPrint(service)
		}

	case "unset", "rm":
		if len(pos) < 1 {
			sfail("usage: bomclaw secrets unset <service> [KEY…]")
		}
		if err := store.Unset(pos[0], pos[1:]...); err != nil {
			sfail("secrets unset: %v", err)
		}
		fmt.Println("removed")

	case "verify", "check":
		if len(pos) < 1 {
			sfail("usage: bomclaw secrets verify <service>")
		}
		if !verifyAndPrint(pos[0]) {
			os.Exit(1)
		}

	default:
		secretsUsage()
		os.Exit(1)
	}
}

func verifyAndPrint(service string) bool {
	detail, err := deploy.Verify(context.Background(), service)
	if err != nil {
		fmt.Printf("check %s: FAIL — %v\n", service, err)
		return false
	}
	fmt.Printf("check %s: OK — %s\n", service, detail)
	return true
}

// redact keeps an error about a malformed pair from printing its value.
func redact(p string) string {
	if k, _, ok := strings.Cut(p, "="); ok {
		return k + "=…"
	}
	if len(p) > 6 {
		return p[:3] + "…"
	}
	return "…"
}

// parseAll lets flags sit anywhere among the positional arguments.
func parseAll(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return pos, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
	return pos, nil
}

func sfail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

func secretsUsage() {
	fmt.Fprintln(os.Stderr, `Usage: bomclaw secrets <command>

Credentials for the infrastructure agents deploy to — the same store as the
dashboard's Admin → Connections page. Values are never printed back.

  list                              every service, connected or not, masked
  set <service> KEY=VALUE …         store values, then check them
      --stdin                       read KEY=VALUE lines from stdin instead
      --no-verify                   skip the check
  unset <service> [KEY…]            remove values (all of the service's when none named)
  verify <service>                  check the stored credentials against the service

Services and keys:
  cloudflare  CLOUDFLARE_API_TOKEN*  CLOUDFLARE_ACCOUNT_ID*
  fly         FLY_API_TOKEN*  FLY_ORG  FLY_REGION
  supabase    SUPABASE_ACCESS_TOKEN  SUPABASE_URL  SUPABASE_SERVICE_ROLE_KEY  (stored for later)

A value stored here wins over the same variable in .env.`)
}
