# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`kobolt` scrapes Kobo eBook list/sale prices for a file of URLs, optionally across multiple regional Kobo storefronts, and writes a dated JSON file next to the input. Commands live under `cmd/`: `get_list_prices` (the scraper), `diff_list_prices`, `arb_list_prices` and `list_prices` (analyse snapshots), `sync_wishlist` (pulls the URL list down from a Google Sheet — see below), and `parse_booklog` (parses the `~/Books` reading log into JSON — see below). The `data/` directory (input wishlists and dated outputs) is gitignored.

Flag parsing across all commands uses `alecthomas/kong` (with `gavincarr/kong-help-colours` for colourised `--help`). Repeatable verbosity flags (`-v`/`-vv`) are kong `type:"counter"` ints.

## Build and run

```
go build ./cmd/get_list_prices
./get_list_prices data/wishlist.txt                # single-region (region taken from each URL)
./get_list_prices --cc my,au,us data/wishlist.txt  # multi-region
KOBOLT_CC=my,au ./get_list_prices data/wishlist.txt
./get_list_prices -v data/wishlist.txt             # debug logging
./get_list_prices -vv data/wishlist.txt            # also dump raw price data (gizmo config / RSC priceDetail)
./get_list_prices --headful data/wishlist.txt      # visible Chrome for debugging
```

Requires Chrome/Chromium on `$PATH` — chromedp drives it via CDP.

## Wishlist maintenance (Google Sheet loop)

The wishlist is maintained in a Google Spreadsheet, editable from anywhere. A bookmarklet (`Bookmarklet.md`) hits a deployed Apps Script web app to append the current page's URL to column A of the sheet. `sync_wishlist` closes the loop in the other direction:

```
go build ./cmd/sync_wishlist
./sync_wishlist                    # GET $KOBOLT_SHEET_URL?action=list -> data/wishlist.txt
./sync_wishlist path/to/list.txt   # explicit output path
```

The same Apps Script `/exec` deployment serves both directions: `?url=&title=` appends a row, `?action=list` returns column A as newline-separated plain text. The script runs as the user, so the sheet stays private — no API keys. The deployment URL is read from `KOBOLT_SHEET_URL` (kept in `.env.local`). `sync_wishlist` lowercases (Kobo URLs are case-insensitive, but the sheet sometimes has uppercase `/CC/LANG/` codes that would break sorting/deduping), dedups + sorts the URLs, and **atomically overwrites** the output file; a zero-URL response is refused rather than truncating the existing list. Run `sync_wishlist` before `get_list_prices` to scrape against the latest list.

`sync_wishlist` is **silent by default** (cron-friendly) — only a duplicate-URL warning and errors surface. `-v` adds the info-level sync summary; `-vv` additionally itemises each duplicated URL at debug so they're easy to find and clean up in the sheet.

## Reading log (parse_booklog)

`parse_booklog` parses the personal, fixed-column reading log at `~/Books` into structured JSON:

```
go build ./cmd/parse_booklog
./parse_booklog                       # parse ~/Books -> JSON on stdout
./parse_booklog -o data/books.json    # atomic write to a file instead
./parse_booklog path/to/log           # explicit input path (~ is expanded)
./parse_booklog -v                    # add an info-level parse summary
./parse_booklog -vv                   # additionally itemise skipped lines
```

The log is fixed-column but fields overflow their padding, so the parser is a hybrid: the month is the leading `MM/YY` token (normalised to `YYYY-MM`, pivot `yy<=26 -> 20yy` else `19yy`), the author/title boundary is **rune** column 32 (rune- not byte-indexed, so multibyte authors like `China Miéville` stay aligned), and the genre + optional ISBN/ASIN are right-anchored by token pattern. Genre codes map `F/N/I/C/B/K/FR -> Fiction/Non-fiction/IT/Christian/Business/Kids/French`; a trailing `*` (e.g. `C*`) is dropped and unknown codes pass through verbatim. `id`/`id_type` are `omitempty` (older entries have neither). Parsing is silent by default; unparseable lines are skipped (never fatal) and surface as warnings.

## Architecture and non-obvious decisions

**Cloudflare drives the design.** Kobo serves a JS challenge to non-browser clients (plain HTTP returns 403 with a "Just a moment..." body), so the scraper drives real Chrome via `chromedp`. Two settings are load-bearing:

- `--headless=new` (not legacy `--headless`) plus a non-headless UA — without these the page stalls on the challenge. The UA is derived from the installed Chrome rather than hardcoded (`ua.go`): a short-lived headless browser is launched first and asked for its UA via CDP `Browser.getVersion`, `HeadlessChrome/` is rewritten to `Chrome/`, and the real browser is launched with that as `--user-agent` (launch-time, so it covers every request, not just one tab). The claimed version therefore always matches the real one. If detection fails it warns and falls back to a stale hardcoded UA. `--headful` skips all this (its UA is already non-headless).
- Fresh tab (`chromedp.NewContext(browserCtx)`) per URL. Reusing a tab across navigations consistently hangs on the 2nd page; per-tab overhead is small.

**Two page templates.** Kobo is migrating storefronts to a new Next.js product page region by region (MY and AU are on it as of Nov 2026; US is still legacy), so the scraper understands both and decides per page.

*Legacy template* — prices come from two schema.org blobs inside the `data-kobo-gizmo-config` attribute on `#ratings-widget-details-wrapper`, doubly JSON-encoded (`parseGizmoConfig`):

- `googleBook.workExample.potentialAction.expectsAcceptanceOf.price` → current / sale price (+ currency).
- `googleProduct.offers.price` → **list price** (RRP).
- `googleBook.{name, author, workExample.isbn}` → title, author, ISBN.

When a book is unavailable in a region, the Offer block has no price; the record keeps isbn/title/author and a `scraped_at` timestamp, and `omitempty` strips the zero-value price fields from JSON.

*New template* — no gizmo element; data arrives as a React Server Components payload streamed via inline `<script>self.__next_f.push([1,"<chunk>"])</script>` tags (`extract.go`, `parseRSC`). The probe JS reads and joins those **script tags**, not the `self.__next_f` array — once the Next.js runtime boots it replaces `push()` and the array stays empty on live pages. In the joined text:

- `"priceDetail":{"listPrice":..,"displayPrice":..,"currency":..}` → list price, current / sale price, currency. Decoded with `json.Decoder` from just after the key. It lives in a react-query dehydrated-state row that streams in several seconds **after** the visible DOM price, so wait for the data, not the DOM.
- `"itemDetails":{"metadata":{"title","isbn"},"contributors":[..]}` → title, ISBN, author (`type == "Author"`, non-narrator names joined with ", "). Metadata is only read from `itemDetails` — related-book lists elsewhere in the payload carry their own titles/`authors`.
- No `priceDetail` once the page has settled: if the visible DOM price (`[data-testid="price"]` containing a digit, which renders before `priceDetail` streams) is present, it's an **error** (`price shown on page but no priceDetail data found`; retried on rerun, counted in `failed`) so a Kobo restructure can't silently drop prices. Only with `itemDetails` metadata and **no** DOM price is it treated as unavailable in region (metadata kept, no price, no error; logged at Info as `no price on page; treating as unavailable in region`). This page variant hasn't actually been observed yet. A non-object `priceDetail` value (`null`, `"$undefined"`, an RSC ref) counts as not-yet-present and keeps polling.

Test fixtures in `cmd/get_list_prices/testdata/` are trimmed real excerpts of saved pages (whole RSC rows, including decoys).

**Polling, not waiting on a selector.** `scrapeOne` navigates without waiting for the load event (`page.Navigate(...).Do`; legacy US pages have been seen stuck at `readyState=interactive` for over a minute), then polls a cheap in-page probe every 500ms: legacy gizmo attribute present → legacy parser; RSC text containing `priceDetail` → RSC parser (a truncated, still-streaming `priceDetail` keeps polling). If `readyState` has been `complete` for 5s with neither (the clock doesn't run while the title starts with Cloudflare's "Just a moment"), it **fails fast** with `no price data found (unrecognised page template)` instead of burning the per-URL `--timeout`, which still caps everything; a timeout error reports the last seen `readyState` / title / gizmo element / RSC chunk count. The non-headless UA is still required on both templates (headless Chrome's default `HeadlessChrome/...` UA stalls on the challenge). Pages for other storefronts may show a geo banner (e.g. "Looks like you're in Malaysia") but still carry prices.

**Progress logging.** Each `scraped` / `scrape error` line carries `req=<done>/<total>` and a running `failed=` count, so a run that's failing every job is obvious early; `scraped` lines carry `unavailable=true` for records with no price and no error; the final `scrape complete` line gives `jobs`, `ok` (excluding unavailable), `unavailable` and `failed` totals, and is logged at WARN when anything failed.

**Exit status.** The output file is always written (so partial progress is kept for the next run's resume). The process then exits 1 with `all N scrape jobs failed` only if *every* job failed — wholesale breakage such as a Kobo page change, which cron mail should surface. Partial failures still exit 0; they're retried on the next run.

**Output schema is region-keyed:**

```json
{ "url": "<input URL>", "isbn": "...", "title": "...", "author": "...",
  "regions": {
    "my": { "url": "...", "price": ..., "list_price": ..., "currency": "MYR", "scraped_at": "..." },
    "au": { ... } } }
```

Output path is `<inputfile-no-ext>_<YYYYMMDD>.json` in the input's directory.

**Resume is cumulative.** On each run the existing same-day output file is loaded and merged:
- `(url, cc)` pairs with a non-error record are skipped; errored regions are retried.
- Regions present in the file but not in current `--cc` are preserved across runs.
- Books no longer in the input file are dropped from the output.
- v1 (flat) records are migrated into `regions[<cc-from-url>]` so prior runs survive schema changes.

Books are matched across runs by exact URL string. Region substitution uses the regex `^(https?://(?:www\.)?kobo\.com/)([a-z]{2})(/.*)$` — only the cc segment is swapped, the language segment (usually `/en/`) is left alone. AU/US/GB/IE/CA/NZ work transparently; non-English regional stores may need separate handling.

## Stack notes specific to this repo

- **Env loading (`internal/env`).** Every command calls `env.Load()` first thing in `main()`. It loads `.env.local` then `.env`, resolved from the **module root** (derived from the executable via `internal/moduleroot`, or the `MODULE_ROOT` override) rather than the cwd. Precedence is real-env > `.env.local` > `.env` (godotenv never overwrites an already-set var). This deliberately replaces the bare `joho/godotenv/autoload` import, which reads only `.env` and only from the process cwd — fine interactively (direnv via `.envrc` also loads `.env.local`) but broken under cron, where direnv doesn't run and cwd is `$HOME`. So `KOBOLT_CC` / `KOBOLT_SHEET_URL` in `.env.local` now reach the binary without direnv.
- **Running from cron.** `env.Load()` finds `.env.local` if *any* of these holds: the binary lives in `<root>/bin/` (so build with `go build -o bin/<name> ./cmd/<name>`), or `MODULE_ROOT` is exported, or the job `cd`s into the repo. `cd`-ing in is needed anyway because `get_list_prices`/`sync_wishlist` take relative paths like `data/wishlist.txt`. Typical line: `cd /path/to/kobolt && ./bin/sync_wishlist && ./bin/get_list_prices data/wishlist.txt`.
- The `data/` directory and dated JSON outputs are gitignored — don't commit scraped data.
