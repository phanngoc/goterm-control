package coord

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A project's code lives in repositories, and a project can have more than
// one: ch_arcade is a platform and one repo per game; shopee-deals is one repo
// with a private production checkout. Which repos those are was written down
// nowhere but prose — a line in AGENTS.md, a link in a chat — so an agent
// starting on the project had to rediscover it, and the person had no place
// to see or change it.
//
// So a channel links its repos, the way a Lovable project connects to
// GitHub: one PRIMARY (where the project's own code is, what "the repo" means
// when nobody says which), any number of others. The link is a record — the
// repo, where it is checked out, its branch — and every agent working in the
// room is told the list.

// Repo roles.
const (
	RepoPrimary   = "primary"
	RepoSecondary = "secondary"
)

// ChannelRepo is one repository linked to a channel.
type ChannelRepo struct {
	ID        string    `json:"id"`
	ChannelID string    `json:"channel_id"`
	Provider  string    `json:"provider"` // github
	Repo      string    `json:"repo"`     // owner/name
	URL       string    `json:"url"`
	Role      string    `json:"role"` // primary | secondary
	Path      string    `json:"path,omitempty"`
	Branch    string    `json:"branch,omitempty"`
	Label     string    `json:"label,omitempty"`
	AddedBy   string    `json:"added_by"`
	CreatedAt time.Time `json:"created_at"`
}

// ParseGitHubRepo accepts owner/name, an https URL (with or without .git or a
// trailing path), or an ssh remote, and returns "owner/name".
func ParseGitHubRepo(in string) (string, error) {
	s := strings.TrimSpace(in)
	s = strings.TrimSuffix(s, "/")
	switch {
	case strings.HasPrefix(s, "git@github.com:"):
		s = strings.TrimPrefix(s, "git@github.com:")
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || !strings.EqualFold(strings.TrimPrefix(u.Host, "www."), "github.com") {
			return "", fmt.Errorf("coord: %q is not a GitHub repository", in)
		}
		s = strings.Trim(u.Path, "/")
	case strings.HasPrefix(s, "github.com/"):
		s = strings.TrimPrefix(s, "github.com/")
	}
	s = strings.TrimSuffix(s, ".git")
	parts := strings.Split(s, "/")
	// A host without a scheme — gitlab.com/x/y — is not owner/name.
	if len(parts) >= 3 && strings.Contains(parts[0], ".") {
		return "", fmt.Errorf("coord: %q is not a GitHub repository", in)
	}
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("coord: %q is not owner/name or a GitHub URL", in)
	}
	for _, p := range parts[:2] {
		for _, r := range p {
			if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return "", fmt.Errorf("coord: %q is not a valid GitHub name", p)
			}
		}
	}
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), nil
}

const repoCols = `id, channel_id, provider, repo, url, role, path, branch, label, added_by, created_at`

func scanRepo(row interface{ Scan(...any) error }) (*ChannelRepo, error) {
	var r ChannelRepo
	var created string
	if err := row.Scan(&r.ID, &r.ChannelID, &r.Provider, &r.Repo, &r.URL, &r.Role, &r.Path, &r.Branch,
		&r.Label, &r.AddedBy, &created); err != nil {
		return nil, err
	}
	r.CreatedAt = parseTS(created)
	return &r, nil
}

// AddRepo links a repo to a channel. The first repo of a channel is its
// primary; asking for primary moves the role from whichever repo held it.
// Linking a repo that is already linked updates its path, branch and label.
func (db *DB) AddRepo(channelID, repo, path, branch, label, by string, primary bool) (*ChannelRepo, error) {
	name, err := ParseGitHubRepo(repo)
	if err != nil {
		return nil, err
	}
	if _, err := db.GetChannel(channelID); err != nil {
		return nil, err
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM channel_repos WHERE channel_id = ? AND role = ?`,
		channelID, RepoPrimary).Scan(&n); err != nil {
		return nil, err
	}
	role := RepoSecondary
	if primary || n == 0 {
		role = RepoPrimary
	}

	var id string
	err = tx.QueryRow(`SELECT id FROM channel_repos WHERE channel_id = ? AND repo = ? COLLATE NOCASE`,
		channelID, name).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		id = "cr_" + uuid.NewString()
		if role == RepoPrimary {
			if _, err := tx.Exec(`UPDATE channel_repos SET role = ? WHERE channel_id = ? AND role = ?`,
				RepoSecondary, channelID, RepoPrimary); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(`INSERT INTO channel_repos (`+repoCols+`) VALUES (?, ?, 'github', ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, channelID, name, "https://github.com/"+name, role, strings.TrimSpace(path), strings.TrimSpace(branch),
			strings.TrimSpace(label), by, ts(time.Now())); err != nil {
			return nil, fmt.Errorf("link %s to %s: %w", name, channelID, err)
		}
	case err != nil:
		return nil, err
	default:
		if primary {
			if _, err := tx.Exec(`UPDATE channel_repos SET role = ? WHERE channel_id = ? AND role = ? AND id <> ?`,
				RepoSecondary, channelID, RepoPrimary, id); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(`UPDATE channel_repos SET role = ? WHERE id = ?`, RepoPrimary, id); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(`UPDATE channel_repos SET
				path = CASE WHEN ? <> '' THEN ? ELSE path END,
				branch = CASE WHEN ? <> '' THEN ? ELSE branch END,
				label = CASE WHEN ? <> '' THEN ? ELSE label END
			WHERE id = ?`, path, strings.TrimSpace(path), branch, strings.TrimSpace(branch), label, strings.TrimSpace(label), id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return db.GetRepo(id)
}

// GetRepo reads one link.
func (db *DB) GetRepo(id string) (*ChannelRepo, error) {
	r, err := scanRepo(db.conn.QueryRow(`SELECT `+repoCols+` FROM channel_repos WHERE id = ?`, id))
	if err != nil {
		return nil, fmt.Errorf("repo %s: %w", id, err)
	}
	return r, nil
}

// ListRepos is a channel's repos, primary first, then in the order linked.
func (db *DB) ListRepos(channelID string) ([]ChannelRepo, error) {
	rows, err := db.conn.Query(`SELECT `+repoCols+` FROM channel_repos WHERE channel_id = ?
		ORDER BY role = 'primary' DESC, created_at, rowid`, channelID)
	if err != nil {
		return nil, fmt.Errorf("repos of %s: %w", channelID, err)
	}
	defer rows.Close()
	out := []ChannelRepo{}
	for rows.Next() {
		r, err := scanRepo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// findRepo resolves a link by id or by owner/name within a channel.
func (db *DB) findRepo(channelID, ref string) (*ChannelRepo, error) {
	if strings.HasPrefix(ref, "cr_") {
		return db.GetRepo(ref)
	}
	name, err := ParseGitHubRepo(ref)
	if err != nil {
		return nil, err
	}
	r, err := scanRepo(db.conn.QueryRow(`SELECT `+repoCols+` FROM channel_repos
		WHERE channel_id = ? AND repo = ? COLLATE NOCASE`, channelID, name))
	if err != nil {
		return nil, fmt.Errorf("coord: %s is not linked to %s", name, channelID)
	}
	return r, nil
}

// SetPrimaryRepo makes ref the channel's primary repo.
func (db *DB) SetPrimaryRepo(channelID, ref string) (*ChannelRepo, error) {
	r, err := db.findRepo(channelID, ref)
	if err != nil {
		return nil, err
	}
	return db.AddRepo(channelID, r.Repo, "", "", "", r.AddedBy, true)
}

// RemoveRepo unlinks a repo. Removing the primary promotes the oldest of the
// rest, so a channel with repos always has one that "the repo" means.
func (db *DB) RemoveRepo(channelID, ref string) error {
	r, err := db.findRepo(channelID, ref)
	if err != nil {
		return err
	}
	if r.ChannelID != channelID {
		return fmt.Errorf("coord: %s does not belong to %s", r.ID, channelID)
	}
	if _, err := db.conn.Exec(`DELETE FROM channel_repos WHERE id = ?`, r.ID); err != nil {
		return fmt.Errorf("unlink %s: %w", r.Repo, err)
	}
	if r.Role == RepoPrimary {
		_, err = db.conn.Exec(`UPDATE channel_repos SET role = ? WHERE id = (
			SELECT id FROM channel_repos WHERE channel_id = ? ORDER BY created_at, rowid LIMIT 1)`,
			RepoPrimary, channelID)
	}
	return err
}

// RepoBrief is the section an agent working in the channel reads about its
// code: which repos, which is primary, where each is checked out. "" when the
// channel has none.
func (db *DB) RepoBrief(channelID string) string {
	repos, err := db.ListRepos(channelID)
	if err != nil || len(repos) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("### Repo GitHub của dự án\n\n")
	for _, r := range repos {
		role := "phụ"
		if r.Role == RepoPrimary {
			role = "**chính**"
		}
		fmt.Fprintf(&b, "- %s `%s` — %s", role, r.Repo, r.URL)
		if r.Path != "" {
			fmt.Fprintf(&b, " · bản checkout: `%s`", r.Path)
		}
		if r.Branch != "" {
			fmt.Fprintf(&b, " · nhánh mặc định `%s`", r.Branch)
		}
		if r.Label != "" {
			fmt.Fprintf(&b, " · %s", r.Label)
		}
		b.WriteString("\n")
	}
	b.WriteString("\nKhi nói \"repo của dự án\" mà không nói rõ, đó là repo chính. Repo mới cho dự án: " +
		"`bomclaw ch repo add " + channelID + " <owner/name>`; đổi repo chính: `bomclaw ch repo primary " + channelID + " <owner/name>`.\n\n")
	return b.String()
}
