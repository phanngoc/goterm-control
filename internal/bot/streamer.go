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

	// Coalescing: hold first send until meaningful content (openclaw minInitialChars)
	initialSent bool

	// Dedup and anti-regression guards (openclaw pattern)
	lastSentHTML  string
	lastAssistLen int

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
	s.buf.WriteString(chunk)
	s.dirty = true
	bufLen := len([]rune(s.buf.String()))
	s.mu.Unlock()

	// Force flush at maxCoalesceChars to prevent excessive buffering
	if bufLen >= maxCoalesceChars {
		s.flush()
	}
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
	assistantText := strings.TrimSpace(s.buf.String())
	toolLine := s.toolStatusLine()

	// Hold first send until minInitialChars for meaningful push notification
	if !s.initialSent && len([]rune(assistantText)) < minInitialChars && toolLine == "" {
		s.mu.Unlock()
		return
	}

	s.dirty = false
	s.mu.Unlock()

	s.sendFormatted(assistantText, toolLine)

	s.mu.Lock()
	s.initialSent = true
	s.mu.Unlock()
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

// toolStatusLine returns a compact tool progress indicator.
func (s *Streamer) toolStatusLine() string {
	if s.toolCount == 0 {
		return ""
	}
	names := strings.Join(s.toolNames, " → ")
	if s.toolCount > len(s.toolNames) {
		names += fmt.Sprintf(" (+%d more)", s.toolCount-len(s.toolNames))
	}
	return fmt.Sprintf("🔧 <i>%s</i>", names)
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

	// Final flush with tool status stripped — show only clean assistant text
	s.mu.Lock()
	finalText := strings.TrimSpace(s.buf.String())
	hasPendingTools := s.toolCount > 0 && finalText != ""
	s.mu.Unlock()

	if hasPendingTools {
		// Re-render without tool status line for clean final message
		s.sendFormatted(finalText, "")
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
