package tools

import (
	"context"
	"encoding/json"
	"fmt"
)

// DesktopTool provides operations against the logged-in user's desktop:
// screenshots, the clipboard, launching applications and opening URLs.
//
// Every one of these needs a real interactive session, so the implementations
// live per platform (desktop_darwin.go, desktop_windows.go, desktop_other.go)
// and this file holds only the JSON plumbing.
type DesktopTool struct{}

// ScreenshotPath is where take_screenshot writes. The bot layer detects the
// SCREENSHOT: sentinel below and sends the file as a photo.
var ScreenshotPath = tempFile("goterm-screenshot.png")

func (d *DesktopTool) Screenshot(ctx context.Context, _ json.RawMessage) (string, error) {
	if err := captureScreen(ctx, ScreenshotPath); err != nil {
		return "", err
	}
	// Return magic sentinel — the bot detects this and sends the image.
	return "SCREENSHOT:" + ScreenshotPath, nil
}

func (d *DesktopTool) GetClipboard(ctx context.Context, _ json.RawMessage) (string, error) {
	content, err := clipboardGet(ctx)
	if err != nil {
		return "", err
	}
	if content == "" {
		return "(clipboard is empty)", nil
	}
	return "Clipboard contents:\n" + content, nil
}

type clipboardSetInput struct {
	Text string `json:"text"`
}

func (d *DesktopTool) SetClipboard(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp clipboardSetInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := clipboardSet(ctx, inp.Text); err != nil {
		return "", err
	}
	return fmt.Sprintf("Copied %d chars to clipboard", len(inp.Text)), nil
}

type appleScriptInput struct {
	Script string `json:"script"`
}

func (d *DesktopTool) RunAppleScript(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp appleScriptInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	return runAutomationScript(ctx, inp.Script)
}

type openAppInput struct {
	Name string `json:"name"`
}

func (d *DesktopTool) OpenApp(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp openAppInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := openApp(ctx, inp.Name); err != nil {
		return fmt.Sprintf("Failed to open %q: %v", inp.Name, err), nil
	}
	return fmt.Sprintf("Opened %q", inp.Name), nil
}

type browseURLInput struct {
	URL string `json:"url"`
}

func (d *DesktopTool) BrowseURL(ctx context.Context, raw json.RawMessage) (string, error) {
	var inp browseURLInput
	if err := json.Unmarshal(raw, &inp); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if err := openURL(ctx, inp.URL); err != nil {
		return fmt.Sprintf("Failed to open URL: %v", err), nil
	}
	return fmt.Sprintf("Opened %s in default browser", inp.URL), nil
}
