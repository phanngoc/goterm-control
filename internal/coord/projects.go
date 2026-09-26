package coord

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A channel is a project.
//
// The room, the work and the files are the same thing seen from three sides:
// people talk about a project in its room, the tasks that come out of that
// talk belong to it, and what the agents produce lands in its folder. Modelling
// them apart would mean keeping three names in step and explaining to every
// agent which of the three it is currently in.
//
// #general and DMs stay rooms with no folder. Not every conversation is a
// project, and giving the catch-all room a repository would invite work into
// the one place that exists for things too small to organise.

// DefaultProjectsDir is where project folders live unless config says
// otherwise. Outside any agent's own workspace: a project belongs to the
// people and agents working on it, not to whichever agent happened to start it.
func DefaultProjectsDir() string {
	if d := strings.TrimSpace(os.Getenv("BOMCLAW_PROJECTS_DIR")); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "goterm-projects")
}

// agentsFile is the brief every agent reads before working on a project: what
// it is, what it is for, who does what. A file rather than a database column
// because the agents edit it the way they edit anything else, and because it
// belongs in the repository it describes.
const AgentsFile = "AGENTS.md"

// CreateProject makes a channel with a folder behind it, seeds the brief, and
// returns the channel. Idempotent like CreateChannel: calling it twice gives
// the existing project rather than a second one.
func (db *DB) CreateProject(name, purpose, createdBy, projectsDir string, members []Member) (*Channel, error) {
	c, err := db.CreateChannel("", name, ChannelPublic, purpose, createdBy, members)
	if err != nil {
		return nil, err
	}
	if c.Workspace != "" {
		return c, nil // already a project; leave its folder alone
	}
	if projectsDir == "" {
		projectsDir = DefaultProjectsDir()
	}
	ws := filepath.Join(projectsDir, slug(name))
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return nil, fmt.Errorf("project folder %s: %w", ws, err)
	}
	if err := seedAgentsFile(ws, c, purpose); err != nil {
		return nil, err
	}
	if _, err := db.conn.Exec(`UPDATE channels SET workspace = ? WHERE id = ?`, ws, c.ID); err != nil {
		return nil, fmt.Errorf("record workspace for %s: %w", c.ID, err)
	}
	c.Workspace = ws
	return c, nil
}

// seedAgentsFile writes the starting brief, and never overwrites one that is
// already there — a project folder that outlived its row still holds the only
// copy of what people wrote about it.
func seedAgentsFile(workspace string, c *Channel, purpose string) error {
	path := filepath.Join(workspace, AgentsFile)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if purpose == "" {
		purpose = "_(chưa mô tả — sửa file này)_"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", c.Name)
	fmt.Fprintf(&b, "%s\n\n", purpose)
	b.WriteString("## Mục tiêu\n\n_Dự án này cố gắng đạt được gì. Viết cụ thể: một agent đọc xong phải biết thế nào là xong._\n\n")
	b.WriteString("## Thành phần\n\n_Các phần của dự án, và chúng nằm ở đâu trong thư mục này._\n\n")
	b.WriteString("## Vai trò\n\n_Agent nào lo mảng nào. Ghi cả điểm yếu, không chỉ điểm mạnh —\n" +
		"định tuyến theo điểm mạnh mà bỏ qua điểm yếu thì được câu trả lời nhanh và sai._\n\n")
	b.WriteString("## Quy ước\n\n_Những thứ một người mới vào phải biết mà không đọc được từ code:\n" +
		"nhánh git, cách chạy, chỗ không được đụng vào._\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// WriteProjectBrief replaces a project's AGENTS.md.
//
// The file is the source of truth, not a database column, so this writes the
// file — and it is the same file the agents read and edit with their own
// tools. Whoever wrote last wins, which is how a shared document in a
// repository has always worked.
func (db *DB) WriteProjectBrief(channelID, body string) error {
	c, err := db.GetChannel(channelID)
	if err != nil {
		return err
	}
	if c.Workspace == "" {
		return fmt.Errorf("coord: %s is a room, not a project — it has no folder to write into", c.Name)
	}
	if err := os.MkdirAll(c.Workspace, 0o755); err != nil {
		return fmt.Errorf("project folder %s: %w", c.Workspace, err)
	}
	path := filepath.Join(c.Workspace, AgentsFile)
	tmp, err := os.CreateTemp(c.Workspace, ".AGENTS-*.md")
	if err != nil {
		return fmt.Errorf("write brief: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return fmt.Errorf("write brief: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write brief: %w", err)
	}
	// Replaced whole, never truncated-then-written: an agent reading the file
	// mid-save would otherwise get half a brief and act on it.
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// ProjectBrief reads a project's AGENTS.md, or "" when there is none. Missing
// is normal — a project whose brief nobody has written yet — and not an error
// worth failing a turn over.
func (db *DB) ProjectBrief(channelID string) string {
	c, err := db.GetChannel(channelID)
	if err != nil || c.Workspace == "" {
		return ""
	}
	body, err := os.ReadFile(filepath.Join(c.Workspace, AgentsFile))
	if err != nil {
		return ""
	}
	return string(body)
}
