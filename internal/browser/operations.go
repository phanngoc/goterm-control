package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Navigate loads a URL in the active page tab.
func (c *Chrome) Navigate(ctx context.Context, url string) error {
	wsURL, err := c.PageWsURL()
	if err != nil {
		return err
	}
	return WithCdpSocket(ctx, wsURL, func(send CdpSendFn) error {
		_, err := send("Page.navigate", map[string]any{"url": url})
		return err
	})
}

// CaptureScreenshot takes a PNG screenshot. If fullPage is true,
// the viewport is temporarily expanded to capture the entire page.
func (c *Chrome) CaptureScreenshot(ctx context.Context, fullPage bool) ([]byte, error) {
	wsURL, err := c.PageWsURL()
	if err != nil {
		return nil, err
	}
	var data []byte
	err = WithCdpSocket(ctx, wsURL, func(send CdpSendFn) error {
		send("Page.enable", nil)

		if fullPage {
			// Get content size and expand viewport.
			metrics, _ := send("Page.getLayoutMetrics", nil)
			var m struct {
				CSSContentSize struct {
					Width  float64 `json:"width"`
					Height float64 `json:"height"`
				} `json:"cssContentSize"`
			}
			json.Unmarshal(metrics, &m)
			if m.CSSContentSize.Width > 0 && m.CSSContentSize.Height > 0 {
				send("Emulation.setDeviceMetricsOverride", map[string]any{
					"width":             int(m.CSSContentSize.Width),
					"height":            int(m.CSSContentSize.Height),
					"deviceScaleFactor": 1,
					"mobile":            false,
				})
				defer send("Emulation.clearDeviceMetricsOverride", nil)
			}
		}

		result, err := send("Page.captureScreenshot", map[string]any{
			"format":                "png",
			"captureBeyondViewport": true,
		})
		if err != nil {
			return err
		}
		var ss struct {
			Data string `json:"data"`
		}
		json.Unmarshal(result, &ss)
		if ss.Data == "" {
			return fmt.Errorf("screenshot: empty data")
		}
		data, err = base64.StdEncoding.DecodeString(ss.Data)
		return err
	})
	return data, err
}

// EvalJS evaluates a JavaScript expression and returns the string result.
func (c *Chrome) EvalJS(ctx context.Context, expr string) (string, error) {
	wsURL, err := c.PageWsURL()
	if err != nil {
		return "", err
	}
	var result string
	err = WithCdpSocket(ctx, wsURL, func(send CdpSendFn) error {
		send("Runtime.enable", nil)
		raw, err := send("Runtime.evaluate", map[string]any{
			"expression":            expr,
			"returnByValue":         true,
			"awaitPromise":          true,
			"userGesture":           true,
			"includeCommandLineAPI": true,
		})
		if err != nil {
			return err
		}
		var resp struct {
			Result struct {
				Type  string          `json:"type"`
				Value json.RawMessage `json:"value"`
			} `json:"result"`
			ExceptionDetails *struct {
				Text string `json:"text"`
			} `json:"exceptionDetails"`
		}
		json.Unmarshal(raw, &resp)
		if resp.ExceptionDetails != nil {
			return fmt.Errorf("js error: %s", resp.ExceptionDetails.Text)
		}
		// Try to get string value; fall back to raw JSON.
		var s string
		if json.Unmarshal(resp.Result.Value, &s) == nil {
			result = s
		} else {
			result = string(resp.Result.Value)
		}
		return nil
	})
	return result, err
}

// SnapshotDOM returns a text representation of the DOM with element refs.
// The injected JS does a DFS traversal, matching OpenClaw's snapshotDom.
func (c *Chrome) SnapshotDOM(ctx context.Context, selector string) (string, error) {
	expr := `(() => {
  const maxNodes = 800, maxText = 220;
  const nodes = [];
  const root = document.documentElement;
  if (!root) return JSON.stringify({nodes});
  const stack = [{el: root, depth: 0, parentRef: null}];
  while (stack.length && nodes.length < maxNodes) {
    const cur = stack.pop();
    const el = cur.el;
    if (!el || el.nodeType !== 1) continue;
    const ref = "n" + (nodes.length + 1);
    const tag = (el.tagName || "").toLowerCase();
    const id = el.id || undefined;
    const role = el.getAttribute && el.getAttribute("role") || undefined;
    const name = el.getAttribute && el.getAttribute("aria-label") || undefined;
    let text = "";
    try { text = (el.innerText || "").trim(); } catch {}
    if (text.length > maxText) text = text.slice(0, maxText) + "...";
    const href = el.href || undefined;
    const type = el.type || undefined;
    const value = el.value !== undefined && el.value !== null && el.value !== "" ? String(el.value).slice(0, 500) : undefined;
    nodes.push({ref, parentRef: cur.parentRef, depth: cur.depth, tag, id, role, name, text, href, type, value});
    const children = el.children ? Array.from(el.children) : [];
    for (let i = children.length - 1; i >= 0; i--) {
      stack.push({el: children[i], depth: cur.depth + 1, parentRef: ref});
    }
  }
  return JSON.stringify({nodes});
})()`

	raw, err := c.EvalJS(ctx, expr)
	if err != nil {
		return "", err
	}

	var snap struct {
		Nodes []struct {
			Ref   string `json:"ref"`
			Depth int    `json:"depth"`
			Tag   string `json:"tag"`
			ID    string `json:"id,omitempty"`
			Role  string `json:"role,omitempty"`
			Name  string `json:"name,omitempty"`
			Text  string `json:"text,omitempty"`
			Href  string `json:"href,omitempty"`
			Type  string `json:"type,omitempty"`
			Value string `json:"value,omitempty"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(raw), &snap); err != nil {
		return raw, nil // Return raw text if parse fails.
	}

	// Format as readable text tree.
	var b strings.Builder
	for _, n := range snap.Nodes {
		indent := strings.Repeat("  ", n.Depth)
		fmt.Fprintf(&b, "%s[%s] <%s>", indent, n.Ref, n.Tag)
		if n.Role != "" {
			fmt.Fprintf(&b, " role=%s", n.Role)
		}
		if n.Name != "" {
			fmt.Fprintf(&b, " %q", n.Name)
		}
		if n.ID != "" {
			fmt.Fprintf(&b, " #%s", n.ID)
		}
		if n.Type != "" {
			fmt.Fprintf(&b, " type=%s", n.Type)
		}
		if n.Value != "" {
			fmt.Fprintf(&b, " value=%q", n.Value)
		}
		if n.Href != "" {
			fmt.Fprintf(&b, " href=%s", n.Href)
		}
		if n.Text != "" && len(n.Text) < 80 {
			fmt.Fprintf(&b, " %q", n.Text)
		}
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// refActionJS builds a JS expression that finds an element by DFS ref
// and executes the given action code on it. The action code can use "el"
// as the found element. Returns "ref not found" if the ref is invalid.
func refActionJS(ref, actionCode string) string {
	return fmt.Sprintf(`(() => {
  const idx = parseInt(%q.replace("n","")) - 1;
  const stack = [document.documentElement];
  let count = 0;
  while (stack.length) {
    const el = stack.pop();
    if (!el || el.nodeType !== 1) continue;
    if (count === idx) { %s }
    count++;
    const ch = el.children ? Array.from(el.children) : [];
    for (let i = ch.length - 1; i >= 0; i--) stack.push(ch[i]);
  }
  return "ref not found";
})()`, ref, actionCode)
}

// execRefAction evaluates a ref-based JS action and returns an error
// if the ref was not found.
func (c *Chrome) execRefAction(ctx context.Context, ref, actionCode, refLabel string) error {
	result, err := c.EvalJS(ctx, refActionJS(ref, actionCode))
	if err != nil {
		return err
	}
	if result == "ref not found" {
		return fmt.Errorf("element %s not found", refLabel)
	}
	return nil
}

// Click clicks the element identified by a snapshot ref (e.g. "n5").
func (c *Chrome) Click(ctx context.Context, ref string) error {
	return c.execRefAction(ctx, ref, `el.click(); return "clicked";`, ref)
}

// Fill clears an input and sets its value, dispatching input events.
func (c *Chrome) Fill(ctx context.Context, ref string, text string) error {
	textJSON, _ := json.Marshal(text)
	action := fmt.Sprintf(`el.focus(); el.value = %s; el.dispatchEvent(new Event("input", {bubbles: true})); el.dispatchEvent(new Event("change", {bubbles: true})); return "filled";`, string(textJSON))
	return c.execRefAction(ctx, ref, action, ref)
}

// TypeText appends text to an input element (does not clear first).
func (c *Chrome) TypeText(ctx context.Context, ref string, text string) error {
	textJSON, _ := json.Marshal(text)
	action := fmt.Sprintf(`el.focus(); el.value += %s; el.dispatchEvent(new Event("input", {bubbles: true})); return "typed";`, string(textJSON))
	return c.execRefAction(ctx, ref, action, ref)
}

// SelectOption selects a dropdown value.
func (c *Chrome) SelectOption(ctx context.Context, ref string, value string) error {
	valJSON, _ := json.Marshal(value)
	action := fmt.Sprintf(`el.value = %s; el.dispatchEvent(new Event("change", {bubbles: true})); return "selected";`, string(valJSON))
	return c.execRefAction(ctx, ref, action, ref)
}

// Scroll scrolls the page in the given direction.
func (c *Chrome) Scroll(ctx context.Context, direction string, pixels int) error {
	if pixels <= 0 {
		pixels = 300
	}
	var dx, dy int
	switch direction {
	case "up":
		dy = -pixels
	case "down":
		dy = pixels
	case "left":
		dx = -pixels
	case "right":
		dx = pixels
	default:
		dy = pixels
	}
	expr := fmt.Sprintf("window.scrollBy(%d, %d)", dx, dy)
	_, err := c.EvalJS(ctx, expr)
	return err
}

// GetText extracts a property from an element or the page.
func (c *Chrome) GetText(ctx context.Context, ref string, property string) (string, error) {
	if property == "" {
		property = "text"
	}

	// Page-level properties (no ref needed).
	if ref == "" {
		switch property {
		case "title":
			return c.EvalJS(ctx, "document.title")
		case "url":
			return c.EvalJS(ctx, "location.href")
		case "html":
			return c.EvalJS(ctx, "document.documentElement.outerHTML.slice(0, 200000)")
		default:
			return c.EvalJS(ctx, "(document.body && document.body.innerText || '').slice(0, 200000)")
		}
	}

	propExpr := "el.innerText || ''"
	switch property {
	case "html":
		propExpr = "el.outerHTML || ''"
	case "value":
		propExpr = "String(el.value || '')"
	}

	return c.EvalJS(ctx, refActionJS(ref, fmt.Sprintf("return %s;", propExpr)))
}

// GoBack navigates back in history.
func (c *Chrome) GoBack(ctx context.Context) error {
	_, err := c.EvalJS(ctx, "history.back()")
	return err
}

// WaitFor waits for an element ref, text on page, or a duration.
func (c *Chrome) WaitFor(ctx context.Context, ref string, text string, ms int) error {
	if ms > 0 && ref == "" && text == "" {
		time.Sleep(time.Duration(ms) * time.Millisecond)
		return nil
	}

	timeout := 10 * time.Second
	if ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	poll := 200 * time.Millisecond

	for time.Now().Before(deadline) {
		if ref != "" {
			result, err := c.EvalJS(ctx, refActionJS(ref, `return "found";`))
			if err == nil && result == "found" {
				return nil
			}
		}
		if text != "" {
			textJSON, _ := json.Marshal(text)
			expr := fmt.Sprintf("(document.body && document.body.innerText || '').includes(%s)", string(textJSON))
			result, err := c.EvalJS(ctx, expr)
			if err == nil && result == "true" {
				return nil
			}
		}
		time.Sleep(poll)
	}
	return fmt.Errorf("wait timed out after %s", timeout)
}
