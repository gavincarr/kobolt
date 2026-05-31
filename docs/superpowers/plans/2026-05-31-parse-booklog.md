# parse_booklog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `cmd/parse_booklog` Go CLI that parses the fixed-column reading log at `~/Books` into structured JSON, emitted to stdout or a file via `-o/--outfile`.

**Architecture:** Pure parsing logic in `parse.go` (no I/O, table-testable like `sync_wishlist`'s `parseURLList`), CLI wiring in `main.go`. The parser is a hybrid: front-anchor the month, split author/title by **rune** column 32, and right-anchor genre + ISBN/ASIN by token pattern. Rune-based slicing (not byte) keeps multibyte authors like `China Miéville` aligned.

**Tech Stack:** Go 1.26, `github.com/jessevdk/go-flags` (the repo's standard, not the user's global `kong` default), `log/slog` + `github.com/lmittmann/tint`, `github.com/gavincarr/kobolt/internal/env`. Stdlib `encoding/json`, `regexp`.

**Spec:** `docs/superpowers/specs/2026-05-31-parse-booklog-design.md`

---

## File Structure

- **Create** `cmd/parse_booklog/parse.go` — pure parsing: `Book`/`SkippedLine` types, `parseLine`, `parseBooklog`, and the `normalizeMonth`/`matchID`/`genreName` helpers. No I/O.
- **Create** `cmd/parse_booklog/parse_test.go` — table tests for all of the above.
- **Create** `cmd/parse_booklog/main.go` — CLI wiring: `Options`, `main`, `run`, `expandHome`, `atomicWrite`.
- **Modify** `CLAUDE.md` — document the new command.

---

### Task 1: Pure helpers — types, `normalizeMonth`, `matchID`, `genreName`

**Files:**
- Create: `cmd/parse_booklog/parse.go`
- Test: `cmd/parse_booklog/parse_test.go`

- [ ] **Step 1: Write `parse.go` with the types, constants, regexes, and helpers**

```go
package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Book is one parsed entry from the reading log. The struct field order fixes
// the JSON key order; id/id_type are omitempty because older entries carry
// neither.
type Book struct {
	Month  string `json:"month"`
	Author string `json:"author"`
	Title  string `json:"title"`
	Genre  string `json:"genre"`
	ID     string `json:"id,omitempty"`
	IDType string `json:"id_type,omitempty"`
}

// SkippedLine records a non-blank line that parseLine could not turn into a
// Book, for the caller to report. LineNo is 1-based.
type SkippedLine struct {
	LineNo int
	Text   string
}

// titleCol is the rune column where the title field begins. The log is
// fixed-column in origin but fields overflow their padding, so we anchor the
// author/title boundary at this rune index and right-anchor genre+id by token
// pattern. Slicing is by rune (not byte) so multibyte authors (e.g. "China
// Miéville") stay aligned with the column the human typed against.
const titleCol = 32

// yearPivot maps a two-digit year to its century: yy <= yearPivot -> 20yy,
// else 19yy. The log spans 1997-2026, so 26 is the boundary.
const yearPivot = 26

var (
	monthRe = regexp.MustCompile(`^(\d{2})/(\d{2})$`)
	genreRe = regexp.MustCompile(`^[A-Za-z]{1,2}\*?$`)
	isbnRe  = regexp.MustCompile(`^(?:\d{13}|\d{9}[\dXx])$`)
	asinRe  = regexp.MustCompile(`^B[0-9A-Z]{9}$`)
)

// genreNames maps the log's genre codes to display names. The * suffix is
// stripped before lookup. The eight codes below account for every non-blank
// line in the log; an unrecognised code falls back to the raw code so nothing
// is silently lost.
var genreNames = map[string]string{
	"F":  "Fiction",
	"N":  "Non-fiction",
	"I":  "IT",
	"C":  "Christian",
	"B":  "Business",
	"K":  "Kids",
	"FR": "French",
}

// normalizeMonth parses a "MM/YY" string into "YYYY-MM", applying the
// two-digit-year pivot. It returns ok=false when the input is not MM/YY or the
// month is out of range.
func normalizeMonth(s string) (string, bool) {
	m := monthRe.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	mm, _ := strconv.Atoi(m[1])
	yy, _ := strconv.Atoi(m[2])
	if mm < 1 || mm > 12 {
		return "", false
	}
	year := 1900 + yy
	if yy <= yearPivot {
		year = 2000 + yy
	}
	return fmt.Sprintf("%04d-%02d", year, mm), true
}

// matchID classifies a trailing token as an ISBN or Amazon ASIN. ok is false
// when the token is neither (older log entries have no id).
func matchID(tok string) (id, idType string, ok bool) {
	switch {
	case asinRe.MatchString(tok):
		return tok, "asin", true
	case isbnRe.MatchString(tok):
		return tok, "isbn", true
	}
	return "", "", false
}

// genreName maps a genre code (with any trailing * stripped) to its display
// name, falling back to the raw code for anything unrecognised.
func genreName(code string) string {
	code = strings.TrimSuffix(code, "*")
	if name, ok := genreNames[code]; ok {
		return name
	}
	return code
}
```

- [ ] **Step 2: Write `parse_test.go` with helper unit tests**

```go
package main

import (
	"reflect"
	"testing"
)

func TestNormalizeMonth(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOk bool
	}{
		{"05/26", "2026-05", true},  // pivot: yy<=26 -> 20yy
		{"11/97", "1997-11", true},  // pivot: yy>26 -> 19yy
		{"01/00", "2000-01", true},  // boundary low
		{"12/26", "2026-12", true},  // boundary high (==pivot)
		{"06/27", "1927-06", true},  // just past pivot
		{"13/20", "", false},        // month out of range
		{"00/20", "", false},        // month out of range
		{"2026-05", "", false},      // wrong shape
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := normalizeMonth(tt.in)
		if got != tt.want || ok != tt.wantOk {
			t.Errorf("normalizeMonth(%q) = (%q, %v), want (%q, %v)", tt.in, got, ok, tt.want, tt.wantOk)
		}
	}
}

func TestMatchID(t *testing.T) {
	tests := []struct {
		in         string
		wantID     string
		wantType   string
		wantOk     bool
	}{
		{"9781857988062", "9781857988062", "isbn", true}, // ISBN-13
		{"0958651728", "0958651728", "isbn", true},       // ISBN-10
		{"080652121X", "080652121X", "isbn", true},       // ISBN-10 with X check digit
		{"B09VPKZR3G", "B09VPKZR3G", "asin", true},        // ASIN
		{"F", "", "", false},                              // genre code
		{"The", "", "", false},                            // title word
		{"", "", "", false},
	}
	for _, tt := range tests {
		id, typ, ok := matchID(tt.in)
		if id != tt.wantID || typ != tt.wantType || ok != tt.wantOk {
			t.Errorf("matchID(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.in, id, typ, ok, tt.wantID, tt.wantType, tt.wantOk)
		}
	}
}

func TestGenreName(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"F", "Fiction"},
		{"N", "Non-fiction"},
		{"I", "IT"},
		{"C", "Christian"},
		{"B", "Business"},
		{"K", "Kids"},
		{"FR", "French"},
		{"C*", "Christian"}, // star stripped
		{"Z", "Z"},          // unknown -> passthrough
	}
	for _, tt := range tests {
		if got := genreName(tt.in); got != tt.want {
			t.Errorf("genreName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	_ = reflect.DeepEqual // keep import stable for later tasks
}
```

- [ ] **Step 3: Run the tests to verify they pass**

Run: `go test ./cmd/parse_booklog/`
Expected: PASS (`ok  github.com/gavincarr/kobolt/cmd/parse_booklog`)

- [ ] **Step 4: Commit**

```bash
git add cmd/parse_booklog/parse.go cmd/parse_booklog/parse_test.go
git commit -m "feat(parse_booklog): pure helpers for month/id/genre parsing"
```

---

### Task 2: `parseLine` — the hybrid column/token parser

**Files:**
- Modify: `cmd/parse_booklog/parse.go`
- Test: `cmd/parse_booklog/parse_test.go`

- [ ] **Step 1: Add the `parseLine` table test**

Append to `parse_test.go`. Lines 1-10 are copied verbatim from the real log (confirmed byte-for-byte); the `unknown_genre` line is hand-aligned so the title starts at rune column 32.

```go
func TestParseLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   Book
		wantOk bool
	}{
		{
			name:   "isbn13",
			line:   "05/26   Olaf Stapledon          The Last and First Men                      F   9781857988062",
			want:   Book{Month: "2026-05", Author: "Olaf Stapledon", Title: "The Last and First Men", Genre: "Fiction", ID: "9781857988062", IDType: "isbn"},
			wantOk: true,
		},
		{
			name:   "asin",
			line:   "12/25   Balaji Srinivasan       The Network State                           N   B09VPKZR3G",
			want:   Book{Month: "2025-12", Author: "Balaji Srinivasan", Title: "The Network State", Genre: "Non-fiction", ID: "B09VPKZR3G", IDType: "asin"},
			wantOk: true,
		},
		{
			name:   "isbn10",
			line:   "08/16   Neil Jenman             Real Estate Mistakes                        N   0958651728",
			want:   Book{Month: "2016-08", Author: "Neil Jenman", Title: "Real Estate Mistakes", Genre: "Non-fiction", ID: "0958651728", IDType: "isbn"},
			wantOk: true,
		},
		{
			name:   "no_id",
			line:   "11/15   Douglas Hubbard         How to Measure Anything                     B",
			want:   Book{Month: "2015-11", Author: "Douglas Hubbard", Title: "How to Measure Anything", Genre: "Business"},
			wantOk: true,
		},
		{
			name:   "star_dropped",
			line:   "09/97   Andrew Louth            Origins/Christian Mystical Tradition    C*",
			want:   Book{Month: "1997-09", Author: "Andrew Louth", Title: "Origins/Christian Mystical Tradition", Genre: "Christian"},
			wantOk: true,
		},
		{
			name:   "long_author_overflow",
			line:   "01/22  Seth Stephens-Davidowitz Everybody Lies                              N   9781408894736",
			want:   Book{Month: "2022-01", Author: "Seth Stephens-Davidowitz", Title: "Everybody Lies", Genre: "Non-fiction", ID: "9781408894736", IDType: "isbn"},
			wantOk: true,
		},
		{
			name:   "multibyte_author",
			line:   "07/12   China Miéville          The City and the City                       F",
			want:   Book{Month: "2012-07", Author: "China Miéville", Title: "The City and the City", Genre: "Fiction"},
			wantOk: true,
		},
		{
			name:   "long_title_overflow",
			line:   "06/97   Thomas S. Kuhn          The Structure of Scientific Revolutions N",
			want:   Book{Month: "1997-06", Author: "Thomas S. Kuhn", Title: "The Structure of Scientific Revolutions", Genre: "Non-fiction"},
			wantOk: true,
		},
		{
			name:   "author_less",
			line:   "06/00                           VPN                                     I",
			want:   Book{Month: "2000-06", Author: "", Title: "VPN", Genre: "IT"},
			wantOk: true,
		},
		{
			name:   "fr_genre_multibyte_author",
			line:   "12/16   Sylvie Lainé            Voyage en France (Part 2)                   FR  9782370610072",
			want:   Book{Month: "2016-12", Author: "Sylvie Lainé", Title: "Voyage en France (Part 2)", Genre: "French", ID: "9782370610072", IDType: "isbn"},
			wantOk: true,
		},
		{
			name:   "unknown_genre_passthrough",
			line:   "05/20   Some Author             Some Title                                  Z",
			want:   Book{Month: "2020-05", Author: "Some Author", Title: "Some Title", Genre: "Z"},
			wantOk: true,
		},
		{
			name:   "blank_line",
			line:   "   ",
			want:   Book{},
			wantOk: false,
		},
		{
			name:   "no_valid_month",
			line:   "notamonth  Some Author             Some Title                              F",
			want:   Book{},
			wantOk: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseLine(tt.line)
			if ok != tt.wantOk {
				t.Fatalf("parseLine() ok = %v, want %v", ok, tt.wantOk)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseLine() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/parse_booklog/ -run TestParseLine`
Expected: FAIL — `undefined: parseLine`

- [ ] **Step 3: Implement `parseLine` in `parse.go`**

Add after `genreName`:

```go
// parseLine parses one log line into a Book. ok is false for lines that should
// be skipped: blank lines, lines shorter than the title column, lines without
// a valid MM/YY month, and lines that leave no title text.
//
// The log is fixed-column but fields overflow their padding, so the parse is a
// hybrid: the month is the leading MM/YY token, the author/title boundary is
// the rune column titleCol, and the genre + optional id are right-anchored by
// token pattern (which is robust to long titles that crowd the genre column).
func parseLine(line string) (Book, bool) {
	line = strings.TrimRight(line, " \t")
	if strings.TrimSpace(line) == "" {
		return Book{}, false
	}

	r := []rune(line)
	if len(r) < titleCol {
		return Book{}, false
	}

	month, ok := normalizeMonth(strings.TrimSpace(string(r[:5])))
	if !ok {
		return Book{}, false
	}

	b := Book{
		Month:  month,
		Author: strings.TrimSpace(string(r[5:titleCol])),
	}

	fields := strings.Fields(strings.TrimSpace(string(r[titleCol:])))
	if len(fields) == 0 {
		return Book{}, false
	}

	// Right-anchor: pop an optional trailing id, then the genre code.
	if id, idType, ok := matchID(fields[len(fields)-1]); ok {
		b.ID, b.IDType = id, idType
		fields = fields[:len(fields)-1]
	}
	if len(fields) > 0 && genreRe.MatchString(fields[len(fields)-1]) {
		b.Genre = genreName(fields[len(fields)-1])
		fields = fields[:len(fields)-1]
	}

	b.Title = strings.Join(fields, " ")
	if b.Title == "" {
		return Book{}, false
	}
	return b, true
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/parse_booklog/ -run TestParseLine -v`
Expected: PASS — all subtests `--- PASS`

- [ ] **Step 5: Commit**

```bash
git add cmd/parse_booklog/parse.go cmd/parse_booklog/parse_test.go
git commit -m "feat(parse_booklog): parseLine hybrid column/token parser"
```

---

### Task 3: `parseBooklog` — whole-file parse with skipped-line reporting

**Files:**
- Modify: `cmd/parse_booklog/parse.go`
- Test: `cmd/parse_booklog/parse_test.go`

- [ ] **Step 1: Add the `parseBooklog` test**

Append to `parse_test.go`:

```go
func TestParseBooklog(t *testing.T) {
	content := "05/26   Olaf Stapledon          The Last and First Men                      F   9781857988062\n" +
		"\n" + // blank line: skipped silently, not reported
		"11/15   Douglas Hubbard         How to Measure Anything                     B\n" +
		"notamonth  Bad Line                Whatever                                F\n" // unparseable: reported

	books, skipped := parseBooklog(content)

	wantBooks := []Book{
		{Month: "2026-05", Author: "Olaf Stapledon", Title: "The Last and First Men", Genre: "Fiction", ID: "9781857988062", IDType: "isbn"},
		{Month: "2015-11", Author: "Douglas Hubbard", Title: "How to Measure Anything", Genre: "Business"},
	}
	if !reflect.DeepEqual(books, wantBooks) {
		t.Errorf("books = %#v, want %#v", books, wantBooks)
	}

	if len(skipped) != 1 {
		t.Fatalf("skipped = %#v, want exactly 1 entry", skipped)
	}
	if skipped[0].LineNo != 4 {
		t.Errorf("skipped[0].LineNo = %d, want 4", skipped[0].LineNo)
	}
}

func TestParseBooklogEmpty(t *testing.T) {
	books, skipped := parseBooklog("")
	if books != nil {
		t.Errorf("books = %#v, want nil", books)
	}
	if skipped != nil {
		t.Errorf("skipped = %#v, want nil", skipped)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/parse_booklog/ -run TestParseBooklog`
Expected: FAIL — `undefined: parseBooklog`

- [ ] **Step 3: Implement `parseBooklog` in `parse.go`**

Add after `parseLine`:

```go
// parseBooklog parses the full log content into books, returning any non-blank
// lines it could not parse (with 1-based line numbers) for the caller to
// report. It is pure: no I/O, no logging. Blank lines are skipped silently.
func parseBooklog(content string) (books []Book, skipped []SkippedLine) {
	for i, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if b, ok := parseLine(line); ok {
			books = append(books, b)
		} else {
			skipped = append(skipped, SkippedLine{LineNo: i + 1, Text: line})
		}
	}
	return books, skipped
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./cmd/parse_booklog/ -run TestParseBooklog -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/parse_booklog/parse.go cmd/parse_booklog/parse_test.go
git commit -m "feat(parse_booklog): parseBooklog with skipped-line reporting"
```

---

### Task 4: `main.go` — CLI wiring, home expansion, atomic write

**Files:**
- Create: `cmd/parse_booklog/main.go`

- [ ] **Step 1: Write `main.go`**

```go
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/gavincarr/kobolt/internal/env"
	"github.com/jessevdk/go-flags"
	"github.com/lmittmann/tint"
)

type Options struct {
	Outfile string `short:"o" long:"outfile" description:"Write JSON output to FILE instead of stdout"`
	Verbose []bool `short:"v" long:"verbose" description:"Verbose output: -v logs a parse summary (info), -vv also logs skipped lines (debug)"`

	Args struct {
		Input string `positional-arg-name:"input" description:"Input booklog path (default ~/Books)"`
	} `positional-args:"yes"`
}

const defaultInput = "~/Books"

func main() {
	env.Load()

	var opts Options
	if _, err := flags.NewParser(&opts, flags.Default).Parse(); err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		}
		os.Exit(1)
	}

	// Silent by default: skipped lines surface as warnings. -v adds the parse
	// summary (info); -vv itemises skipped lines (debug).
	level := slog.LevelWarn
	switch {
	case len(opts.Verbose) >= 2:
		level = slog.LevelDebug
	case len(opts.Verbose) >= 1:
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: level})))

	if err := run(opts); err != nil {
		slog.Error("failed", "error", err)
		os.Exit(1)
	}
}

func run(opts Options) error {
	inPath := opts.Args.Input
	if inPath == "" {
		inPath = defaultInput
	}
	inPath = expandHome(inPath)

	content, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", inPath, err)
	}

	books, skipped := parseBooklog(string(content))

	if len(skipped) > 0 {
		slog.Warn("skipped unparseable lines", "count", len(skipped))
		for _, s := range skipped {
			slog.Debug("skipped line", "line", s.LineNo, "text", s.Text)
		}
	}
	slog.Info("parsed booklog", "books", len(books), "skipped", len(skipped), "path", inPath)

	out, err := json.MarshalIndent(books, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	out = append(out, '\n')

	if opts.Outfile == "" {
		_, err = os.Stdout.Write(out)
		return err
	}
	return atomicWrite(opts.Outfile, out)
}

// expandHome replaces a leading ~ or ~/ with the user's home directory.
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// atomicWrite writes data to a temp file in the same directory as path, then
// renames it over path, so a failure never truncates an existing output file.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".booklog-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
```

- [ ] **Step 2: Build the command**

Run: `go build ./cmd/parse_booklog`
Expected: builds with no output, no errors.

- [ ] **Step 3: Run the full test suite and vet**

Run: `go test ./... && go vet ./cmd/parse_booklog`
Expected: PASS for all packages; vet clean.

- [ ] **Step 4: Smoke-test against the real log**

Run: `go run ./cmd/parse_booklog | head -20`
Expected: a JSON array; first object is the most recent entry, e.g.
```json
[
  {
    "month": "2026-05",
    "author": "Olaf Stapledon",
    "title": "The Last and First Men",
    "genre": "Fiction",
    "id": "9781857988062",
    "id_type": "isbn"
  },
```

Run: `go run ./cmd/parse_booklog -v >/dev/null`
Expected: a single info line on stderr like `parsed booklog books=928 skipped=0 path=/home/gavin/Books` (skipped=0 confirms every line parsed; if non-zero, inspect with `-vv`).

- [ ] **Step 5: Verify `-o` writes a file atomically**

Run: `go run ./cmd/parse_booklog -o /tmp/booklog.json && python3 -c "import json;print(len(json.load(open('/tmp/booklog.json'))))"`
Expected: prints the book count (e.g. `928`) — confirms valid JSON written to the file.

- [ ] **Step 6: Commit**

```bash
git add cmd/parse_booklog/main.go
git commit -m "feat(parse_booklog): CLI wiring, ~ expansion, atomic -o write"
```

---

### Task 5: Document the command in CLAUDE.md

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update the Overview command list**

In the `## Overview` paragraph, the commands sentence currently reads:

> Commands live under `cmd/`: `get_list_prices` (the scraper), `diff_list_prices` and `arb_list_prices` (analyse snapshots), and `sync_wishlist` (pulls the URL list down from a Google Sheet — see below).

Replace it with:

> Commands live under `cmd/`: `get_list_prices` (the scraper), `diff_list_prices` and `arb_list_prices` (analyse snapshots), `sync_wishlist` (pulls the URL list down from a Google Sheet — see below), and `parse_booklog` (parses the `~/Books` reading log into JSON — see below).

- [ ] **Step 2: Add a section documenting `parse_booklog`**

Insert this section immediately before `## Architecture and non-obvious decisions`:

````markdown
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
````

- [ ] **Step 3: Commit**

```bash
git add CLAUDE.md
git commit -m "doc(parse_booklog): document the ~/Books reading-log parser"
```

---

## Notes for the implementer

- **Run all `go` commands from the repo root** (`/home/gavin/work/kobolt`).
- **`~/Books` is a symlink** to `~/Important/Books`; reading through it works normally. It is real personal data — read-only; never write to it.
- **Do not push.** Per the user's standing instruction, commit locally only and stop after the final commit.
- The test fixture lines in Task 2 are byte-exact copies from the real log (including the 2-space gap on the `Seth Stephens-Davidowitz` line and the multibyte `é` in `Miéville`/`Lainé`). Preserve them exactly — the parse depends on rune column 32.
