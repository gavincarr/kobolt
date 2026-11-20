package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
)

// fallbackUA is used only if the installed Chrome's own UA can't be
// detected. It will drift out of date; detectUA keeps the real one current.
const fallbackUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// browserUA returns the UA to launch headless Chrome with: the installed
// browser's own UA with the HeadlessChrome token rewritten, so the claimed
// version always matches the real one (Cloudflare stalls on the default
// HeadlessChrome UA). Falls back to fallbackUA, with a warning, on failure.
func browserUA(allocOpts []chromedp.ExecAllocatorOption) string {
	ua, err := detectUA(allocOpts)
	if err == nil {
		ua, err = mungeUA(ua)
	}
	if err != nil {
		slog.Warn("could not detect browser user agent; using fallback", "error", err, "ua", fallbackUA)
		return fallbackUA
	}
	slog.Debug("using browser user agent", "ua", ua)
	return ua
}

// detectUA launches a short-lived browser with allocOpts and asks it for its
// default user agent via CDP Browser.getVersion.
func detectUA(allocOpts []chromedp.ExecAllocatorOption) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, allocOpts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	var ua string
	err := chromedp.Run(browserCtx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		_, _, _, ua, _, err = browser.GetVersion().Do(ctx)
		return err
	}))
	if err != nil {
		return "", fmt.Errorf("get browser version: %w", err)
	}
	return ua, nil
}

// mungeUA turns a headless Chrome UA into the equivalent regular Chrome UA.
func mungeUA(ua string) (string, error) {
	out := strings.Replace(ua, "HeadlessChrome/", "Chrome/", 1)
	if !strings.Contains(out, "Chrome/") || strings.Contains(out, "Headless") {
		return "", fmt.Errorf("unexpected browser user agent %q", ua)
	}
	return out, nil
}
