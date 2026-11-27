package deploy

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Which version each environment runs. DEV is the project folder's git
// checkout; PROD is what the provider says is live, and — when bomclaw
// published it — the commit it was built from, carried in the deploy's own
// message (Cloudflare) or image label (Fly). Comparing the two is how a room
// says "PROD is three commits behind" without anyone opening a terminal.

// GitVersion is a checkout's HEAD.
type GitVersion struct {
	Commit  string `json:"commit"`
	Short   string `json:"short"`
	Branch  string `json:"branch,omitempty"`
	Subject string `json:"subject,omitempty"`
	At      string `json:"at,omitempty"`
	// Dirty counts files changed and not committed: what a publish from here
	// would carry that no commit names.
	Dirty int `json:"dirty"`
}

// ProdVersion is the live version, as the provider names it.
type ProdVersion struct {
	// ID is the provider's own version id (a Worker version, a Fly image tag).
	ID string `json:"id,omitempty"`
	// Message is the deploy's message, when one was given.
	Message string `json:"message,omitempty"`
	// Commit is the git commit the live version was built from, when the
	// deploy recorded it — bomclaw does; a hand-run wrangler usually does not.
	Commit string `json:"commit,omitempty"`
	// By is who or what deployed it ("wrangler", an e-mail).
	By string `json:"by,omitempty"`
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err
}

// Git reads dir's HEAD, or nil when dir is not a git checkout.
func Git(ctx context.Context, dir string) *GitVersion {
	if dir == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	line, err := gitOut(ctx, dir, "log", "-1", "--format=%H%x00%h%x00%cI%x00%s")
	if err != nil || line == "" {
		return nil
	}
	f := strings.SplitN(line, "\x00", 4)
	if len(f) < 4 {
		return nil
	}
	g := &GitVersion{Commit: f[0], Short: f[1], At: f[2], Subject: f[3]}
	if b, err := gitOut(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil && b != "HEAD" {
		g.Branch = b
	}
	if s, err := gitOut(ctx, dir, "status", "--porcelain"); err == nil && s != "" {
		g.Dirty = len(strings.Split(s, "\n"))
	}
	return g
}

// CommitsSince counts the commits on dir's HEAD after commit: how far DEV is
// ahead of what PROD runs. -1 when it cannot be told (commit unknown here,
// not an ancestor of HEAD).
func CommitsSince(ctx context.Context, dir, commit string) int {
	if dir == "" || commit == "" {
		return -1
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := gitOut(ctx, dir, "merge-base", "--is-ancestor", commit, "HEAD"); err != nil {
		return -1
	}
	s, err := gitOut(ctx, dir, "rev-list", "--count", commit+"..HEAD")
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// deployMessage is the message a bomclaw deploy carries, naming the commit
// so PROD can be matched to it later. Empty outside a git checkout.
func deployMessage(g *GitVersion) string {
	if g == nil {
		return ""
	}
	msg := "bomclaw git " + g.Short
	if g.Dirty > 0 {
		msg += "+dirty"
	}
	if g.Subject != "" {
		msg += ": " + g.Subject
	}
	if len(msg) > 100 {
		msg = msg[:100]
	}
	return msg
}

// imageLabel is the same for a Fly image tag, which allows fewer characters.
func imageLabel(g *GitVersion) string {
	if g == nil {
		return ""
	}
	l := "git-" + g.Short
	if g.Dirty > 0 {
		l += "-dirty"
	}
	return l
}

var commitRe = regexp.MustCompile(`\bgit[ -]([0-9a-f]{7,40})\b`)

// commitIn finds the commit a deploy message or label names.
func commitIn(s string) string {
	if m := commitRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}
