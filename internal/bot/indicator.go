package bot

import (
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/ngocp/goterm-control/internal/config"
)

// NameIndicator changes the bot's display name to show a loading animation
// while processing requests. Tracks active request count so the original name
// is only restored when all concurrent requests finish.
type NameIndicator struct {
	bot *tgbotapi.BotAPI
	cfg config.IndicatorConfig

	mu          sync.Mutex
	baseName    string
	activeCount int
	frameIdx    int
	ticker      *time.Ticker
	stopCh      chan struct{}
	lastSet     time.Time
	minInterval time.Duration
}

// NewNameIndicator creates an indicator. Returns nil if disabled.
func NewNameIndicator(bot *tgbotapi.BotAPI, cfg config.IndicatorConfig) *NameIndicator {
	if !cfg.Enabled {
		return nil
	}

	n := &NameIndicator{
		bot:         bot,
		cfg:         cfg,
		minInterval: 3 * time.Second,
	}

	// Resolve base name
	if cfg.BotName != "" {
		n.baseName = cfg.BotName
	} else {
		n.baseName = fetchBotName(bot)
	}

	// Strip stale emoji prefix (e.g. bot crashed while "thinking")
	for _, frame := range cfg.Frames {
		if strings.HasPrefix(n.baseName, frame+" ") {
			n.baseName = strings.TrimPrefix(n.baseName, frame+" ")
			break
		}
	}

	if n.baseName == "" {
		n.baseName = bot.Self.UserName
	}

	log.Printf("indicator: enabled, base name=%q, frames=%v, interval=%ds", n.baseName, cfg.Frames, cfg.Interval)
	return n
}

// fetchBotName calls getMyName to get the current display name.
func fetchBotName(bot *tgbotapi.BotAPI) string {
	resp, err := bot.MakeRequest("getMyName", nil)
	if err != nil {
		log.Printf("indicator: getMyName error: %v", err)
		return ""
	}
	var result struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		log.Printf("indicator: getMyName parse error: %v", err)
		return ""
	}
	return result.Name
}

// Start signals that a request has begun processing.
func (n *NameIndicator) Start() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.activeCount++
	shouldStart := n.activeCount == 1
	n.mu.Unlock()

	if shouldStart {
		n.setThinkingName()
		n.startAnimation()
	}
}

// Done signals that a request has finished processing.
func (n *NameIndicator) Done() {
	if n == nil {
		return
	}
	n.mu.Lock()
	n.activeCount--
	if n.activeCount < 0 {
		n.activeCount = 0
	}
	shouldRestore := n.activeCount == 0
	n.mu.Unlock()

	if shouldRestore {
		n.stopAnimation()
		n.restoreName()
	}
}

// Close stops the animation and restores the original name.
func (n *NameIndicator) Close() {
	if n == nil {
		return
	}
	n.stopAnimation()
	n.restoreName()
}

func (n *NameIndicator) startAnimation() {
	if len(n.cfg.Frames) < 2 {
		return // no animation needed with single frame
	}

	interval := time.Duration(n.cfg.Interval) * time.Second
	n.mu.Lock()
	n.frameIdx = 0
	n.ticker = time.NewTicker(interval)
	n.stopCh = make(chan struct{})
	n.mu.Unlock()

	go func() {
		for {
			select {
			case <-n.ticker.C:
				n.advanceFrame()
			case <-n.stopCh:
				return
			}
		}
	}()
}

func (n *NameIndicator) stopAnimation() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.ticker != nil {
		n.ticker.Stop()
		close(n.stopCh)
		n.ticker = nil
	}
}

func (n *NameIndicator) advanceFrame() {
	n.mu.Lock()
	n.frameIdx = (n.frameIdx + 1) % len(n.cfg.Frames)
	n.mu.Unlock()
	n.setThinkingName()
}

func (n *NameIndicator) setThinkingName() {
	n.mu.Lock()
	frame := n.cfg.Frames[n.frameIdx]
	name := frame + " " + n.baseName
	n.mu.Unlock()
	n.setName(name)
}

func (n *NameIndicator) restoreName() {
	n.setName(n.baseName)
}

func (n *NameIndicator) setName(name string) {
	n.mu.Lock()
	elapsed := time.Since(n.lastSet)
	if elapsed < n.minInterval {
		wait := n.minInterval - elapsed
		n.mu.Unlock()
		time.Sleep(wait)
		n.mu.Lock()
	}
	n.lastSet = time.Now()
	n.mu.Unlock()

	if _, err := n.bot.MakeRequest("setMyName", tgbotapi.Params{"name": name}); err != nil {
		log.Printf("indicator: setMyName(%q) error: %v", name, err)
	}
}
