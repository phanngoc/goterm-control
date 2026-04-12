package bot

import (
	"log"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/ngocp/goterm-control/internal/config"
)

// TypingIndicator sends Telegram "typing..." chat actions on a per-chat
// keepalive loop. Telegram's typing indicator expires after ~5 seconds,
// so we re-send every interval (default 4s) to keep it visible.
type TypingIndicator struct {
	bot      *tgbotapi.BotAPI
	interval time.Duration
	ttl      time.Duration

	mu     sync.Mutex
	active map[int64]chan struct{} // chatID → stop channel
}

// NewTypingIndicator creates a typing indicator. Returns nil if disabled.
// execTTL is the execution timeout — typing TTL will be at least this long
// so the indicator stays visible for the full duration of a request.
func NewTypingIndicator(bot *tgbotapi.BotAPI, cfg config.IndicatorConfig, execTTL time.Duration) *TypingIndicator {
	if !cfg.UseChatAction {
		return nil
	}
	interval := time.Duration(cfg.ChatActionInterval) * time.Second
	ttl := execTTL
	if cfg.ChatActionTTL > 0 {
		cfgTTL := time.Duration(cfg.ChatActionTTL) * time.Second
		if cfgTTL > ttl {
			ttl = cfgTTL
		}
	}
	log.Printf("typing: enabled, interval=%s, ttl=%s", interval, ttl)
	return &TypingIndicator{
		bot:      bot,
		interval: interval,
		ttl:      ttl,
		active:   make(map[int64]chan struct{}),
	}
}

// Start begins sending typing actions for the given chat.
// If a loop is already active for this chat, the old one is stopped first.
func (t *TypingIndicator) Start(chatID int64) {
	if t == nil {
		return
	}

	t.mu.Lock()
	// Stop existing loop for this chat (duplicate guard)
	if stopCh, ok := t.active[chatID]; ok {
		close(stopCh)
		delete(t.active, chatID)
	}
	stopCh := make(chan struct{})
	t.active[chatID] = stopCh
	t.mu.Unlock()

	go t.loop(chatID, stopCh)
}

// Stop cancels the typing loop for the given chat.
func (t *TypingIndicator) Stop(chatID int64) {
	if t == nil {
		return
	}

	t.mu.Lock()
	if stopCh, ok := t.active[chatID]; ok {
		close(stopCh)
		delete(t.active, chatID)
	}
	t.mu.Unlock()
}

// Close stops all active typing loops (used during shutdown).
func (t *TypingIndicator) Close() {
	if t == nil {
		return
	}

	t.mu.Lock()
	for chatID, stopCh := range t.active {
		close(stopCh)
		delete(t.active, chatID)
	}
	t.mu.Unlock()
}

// loop runs the keepalive loop: sends typing action immediately, then every
// interval until stopped or TTL expires.
func (t *TypingIndicator) loop(chatID int64, stopCh chan struct{}) {
	// Send immediately on start
	t.sendTyping(chatID)

	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()

	ttlTimer := time.NewTimer(t.ttl)
	defer ttlTimer.Stop()

	var consecutiveErrors int
	const maxErrors = 3

	for {
		select {
		case <-stopCh:
			return
		case <-ttlTimer.C:
			log.Printf("typing: TTL expired for chat %d", chatID)
			t.mu.Lock()
			delete(t.active, chatID)
			t.mu.Unlock()
			return
		case <-ticker.C:
			if err := t.sendTyping(chatID); err != nil {
				consecutiveErrors++
				if consecutiveErrors >= maxErrors {
					log.Printf("typing: circuit breaker for chat %d after %d errors", chatID, consecutiveErrors)
					t.mu.Lock()
					delete(t.active, chatID)
					t.mu.Unlock()
					return
				}
			} else {
				consecutiveErrors = 0
			}
		}
	}
}

// sendTyping sends a single "typing" chat action to Telegram.
func (t *TypingIndicator) sendTyping(chatID int64) error {
	if t.bot == nil {
		return nil
	}
	action := tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)
	_, err := t.bot.Request(action)
	if err != nil {
		log.Printf("typing: sendChatAction error for chat %d: %v", chatID, err)
	}
	return err
}
