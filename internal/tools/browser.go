package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/ngocp/goterm-control/internal/browser"
)

const browserScreenshotPath = "/tmp/browser-screenshot.png"

// BrowserTool provides browser automation via native CDP over WebSocket.
// Chrome is launched lazily on first use.
type BrowserTool struct {
	chrome *browser.Chrome
	mu     sync.Mutex
}

// EnsureChrome lazily launches Chrome if not already running.
func (b *BrowserTool) EnsureChrome(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.chrome != nil && b.chrome.IsReachable() {
		return nil
	}
	c, err := browser.Launch(ctx)
	if err != nil {
		return err
	}
	b.chrome = c
	return nil
}

// Shutdown stops the Chrome process.
func (b *BrowserTool) Shutdown() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.chrome != nil {
		b.chrome.Stop()
		b.chrome = nil
	}
}

// --- Navigate ---

type browserNavigateInput struct {
	URL string `json:"url"`
}

func (b *BrowserTool) Navigate(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserNavigateInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	if err := b.chrome.Navigate(ctx, inp.URL); err != nil {
		return fmt.Sprintf("Navigation failed: %v", err), nil
	}
	return fmt.Sprintf("Navigated to %s", inp.URL), nil
}

// --- Snapshot ---

type browserSnapshotInput struct {
	Selector string `json:"selector"`
}

func (b *BrowserTool) Snapshot(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserSnapshotInput
	_ = json.Unmarshal(raw, &inp)
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	out, err := b.chrome.SnapshotDOM(ctx, inp.Selector)
	if err != nil {
		return fmt.Sprintf("Snapshot failed: %v", err), nil
	}
	return out, nil
}

// --- Click ---

type browserClickInput struct {
	Ref string `json:"ref"`
}

func (b *BrowserTool) Click(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserClickInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	if err := b.chrome.Click(ctx, inp.Ref); err != nil {
		return fmt.Sprintf("Click failed: %v", err), nil
	}
	return fmt.Sprintf("Clicked %s", inp.Ref), nil
}

// --- Fill ---

type browserFillInput struct {
	Ref  string `json:"ref"`
	Text string `json:"text"`
}

func (b *BrowserTool) Fill(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserFillInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	if err := b.chrome.Fill(ctx, inp.Ref, inp.Text); err != nil {
		return fmt.Sprintf("Fill failed: %v", err), nil
	}
	return fmt.Sprintf("Filled %s with %q", inp.Ref, inp.Text), nil
}

// --- Type ---

type browserTypeInput struct {
	Ref  string `json:"ref"`
	Text string `json:"text"`
}

func (b *BrowserTool) Type(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserTypeInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	if err := b.chrome.TypeText(ctx, inp.Ref, inp.Text); err != nil {
		return fmt.Sprintf("Type failed: %v", err), nil
	}
	return fmt.Sprintf("Typed %q into %s", inp.Text, inp.Ref), nil
}

// --- Select ---

type browserSelectInput struct {
	Ref   string `json:"ref"`
	Value string `json:"value"`
}

func (b *BrowserTool) Select(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserSelectInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	if err := b.chrome.SelectOption(ctx, inp.Ref, inp.Value); err != nil {
		return fmt.Sprintf("Select failed: %v", err), nil
	}
	return fmt.Sprintf("Selected %q in %s", inp.Value, inp.Ref), nil
}

// --- Scroll ---

type browserScrollInput struct {
	Direction string `json:"direction"`
	Pixels    int    `json:"pixels"`
}

func (b *BrowserTool) Scroll(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserScrollInput
	_ = json.Unmarshal(raw, &inp)
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	dir := inp.Direction
	if dir == "" {
		dir = "down"
	}
	if err := b.chrome.Scroll(ctx, dir, inp.Pixels); err != nil {
		return fmt.Sprintf("Scroll failed: %v", err), nil
	}
	return fmt.Sprintf("Scrolled %s", dir), nil
}

// --- Screenshot ---

type browserScreenshotInput struct {
	Path string `json:"path"`
}

func (b *BrowserTool) Screenshot(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserScreenshotInput
	_ = json.Unmarshal(raw, &inp)
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}

	path := inp.Path
	if path == "" {
		path = browserScreenshotPath
	}

	data, err := b.chrome.CaptureScreenshot(ctx, true)
	if err != nil {
		return fmt.Sprintf("Screenshot failed: %v", err), nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Sprintf("Screenshot write failed: %v", err), nil
	}
	return "SCREENSHOT:" + path, nil
}

// --- GetText ---

type browserGetTextInput struct {
	Ref      string `json:"ref"`
	Property string `json:"property"`
}

func (b *BrowserTool) GetText(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserGetTextInput
	_ = json.Unmarshal(raw, &inp)
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	out, err := b.chrome.GetText(ctx, inp.Ref, inp.Property)
	if err != nil {
		return fmt.Sprintf("Get %s failed: %v", inp.Property, err), nil
	}
	return out, nil
}

// --- Eval ---

type browserEvalInput struct {
	Expression string `json:"expression"`
}

func (b *BrowserTool) Eval(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserEvalInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	out, err := b.chrome.EvalJS(ctx, inp.Expression)
	if err != nil {
		return fmt.Sprintf("Eval failed: %v", err), nil
	}
	return out, nil
}

// --- Wait ---

type browserWaitInput struct {
	Ref  string `json:"ref"`
	Text string `json:"text"`
	Ms   int    `json:"ms"`
}

func (b *BrowserTool) Wait(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserWaitInput
	_ = json.Unmarshal(raw, &inp)
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	if err := b.chrome.WaitFor(ctx, inp.Ref, inp.Text, inp.Ms); err != nil {
		return fmt.Sprintf("Wait failed: %v", err), nil
	}
	return "Wait completed", nil
}

// --- Back ---

func (b *BrowserTool) Back(ctx context.Context, _ json.RawMessage) (string, error) {
	if err := b.EnsureChrome(ctx); err != nil {
		return "", err
	}
	if err := b.chrome.GoBack(ctx); err != nil {
		return fmt.Sprintf("Back failed: %v", err), nil
	}
	return "Navigated back", nil
}
