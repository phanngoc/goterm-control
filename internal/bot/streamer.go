package bot

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	maxTelegramMsg  = 4000 // Telegram limit is 4096, leave room for formatting
	streamInterval  = 500 * time.Millisecond
)

// Streamer manages real-time streaming of text to a Telegram message.
// It throttles edits to avoid Telegram rate limits (30 edits/sec global).
type Streamer struct {
	bot       *tgbotapi.BotAPI
	chatID    int64
	messageID int // current message being edited

	mu     sync.Mutex
	buf    strings.Builder
	dirty  bool // buf changed since last edit

	ticker *time.Ticker
	done   chan struct{}
	wg     sync.WaitGroup

	// For multi-message overflow
	overflow []string // completed messages
}

// NewStreamer creates a streamer that edits messageID in chatID.
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

// Write appends text to the buffer. Safe for concurrent use.
func (s *Streamer) Write(chunk string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buf.WriteString(chunk)
	s.dirty = true
}

// Append adds a formatted section (e.g. tool notice) immediately.
func (s *Streamer) Append(text string) {
	s.mu.Lock()
	s.buf.WriteString(text)
	s.dirty = true
	s.mu.Unlock()
	s.flush()
}

// Flush forces an immediate edit of the Telegram message with current buffer content.
func (s *Streamer) Flush() {
	s.flush()
}

// flush is the internal implementation.
func (s *Streamer) flush() {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return
	}
	text := s.buf.String()
	s.dirty = false
	s.mu.Unlock()

	s.sendText(text)
}

// sendText edits (or sends) the message with the given text, splitting if needed.
func (s *Streamer) sendText(text string) {
	if text == "" {
		return
	}

	// Check if we need to split
	if utf8.RuneCountInString(text) <= maxTelegramMsg {
		s.editCurrent(text)
		return
	}

	// Text is too long — finalize current message and start a new one
	// Find a good split point
	runes := []rune(text)
	head := string(runes[:maxTelegramMsg])
	tail := string(runes[maxTelegramMsg:])

	s.editCurrent(head + "\n_(continued...)_")

	// Send new message for overflow
	msg := tgbotapi.NewMessage(s.chatID, "_(continued)_\n"+tail)
	msg.ParseMode = "Markdown"
	sent, err := s.bot.Send(msg)
	if err != nil {
		// Retry without markdown
		msg2 := tgbotapi.NewMessage(s.chatID, "(continued)\n"+tail)
		sent, err = s.bot.Send(msg2)
	}
	if err != nil {
		log.Printf("streamer: send overflow: %v", err)
		return
	}

	s.mu.Lock()
	s.overflow = append(s.overflow, head)
	s.messageID = sent.MessageID
	// Reset buffer to just the tail
	s.buf.Reset()
	s.buf.WriteString(tail)
	s.mu.Unlock()
}

func (s *Streamer) editCurrent(text string) {
	if s.messageID == 0 {
		msg := tgbotapi.NewMessage(s.chatID, text)
		msg.ParseMode = "Markdown"
		sent, err := s.bot.Send(msg)
		if err != nil {
			msg2 := tgbotapi.NewMessage(s.chatID, text)
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
		// Retry without markdown (often fails with unclosed backticks mid-stream)
		edit2 := tgbotapi.NewEditMessageText(s.chatID, s.messageID, stripMarkdown(text))
		edit2.ParseMode = ""
		_, _ = s.bot.Send(edit2)
	}
}

// Finalize stops the ticker and sends the final state.
func (s *Streamer) Finalize() {
	s.ticker.Stop()
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

// SendRaw sends a new message (not an edit) — used for tool approval keyboards.
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

// MessageID returns the current Telegram message ID being streamed.
func (s *Streamer) MessageID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.messageID
}

// CurrentText returns the current buffer content.
func (s *Streamer) CurrentText() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// stripMarkdown removes common Markdown so Telegram doesn't reject mid-stream broken syntax.
func stripMarkdown(s string) string {
	r := strings.NewReplacer(
		"```", "",
		"`", "",
		"**", "",
		"__", "",
		"*", "",
		"_", "",
	)
	return r.Replace(s)
}

// FormatToolCall formats a tool call announcement for display.
func FormatToolCall(name string, input string) string {
	return fmt.Sprintf("\n\n🔧 *%s*\n```\n%s\n```\n", name, truncate(input, 300))
}

// FormatToolResult formats a tool result for display.
func FormatToolResult(name string, result string, isError bool) string {
	icon := "✅"
	if isError {
		icon = "❌"
	}
	return fmt.Sprintf("\n%s *%s result:*\n```\n%s\n```\n", icon, name, truncate(result, 800))
}

func truncate(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "\n...(truncated)"
}
