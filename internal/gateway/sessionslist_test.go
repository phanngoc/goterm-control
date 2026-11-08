package gateway

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/session"
	"github.com/ngocp/goterm-control/internal/transcript"
)

// The Sessions list filled with ch_cm_… rows at "0 turns": channel threads
// were hidden from the store's half of the list and then put straight back by
// the transcript scan, as empty stubs. Neither half may show them.
func TestSessionsListLeavesChannelThreadsAndTaskRunsOut(t *testing.T) {
	dir := t.TempDir()
	mgr := session.NewManager(nil)
	w := transcript.NewWriter(filepath.Join(dir, "transcripts"))
	now := time.Now()

	// A real chat, in the store and with a transcript.
	chat := mgr.Get(42)
	_ = w.Append(chat.ID, transcript.Event{Type: transcript.EventUserMessage, Timestamp: now, ChatID: 42, Content: "hi"})

	// A channel thread still in the store.
	thread := session.New(-2)
	thread.ID = "ch_cm_live"
	mgr.Adopt(thread)
	_ = w.Append(thread.ID, transcript.Event{Type: transcript.EventUserMessage, Timestamp: now, ChatID: -2, Content: "named in #room"})

	// A channel thread and a task run whose rows are gone, transcripts only.
	_ = w.Append("ch_cm_orphan", transcript.Event{Type: transcript.EventUserMessage, Timestamp: now, ChatID: -2, Content: "x"})
	_ = w.Append("task_orphan", transcript.Event{Type: transcript.EventUserMessage, Timestamp: now, ChatID: -1, Content: "x"})

	// An old chat known only from its transcript still belongs on the list.
	_ = w.Append("chat_old", transcript.Event{Type: transcript.EventUserMessage, Timestamp: now, ChatID: 7, Content: "x"})

	raw, err := handleSessionsList(Deps{Sessions: mgr, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	var out []SessionInfo
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range out {
		got[s.ID] = true
	}
	for _, id := range []string{"ch_cm_live", "ch_cm_orphan", "task_orphan"} {
		if got[id] {
			t.Errorf("%s is on the Sessions list; it belongs to a room or the task board", id)
		}
	}
	for _, id := range []string{chat.ID, "chat_old"} {
		if !got[id] {
			t.Errorf("%s is missing from the Sessions list", id)
		}
	}
}
