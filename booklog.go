package kobolt

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// BooklogEntry is one parsed entry from the reading log. The struct field order
// fixes the JSON key order; ID/IDType are omitempty because older entries carry
// neither. (Distinct from Book, which is a price-snapshot record.)
type BooklogEntry struct {
	Month  string `json:"month"`
	Author string `json:"author"`
	Title  string `json:"title"`
	Genre  string `json:"genre"`
	ID     string `json:"id,omitempty"`
	IDType string `json:"id_type,omitempty"`
}

// SkippedLine records a non-blank line that parseLine could not turn into a
// BooklogEntry, for the caller to report. LineNo is 1-based.
type SkippedLine struct {
	LineNo int
	Text   string
}

// AuthorCount pairs an author with the number of entries attributed to them.
// CollateAuthors returns these sorted by descending Count so a caller can walk
// authors in decreasing-popularity order.
type AuthorCount struct {
	Author string
	Count  int
}

// titleCol is the rune column where the title field begins. The log is
// fixed-column in origin but fields overflow their padding, so we anchor the
// author/title boundary at this rune index and right-anchor genre+id by token
// pattern. Slicing is by rune (not byte) so multibyte authors (e.g. "China
// Miéville") stay aligned with the column the human typed against.
const titleCol = 32

// yearPivot maps a two-digit year to its century: yy <= yearPivot -> 20yy,
// else 19yy. The log spans 1997-2026, so 26 is the boundary. Bump this each
// year as the log gains newer entries — a 2-digit year above it is read as
// 19xx, so an un-bumped pivot would misparse e.g. 01/27 as 1927-01.
const yearPivot = 26

var (
	monthRe = regexp.MustCompile(`^(\d{2})/(\d{2})$`)
	genreRe = regexp.MustCompile(`^[A-Za-z]{1,2}\*?$`)
	isbnRe  = regexp.MustCompile(`^(?:\d{13}|\d{9}[\dXx])$`)
	asinRe  = regexp.MustCompile(`^B[0-9A-Z]{9}$`)
)

// genreNames maps the log's genre codes to display names. The * suffix is
// stripped before lookup. The seven codes below account for every non-blank
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

// parseLine parses one log line into a BooklogEntry. ok is false for lines that
// should be skipped: blank lines, lines shorter than the title column, lines
// without a valid MM/YY month, and lines that leave no title text.
//
// The log is fixed-column but fields overflow their padding, so the parse is a
// hybrid: the month is the leading MM/YY token, the author/title boundary is
// the rune column titleCol, and the genre + optional id are right-anchored by
// token pattern (which is robust to long titles that crowd the genre column).
func parseLine(line string) (BooklogEntry, bool) {
	line = strings.TrimRight(line, " \t")
	if strings.TrimSpace(line) == "" {
		return BooklogEntry{}, false
	}

	r := []rune(line)
	if len(r) < titleCol {
		return BooklogEntry{}, false
	}

	month, ok := normalizeMonth(strings.TrimSpace(string(r[:5])))
	if !ok {
		return BooklogEntry{}, false
	}

	b := BooklogEntry{
		Month:  month,
		Author: strings.TrimSpace(string(r[5:titleCol])),
	}

	fields := strings.Fields(strings.TrimSpace(string(r[titleCol:])))
	if len(fields) == 0 {
		return BooklogEntry{}, false
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
		return BooklogEntry{}, false
	}
	return b, true
}

// ParseBooklog parses the full log content into entries, returning any
// non-blank lines it could not parse (with 1-based line numbers) for the caller
// to report. It is pure: no I/O, no logging. Blank lines are skipped silently.
func ParseBooklog(content string) (entries []BooklogEntry, skipped []SkippedLine) {
	for i, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if b, ok := parseLine(line); ok {
			entries = append(entries, b)
		} else {
			skipped = append(skipped, SkippedLine{LineNo: i + 1, Text: line})
		}
	}
	return entries, skipped
}

// LoadBooklog reads a parsed-booklog JSON file (the output of ParseBooklog, as
// written by the parse_booklog command) into a slice of entries. A missing or
// unreadable file is an error, since the caller asked for a specific path.
func LoadBooklog(path string) ([]BooklogEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []BooklogEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return entries, nil
}

// CollateAuthors tallies entries by author and returns the counts sorted by
// descending frequency, ties broken by author name ascending, so a caller can
// walk authors in decreasing-popularity order. Entries with an empty author are
// not counted.
func CollateAuthors(entries []BooklogEntry) []AuthorCount {
	counts := make(map[string]int)
	for _, e := range entries {
		if e.Author == "" {
			continue
		}
		counts[e.Author]++
	}

	out := make([]AuthorCount, 0, len(counts))
	for author, n := range counts {
		out = append(out, AuthorCount{Author: author, Count: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Author < out[j].Author
	})
	return out
}
