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

// runUserCmd handles `bomclaw user <add|list|remove|passwd> ...` — dashboard
// account management. There is deliberately no self-registration endpoint.
func runUserCmd(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: bomclaw user <add|list|remove|passwd> [flags]")
		os.Exit(1)
	}

	sub := args[0]
	fs := flag.NewFlagSet("user "+sub, flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "Path to config file")
	role := fs.String("role", "admin", "Role for new user: admin | viewer")

	// Usage: bomclaw user <sub> [username] [flags]
	username := ""
	flagArgs := args[1:]
	if len(flagArgs) > 0 && !strings.HasPrefix(flagArgs[0], "-") {
		username = flagArgs[0]
		flagArgs = flagArgs[1:]
	}
	_ = fs.Parse(flagArgs)

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

	switch sub {
	case "add":
		if username == "" {
			log.Fatal("usage: bomclaw user add <username> [--role admin|viewer]")
		}
		if *role != "admin" && *role != "viewer" {
			log.Fatalf("invalid role %q (admin | viewer)", *role)
		}
		hash := mustReadPasswordHash()
		if err := users.CreateUser(username, hash, *role); err != nil {
			log.Fatalf("create user: %v", err)
		}
		fmt.Printf("user %q created (role=%s)\n", username, *role)

	case "list":
		all, err := users.ListUsers()
		if err != nil {
			log.Fatalf("list users: %v", err)
		}
		if len(all) == 0 {
			fmt.Println("no users — create one with: bomclaw user add <username>")
			return
		}
		for _, u := range all {
			fmt.Printf("%-20s role=%-7s created=%s\n", u.Username, u.Role, u.CreatedAt.Format("2006-01-02"))
		}

	case "remove":
		if username == "" {
			log.Fatal("usage: bomclaw user remove <username>")
		}
		if err := users.DeleteUser(username); err != nil {
			log.Fatalf("remove user: %v", err)
		}
		fmt.Printf("user %q removed\n", username)

	case "passwd":
		if username == "" {
			log.Fatal("usage: bomclaw user passwd <username>")
		}
		hash := mustReadPasswordHash()
		if err := users.SetPassword(username, hash); err != nil {
			log.Fatalf("set password: %v", err)
		}
		fmt.Printf("password updated for %q\n", username)

	default:
		fmt.Printf("unknown subcommand %q\nUsage: bomclaw user <add|list|remove|passwd>\n", sub)
		os.Exit(1)
	}
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
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		log.Fatalf("read password: %v", err)
	}
	return strings.TrimSpace(line)
}
