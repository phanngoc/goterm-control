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
// one. The task is a record, not a queue entry — until its turn runs out of
// time (HandOffMentionTask, below): the reply in the thread is its report, and the run under it is what the economics report
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
	// this kind until HandOffMentionTask queues it, so no runner picks up a
	// turn that is still running.
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

// A mention turn has eight minutes; a task has as many fifteen-minute runs as
// it needs. Until 2026-11-15 a turn that ran out of its eight minutes closed
// its record as failed, in the middle of the work, with a note telling the
// owner to send it again as a task. The record WAS a task by then — on the
// board, with "attempt 1/3 · run 1/20" printed on it — and the only run it
// ever got was the one that was cut off.
//
// So running out of time hands the record over instead: it goes into the
// queue, pinned to the agent that was doing it, carrying the CLI session the
// turn used, and the task runner resumes that conversation where it stopped.
// It is still a mention task — it answers into the thread it was asked in,
// which ConversationThread already finds — but from here on it is a queued
// one, and HandedOff says so.

// HandedOff reports whether a mention task has left its turn for the queue.
// Before that it is a record of a turn: no runner may claim it, and its
// conversation is the thread's. After, it is ordinary queued work.
func (t *Task) HandedOff() bool {
	return t != nil && t.Kind == KindMention && t.Continuations > 0
}

// HandOffMentionTask moves a mention task whose turn ran out of time into the
// queue: submitted, pinned to agent, carrying ref so the next run resumes the
// same CLI conversation, with checkpoint as the note on where it stopped. The
// turn's run is closed as timed out. The attempt is given back, as FinishRun
// does for a run that timed out with progress: running out of time is not a
// failure, and three of them must not fail the task.
func (db *DB) HandOffMentionTask(taskID, agent string, ref SessionRef, checkpoint string) (*Task, error) {
	tx, err := db.conn.Begin()
	if err != nil {
		return nil, fmt.Errorf("hand off %s: %w", taskID, err)
	}
	defer tx.Rollback()

	now := time.Now()
	res, err := tx.Exec(`UPDATE tasks SET state = ?, assigned_to = ?, lease_until = ?,
			attempts = max(attempts - 1, 0), continuations = continuations + 1,
			session_ref = ?, checkpoint = ?, updated_at = ?
		WHERE id = ? AND kind = ? AND state = ? AND claimed_by = ?`,
		TaskSubmitted, agent, ts(now), ref.String(), checkpoint, ts(now),
		taskID, KindMention, TaskWorking, agent)
	if err != nil {
		return nil, fmt.Errorf("hand off %s: %w", taskID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Canceled from the board meanwhile, or not this agent's: either way
		// not ours to requeue.
		return nil, fmt.Errorf("hand off %s: %w", taskID, ErrTaskFinished)
	}
	if _, err := tx.Exec(`UPDATE task_runs SET liveness = ?, ended_at = ?, note = ?
		WHERE task_id = ? AND liveness = ?`,
		RunTimedOut, ts(now), "reply window ran out; continued in the queue", taskID, RunRunning); err != nil {
		return nil, fmt.Errorf("hand off %s: close run: %w", taskID, err)
	}
	if _, err := tx.Exec(`INSERT INTO task_events (task_id, agent_id, from_state, to_state, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, taskID, agent, TaskWorking, TaskSubmitted,
		"reply window ran out; handed to the task queue", ts(now)); err != nil {
		return nil, fmt.Errorf("hand off %s: event: %w", taskID, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("hand off %s: %w", taskID, err)
	}
	return db.GetTask(taskID)
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

// MentionThread is the thread a mention task's work was asked in: the room
// conversation it belongs to, which is where its talk and its answer are.
// "" for any other task.
func (db *DB) MentionThread(t *Task) (rootID, channelID string) {
	if t == nil || t.Kind != KindMention || !strings.HasPrefix(t.ContextID, "mention_") {
		return "", ""
	}
	m, err := db.GetMessage(strings.TrimPrefix(t.ContextID, "mention_"))
	if err != nil {
		return "", ""
	}
	return ThreadKey(*m), m.ChannelID
}
