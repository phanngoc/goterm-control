package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ngocp/goterm-control/internal/coord"
)

func hookLine() coord.Forward {
	return coord.Forward{
		MessageID: "cm_1", ChannelID: "ch_trading", ChannelName: "trading",
		ThreadRoot: "cm_0", AuthorID: "bomclaw2", Body: "BTC 64k",
		GatewayID: "cg_1", Kind: coord.GatewayWebhook, Mode: coord.ForwardAll,
		CreatedAt: time.Now(),
	}
}

// The body carries the line twice so a Slack URL and a Discord URL both work
// with no adapter: they read different field names for the same thing.
func TestWebhookPostsTheLineAndHasNoIDToGiveBack(t *testing.T) {
	var got map[string]any
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("not JSON: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	f := hookLine()
	f.Target = srv.URL
	f.Secret = "s3cret"
	ext, err := NewWebhookTransport().Deliver(f, ForwardLine(f))
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if ext != "" {
		t.Errorf("a webhook has no message id for a reply to quote, got %q", ext)
	}
	if auth != "Bearer s3cret" {
		t.Errorf("secret not sent: %q", auth)
	}
	if got["text"] == "" || got["text"] != got["content"] {
		t.Errorf("Slack reads text and Discord reads content; one of them got nothing: %v", got)
	}
	if got["author"] != "bomclaw2" || got["body"] != "BTC 64k" || got["channel"] != "trading" {
		t.Errorf("the line is not in the body as data, only as a sentence: %v", got)
	}
}

func TestARefusedWebhookLeavesTheLineUnsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid_token", http.StatusForbidden)
	}))
	defer srv.Close()

	f := hookLine()
	f.Target = srv.URL
	if _, err := NewWebhookTransport().Deliver(f, ForwardLine(f)); err == nil {
		t.Fatal("a rejected POST reported success; the line would be marked delivered and lost")
	} else if !strings.Contains(err.Error(), "invalid_token") {
		t.Errorf("the error does not say why it was refused: %v", err)
	}
}
