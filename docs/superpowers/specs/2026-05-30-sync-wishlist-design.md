# Design: sync wishlist from Google Sheet

## Problem

The Kobo wishlist is maintained as a flat file, `data/wishlist.txt`, which is
awkward to edit away from the dev machine. A Google Spreadsheet is now the
preferred place to maintain the list (editable from anywhere, plus a bookmarklet
for one-click adds). A bookmarklet already writes URLs *to* the sheet via a
deployed Apps Script web app. This design covers the missing return path:
pulling the maintained URL list back down into `data/wishlist.txt` so the
existing `get_list_prices` workflow runs unchanged.

## Decisions

- **Read path: reuse the existing Apps Script** (not published-CSV, not Sheets
  API v4). The script already runs as the user, so the sheet stays fully
  private and no new auth machinery is introduced. The same `/exec` deployment
  now serves both directions of the loop.
- **Landing: sync into `data/wishlist.txt`.** The file remains the on-disk
  source of truth for scraping; the sheet is the editable upstream.
  `get_list_prices` is untouched.
- **Reconciliation: dedup + sort, overwrite wholesale.** The sheet is
  authoritative — deletions in the sheet propagate down. A sorted file is
  stable and easy to inspect.

## Apps Script side (already implemented)

The user has extended the deployed web app's `doGet` with an `action=list`
branch (recorded in `Bookmarklet.md`). The sheet stores the URL in **column A,
no header row**. `action=list` returns the column as newline-separated plain
text, filtered to rows beginning with `http`:

```javascript
if (e.parameter.action === 'list') {
  var sheet = SpreadsheetApp.getActiveSpreadsheet().getSheets()[0];
  var values = sheet.getRange('A1:A').getValues();
  var urls = values.map(function(r){ return String(r[0]).trim(); })
                    .filter(function(u){ return u.indexOf('http') === 0; });
  return ContentService.createTextOutput(urls.join('\n'))
                        .setMimeType(ContentService.MimeType.PLAIN_TEXT);
}
```

No further changes are needed on the Apps Script side for this feature.

## New command: `cmd/sync_wishlist`

Matches the existing `cmd/` layout and conventions (`get_list_prices`,
`arb_list_prices`, `diff_list_prices`): `jessevdk/go-flags` for parsing,
`slog`/`tint` for logging, `joho/godotenv/autoload` for env loading.

### Configuration

- Reads the deployment URL from env var **`KOBOLT_SHEET_URL`**, which lives in
  `.env.local` (auto-loaded, same mechanism as `KOBOLT_CC`). The macros ID is
  secret-ish, so it is never hardcoded or committed.

### Behaviour

1. Resolve `KOBOLT_SHEET_URL`; if unset, error and exit non-zero.
2. `GET $KOBOLT_SHEET_URL?action=list` with a sane timeout (e.g. 30s). Apps
   Script issues a 302 redirect to `googleusercontent.com`; Go's default
   `http.Client` follows it.
3. On non-200 or network error: error out, leave the existing file untouched.
4. Parse the body: split on newlines, trim each line, drop blanks, **dedup**
   (set), then **sort**.
5. **Zero-URL guard:** if the parsed result is empty, refuse to overwrite —
   print a warning and exit non-zero. Protects against a script bug or empty
   response nuking the local list.
6. **Atomic write:** write to a temp file in the target directory, then
   `os.Rename` over the destination, so a partial/failed write never truncates
   the existing `wishlist.txt`.
7. Print a one-line summary to stdout: `synced N urls -> data/wishlist.txt`.
   Report collapsed-duplicate count to stderr when non-zero.

### Usage

```
go build ./cmd/sync_wishlist
./sync_wishlist                      # writes data/wishlist.txt (default)
./sync_wishlist data/wishlist.txt    # explicit output path
```

Output path is a single optional positional arg, default `data/wishlist.txt`.
Sync and scrape remain separate steps (no piping into `get_list_prices`).

## Shared code

The fetch/parse/dedup/sort/write logic is small and command-specific, so it
lives in `cmd/sync_wishlist/main.go`. The root `kobolt` package is not modified.
(If a second consumer ever needs a "sheet client," extract then — YAGNI now.)

## Error handling summary

| Condition                  | Behaviour                                  |
|----------------------------|--------------------------------------------|
| `KOBOLT_SHEET_URL` unset   | Error, exit non-zero, no write             |
| Non-200 / network error    | Error, exit non-zero, existing file intact |
| Zero URLs parsed           | Warn, exit non-zero, existing file intact  |
| Success                    | Atomic overwrite, one-line summary         |

## Testing

- **Parse/dedup/sort** — table-driven test over sample multi-line blobs
  (duplicates, blank lines, trailing newline, already-sorted).
- **Atomic write** — verify the original file is intact when the write step is
  made to fail; verify content + ordering on success.
- **HTTP fetch** — `httptest.Server` returning canned plain text; assert the
  end-to-end parse. Also assert non-200 is surfaced as an error.
- **Zero-URL guard** — empty body leaves the destination untouched.
```
