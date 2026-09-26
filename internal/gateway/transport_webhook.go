package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

// A room speaking into an HTTP endpoint.
//
// This is the cheapest transport there is — no token to mint, no library, no
// poller — which is exactly why it is the second one: if the seam only ever
// held Telegram it would not be a seam, it would be a rename.
//
// The body carries the line twice on purpose. `text` is what a Slack incoming
// webhook reads and `content` is what a Discord one reads, so either URL can be
// pasted in and works with no adapter at all. The remaining fields are for
// anything else: they are the line as data, not as a sentence.

// webhookTimeout is generous for a POST and short enough that a hung endpoint
// does not hold a sweep. A failure here leaves the line unsettled, so the cost
// of timing out is one more attempt, not a lost message.
const webhookTimeout = 10 * time.Second

// webhookErrBody is how much of a rejection is worth quoting into the log.
// Enough to see "invalid_token"; not so much that an HTML error page fills the
// file.
const webhookErrBody = 200

type webhookTransport struct{ client *http.Client }

// NewWebhookTransport returns a transport every gateway process can register:
// posting JSON needs no bot and no poller, unlike Telegram.
func NewWebhookTransport() ChannelTransport {
	return webhookTransport{client: &http.Client{Timeout: webhookTimeout}}
}

func (webhookTransport) Kind() string { return coord.GatewayWebhook }

type webhookPayload struct {
	Text       string `json:"text"`    // Slack reads this
	Content    string `json:"content"` // Discord reads this
	ChannelID  string `json:"channel_id"`
	Channel    string `json:"channel"`
	MessageID  string `json:"message_id"`
	ThreadRoot string `json:"thread_root,omitempty"`
	Author     string `json:"author"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
}

func (w webhookTransport) Deliver(f coord.Forward, text string) (string, error) {
	payload, err := json.Marshal(webhookPayload{
		Text: text, Content: text,
		ChannelID: f.ChannelID, Channel: f.ChannelName,
		MessageID: f.MessageID, ThreadRoot: f.ThreadRoot,
		Author: f.AuthorID, Body: f.Body,
		CreatedAt: f.CreatedAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, f.Target, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("webhook %s: %w", f.GatewayID, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if f.Secret != "" {
		req.Header.Set("Authorization", "Bearer "+f.Secret)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, webhookErrBody))
		return "", fmt.Errorf("webhook %s: %s: %s", f.Target, resp.Status, bytes.TrimSpace(snippet))
	}
	// Nothing to hand back. A webhook is one-way: there is no id for a reply to
	// quote, so no reply can ever find its way home through it.
	return "", nil
}
