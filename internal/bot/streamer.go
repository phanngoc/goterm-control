package bot

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	maxTelegramMsg   = 4000 // Telegram limit is 4096, leave room for formatting
	streamInterval   = 800 * time.Millisecond
	maxCoalesceChars = 1200 // force flush when buffer exceeds this (openclaw pattern)
	minInitialChars  = 30   // hold first send until meaningful content
)

// Streamer manages real-time streaming of text to a Telegram message.
// It shows only assistant text to the user. Tool calls are shown as a
// compact progress line that gets replaced by the final response.
type Streamer struct {
	bot       *tgbotapi.BotAPI
	chatID    int64
	messageID int

	mu    sync.Mutex
	buf   strings.Builder
	dirty bool

	// Reasoning lane: separate message for thinking content (openclaw lane system)
	reasonBuf   strings.Builder
	reasonMsgID int
	reasonDirty bool
	thinkState  thinkingState

	// Coalescing: hold first send until meaningful content (openclaw minInitialChars)
	initialSent bool

	// Dedup and anti-regression guards (openclaw pattern)
	lastSentHTML  string
	lastAssistLen int

	// In-flight tracking: prevent concurrent Telegram API calls (openclaw pattern)
	inflight     bool
	pendingFlush bool

	// Tool progress tracking — rolling window display.
	// toolHead: first 8 tools (always shown).
	// toolCheckpoints: 2 representative tools sampled every 10 after the head.
	toolHead        []string
	toolCheckpoints []string
	toolCount       int

	startTime time.Time // for heartbeat elapsed display

	ticker *time.Ticker
	done   chan struct{}
	wg     sync.WaitGroup

	overflow []string
}

func NewStreamer(bot *tgbotapi.BotAPI, chatID int64, messageID int) *Streamer {
	s := &Streamer{
		bot:       bot,
		chatID:    chatID,
		messageID: messageID,
		startTime: time.Now(),
		ticker:    time.NewTicker(streamInterval),
		done:      make(chan struct{}),
	}
	s.wg.Add(1)
	go s.loop()
	return s
}

func (s *Streamer) loop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ticker.C:
			s.flush()
		case <-s.done:
			s.flush()
			return
		}
	}
}

// Write appends assistant text to the buffer.
// Thinking tags are parsed and routed to the reasoning lane; answer text
// goes to the main buffer. Tool markers and special tokens are stripped.
func (s *Streamer) Write(chunk string) {
	// Strip tool markers and special tokens (thinking tags handled by thinkState)
	chunk = toolCallRe.ReplaceAllString(chunk, "")
	chunk = specialRe.ReplaceAllString(chunk, "")
	if chunk == "" {
		return
	}

	var forceFlush bool

	s.mu.Lock()
	thinkText, answerText := s.thinkState.processChunk(chunk)

	if thinkText != "" {
		s.reasonBuf.WriteString(thinkText)
		s.reasonDirty = true
	}
	if answerText != "" {
		s.buf.WriteString(answerText)
		s.dirty = true
		if len([]rune(s.buf.String())) >= maxCoalesceChars {
			forceFlush = true
		}
	}
	s.mu.Unlock()

	if forceFlush {
		s.flush()
	}
}

// NoteTool records a tool call as a compact rolling-window progress indicator.
// The first 8 tools are always shown (head). After that, every batch of 10
// tools contributes 2 "checkpoint" tools so the user sees recent activity:
//   🔧 T1 → … → T8 → (+10 more) → Ta → Tb → (+10 more) → Tc → Td → (+5 more)
func (s *Streamer) NoteTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCount++

	const headSize = 8
	const batchSize = 10

	if s.toolCount <= headSize {
		s.toolHead = append(s.toolHead, name)
	} else {
		// Position within the current batch of 10 (1-indexed)
		posInBatch := (s.toolCount - headSize - 1) % batchSize
		// Sample the 2nd-to-last and last tool of each batch (positions 8 and 9)
		if posInBatch == batchSize-2 || posInBatch == batchSize-1 {
			s.toolCheckpoints = append(s.toolCheckpoints, name)
		}
	}
	s.dirty = true
}

// Flush forces an immediate edit.
func (s *Streamer) Flush() {
	s.flush()
}

func (s *Streamer) flush() {
	s.mu.Lock()
	// Allow heartbeat ticks even when no new content has arrived,
	// so the user sees "Thinking... (Xs)" updating during Claude's thinking phase.
	needsHeartbeat := !s.initialSent && time.Since(s.startTime) >= 2*time.Second
	if !s.dirty && !s.reasonDirty && !needsHeartbeat {
		s.mu.Unlock()
		return
	}
	// In-flight guard: if a Telegram API call is active, defer this flush
	if s.inflight {
		s.pendingFlush = true
		s.mu.Unlock()
		return
	}

	assistantText := strings.TrimSpace(s.buf.String())
	reasonText := strings.TrimSpace(s.reasonBuf.String())
	toolLine := s.toolStatusLine()

	// Hold first send until minInitialChars for meaningful push notification.
	// While waiting, send a heartbeat with elapsed time so the user sees
	// the bot is alive (instead of a frozen "⏳ Thinking..." placeholder).
	totalChars := len([]rune(assistantText)) + len([]rune(reasonText))
	if !s.initialSent && totalChars < minInitialChars && toolLine == "" {
		elapsed := time.Since(s.startTime).Truncate(time.Second)
		if elapsed >= 2*time.Second {
			s.inflight = true
			s.mu.Unlock()
			s.editCurrent(fmt.Sprintf("⏳ <i>Thinking... (%s)</i>", elapsed))
			s.mu.Lock()
			s.inflight = false
			needsFlush := s.pendingFlush
			s.pendingFlush = false
			s.mu.Unlock()
			if needsFlush {
				s.flush()
			}
			return
		}
		s.mu.Unlock()
		return
	}

	s.dirty = false
	s.reasonDirty = false
	s.inflight = true
	s.mu.Unlock()

	// Flush reasoning lane (thinking content → separate italic message)
	if reasonText != "" {
		s.flushReasonLane(reasonText)
	}

	// Flush answer lane
	if assistantText != "" || toolLine != "" {
		s.sendFormatted(assistantText, toolLine)
	}

	s.mu.Lock()
	s.inflight = false
	s.initialSent = true
	needsFlush := s.pendingFlush
	s.pendingFlush = false
	s.mu.Unlock()

	// Drain: if another flush was requested while we were sending, do it now
	if needsFlush {
		s.flush()
	}
}

// sendFormatted converts assistant markdown to Telegram HTML, appends the
// tool status line (already HTML), and sends the combined message.
// This avoids corrupting the tool status by running it through markdownToTelegramHTML.
func (s *Streamer) sendFormatted(assistantText, toolLine string) {
	var html string

	switch {
	case assistantText == "" && toolLine != "":
		html = "⏳ " + toolLine
	case assistantText != "" && toolLine != "":
		html = markdownToTelegramHTML(assistantText) + "\n\n" + toolLine
	case assistantText == "":
		html = "⏳ <i>Thinking...</i>"
	default:
		html = markdownToTelegramHTML(assistantText)
	}

	if html == "" {
		return
	}

	// Dedup: skip if identical to last sent (avoids pointless API calls)
	// Anti-regression: skip if assistant text got shorter (openclaw pattern —
	// prevents edits that make the message shrink during streaming)
	assistLen := len([]rune(assistantText))
	s.mu.Lock()
	if html == s.lastSentHTML {
		s.mu.Unlock()
		return
	}
	if assistLen < s.lastAssistLen && s.lastAssistLen > 0 {
		s.mu.Unlock()
		return
	}
	s.lastSentHTML = html
	s.lastAssistLen = assistLen
	s.mu.Unlock()

	if len(html) <= maxTelegramMsg {
		s.editCurrent(html)
		return
	}

	// Split respecting HTML tag and entity boundaries
	chunks := chunkHTML(html, maxTelegramMsg)
	if len(chunks) == 0 {
		return
	}

	s.editCurrent(chunks[0])

	for _, chunk := range chunks[1:] {
		msg := tgbotapi.NewMessage(s.chatID, chunk)
		msg.ParseMode = "HTML"
		sent, err := s.bot.Send(msg)
		if err != nil {
			msg2 := tgbotapi.NewMessage(s.chatID, stripHTML(chunk))
			sent, err = s.bot.Send(msg2)
		}
		if err != nil {
			log.Printf("streamer: send overflow: %v", err)
			continue
		}
		s.mu.Lock()
		s.overflow = append(s.overflow, chunks[0])
		s.messageID = sent.MessageID
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.buf.Reset()
	s.buf.WriteString(chunks[len(chunks)-1])
	s.mu.Unlock()
}

// toolStatusLine returns a compact rolling-window tool progress indicator.
// Format: Head(8) → (+10 more) → Checkpoint1 → Checkpoint2 → (+10 more) → ...
func (s *Streamer) toolStatusLine() string {
	if s.toolCount == 0 {
		return ""
	}
	return fmt.Sprintf("🔧 <i>%s</i>", buildToolStatusLine(s.toolHead, s.toolCheckpoints, s.toolCount))
}

// buildToolStatusLine is the pure logic for tool status formatting, separated
// for testability. head has up to 8 items; checkpoints has 2 items per
// completed batch of 10 tools after the head.
func buildToolStatusLine(head, checkpoints []string, total int) string {
	const headSize = 8
	const batchSize = 10

	var b strings.Builder
	b.WriteString(strings.Join(head, " → "))

	if total <= headSize {
		return b.String()
	}

	tail := total - headSize // tools after the head
	cpIdx := 0              // index into checkpoints

	for batch := 0; ; batch++ {
		batchStart := batch * batchSize
		if batchStart >= tail {
			break
		}
		batchEnd := batchStart + batchSize
		if batchEnd > tail {
			batchEnd = tail
		}
		batchLen := batchEnd - batchStart

		// How many non-checkpoint tools to skip in this batch?
		cpInBatch := 0
		if batchLen >= batchSize-1 {
			cpInBatch = 1
		}
		if batchLen >= batchSize {
			cpInBatch = 2
		}
		skipped := batchLen - cpInBatch

		if skipped > 0 {
			b.WriteString(fmt.Sprintf(" → (+%d more)", skipped))
		}
		for i := 0; i < cpInBatch && cpIdx < len(checkpoints); i++ {
			b.WriteString(" → ")
			b.WriteString(checkpoints[cpIdx])
			cpIdx++
		}
	}

	return b.String()
}

func (s *Streamer) editCurrent(text string) {
	if s.messageID == 0 {
		msg := tgbotapi.NewMessage(s.chatID, text)
		msg.ParseMode = "HTML"
		sent, err := s.bot.Send(msg)
		if err != nil {
			msg2 := tgbotapi.NewMessage(s.chatID, stripHTML(text))
			sent, err = s.bot.Send(msg2)
		}
		if err != nil {
			log.Printf("streamer: initial send: %v", err)
			return
		}
		s.mu.Lock()
		s.messageID = sent.MessageID
		s.mu.Unlock()
		return
	}

	edit := tgbotapi.NewEditMessageText(s.chatID, s.messageID, text)
	edit.ParseMode = "HTML"
	_, err := s.bot.Send(edit)
	if err != nil {
		edit2 := tgbotapi.NewEditMessageText(s.chatID, s.messageID, stripHTML(text))
		edit2.ParseMode = ""
		_, _ = s.bot.Send(edit2)
	}
}

// Finalize stops the ticker and sends the final clean response (no tool status).
func (s *Streamer) Finalize() {
	s.ticker.Stop()

	// Wait for any in-flight flush to complete
	for {
		s.mu.Lock()
		if !s.inflight {
			s.mu.Unlock()
			break
		}
		s.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}

	// Flush any buffered partial thinking tag as text
	s.mu.Lock()
	if s.thinkState.tagBuf != "" {
		if s.thinkState.inside {
			s.reasonBuf.WriteString(s.thinkState.tagBuf)
		} else {
			s.buf.WriteString(s.thinkState.tagBuf)
		}
		s.thinkState.tagBuf = ""
	}
	finalText := strings.TrimSpace(s.buf.String())
	reasonText := strings.TrimSpace(s.reasonBuf.String())
	hadTools := s.toolCount > 0
	s.mu.Unlock()

	// Final flush of reasoning lane
	if reasonText != "" {
		s.flushReasonLane(reasonText)
	}

	// Re-render answer without tool status line for clean final message.
	// If Claude was interrupted mid-tool-calls with no assistant text,
	// send a notice so the user isn't stuck on a stale tool progress line.
	if finalText != "" {
		s.sendFormatted(finalText, "")
	} else if hadTools {
		s.editCurrent("⚠️ <i>Task interrupted — no final response received.</i>")
	}

	close(s.done)
	s.wg.Wait()
}

// SendPhoto sends an image file to Telegram (for screenshots).
func (s *Streamer) SendPhoto(path string, caption string) {
	photo := tgbotapi.NewPhoto(s.chatID, tgbotapi.FilePath(path))
	photo.Caption = caption
	if _, err := s.bot.Send(photo); err != nil {
		log.Printf("streamer: send photo %s: %v", path, err)
	}
}

// SendRaw sends a new message (not an edit).
func (s *Streamer) SendRaw(text string, markup *tgbotapi.InlineKeyboardMarkup) (int, error) {
	html := markdownToTelegramHTML(text)
	msg := tgbotapi.NewMessage(s.chatID, html)
	msg.ParseMode = "HTML"
	if markup != nil {
		msg.ReplyMarkup = markup
	}
	sent, err := s.bot.Send(msg)
	if err != nil {
		msg := tgbotapi.NewMessage(s.chatID, stripHTML(html))
		msg.ReplyMarkup = markup
		sent, err = s.bot.Send(msg)
	}
	return sent.MessageID, err
}

func (s *Streamer) MessageID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messageID
}

func (s *Streamer) CurrentText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// --- Reasoning lane (openclaw dual-lane pattern) ---

// flushReasonLane updates the reasoning lane message with thinking content.
// On first call, it takes over the placeholder message (⏳ Thinking...).
// The answer lane then creates a new message when it has content.
func (s *Streamer) flushReasonLane(text string) {
	html := "💭 <i>" + markdownToTelegramHTML(text) + "</i>"
	if len(html) > maxTelegramMsg {
		runes := []rune(html)
		if len(runes) > maxTelegramMsg-20 {
			html = string(runes[:maxTelegramMsg-20]) + "...</i>"
		}
	}

	s.mu.Lock()
	msgID := s.reasonMsgID

	// First reasoning flush: take over the placeholder message
	if msgID == 0 && s.messageID != 0 {
		s.reasonMsgID = s.messageID
		s.messageID = 0 // answer lane will create a new message
		msgID = s.reasonMsgID
	}
	s.mu.Unlock()

	if msgID == 0 {
		// No placeholder — send new message
		msg := tgbotapi.NewMessage(s.chatID, html)
		msg.ParseMode = "HTML"
		sent, err := s.bot.Send(msg)
		if err != nil {
			msg2 := tgbotapi.NewMessage(s.chatID, stripHTML(html))
			sent, err = s.bot.Send(msg2)
		}
		if err != nil {
			log.Printf("streamer: reason lane send: %v", err)
			return
		}
		s.mu.Lock()
		s.reasonMsgID = sent.MessageID
		s.mu.Unlock()
		return
	}

	edit := tgbotapi.NewEditMessageText(s.chatID, msgID, html)
	edit.ParseMode = "HTML"
	if _, err := s.bot.Send(edit); err != nil {
		edit2 := tgbotapi.NewEditMessageText(s.chatID, msgID, stripHTML(html))
		edit2.ParseMode = ""
		_, _ = s.bot.Send(edit2)
	}
}

// --- Thinking tag parser (streaming state machine) ---

// thinkingState tracks whether we're inside a thinking block during streaming.
// Handles thinking tags that may be split across Write() chunks.
type thinkingState struct {
	inside bool   // currently inside a thinking block
	tagBuf string // accumulates characters of a potential partial tag
}

var (
	thinkOpenTags  = []string{"<thinking>", "<think>", "<thought>"}
	thinkCloseTags = []string{"</thinking>", "</think>", "</thought>"}
)

// processChunk splits a text chunk into thinking and answer portions.
func (t *thinkingState) processChunk(chunk string) (thinking, answer string) {
	var thinkBuf, answerBuf strings.Builder

	// Prepend any buffered partial tag from previous chunk
	input := t.tagBuf + chunk
	t.tagBuf = ""

	i := 0
	for i < len(input) {
		if input[i] == '<' {
			remaining := input[i:]

			// Try to match complete thinking tags
			if matched, tag := matchThinkTag(remaining, thinkOpenTags); matched {
				t.inside = true
				i += len(tag)
				continue
			}
			if matched, tag := matchThinkTag(remaining, thinkCloseTags); matched {
				t.inside = false
				i += len(tag)
				continue
			}

			// Check if this could be a partial thinking tag (split across chunks)
			if couldBeThinkTag(remaining) {
				t.tagBuf = remaining
				break
			}
		}

		if t.inside {
			thinkBuf.WriteByte(input[i])
		} else {
			answerBuf.WriteByte(input[i])
		}
		i++
	}

	return thinkBuf.String(), answerBuf.String()
}

func matchThinkTag(s string, tags []string) (bool, string) {
	for _, tag := range tags {
		if strings.HasPrefix(s, tag) {
			return true, tag
		}
	}
	return false, ""
}

func couldBeThinkTag(s string) bool {
	allTags := append(thinkOpenTags, thinkCloseTags...)
	for _, tag := range allTags {
		if len(s) < len(tag) && strings.HasPrefix(tag, s) {
			return true
		}
	}
	return false
}

// --- Content filtering ---

var (
	toolCallRe = regexp.MustCompile(`\[Tool (?:Call|Result)[^\]]*\]`)
	specialRe  = regexp.MustCompile(`<\|[^|]*\|>`)
)



func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "...(truncated)"
}

// FormatToolCall — kept for transcript logging, NOT displayed to user.
func FormatToolCall(name string, input string) string {
	return fmt.Sprintf("[tool_call: %s] %s", name, truncate(input, 200))
}

// FormatToolResult — kept for transcript logging, NOT displayed to user.
func FormatToolResult(name string, result string, isError bool) string {
	icon := "ok"
	if isError {
		icon = "error"
	}
	return fmt.Sprintf("[tool_result: %s %s] %s", name, icon, truncate(result, 200))
}
