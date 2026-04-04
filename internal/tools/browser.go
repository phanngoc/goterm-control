package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

const (
	browserBin            = "agent-browser"
	browserScreenshotPath = "/tmp/browser-screenshot.png"
)

// BrowserTool wraps the agent-browser CLI for browser automation via CDP.
// See: https://github.com/vercel-labs/agent-browser
type BrowserTool struct{}

// run executes an agent-browser command and returns stdout.
func (b *BrowserTool) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, browserBin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return "", fmt.Errorf("%s", errMsg)
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
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
	out, err := b.run(ctx, "open", inp.URL)
	if err != nil {
		return fmt.Sprintf("Navigation failed: %v", err), nil
	}
	if out == "" {
		return fmt.Sprintf("Navigated to %s", inp.URL), nil
	}
	return out, nil
}

// --- Snapshot ---

type browserSnapshotInput struct {
	Selector    string `json:"selector"`
	Interactive bool   `json:"interactive"`
}

func (b *BrowserTool) Snapshot(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserSnapshotInput
	_ = json.Unmarshal(raw, &inp)

	args := []string{"snapshot", "-i", "-c"}
	if inp.Selector != "" {
		args = append(args, "-s", inp.Selector)
	}

	out, err := b.run(ctx, args...)
	if err != nil {
		return fmt.Sprintf("Snapshot failed: %v", err), nil
	}
	return out, nil
}

// --- Click ---

type browserClickInput struct {
	Ref    string `json:"ref"`
	NewTab bool   `json:"new_tab"`
}

func (b *BrowserTool) Click(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserClickInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	args := []string{"click", inp.Ref}
	if inp.NewTab {
		args = append(args, "--new-tab")
	}
	out, err := b.run(ctx, args...)
	if err != nil {
		return fmt.Sprintf("Click failed: %v", err), nil
	}
	if out == "" {
		return fmt.Sprintf("Clicked %s", inp.Ref), nil
	}
	return out, nil
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
	out, err := b.run(ctx, "fill", inp.Ref, inp.Text)
	if err != nil {
		return fmt.Sprintf("Fill failed: %v", err), nil
	}
	if out == "" {
		return fmt.Sprintf("Filled %s with %q", inp.Ref, inp.Text), nil
	}
	return out, nil
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
	out, err := b.run(ctx, "type", inp.Ref, inp.Text)
	if err != nil {
		return fmt.Sprintf("Type failed: %v", err), nil
	}
	if out == "" {
		return fmt.Sprintf("Typed %q into %s", inp.Text, inp.Ref), nil
	}
	return out, nil
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
	out, err := b.run(ctx, "select", inp.Ref, inp.Value)
	if err != nil {
		return fmt.Sprintf("Select failed: %v", err), nil
	}
	if out == "" {
		return fmt.Sprintf("Selected %q in %s", inp.Value, inp.Ref), nil
	}
	return out, nil
}

// --- Scroll ---

type browserScrollInput struct {
	Direction string `json:"direction"` // up, down, left, right
	Pixels    int    `json:"pixels"`
}

func (b *BrowserTool) Scroll(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserScrollInput
	_ = json.Unmarshal(raw, &inp)

	dir := inp.Direction
	if dir == "" {
		dir = "down"
	}
	args := []string{"scroll", dir}
	if inp.Pixels > 0 {
		args = append(args, strconv.Itoa(inp.Pixels))
	}

	out, err := b.run(ctx, args...)
	if err != nil {
		return fmt.Sprintf("Scroll failed: %v", err), nil
	}
	if out == "" {
		return fmt.Sprintf("Scrolled %s", dir), nil
	}
	return out, nil
}

// --- Screenshot ---

type browserScreenshotInput struct {
	Path string `json:"path"`
}

func (b *BrowserTool) Screenshot(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserScreenshotInput
	_ = json.Unmarshal(raw, &inp)

	path := inp.Path
	if path == "" {
		path = browserScreenshotPath
	}

	_, err := b.run(ctx, "screenshot", path)
	if err != nil {
		return fmt.Sprintf("Screenshot failed: %v", err), nil
	}
	return "SCREENSHOT:" + path, nil
}

// --- GetText ---

type browserGetTextInput struct {
	Ref      string `json:"ref"`
	Property string `json:"property"` // text, html, value, title, url
}

func (b *BrowserTool) GetText(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browserGetTextInput
	_ = json.Unmarshal(raw, &inp)

	prop := inp.Property
	if prop == "" {
		prop = "text"
	}

	args := []string{"get", prop}
	if inp.Ref != "" {
		args = append(args, inp.Ref)
	}

	out, err := b.run(ctx, args...)
	if err != nil {
		return fmt.Sprintf("Get %s failed: %v", prop, err), nil
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
	out, err := b.run(ctx, "eval", inp.Expression)
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

	var args []string
	switch {
	case inp.Ref != "":
		args = []string{"wait", inp.Ref}
	case inp.Text != "":
		args = []string{"wait", "--text", inp.Text}
	case inp.Ms > 0:
		args = []string{"wait", strconv.Itoa(inp.Ms)}
	default:
		args = []string{"wait", "1000"}
	}

	out, err := b.run(ctx, args...)
	if err != nil {
		return fmt.Sprintf("Wait failed: %v", err), nil
	}
	if out == "" {
		return "Wait completed", nil
	}
	return out, nil
}

// --- Back ---

func (b *BrowserTool) Back(ctx context.Context, _ json.RawMessage) (string, error) {
	out, err := b.run(ctx, "back")
	if err != nil {
		return fmt.Sprintf("Back failed: %v", err), nil
	}
	if out == "" {
		return "Navigated back", nil
	}
	return out, nil
}
