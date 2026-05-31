# parse_booklog design

## Purpose

A new Go CLI under `cmd/parse_booklog` that parses the fixed-column,
text-based reading log at `~/Books` into structured JSON, emitted to stdout
or to a file via `-o/--outfile`.

The log is a personal record of books read, one per line, oldest entries from
1997 through the present (2026). Each line carries a month, author, title, a
single-letter genre code, and (for newer entries) an ISBN or Amazon ASIN.

## CLI surface

```
parse_booklog [-o FILE] [-v|-vv] [input]
```

- `input` — positional, optional; defaults to `~/Books`. A leading `~/` is
  expanded to `$HOME`.
- `-o, --outfile FILE` — write the JSON here using an atomic
  write-temp-then-rename (mirroring `sync_wishlist`'s `atomicWrite`); default
  is stdout.
- `-v/-vv` — `[]bool` verbosity mapped to slog levels. **Default is
  `LevelWarn`**: silent on success, but emits a warning (with line number and
  the offending line) for any line it cannot parse. `-v` lifts to `LevelInfo`
  (a one-line parse summary: N books parsed, M lines skipped); `-vv` lifts to
  `LevelDebug`.

Wiring mirrors the existing commands: `env.Load()` first thing in `main()`,
`github.com/jessevdk/go-flags` for parsing, and a `slog` default backed by a
`github.com/lmittmann/tint` handler on stderr. `env.Load()` is a no-op for
this command (it reads no env vars) but is kept for cross-command consistency,
as documented in CLAUDE.md.

> Note on flag library: the user's global preference is `kong`, but this repo
> has standardized on `go-flags` across all existing commands. Following the
> existing-codebase convention wins here.

## Output schema

A JSON array (2-space indented, with a trailing newline) of book records:

```json
{
  "month": "2026-05",
  "author": "Olaf Stapledon",
  "title": "The Last and First Men",
  "genre": "Fiction",
  "id": "9781857988062",
  "id_type": "isbn"
}
```

- `month`, `author`, `title`, `genre` are always present.
- `id` and `id_type` are `omitempty` — older entries have neither.
- The two author-less lines in the log (`VPN`, `Using Samba`) emit
  `"author": ""` rather than being skipped (confirmed with the user).

Go struct (field order fixes JSON key order):

```go
type Book struct {
    Month  string `json:"month"`
    Author string `json:"author"`
    Title  string `json:"title"`
    Genre  string `json:"genre"`
    ID     string `json:"id,omitempty"`
    IDType string `json:"id_type,omitempty"`
}
```

## Files

- `cmd/parse_booklog/main.go` — CLI wiring and `run()`: resolve/expand the
  input path, read the file, call the parser, marshal indented JSON, and write
  to stdout or atomically to `-o`.
- `cmd/parse_booklog/parse.go` — **pure** parsing logic, no I/O, so it is
  table-testable in isolation (the same separation `sync_wishlist` uses for
  `parseURLList`).
- `cmd/parse_booklog/parse_test.go` — table tests.

## Parsing algorithm

The file is fixed-column in origin, but fields that overflow their column
width eat their trailing padding. A naive "split on runs of 2+ spaces" gets
~10 of 928 lines wrong: long author names that collide with the title column,
long titles that collide with the genre column, and two author-less lines. The
parser is therefore a **hybrid**: front-anchor the month, split author/title
by **column position 32**, and right-anchor the genre and ID by **token
pattern**.

`parseLine(line string) (Book, bool)` — `bool` is false for lines that should
be skipped (blank or unparseable):

1. Right-trim the line. If blank, return `ok = false`.
2. `month = line[0:5]`; validate against `^\d\d/\d\d$`. If it does not match,
   the line is unparseable → `ok = false`. Normalize to `YYYY-MM` using the
   pivot: **two-digit year `yy <= 26` → `20yy`, else `19yy`** (the file spans
   1997–2026). The pivot boundary is a named, documented constant.
3. Slice by **rune index, not byte** — the log is UTF-8 and the human aligned
   columns visually (by rune), so a handful of lines carry multibyte runes
   (`Sylvie Lainé`, `Dag Hammarsköld`, `Jean-Benoît Nadeau`, `China Miéville`)
   before the title column; byte-slicing would misalign them. Convert once:
   `r := []rune(line)`. Then `author = strings.TrimSpace(string(r[5:32]))`.
   Column 32 is where the title column begins; trimming absorbs both the
   2-or-3-space month gap and any author overflow. Guard against lines shorter
   than 32 runes.
4. `rest = strings.TrimSpace(string(r[32:]))`, then `fields := strings.Fields(rest)`:
   - If the **last** token matches an ID pattern, pop it as `id`:
     - ASIN: `^B[0-9A-Z]{9}$` → `id_type = "asin"`.
     - ISBN: `^\d{13}$` or `^\d{9}[\dXx]$` → `id_type = "isbn"`.
   - Pop the next token as the **genre** if it matches `^[A-Za-z]{1,2}\*?$`
     (codes are one or two letters — see `FR`); strip any trailing `*`
     (dropped per user decision); map the code to a name.
   - `title = strings.Join(remaining, " ")`. Internal whitespace is normalized
     to single spaces, which is safe for this data (titles use single spaces).
   - If nothing remains for the title, the line is unparseable → `ok = false`.

### Genre mapping

| Code | Name         |
|------|--------------|
| F    | Fiction      |
| N    | Non-fiction  |
| I    | IT           |
| C    | Christian    |
| B    | Business     |
| K    | Kids         |
| FR   | French       |

This is the complete set of codes in the file (the eight codes account for
all 928 non-blank lines). The `*` suffix (e.g. `C*`) is **dropped** — `C*`
becomes `Christian`. An unknown code passes through as the raw code and
triggers a warning, so nothing is ever silently lost.

`parseBooklog(content string) (books []Book, skipped []SkippedLine)` iterates
the lines, calling `parseLine`, and returns the parsed books plus a list of
skipped lines (line number + content). It is pure — the caller (`run`) does
the logging, mirroring how `sync_wishlist` returns `dupes` for the caller to
log.

## Error handling

- Unreadable or missing input file → error, exit 1.
- Unparseable lines are skipped, never abort the run, and are surfaced as
  warnings carrying their line number and content.
- The `-o` write is atomic (temp file in the same directory, then rename), so
  a failure never truncates an existing output file.

## Testing

Table tests on `parseLine` covering:

- ISBN-13, ASIN (`B...`), and ISBN-10 trailing IDs.
- A no-ID older entry.
- `C*` with the star dropped.
- Long-author overflow (Csikszentmihalyi; Stephens-Davidowitz).
- Long-title overflow (Kuhn; LeClerq).
- Author-less line (`VPN`).
- A multibyte-author line (`China Miéville` / `Sylvie Lainé`) to lock in
  rune-based column slicing.
- The two-letter `FR` genre code (French).
- An unknown genre code (passthrough + flagged).
- Month pivot in both centuries (e.g. `05/26` → `2026-05`, `11/97` →
  `1997-11`).
- Blank line → skipped.

Plus a `parseBooklog` test over a small multi-line fixture (asserting both
parsed books and the skipped-line list) and a JSON marshal round-trip checking
key order and `omitempty` behavior.
