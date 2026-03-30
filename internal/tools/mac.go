package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// MacTool provides Mac-specific operations.
type MacTool struct{}

// Screenshot takes a screenshot and returns the file path.
// The bot layer will detect this and send it as a photo.
const ScreenshotPath = "/tmp/goterm-screenshot.png"

func (m *MacTool) Screenshot(_ context.Context, _ json.RawMessage) (string, error) {
	cmd := exec.Command("screencapture", "-x", ScreenshotPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("screencapture failed: %w\n%s", err, out)
	}
	// Return magic sentinel — the bot detects this and sends the image.
	return "SCREENSHOT:" + ScreenshotPath, nil
}

type clipboardSetInput struct {
	Text string `json:"text"`
}

func (m *MacTool) GetClipboard(_ context.Context, _ json.RawMessage) (string, error) {
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		return "", fmt.Errorf("pbpaste: %w", err)
	}
	content := strings.TrimRight(string(out), "\n")
	if content == "" {
		return "(clipboard is empty)", nil
	}
	return "Clipboard contents:\n" + content, nil
}

func (m *MacTool) SetClipboard(_ context.Context, raw json.RawMessage) (string, error) {
	var inp clipboardSetInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(inp.Text)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pbcopy: %w", err)
	}
	return fmt.Sprintf("Copied %d chars to clipboard", len(inp.Text)), nil
}

type appleScriptInput struct {
	Script string `json:"script"`
}

func (m *MacTool) RunAppleScript(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp appleScriptInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	cmd := exec.CommandContext(ctx, "osascript", "-e", inp.Script)
	out, err := cmd.CombinedOutput()
	result := strings.TrimRight(string(out), "\n")
	if err != nil {
		return fmt.Sprintf("AppleScript error: %v\n%s", err, result), nil
	}
	if result == "" {
		return "AppleScript executed successfully (no output)", nil
	}
	return result, nil
}

type openAppInput struct {
	Name string `json:"name"`
}

func (m *MacTool) OpenApp(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp openAppInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	// Try as app name first, then as file path
	var cmd *exec.Cmd
	if strings.HasPrefix(inp.Name, "/") || strings.HasPrefix(inp.Name, "~/") {
		cmd = exec.CommandContext(ctx, "open", resolvePath(inp.Name))
	} else {
		cmd = exec.CommandContext(ctx, "open", "-a", inp.Name)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("Failed to open %q: %v\n%s", inp.Name, err, out), nil
	}
	return fmt.Sprintf("Opened %q", inp.Name), nil
}

type browseURLInput struct {
	URL string `json:"url"`
}

func (m *MacTool) BrowseURL(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browseURLInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	cmd := exec.CommandContext(ctx, "open", inp.URL)
	if err := cmd.Run(); err != nil {
		return fmt.Sprintf("Failed to open URL: %v", err), nil
	}
	return fmt.Sprintf("Opened %s in default browser", inp.URL), nil
}
