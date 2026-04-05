package bot

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	maxTelegramMsg = 4000 // Telegram limit is 4096, leave room for formatting
	streamInterval = 800 * time.Millisecond
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

	// Tool progress tracking — shown as compact status, not full output
	toolNames []string
	toolCount int

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
func (s *Streamer) Write(chunk string) {
	// Filter thinking tags and tool markers from streamed text
	chunk = filterContent(chunk)
	if chunk == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.WriteString(chunk)
	s.dirty = true
}

// NoteTool records a tool call as a compact progress indicator.
// Instead of showing full tool input/output, we show: 🔧 Tool1 → Tool2 → ...
func (s *Streamer) NoteTool(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCount++
	// Keep unique names, max 8 shown
	if len(s.toolNames) < 8 {
		s.toolNames = append(s.toolNames, name)
	}
	s.dirty = true
}

// Flush forces an immediate edit.
func (s *Streamer) Flush() {
	s.flush()
}

func (s *Streamer) flush() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	text := s.renderDisplay()
	s.dirty = false
	s.mu.Unlock()

	s.sendText(text)
}

// renderDisplay builds the display text: assistant text + optional tool status line.
// Must be called with mu held.
func (s *Streamer) renderDisplay() string {
	assistantText := strings.TrimSpace(s.buf.String())

	if assistantText == "" && s.toolCount > 0 {
		// Only tools running, no text yet
		return "⏳ " + s.toolStatusLine()
	}

	if s.toolCount > 0 && assistantText != "" {
		// Text + tool progress
		return assistantText + "\n\n" + s.toolStatusLine()
	}

	if assistantText == "" {
		return "⏳ _Thinking..._"
	}

	return assistantText
}

// toolStatusLine returns a compact tool progress indicator.
func (s *Streamer) toolStatusLine() string {
	if s.toolCount == 0 {
		return ""
	}
	names := strings.Join(s.toolNames, " → ")
	if s.toolCount > len(s.toolNames) {
		names += fmt.Sprintf(" (+%d more)", s.toolCount-len(s.toolNames))
	}
	return fmt.Sprintf("🔧 _%s_", names)
}

func (s *Streamer) sendText(text string) {
	if text == "" {
		return
	}

	if utf8.RuneCountInString(text) <= maxTelegramMsg {
		s.editCurrent(text)
		return
	}

	// Smart split: respect paragraph boundaries and code fences
	chunks := smartSplit(text, maxTelegramMsg)
	if len(chunks) == 0 {
		return
	}

	// Edit current message with first chunk
	s.editCurrent(chunks[0])

	// Send remaining chunks as new messages
	for _, chunk := range chunks[1:] {
		msg := tgbotapi.NewMessage(s.chatID, chunk)
		msg.ParseMode = "Markdown"
		sent, err := s.bot.Send(msg)
		if err != nil {
			msg2 := tgbotapi.NewMessage(s.chatID, stripMarkdown(chunk))
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

	// Reset buffer to last chunk only
	s.mu.Lock()
	s.buf.Reset()
	s.buf.WriteString(chunks[len(chunks)-1])
	s.mu.Unlock()
}

func (s *Streamer) editCurrent(text string) {
	if s.messageID == 0 {
		msg := tgbotapi.NewMessage(s.chatID, text)
		msg.ParseMode = "Markdown"
		sent, err := s.bot.Send(msg)
		if err != nil {
			msg2 := tgbotapi.NewMessage(s.chatID, stripMarkdown(text))
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
	edit.ParseMode = "Markdown"
	_, err := s.bot.Send(edit)
	if err != nil {
		edit2 := tgbotapi.NewEditMessageText(s.chatID, s.messageID, stripMarkdown(text))
		edit2.ParseMode = ""
		_, _ = s.bot.Send(edit2)
	}
}

// Finalize stops the ticker and sends the final clean response (no tool status).
func (s *Streamer) Finalize() {
	s.ticker.Stop()

	// Final flush with tool status stripped — show only clean assistant text
	s.mu.Lock()
	finalText := strings.TrimSpace(s.buf.String())
	hasPendingTools := s.toolCount > 0 && finalText != ""
	s.mu.Unlock()

	if hasPendingTools {
		// Re-render without tool status line for clean final message
		s.sendText(finalText)
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
	msg := tgbotapi.NewMessage(s.chatID, text)
	msg.ParseMode = "Markdown"
	if markup != nil {
		msg.ReplyMarkup = markup
	}
	sent, err := s.bot.Send(msg)
	if err != nil {
		msg.ParseMode = ""
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

// --- Content filtering (openclaw pattern) ---

var (
	thinkingRe  = regexp.MustCompile(`(?s)<(?:think|thinking|thought)>.*?</(?:think|thinking|thought)>`)
	toolCallRe  = regexp.MustCompile(`\[Tool (?:Call|Result)[^\]]*\]`)
	specialRe   = regexp.MustCompile(`<\|[^|]*\|>`)
)

// filterContent strips thinking tags, tool markers, and model special tokens.
func filterContent(text string) string {
	text = thinkingRe.ReplaceAllString(text, "")
	text = toolCallRe.ReplaceAllString(text, "")
	text = specialRe.ReplaceAllString(text, "")
	return text
}

// --- Smart chunking ---

// smartSplit splits text at paragraph boundaries (\n\n), respecting code fences.
func smartSplit(text string, limit int) []string {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}
	}

	var chunks []string
	remaining := text

	for utf8.RuneCountInString(remaining) > limit {
		// Find the best split point within the limit
		runes := []rune(remaining)
		cutAt := limit

		// Try splitting at paragraph boundary (\n\n)
		searchRegion := string(runes[:cutAt])
		lastParaBreak := strings.LastIndex(searchRegion, "\n\n")
		if lastParaBreak > limit/4 { // don't split too early
			cutAt = utf8.RuneCountInString(searchRegion[:lastParaBreak])
		} else {
			// Try splitting at single newline
			lastNewline := strings.LastIndex(searchRegion, "\n")
			if lastNewline > limit/4 {
				cutAt = utf8.RuneCountInString(searchRegion[:lastNewline])
			}
		}

		// Check if we're inside a code fence — if so, find the closing fence
		chunk := string(runes[:cutAt])
		if isInsideCodeFence(chunk) {
			// Extend to include the closing fence
			closingIdx := strings.Index(string(runes[cutAt:]), "```")
			if closingIdx >= 0 && closingIdx < limit/2 {
				cutAt += utf8.RuneCountInString(string(runes[cutAt:][:closingIdx+3]))
			}
		}

		chunks = append(chunks, strings.TrimSpace(string(runes[:cutAt])))
		remaining = strings.TrimSpace(string(runes[cutAt:]))
	}

	if remaining != "" {
		chunks = append(chunks, remaining)
	}

	return chunks
}

// isInsideCodeFence checks if text ends with an unclosed ``` block.
func isInsideCodeFence(text string) bool {
	count := strings.Count(text, "```")
	return count%2 != 0 // odd = unclosed
}

// --- Formatting helpers (kept for backward compat but simplified) ---

func stripMarkdown(s string) string {
	r := strings.NewReplacer("```", "", "`", "", "**", "", "__", "", "*", "", "_", "")
	return r.Replace(s)
}

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
