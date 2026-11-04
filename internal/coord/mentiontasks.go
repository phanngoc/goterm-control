package coord

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Work handed over in a thread is still work.
//
// An agent that splits a job ("@bomclaw2 — puzzle research, @bomclaw3 —
// action games") does it with mentions, because that is how a room talks. A
// mention wakes the peer for one turn and nothing else: no task, nothing on
// the board, and no task id for `artifact put`, so the research the peers did
// for ch_arcade on 2026-11-04 left the board at zero and one of them said so
// in its reply. Asking every agent to remember `task new --to` instead is a
// rule that already existed and did not hold.
//
// So the turn records itself. A mention that turns out to be work — it reaches
// for a tool, or runs long — opens a task for it: on the room's board, carried
// by the agent doing it, closed with the reply. A quick answer never becomes
// one. The task is a record, not a queue entry: nobody claims it, the reply in
// the thread is its report, and the run under it is what the economics report
// counts as task work.

// KindMention marks a task that records a mention turn rather than queues one.
const KindMention = "mention"

// mentionContext is the stable handle of the task for one mention, so a turn
// re-run after a restart finds the task it opened the first time.
func mentionContext(messageID string) string { return "mention_" + messageID }

// OpenMentionTask records that agent is working on the mention m. It returns
// the open task for that mention if one already exists (a re-run after a
// restart), and links the turn's trace as the task's run.
func (db *DB) OpenMentionTask(m ChannelMessage, agent, traceID string) (*Task, error) {
	ctxID := mentionContext(m.ID)
	var existing string
	err := db.conn.QueryRow(`SELECT id FROM tasks WHERE context_id = ? AND kind = ? AND state = ?`,
		ctxID, KindMention, TaskWorking).Scan(&existing)
	switch {
	case err == nil:
		if traceID != "" {
			if _, err := db.StartRun(existing, agent, 1, traceID); err != nil {
				return nil, err
			}
		}
		return db.GetTask(existing)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("mention task for %s: %w", m.ID, err)
	}

	title, body := mentionTitle(m.Body), strings.TrimSpace(m.Body)
	t, err := db.CreateTask(NewTask{
		ContextID:  ctxID,
		ChannelID:  m.ChannelID,
		Kind:       KindMention,
		CreatedBy:  m.AuthorID,
		AssignedTo: agent,
		Title:      title,
		Body:       body + "\n\n— asked in " + m.ChannelID + ", thread " + ThreadKey(m),
	})
	if err != nil {
		return nil, err
	}
	// Working from the first moment, carried by this agent, and already
	// "reported": its reply lands in the thread, which is where the person
	// who asked is reading. The long lease is only a guard — ClaimTask skips
	// this kind, so no runner ever picks it up.
	now := time.Now()
	if _, err := db.conn.Exec(`UPDATE tasks SET state = ?, claimed_by = ?, attempts = 1,
			lease_until = ?, reported_at = ?, trace_id = ?, updated_at = ?
		WHERE id = ?`,
		TaskWorking, agent, ts(now.Add(24*time.Hour)), ts(now), traceID, ts(now), t.ID); err != nil {
		return nil, fmt.Errorf("start mention task %s: %w", t.ID, err)
	}
	_ = db.appendEvent(t.ID, agent, TaskSubmitted, TaskWorking, "opened from a mention in "+m.ChannelID)
	if traceID != "" {
		if _, err := db.StartRun(t.ID, agent, 1, traceID); err != nil {
			return nil, err
		}
	}
	return db.GetTask(t.ID)
}

// CloseMentionTask ends the record with the turn: completed with the reply,
// or failed with why.
func (db *DB) CloseMentionTask(taskID, agent string, ok bool, result string) error {
	state, liveness := TaskCompleted, "completed"
	if !ok {
		state, liveness = TaskFailed, "failed"
	}
	if err := db.FinishTask(taskID, agent, state, result, 1); err != nil {
		return err
	}
	_, err := db.conn.Exec(`UPDATE task_runs SET liveness = ?, ended_at = ?
		WHERE task_id = ? AND liveness = 'running'`, liveness, ts(time.Now()), taskID)
	return err
}

// CloseStaleMentionTask fails the open record for a mention that will not be
// answered (skipped as too old), so it does not sit on the board as working.
func (db *DB) CloseStaleMentionTask(messageID, agent string) {
	var id string
	if err := db.conn.QueryRow(`SELECT id FROM tasks WHERE context_id = ? AND kind = ? AND state = ?`,
		mentionContext(messageID), KindMention, TaskWorking).Scan(&id); err != nil {
		return
	}
	_ = db.CloseMentionTask(id, agent, false, "the turn was interrupted and the mention went stale")
}

// mentionTitle is the first line of the ask, without the @names that only
// routed it, cut to fit a board card.
func mentionTitle(body string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(body), "\n", 2)[0])
	var keep []string
	for _, w := range strings.Fields(line) {
		if strings.HasPrefix(w, "@") {
			continue
		}
		keep = append(keep, w)
	}
	t := strings.Join(keep, " ")
	if t == "" {
		t = "Việc được giao trong thread"
	}
	if r := []rune(t); len(r) > 90 {
		t = string(r[:89]) + "…"
	}
	return t
}

// EnsureMentionTask is OpenMentionTask from outside the turn: the CLI, when an
// agent answering a mention files an artifact and has no task id to give it.
// The turn has usually opened the task already; when it has not yet, this
// opens it, and the turn later attaches its trace to the same row.
func (db *DB) EnsureMentionTask(messageID, agent string) (*Task, error) {
	m, err := db.GetMessage(messageID)
	if err != nil {
		return nil, err
	}
	return db.OpenMentionTask(*m, agent, "")
}
