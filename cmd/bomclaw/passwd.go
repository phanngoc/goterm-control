package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ngocp/goterm-control/internal/auth"
	"github.com/ngocp/goterm-control/internal/config"
	"github.com/ngocp/goterm-control/internal/storage"
	"golang.org/x/term"
)

// runPasswdCmd handles `bomclaw passwd [username]` — the dashboard is
// single-account, so this both bootstraps the account (when none exists) and
// rotates its password. There is deliberately no self-registration endpoint.
func runPasswdCmd(args []string) {
	fs := flag.NewFlagSet("passwd", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "Path to config file")

	// Usage: bomclaw passwd [username] [flags]
	username := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		username = args[0]
		args = args[1:]
	}
	_ = fs.Parse(args)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	db, err := storage.Open(filepath.Join(cfg.Session.DataDir, "goterm.db"))
	if err != nil {
		log.Fatalf("storage: %v", err)
	}
	defer db.Close()
	users := storage.NewUserStore(db)

	existing, err := users.FirstUser()
	if err != nil {
		log.Fatalf("read account: %v", err)
	}

	// Bootstrap: no account yet, so create it.
	if existing == nil {
		if username == "" {
			fmt.Println("No dashboard account exists yet.")
			log.Fatal("usage: bomclaw passwd <username>")
		}
		hash := mustReadPasswordHash()
		if err := users.CreateUser(username, hash); err != nil {
			log.Fatalf("create account: %v", err)
		}
		fmt.Printf("account %q created\n", username)
		return
	}

	// Single-account invariant: refuse to silently create a second account.
	if username != "" && username != existing.Username {
		log.Fatalf("single-account mode: the dashboard account is %q, not %q", existing.Username, username)
	}

	hash := mustReadPasswordHash()
	if err := users.SetPassword(existing.Username, hash); err != nil {
		log.Fatalf("set password: %v", err)
	}
	// A rotated password must actually revoke access.
	if err := users.DeleteAllWebSessions(); err != nil {
		log.Fatalf("revoke sessions: %v", err)
	}
	fmt.Printf("password updated for %q — all browser sessions logged out\n", existing.Username)
}

// mustReadPasswordHash prompts for a password twice (hidden input when on a
// TTY) and returns its bcrypt hash.
func mustReadPasswordHash() string {
	pw := readPassword("Password: ")
	if len(pw) < 8 {
		log.Fatal("password must be at least 8 characters")
	}
	if confirm := readPassword("Confirm password: "); confirm != pw {
		log.Fatal("passwords do not match")
	}
	hash, err := auth.HashPassword(pw)
	if err != nil {
		log.Fatalf("hash password: %v", err)
	}
	return hash
}

// stdinReader is shared across readPassword calls: a fresh bufio.Reader per
// call would buffer ahead and swallow the second (confirm) line of piped input.
var stdinReader = bufio.NewReader(os.Stdin)

func readPassword(prompt string) string {
	fmt.Print(prompt)
	if term.IsTerminal(int(syscall.Stdin)) {
		b, err := term.ReadPassword(int(syscall.Stdin))
		fmt.Println()
		if err != nil {
			log.Fatalf("read password: %v", err)
		}
		return strings.TrimSpace(string(b))
	}
	// Non-TTY (piped) input — read a line.
	line, err := stdinReader.ReadString('\n')
	if err != nil && line == "" {
		log.Fatalf("read password: %v", err)
	}
	return strings.TrimSpace(line)
}
