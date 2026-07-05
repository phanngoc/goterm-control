package bds

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// cdpAllocator is a long-lived Chrome allocator reused across all CDP fetches.
// Initialised once via initCDP().
var (
	cdpOnce      sync.Once
	cdpAllocCtx  context.Context
	cdpAllocCanc context.CancelFunc
)

// chromeBinary returns the first Chrome/Chromium executable found on the system.
func chromeBinary() (string, error) {
	candidates := []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/usr/bin/google-chrome",
		"/usr/bin/google-chrome-stable",
		"/usr/bin/chromium-browser",
		"/usr/bin/chromium",
		"google-chrome",
		"chromium",
	}
	for _, c := range candidates {
		if p, err := exec.LookPath(c); err == nil {
			return p, nil
		}
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("chrome/chromium not found; install Chrome or set CHROME_BIN")
}

// initCDP initialises the shared browser allocator (called once).
func initCDP() error {
	var initErr error
	cdpOnce.Do(func() {
		bin, err := chromeBinary()
		if err != nil {
			initErr = err
			return
		}

		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(bin),

			// Use new headless mode – avoids legacy "HeadlessChrome" UA string
			chromedp.Flag("headless", "new"),

			// Anti-detection: remove automation markers
			chromedp.Flag("disable-blink-features", "AutomationControlled"),
			chromedp.Flag("exclude-switches", "enable-automation"),
			chromedp.Flag("no-first-run", true),
			chromedp.Flag("no-default-browser-check", true),
			chromedp.Flag("disable-infobars", true),

			// Performance
			chromedp.Flag("disable-extensions", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("disable-dev-shm-usage", true),
			chromedp.Flag("no-sandbox", true),

			// Realistic viewport
			chromedp.WindowSize(1440, 900),
			chromedp.UserAgent(
				"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "+
					"AppleWebKit/537.36 (KHTML, like Gecko) "+
					"Chrome/124.0.0.0 Safari/537.36",
			),
		)

		cdpAllocCtx, cdpAllocCanc = chromedp.NewExecAllocator(context.Background(), opts...)
	})
	return initErr
}

// FetchCDPPublic is the exported wrapper used for debugging/testing.
func FetchCDPPublic(url string) (string, error) { return fetchCDP(url) }

// CloseCDP shuts down the shared Chrome process. Call on program exit.
func CloseCDP() {
	if cdpAllocCanc != nil {
		cdpAllocCanc()
	}
}

// fetchCDP loads a URL with a real Chrome instance, waits for Cloudflare
// challenge to pass, and returns the final rendered HTML.
//
// Strategy:
//  1. Navigate to URL.
//  2. Inject script to clear navigator.webdriver flag.
//  3. Poll until either the CF challenge iframe disappears
//     OR a known content selector is present.
//  4. Return outerHTML of <html>.
func fetchCDP(url string) (string, error) {
	if err := initCDP(); err != nil {
		return "", fmt.Errorf("cdp init: %w", err)
	}

	// Each page gets its own tab context (30-second timeout per page)
	ctx, cancel := chromedp.NewContext(cdpAllocCtx)
	defer cancel()
	ctx, cancelTO := context.WithTimeout(ctx, 45*time.Second)
	defer cancelTO()

	var html string
	err := chromedp.Run(ctx,
		// Mask automation before first navigation
		chromedp.ActionFunc(func(ctx context.Context) error {
			return chromedp.Evaluate(`
				Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
				window.chrome = { runtime: {} };
			`, nil).Do(ctx)
		}),

		chromedp.Navigate(url),

		// Wait for either CF challenge to clear or page body to be populated.
		// Cloudflare JS challenge typically takes < 5 s.
		chromedp.ActionFunc(func(ctx context.Context) error {
			return waitForContent(ctx, 20*time.Second)
		}),

		chromedp.OuterHTML("html", &html),
	)
	if err != nil {
		return "", fmt.Errorf("cdp fetch %s: %w", url, err)
	}
	return html, nil
}

// waitForContent polls until the page has real content (CF challenge solved).
// It considers the page ready when:
//   - The CF challenge iframe (#cf-chl-widget-*) is gone, OR
//   - A typical listing element is present.
func waitForContent(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var title string
		_ = chromedp.Evaluate(`document.title`, &title).Do(ctx)

		// Cloudflare sets title to "Just a moment..." during the challenge
		if !strings.Contains(strings.ToLower(title), "just a moment") &&
			!strings.Contains(strings.ToLower(title), "checking your browser") &&
			title != "" {
			// Extra settle time so dynamic content renders
			time.Sleep(800 * time.Millisecond)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	// Timed out waiting – return what we have (better than nothing)
	return nil
}
