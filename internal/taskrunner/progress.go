package taskrunner

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ngocp/goterm-control/internal/chat"
	"github.com/ngocp/goterm-control/internal/coord"
)

// Saying what a task is doing, in the room that asked for it.
//
// A task opened from a conversation runs for minutes and the thread shows
// nothing until it is over. That is the same silence a channel turn had before
// it started reporting progress — except worse, because a task is the long
// kind of work, so the silence lasts longer and the person watching has more
// reason to wonder whether anything is happening at all.
//
// One line per RUN, edited in place and removed when the run ends. Not kept
// between runs on purpose: between runs nothing IS running, and a line that
// still said "working" would be a lie the rest of the time. The report the
// reporter posts when the task finishes is the permanent record; this is only
// the window while it happens.
// progressPrefix is coord.ProgressPrefix; the startup sweep matches the same.
const progressPrefix = coord.ProgressPrefix

// progressLine owns the message a run keeps updated.
type progressLine struct {
	db        *coord.DB
	messageID string
	channelID string
	taskID    string
	title     string
	started   time.Time

	mu      sync.Mutex
	tools   []string
	last    time.Time
	reply   string
	preview *chat.StreamPreview
}

const progressEvery = 3 * time.Second

// openProgress puts a line in the thread this task came from, if it came from
// one. Nil when it did not, and every method below tolerates nil — a task
// queued from the CLI or a schedule has no room to report into and must not
// invent one.
func openProgress(db *coord.DB, task *coord.Task) *progressLine {
	if db == nil || task == nil {
		return nil
	}
	rootID, channelID, err := db.TaskThread(task.ID)
	if err != nil || rootID == "" {
		return nil
	}

	// Sweep a line left behind by a run that died with the gateway. Without
	// this, a crash leaves the room with an agent permanently about to finish.
	if thread, err := db.ThreadMessages(rootID); err == nil {
		for _, m := range thread {
			if m.AuthorKind == coord.MemberAgent && strings.HasPrefix(m.Body, progressPrefix) &&
				strings.Contains(m.Body, task.ID) {
				if err := db.DeleteMessage(m.ID); err != nil {
					log.Printf("taskrunner: stale progress line: %v", err)
				}
			}
		}
	}

	p := &progressLine{
		db: db, channelID: channelID, taskID: task.ID,
		title: task.Title, started: time.Now(), preview: chat.DefaultPreview(),
	}
	msg, _, err := db.PostMessage(coord.NewChannelMessage{
		ChannelID: channelID, ThreadRoot: rootID,
		AuthorKind: coord.MemberAgent, AuthorID: task.ClaimedBy,
		Body: p.body(),
	})
	if err != nil {
		log.Printf("taskrunner: could not open a progress line: %v", err)
		return nil
	}
	p.messageID = msg.ID
	return p
}

// Tool records what the run just reached for and updates the room. A new tool
// reports at once; that is the part worth watching, and it changes on the
// order of seconds rather than tokens.
func (p *progressLine) Tool(name string) {
	if p == nil || name == "" {
		return
	}
	p.mu.Lock()
	if len(p.tools) == 0 || p.tools[len(p.tools)-1] != name {
		p.tools = append(p.tools, name)
	}
	stale := time.Since(p.last) >= progressEvery
	if stale {
		p.last = time.Now()
	}
	body := p.bodyLocked()
	p.mu.Unlock()

	if !stale {
		return
	}
	if err := p.db.UpdateMessageBody(p.messageID, body); err != nil {
		log.Printf("taskrunner: progress update: %v", err)
	}
}

// Text shows the answer as it forms. The threshold is a character count, not a
// timer: a timer fires mid-word as often as not, and the point of watching is
// to see the reasoning arrive in readable pieces.
func (p *progressLine) Text(chunk string) {
	if p == nil || chunk == "" {
		return
	}
	p.mu.Lock()
	p.reply += chunk
	grown := p.preview.Ready(p.reply)
	body := p.bodyLocked()
	p.mu.Unlock()

	if !grown {
		return
	}
	if err := p.db.UpdateMessageBody(p.messageID, body); err != nil {
		log.Printf("taskrunner: progress update: %v", err)
	}
}

// Close removes the line. The result belongs to the reporter, which posts it
// when the task itself finishes rather than when one run of it does.
func (p *progressLine) Close() {
	if p == nil {
		return
	}
	if err := p.db.DeleteMessage(p.messageID); err != nil {
		log.Printf("taskrunner: could not close the progress line: %v", err)
	}
}

func (p *progressLine) body() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bodyLocked()
}

func (p *progressLine) bodyLocked() string {
	var b strings.Builder
	b.WriteString(progressPrefix)
	fmt.Fprintf(&b, "_đang chạy_ `%s` · %s", p.taskID, time.Since(p.started).Round(time.Second))
	if len(p.tools) > 0 {
		from := 0
		if len(p.tools) > 5 {
			from = len(p.tools) - 5
		}
		fmt.Fprintf(&b, " · %s", strings.Join(p.tools[from:], " → "))
	}
	if t := strings.TrimSpace(p.title); t != "" {
		fmt.Fprintf(&b, "\n\n%s", t)
	}
	// What it is actually saying, once it starts saying anything. This is the
	// part a person waiting wants; the tool names above are context for it.
	if r := strings.TrimSpace(p.reply); r != "" {
		fmt.Fprintf(&b, "\n\n%s", p.preview.Cut(r))
	}
	return b.String()
}
