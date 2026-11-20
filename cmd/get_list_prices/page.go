package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/gavincarr/kobolt"
)

const (
	pollInterval = 500 * time.Millisecond
	// How long readyState must have been "complete" with no price data before
	// giving up: priceDetail streams in several seconds after the rest of a
	// new-template page.
	completeGrace = 5 * time.Second
)

// cloudflareTitlePrefix starts the title of Cloudflare's interstitial, which
// reaches readyState "complete" with no page data while the challenge solves.
const cloudflareTitlePrefix = "Just a moment"

// probeJS reports what price data the page has so far, for both templates:
//   - gizmo: the legacy #ratings-widget-details-wrapper data-kobo-gizmo-config.
//   - rsc: the new template's joined RSC payload, returned only once it
//     contains "priceDetail" (or when called with full=true) so polls don't
//     ship ~300KB each time.
//
// The RSC chunks are read from the inline self.__next_f.push(...) script
// tags, not the self.__next_f array: once the Next.js runtime boots it
// replaces push() and later chunks never land in the array (observed empty
// on live pages). Each push argument is a JSON-compatible array literal.
const probeJS = `(full) => {
  const r = {readyState: document.readyState, title: document.title, gizmoEl: false, gizmo: "", rscChunks: 0, rsc: "", domPrice: false};
  const pe = document.querySelector('[data-testid="price"]');
  r.domPrice = !!pe && /\d/.test(pe.textContent || "");
  const el = document.getElementById("ratings-widget-details-wrapper");
  if (el) {
    r.gizmoEl = true;
    r.gizmo = el.getAttribute("data-kobo-gizmo-config") || "";
  }
  const prefix = "self.__next_f.push(";
  const parts = [];
  for (const s of document.scripts) {
    const t = s.textContent;
    if (!t.startsWith(prefix)) continue;
    try {
      const a = JSON.parse(t.slice(prefix.length, t.lastIndexOf(")")));
      if (a[0] === 1 && typeof a[1] === "string") parts.push(a[1]);
    } catch (e) {}
  }
  r.rscChunks = parts.length;
  const text = parts.join("");
  if (full || text.includes('"priceDetail":')) r.rsc = text;
  return r;
}`

type pageState struct {
	ReadyState string `json:"readyState"`
	Title      string `json:"title"`
	GizmoEl    bool   `json:"gizmoEl"`
	Gizmo      string `json:"gizmo"`
	RSCChunks  int    `json:"rscChunks"`
	RSC        string `json:"rsc"`
	// DOMPrice: the visible [data-testid="price"] element shows a digit.
	DOMPrice bool `json:"domPrice"`
}

func (s pageState) String() string {
	return fmt.Sprintf("readyState=%s title=%q gizmo_element=%t rsc_chunks=%d dom_price=%t", s.ReadyState, s.Title, s.GizmoEl, s.RSCChunks, s.DOMPrice)
}

func probePage(ctx context.Context, full bool) (pageState, error) {
	var st pageState
	err := chromedp.Run(ctx, chromedp.Evaluate("("+probeJS+")("+strconv.FormatBool(full)+")", &st))
	return st, err
}

// navigate starts loading url without waiting for the load event, which some
// pages take a very long time to fire (legacy US pages were seen stuck at
// readyState "interactive" for over a minute).
func navigate(ctx context.Context, url string) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, errText, _, err := page.Navigate(url).Do(ctx)
		if err != nil {
			return err
		}
		if errText != "" {
			return errors.New(errText)
		}
		return nil
	}))
}

// extractPrices polls the loaded page until it yields price data from either
// template, fills rp and returns the book's isbn/title/author. ctx bounds the
// whole wait.
func extractPrices(ctx context.Context, url string, rp *kobolt.RegionPrice, dumpRaw bool) (isbn, title, author string, err error) {
	var last pageState
	var seen bool
	var evalErr error
	var completeSince time.Time
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	for {
		st, probeErr := probePage(ctx, false)
		evalErr = probeErr
		if probeErr == nil {
			last, seen = st, true

			// Legacy template.
			if st.Gizmo != "" {
				if dumpRaw {
					slog.Debug("raw gizmo config", "url", url, "config", st.Gizmo)
				}
				gIsbn, gTitle, gAuthor, gErr := parseGizmoConfig(st.Gizmo, rp)
				if gErr != nil {
					return "", "", "", fmt.Errorf("parse: %w", gErr)
				}
				return gIsbn, gTitle, gAuthor, nil
			}
			// Legacy element without its config once the DOM is parsed.
			if st.GizmoEl && st.ReadyState != "loading" {
				return "", "", "", errors.New("data-kobo-gizmo-config attribute missing")
			}

			// New (RSC) template.
			if st.RSC != "" {
				if rIsbn, rTitle, rAuthor, done, rErr := fromRSC(url, st.RSC, rp, dumpRaw); done {
					return rIsbn, rTitle, rAuthor, rErr
				}
			}

			if st.ReadyState != "complete" || strings.HasPrefix(st.Title, cloudflareTitlePrefix) {
				completeSince = time.Time{}
			} else if completeSince.IsZero() {
				completeSince = time.Now()
			} else if time.Since(completeSince) >= completeGrace {
				return settledWithoutPrice(ctx, url, rp, st, dumpRaw)
			}
		}

		select {
		case <-ctx.Done():
			msg := "timed out waiting for price data"
			if seen {
				msg += " (" + last.String() + ")"
			} else if evalErr != nil {
				msg += fmt.Sprintf(" (evaluate: %v)", evalErr)
			}
			return "", "", "", errors.New(msg)
		case <-tick.C:
		}
	}
}

// fromRSC parses the RSC payload text. done is false while priceDetail is
// still missing or streaming in, i.e. the caller should keep polling.
func fromRSC(url, text string, rp *kobolt.RegionPrice, dumpRaw bool) (isbn, title, author string, done bool, err error) {
	isbn, title, author, err = parseRSC(text, rp)
	if errors.Is(err, errNoPriceDetail) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", true, fmt.Errorf("parse rsc: %w", err)
	}
	if dumpRaw {
		if raw, ok, _ := rscValue(text, "priceDetail"); ok {
			slog.Debug("raw priceDetail", "url", url, "priceDetail", string(raw))
		}
	}
	return isbn, title, author, true, nil
}

// Errors from resolveSettled.
var (
	errPriceNotInData = errors.New("price shown on page but no priceDetail data found")
	errUnrecognised   = errors.New("no price data found (unrecognised page template)")
)

// resolveSettled decides what a settled page with no usable priceDetail is.
// hasMeta reports product itemDetails metadata. A visible DOM price without
// priceDetail is an error (Kobo likely changed the data shape; retried and
// counted as failed). Metadata and no DOM price is treated as unavailable in
// region (ok=true, no error). NOTE: that unavailable page variant has not
// actually been observed yet. Neither is an unrecognised template.
func resolveSettled(st pageState, hasMeta bool) (unavailable bool, err error) {
	switch {
	case st.DOMPrice:
		return false, errPriceNotInData
	case hasMeta:
		return true, nil
	}
	return false, errUnrecognised
}

// settledWithoutPrice handles a page that finished loading with no price
// data; see resolveSettled for the rules. Unrecognised pages fail fast
// instead of burning the whole timeout.
func settledWithoutPrice(ctx context.Context, url string, rp *kobolt.RegionPrice, st pageState, dumpRaw bool) (isbn, title, author string, err error) {
	full, probeErr := probePage(ctx, true)
	if probeErr != nil {
		if ctx.Err() != nil {
			return "", "", "", fmt.Errorf("timed out waiting for price data (%s)", st.String())
		}
		slog.Debug("no price data after page load", "url", url, "state", st.String())
		_, err := resolveSettled(st, false)
		return "", "", "", err
	}
	st = full
	// priceDetail may have completed since the last poll.
	if fIsbn, fTitle, fAuthor, done, fErr := fromRSC(url, full.RSC, rp, dumpRaw); done {
		return fIsbn, fTitle, fAuthor, fErr
	}
	mIsbn, mTitle, mAuthor, hasMeta := rscMetadata(full.RSC)
	unavailable, err := resolveSettled(st, hasMeta)
	if err != nil {
		slog.Debug("no price data after page load", "url", url, "state", st.String())
		return "", "", "", err
	}
	if unavailable {
		slog.Info("no price on page; treating as unavailable in region", "url", url, "state", st.String())
	}
	return mIsbn, mTitle, mAuthor, nil
}
