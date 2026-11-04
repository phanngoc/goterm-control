package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// `bomclaw ch repo` links a channel to the GitHub repositories its code lives
// in: one primary, any number of others — the way a Lovable project connects
// to GitHub. The record is what every agent in the room is told; --clone and
// `create` also put the code in the project folder.

func runChannelRepo(args []string) {
	if len(args) == 0 {
		repoUsage()
		os.Exit(1)
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list", "ls":
		fs := flag.NewFlagSet("ch repo list", flag.ExitOnError)
		dbPath := dbFlag(fs)
		ch, _ := positionals(fs, rest)
		db := openCoord(*dbPath)
		defer db.Close()
		c := repoChannel(db, ch)
		repos, err := db.ListRepos(c.ID)
		if err != nil {
			fail("ch repo list", err)
		}
		if len(repos) == 0 {
			fmt.Printf("%s links no repo yet: bomclaw ch repo add %s <owner/name>\n", c.ID, c.ID)
			return
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ROLE\tREPO\tPATH\tBRANCH\tLABEL\tID")
		for _, r := range repos {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Role, r.Repo, dash(r.Path), dash(r.Branch), dash(r.Label), r.ID)
		}
		w.Flush()

	case "add", "link":
		fs := flag.NewFlagSet("ch repo add", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		primary := fs.Bool("primary", false, "Make it the channel's primary repo (the first one always is)")
		path := fs.String("path", "", "Where it is checked out: relative to the project folder, or absolute")
		branch := fs.String("branch", "", "Default branch (looked up on GitHub when empty)")
		label := fs.String("label", "", "What it is, in a few words")
		clone := fs.Bool("clone", false, "Also clone it into the project folder (at --path, default <name>)")
		ch, more := positionals(fs, rest)
		if len(more) == 0 {
			repoUsage()
			os.Exit(1)
		}
		db := openCoord(*dbPath)
		defer db.Close()
		c := repoChannel(db, ch)
		name, err := coord.ParseGitHubRepo(more[0])
		if err != nil {
			fail("ch repo add", err)
		}
		if *clone {
			dest := cloneDest(c, name, *path)
			if err := cloneRepo(name, dest); err != nil {
				fail("ch repo add", err)
			}
			*path = relToProject(c, dest)
			if *branch == "" {
				*branch = gitBranch(dest)
			}
		}
		if *branch == "" {
			*branch = githubDefaultBranch(name)
		}
		r, err := db.AddRepo(c.ID, name, *path, *branch, *label, requireAgent(*agent), *primary)
		if err != nil {
			fail("ch repo add", err)
		}
		fmt.Printf("%s → %s (%s)%s\n", c.ID, r.Repo, r.Role, pathNote(r))

	case "create":
		// Lovable's "connect GitHub": a new repo for the project, cloned into
		// its folder, linked as primary unless the channel already has one.
		fs := flag.NewFlagSet("ch repo create", flag.ExitOnError)
		agent, dbPath := agentFlag(fs), dbFlag(fs)
		public := fs.Bool("public", false, "Public repo (default private)")
		desc := fs.String("description", "", "Repo description (default: the channel's purpose)")
		path := fs.String("path", "", "Where to clone it, relative to the project folder (default <name>)")
		primary := fs.Bool("primary", false, "Make it primary even if the channel has one")
		ch, more := positionals(fs, rest)
		if len(more) == 0 {
			repoUsage()
			os.Exit(1)
		}
		db := openCoord(*dbPath)
		defer db.Close()
		c := repoChannel(db, ch)
		name := more[0]
		if !strings.Contains(name, "/") {
			owner, err := ghOutput("api", "user", "-q", ".login")
			if err != nil {
				fail("ch repo create", fmt.Errorf("who am I on GitHub (gh auth login?): %w", err))
			}
			name = owner + "/" + name
		}
		if _, err := coord.ParseGitHubRepo(name); err != nil {
			fail("ch repo create", err)
		}
		if *desc == "" {
			*desc = c.Purpose
		}
		vis := "--private"
		if *public {
			vis = "--public"
		}
		if out, err := ghOutput("repo", "create", name, vis, "--description", *desc); err != nil {
			fail("ch repo create", fmt.Errorf("gh repo create: %v %s", err, out))
		}
		dest := cloneDest(c, name, *path)
		if err := cloneRepo(name, dest); err != nil {
			fail("ch repo create", err)
		}
		r, err := db.AddRepo(c.ID, name, relToProject(c, dest), gitBranch(dest), "", requireAgent(*agent), *primary)
		if err != nil {
			fail("ch repo create", err)
		}
		fmt.Printf("created https://github.com/%s and linked it to %s (%s)%s\n", r.Repo, c.ID, r.Role, pathNote(r))

	case "primary":
		fs := flag.NewFlagSet("ch repo primary", flag.ExitOnError)
		dbPath := dbFlag(fs)
		ch, more := positionals(fs, rest)
		if len(more) == 0 {
			repoUsage()
			os.Exit(1)
		}
		db := openCoord(*dbPath)
		defer db.Close()
		c := repoChannel(db, ch)
		r, err := db.SetPrimaryRepo(c.ID, more[0])
		if err != nil {
			fail("ch repo primary", err)
		}
		fmt.Printf("%s: primary repo is now %s\n", c.ID, r.Repo)

	case "remove", "rm", "unlink":
		fs := flag.NewFlagSet("ch repo remove", flag.ExitOnError)
		dbPath := dbFlag(fs)
		ch, more := positionals(fs, rest)
		if len(more) == 0 {
			repoUsage()
			os.Exit(1)
		}
		db := openCoord(*dbPath)
		defer db.Close()
		c := repoChannel(db, ch)
		if err := db.RemoveRepo(c.ID, more[0]); err != nil {
			fail("ch repo remove", err)
		}
		fmt.Printf("%s no longer links %s (the GitHub repo and any checkout are untouched)\n", c.ID, more[0])

	default:
		repoUsage()
		os.Exit(1)
	}
}

// positionals parses fs with flags anywhere among the arguments — `add <ch>
// <repo> --primary` reads as naturally as `add --primary <ch> <repo>` — and
// returns the first positional and the rest. The stdlib parser stops at the
// first non-flag, which silently ignored every flag after the repo (--db
// included, which pointed the command at the live database).
func positionals(fs *flag.FlagSet, args []string) (string, []string) {
	var pos, flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			flags = append(flags, args[i+1])
			i++
		}
	}
	fs.Parse(flags)
	if len(pos) == 0 {
		return "", nil
	}
	return pos[0], pos[1:]
}

func repoChannel(db *coord.DB, id string) *coord.Channel {
	if id == "" {
		id = os.Getenv("BOMCLAW_PROJECT")
	}
	if id == "" {
		repoUsage()
		os.Exit(1)
	}
	c, err := db.GetChannel(id)
	if err != nil && !strings.HasPrefix(id, "ch_") {
		c, err = db.GetChannel("ch_" + id)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "ch repo: no channel %s (bomclaw ch list)\n", id)
		os.Exit(1)
	}
	return c
}

func cloneDest(c *coord.Channel, name, path string) string {
	if path == "" {
		path = name[strings.Index(name, "/")+1:]
	}
	if filepath.IsAbs(path) || c.Workspace == "" {
		if !filepath.IsAbs(path) {
			fail("ch repo", fmt.Errorf("%s has no project folder; give an absolute --path", c.ID))
		}
		return path
	}
	return filepath.Join(c.Workspace, path)
}

func relToProject(c *coord.Channel, dest string) string {
	if c.Workspace != "" {
		if rel, err := filepath.Rel(c.Workspace, dest); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return dest
}

func cloneRepo(name, dest string) error {
	if _, err := os.Stat(filepath.Join(dest, ".git")); err == nil {
		return nil // already there
	}
	if _, err := os.Stat(dest); err == nil {
		if entries, _ := os.ReadDir(dest); len(entries) > 0 {
			return fmt.Errorf("%s exists and is not a checkout; pick another --path", dest)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var cmd *exec.Cmd
	if _, err := exec.LookPath("gh"); err == nil {
		cmd = exec.CommandContext(ctx, "gh", "repo", "clone", name, dest)
	} else {
		cmd = exec.CommandContext(ctx, "git", "clone", "https://github.com/"+name+".git", dest)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("clone %s: %v %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func gitBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// githubDefaultBranch asks GitHub, best effort: a link without a branch is
// still a link.
func githubDefaultBranch(name string) string {
	out, err := ghOutput("repo", "view", name, "--json", "defaultBranchRef", "-q", ".defaultBranchRef.name")
	if err != nil {
		return ""
	}
	return out
}

func ghOutput(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "gh", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func pathNote(r *coord.ChannelRepo) string {
	if r.Path == "" {
		return ""
	}
	return " at " + r.Path
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func fail(what string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", what, err)
	os.Exit(1)
}

func repoUsage() {
	fmt.Fprintln(os.Stderr, `Usage: bomclaw ch repo <command> <channel> …

A channel links the GitHub repos its code lives in: one primary, any others.
Every agent working in the room is told the list.

  list    <channel>                         primary first
  add     <channel> <owner/name|url> [--primary] [--path p] [--branch b] [--label l] [--clone]
  create  <channel> <name> [--public] [--path p] [--primary]   new repo on GitHub, cloned into the project
  primary <channel> <owner/name|id>         make it the primary
  remove  <channel> <owner/name|id>         unlink (nothing on GitHub or disk is touched)

<channel> is ch_<name> or <name>; defaults to $BOMCLAW_PROJECT.`)
}
