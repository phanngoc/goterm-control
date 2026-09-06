package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"
	"time"

	"github.com/ngocp/goterm-control/internal/credentials"
	"github.com/ngocp/goterm-control/internal/gateway"
)

// runAccounts shows the credential pool this agent rotates sessions across:
// which accounts exist, how many sessions are pinned to each, and which are
// cooling down after a rate limit.
func runAccounts(args []string) {
	fs := flag.NewFlagSet("accounts", flag.ExitOnError)
	addr := fs.String("addr", gatewayHTTPAddr(), "Gateway base URL")
	asJSON := fs.Bool("json", false, "Print the raw status payload")
	fs.Parse(args)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(*addr + "/api/status")
	if err != nil {
		fmt.Fprintf(os.Stderr, "accounts: gateway unreachable at %s (%v)\n", *addr, err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var st gateway.StatusResult
	if err := json.Unmarshal(raw, &st); err != nil {
		fmt.Fprintf(os.Stderr, "accounts: bad response: %s\n", raw)
		os.Exit(1)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(st.Accounts, "", "  ")
		fmt.Println(string(b))
		return
	}

	agent := orAny(st.AgentName, st.AgentID)
	if len(st.Accounts) == 0 {
		fmt.Printf("%s: no credential pool configured — running on the ambient %s login.\n\n"+
			"Add an `accounts:` section to config.yaml to rotate sessions across several.\n",
			orAny(agent, "this agent"), st.DefaultModel)
		return
	}

	fmt.Printf("%s · provider %s\n\n", orAny(agent, "agent"), st.Accounts[0].Provider)
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  ACCOUNT\tSESSIONS\tLAST USED\tSTATE")
	now := time.Now()
	for _, a := range st.Accounts {
		state := "ready"
		if a.CoolingHint != "" {
			if until, err := time.Parse(time.RFC3339, a.CoolingHint); err == nil {
				state = fmt.Sprintf("cooling %s", until.Sub(now).Round(time.Minute))
			} else {
				state = "cooling"
			}
		}
		fmt.Fprintf(w, "  %s\t%d\t%s\t%s\n", a.Name, a.Sessions, clockOr(a.LastUsed, "never"), state)
	}
	w.Flush()

	// Errors go underneath rather than in a column: they are long, and the
	// reason an account is sidelined is worth reading in full.
	for _, a := range st.Accounts {
		if a.LastError != "" {
			fmt.Printf("\n  %s: %s\n", a.Name, a.LastError)
		}
	}
}

// clockOr renders an RFC3339 stamp as a local time, or a fallback when empty.
func clockOr(stamp, fallback string) string {
	if stamp == "" {
		return fallback
	}
	t, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return stamp
	}
	return t.Local().Format("15:04:05")
}

// assert at compile time that the CLI and the pool agree on the status shape.
var _ = []credentials.Status(nil)
