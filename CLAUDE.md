# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`kobolt` scrapes Kobo eBook list/sale prices for a file of URLs, optionally across multiple regional Kobo storefronts, and writes a dated JSON file next to the input. Commands live under `cmd/`: `get_list_prices` (the scraper), `diff_list_prices` and `arb_list_prices` (analyse snapshots), and `sync_wishlist` (pulls the URL list down from a Google Sheet — see below). The `data/` directory (input wishlists and dated outputs) is gitignored.

## Build and run

```
go build ./cmd/get_list_prices
./get_list_prices data/wishlist.txt                # single-region (region taken from each URL)
./get_list_prices --cc my,au,us data/wishlist.txt  # multi-region
KOBOLT_CC=my,au ./get_list_prices data/wishlist.txt
./get_list_prices -v data/wishlist.txt             # debug logging incl. raw gizmo config
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

The same Apps Script `/exec` deployment serves both directions: `?url=&title=` appends a row, `?action=list` returns column A as newline-separated plain text. The script runs as the user, so the sheet stays private — no API keys. The deployment URL is read from `KOBOLT_SHEET_URL` (kept in `.env.local`). `sync_wishlist` dedups + sorts the URLs and **atomically overwrites** the output file; a zero-URL response is refused rather than truncating the existing list. Run `sync_wishlist` before `get_list_prices` to scrape against the latest list.

## Architecture and non-obvious decisions

**Cloudflare drives the design.** Kobo serves a JS challenge to non-browser clients (plain HTTP returns 403 with a "Just a moment..." body), so the scraper drives real Chrome via `chromedp`. Two settings are load-bearing:

- `--headless=new` (not legacy `--headless`) plus a realistic UA — without these the page stalls on the challenge.
- Fresh tab (`chromedp.NewContext(browserCtx)`) per URL. Reusing a tab across navigations consistently hangs on the 2nd page; per-tab overhead is small.

**Prices come from two schema.org blobs.** Both live inside the `data-kobo-gizmo-config` attribute on `#ratings-widget-details-wrapper`, doubly JSON-encoded:

- `googleBook.workExample.potentialAction.expectsAcceptanceOf.price` → current / sale price (+ currency).
- `googleProduct.offers.price` → **list price** (RRP).
- `googleBook.{name, author, workExample.isbn}` → title, author, ISBN.

When a book is unavailable in a region, the Offer block has no price; the record keeps isbn/title/author and a `scraped_at` timestamp, and `omitempty` strips the zero-value price fields from JSON.

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
