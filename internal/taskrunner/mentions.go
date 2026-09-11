package taskrunner

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// Mentions are how a channel reaches an agent that is not already running.
//
// Posting records the mention and rings the named agent's doorbell, but the
// doorbell wakes this claim loop, and the claim loop only knows how to claim
// tasks. Nothing turned the one into the other: a mention sat unread forever
// while the agent slept a metre away from it. This file is that conversion —
// an unread mention becomes an ordinary task, and then everything the task
// machinery already does (lease, fencing, attempts, trace, transcript, the
// Tasks tab) applies to answering a message for free.

const (
	// MaxMentionTasks is how many unanswered mentions an agent may be
	// carrying before it stops taking new ones. Two agents can otherwise
	// answer each other forever: neither is misbehaving, there is just
	// nothing in a conversation that says when to stop.
	MaxMentionTasks = 3

	// MentionsPerSweep bounds one pass, so an agent that was offline for a
	// day does not wake into a hundred turns at once.
	MentionsPerSweep = 5

	// StaleMention is when answering stops being useful and starts being
	// confusing. Older ones are marked read with a log line instead.
	StaleMention = 24 * time.Hour

	// MentionRuns caps the task. A reply is a reply; if it turns out to need
	// real work, the agent creates a task for that work and says so.
	MentionRuns = 2
)

// mentionsToTasks converts this agent's unread mentions into work. Called from
// sweep, so both the poll tick and a poke reach it.
//
// Each agent handles only its own mentions, so two gateways never race for the
// same one and no cross-process locking is needed.
func (r *Runner) mentionsToTasks() {
	open, err := r.db.OpenMentionTasks(r.cfg.AgentID)
	if err != nil {
		log.Printf("taskrunner: count mention tasks: %v", err)
		return
	}
	if open >= MaxMentionTasks {
		return // already behind; answering more would only deepen it
	}

	msgs, err := r.db.UnreadMentions(coord.MemberAgent, r.cfg.AgentID, MentionsPerSweep)
	if err != nil {
		log.Printf("taskrunner: read mentions: %v", err)
		return
	}
	// Oldest first: a conversation answered out of order reads as nonsense.
	for i := len(msgs) - 1; i >= 0; i-- {
		if open >= MaxMentionTasks {
			log.Printf("taskrunner: %d mention task(s) already open — the rest wait", open)
			return
		}
		m := msgs[i]

		if time.Since(m.CreatedAt) > StaleMention {
			if _, err := r.db.MarkMentionsRead(coord.MemberAgent, r.cfg.AgentID, []string{m.ID}); err != nil {
				log.Printf("taskrunner: clear stale mention %s: %v", m.ID, err)
				return // do not spin on the same row
			}
			log.Printf("taskrunner: mention %s from %s is %s old — cleared without answering",
				m.ID, m.AuthorID, time.Since(m.CreatedAt).Round(time.Hour))
			continue
		}

		// Create first, mark read second. The other order loses a summons
		// silently if the create fails; this order can at worst answer twice,
		// which is visible and bounded by MaxMentionTasks.
		task, err := r.db.CreateTask(coord.NewTask{
			CreatedBy:        m.AuthorID,
			AssignedTo:       r.cfg.AgentID,
			Kind:             coord.KindMention,
			Title:            mentionTitle(r.db.ChannelName(m.ChannelID), m.AuthorID),
			Body:             mentionPrompt(r.db.ChannelName(m.ChannelID), m),
			MaxContinuations: MentionRuns,
		})
		if err != nil {
			log.Printf("taskrunner: mention %s → task: %v", m.ID, err)
			return
		}
		if _, err := r.db.MarkMentionsRead(coord.MemberAgent, r.cfg.AgentID, []string{m.ID}); err != nil {
			log.Printf("taskrunner: mention %s became %s but stayed unread: %v", m.ID, task.ID, err)
		}
		log.Printf("taskrunner: %s named you in %s → task %s", m.AuthorID, m.ChannelID, task.ID)
		open++
	}
}

func mentionTitle(channel, author string) string {
	return fmt.Sprintf("Reply to %s in #%s", author, channel)
}

// mentionPrompt is what the agent reads. It has to carry three things the
// agent cannot look up on its own: what was said, where the reply goes, and
// that a reply is the whole job.
func mentionPrompt(channel string, m coord.ChannelMessage) string {
	thread := m.ThreadRoot
	if thread == "" {
		thread = m.ID // replying to a top-level message opens its thread
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s named you in #%s.\n\n", m.AuthorID, channel)
	fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(strings.TrimSpace(m.Body), "\n", "\n> "))

	if m.ThreadRoot != "" {
		fmt.Fprintf(&b, "It is a reply inside a thread. Read the whole thread first:\n"+
			"`bomclaw ch read %s --thread %s`\n\n", m.ChannelID, thread)
	}
	if m.TaskID != "" {
		fmt.Fprintf(&b, "The thread is about task %s — `bomclaw task show --id %s`.\n\n", m.TaskID, m.TaskID)
	}

	fmt.Fprintf(&b, "Answer in the thread, not in the channel's main line:\n\n"+
		"    bomclaw ch post %s --thread %s \"your reply\"\n\n", m.ChannelID, thread)

	b.WriteString("This task is done when you have posted that reply. Notes:\n\n" +
		"- Answer the question. If you cannot, say what you would need, in the thread.\n" +
		"- Name another agent with `@` only when you actually need something from it. " +
		"Every mention costs that agent a turn, and two agents naming each other back " +
		"and forth is a loop nobody stops.\n" +
		"- If answering properly turns out to be real work, create a task for the work " +
		"(`bomclaw task new`), say so in the thread, and finish this one.\n")
	return b.String()
}
