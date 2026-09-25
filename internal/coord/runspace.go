package coord

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Where a delegated task tree does its work.
//
// A task filed under a project runs in that project's folder. A task filed
// under nothing used to run "where the agent lives" — which is fine for one
// agent and wrong for three. agent2 hands a piece to agent1 and another to
// agent3, and the three of them are then in ~/goterm-workspace,
// ~/goterm-workspace-2 and ~/goterm-workspace-3: same task, three disks, and
// the only way anything crosses between them is a path written into prose,
// which is exactly what the task prompt spends a paragraph telling agents not
// to do.
//
// So a context that has no project gets a folder of its own. A context is the
// task tree — CreateSubTask copies ContextID down to every child — so it is
// already the precise set of tasks that belong to one delegated piece of work,
// whoever ends up carrying each piece.
//
// This is scratch, not product. What the work PRODUCED still goes in an
// artifact: an artifact is found by id, survives the file moving, and shows up
// beside the task on the board. A run folder is a place to work, it is shared
// only inside its own context, and it ages out.

// DefaultRunsDir is where shared run folders live unless config says
// otherwise. It sits beside artifacts in the shared tree rather than in any
// agent's workspace, for the same reason: the work belongs to the task, not to
// whichever agent happened to claim the first piece of it.
func DefaultRunsDir() string {
	if d := strings.TrimSpace(os.Getenv("BOMCLAW_RUNS_DIR")); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "goterm-shared", "runs")
}

// RunsDir is the root this database creates run folders under.
func (db *DB) RunsDir() string {
	if db.runsDir == "" {
		return DefaultRunsDir()
	}
	return db.runsDir
}

// SetRunsDir overrides the root, for config and for tests.
func (db *DB) SetRunsDir(dir string) { db.runsDir = dir }

// RunspacePath is where a context works. It says nothing about whether the
// folder exists.
func (db *DB) RunspacePath(contextID string) string {
	if contextID == "" {
		return ""
	}
	return filepath.Join(db.RunsDir(), contextID)
}

// TaskWorkspace is the directory a run of this task belongs in, and whether
// that directory is shared with the rest of its context. It decides and
// nothing else — no directory is created — so a read-only caller like
// `bomclaw task show` can ask without leaving a folder behind.
//
// The rule is one line: a ROOT task with a project runs in the project folder;
// everything else runs in its context's shared scratch.
//
// The first version of this said simply "a project wins", for the whole tree.
// That was right while decomposition was rare — the system had produced six
// sub-tasks in its life — and it stops being right the moment a goal fans out,
// because MaxOpenChildren allows eight agents at once and they would all be
// writing into the project folder together. Those folders are plain
// directories, not repositories: two agents overwriting each other there is a
// silent loss, with no conflict to see and nothing to undo.
//
// So the product and the scratch are separated. The root assembles what the
// work produced, in the project, where a person goes looking for it; the
// children experiment and hand each other files in the shared run folder, which
// is swept on the artifact clock. Only one task ever writes to the project, so
// there is nothing to collide with. A lost scratch file is not a lost
// deliverable.
//
// Keyed on ParentID rather than on "has this tree been split yet" deliberately:
// ParentID never changes, so a task's directory never moves under it. The other
// rule would move a running task's working directory between two runs, and the
// files it wrote in the first one would be somewhere it is no longer standing.
//
// An empty return means "the agent's own workspace", which is what a task with
// no context (there is none in practice) gets.
func (db *DB) TaskWorkspace(t *Task) (dir string, shared bool) {
	if t == nil {
		return "", false
	}
	if t.ParentID == "" && t.ChannelID != "" {
		if c, err := db.GetChannel(t.ChannelID); err == nil && c.Workspace != "" {
			return c.Workspace, false
		}
		// A channel that is a room and not a project falls through: #general
		// has no folder, and a task filed there still needs somewhere to work.
	}
	path := db.RunspacePath(t.ContextID)
	if path == "" {
		return "", false
	}
	return path, true
}

// EnsureTaskWorkspace is TaskWorkspace with the folder actually there. Only a
// run needs this; three gateways may reach it at once and MkdirAll is
// idempotent, so there is nothing to coordinate.
func (db *DB) EnsureTaskWorkspace(t *Task) (dir string, shared bool, err error) {
	dir, shared = db.TaskWorkspace(t)
	if dir == "" || !shared {
		// A project folder is created by CreateProject and is not this
		// function's to conjure: a project whose folder has gone is a problem
		// to see, not to paper over.
		return dir, shared, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("run folder %s: %w", dir, err)
	}
	return dir, true, nil
}

// PurgeRunspaces removes the folders of task trees that finished before the
// cutoff, and reports how many went.
//
// It reads the same "this context has stopped moving" predicate as the
// artifact purge, because it is the same question. What it does not share is
// the artifact rule that documents are kept forever: nothing in here is a
// product, so there is nothing to keep.
//
// A folder with no context row left behind it is left alone. Deleting
// directories is the one thing here that cannot be undone, and "I do not
// recognise this" is not evidence that nobody wants it.
func (db *DB) PurgeRunspaces(before time.Time) (int, error) {
	rows, err := db.conn.Query(`SELECT context_id FROM tasks
		GROUP BY context_id
		HAVING sum(CASE WHEN state IN (?, ?, ?, ?) THEN 0 ELSE 1 END) = 0
		   AND max(updated_at) < ?`,
		TaskCompleted, TaskFailed, TaskCanceled, TaskRejected, ts(before))
	if err != nil {
		return 0, fmt.Errorf("purge run folders: %w", err)
	}
	defer rows.Close()
	var contexts []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("purge run folders: %w", err)
		}
		contexts = append(contexts, id)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	root := db.RunsDir()
	n := 0
	for _, id := range contexts {
		full, ok := underRoot(root, id)
		if !ok {
			continue // a context id that escapes the root is not ours to delete
		}
		if _, err := os.Stat(full); err != nil {
			continue // never had a folder, or somebody already took it
		}
		if err := os.RemoveAll(full); err != nil {
			return n, fmt.Errorf("remove run folder %s: %w", full, err)
		}
		n++
	}
	return n, nil
}
