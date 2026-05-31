package kobolt

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestNormalizeMonth(t *testing.T) {
	tests := []struct {
		in     string
		want   string
		wantOk bool
	}{
		{"05/26", "2026-05", true}, // pivot: yy<=26 -> 20yy
		{"11/97", "1997-11", true}, // pivot: yy>26 -> 19yy
		{"01/00", "2000-01", true}, // boundary low
		{"12/26", "2026-12", true}, // boundary high (==pivot)
		{"06/27", "1927-06", true}, // just past pivot
		{"13/20", "", false},       // month out of range
		{"00/20", "", false},       // month out of range
		{"2026-05", "", false},     // wrong shape
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
		in       string
		wantID   string
		wantType string
		wantOk   bool
	}{
		{"9781857988062", "9781857988062", "isbn", true}, // ISBN-13
		{"0958651728", "0958651728", "isbn", true},       // ISBN-10
		{"080652121X", "080652121X", "isbn", true},       // ISBN-10 with X check digit
		{"B09VPKZR3G", "B09VPKZR3G", "asin", true},       // ASIN
		{"F", "", "", false},                             // genre code
		{"The", "", "", false},                           // title word
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
}

func TestParseBooklog(t *testing.T) {
	content := "05/26   Olaf Stapledon          The Last and First Men                      F   9781857988062\n" +
		"\n" + // blank line: skipped silently, not reported
		"11/15   Douglas Hubbard         How to Measure Anything                     B\n" +
		"notamonth  Bad Line                Whatever                                F\n" // unparseable: reported

	books, skipped := ParseBooklog(content)

	wantBooks := []BooklogEntry{
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
	books, skipped := ParseBooklog("")
	if books != nil {
		t.Errorf("books = %#v, want nil", books)
	}
	if skipped != nil {
		t.Errorf("skipped = %#v, want nil", skipped)
	}
}

func TestParseLine(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		want   BooklogEntry
		wantOk bool
	}{
		{
			name:   "isbn13",
			line:   "05/26   Olaf Stapledon          The Last and First Men                      F   9781857988062",
			want:   BooklogEntry{Month: "2026-05", Author: "Olaf Stapledon", Title: "The Last and First Men", Genre: "Fiction", ID: "9781857988062", IDType: "isbn"},
			wantOk: true,
		},
		{
			name:   "asin",
			line:   "12/25   Balaji Srinivasan       The Network State                           N   B09VPKZR3G",
			want:   BooklogEntry{Month: "2025-12", Author: "Balaji Srinivasan", Title: "The Network State", Genre: "Non-fiction", ID: "B09VPKZR3G", IDType: "asin"},
			wantOk: true,
		},
		{
			name:   "isbn10",
			line:   "08/16   Neil Jenman             Real Estate Mistakes                        N   0958651728",
			want:   BooklogEntry{Month: "2016-08", Author: "Neil Jenman", Title: "Real Estate Mistakes", Genre: "Non-fiction", ID: "0958651728", IDType: "isbn"},
			wantOk: true,
		},
		{
			name:   "no_id",
			line:   "11/15   Douglas Hubbard         How to Measure Anything                     B",
			want:   BooklogEntry{Month: "2015-11", Author: "Douglas Hubbard", Title: "How to Measure Anything", Genre: "Business"},
			wantOk: true,
		},
		{
			name:   "star_dropped",
			line:   "09/97   Andrew Louth            Origins/Christian Mystical Tradition    C*",
			want:   BooklogEntry{Month: "1997-09", Author: "Andrew Louth", Title: "Origins/Christian Mystical Tradition", Genre: "Christian"},
			wantOk: true,
		},
		{
			name:   "long_author_overflow",
			line:   "01/22  Seth Stephens-Davidowitz Everybody Lies                              N   9781408894736",
			want:   BooklogEntry{Month: "2022-01", Author: "Seth Stephens-Davidowitz", Title: "Everybody Lies", Genre: "Non-fiction", ID: "9781408894736", IDType: "isbn"},
			wantOk: true,
		},
		{
			name:   "multibyte_author",
			line:   "07/12   China Miéville          The City and the City                       F",
			want:   BooklogEntry{Month: "2012-07", Author: "China Miéville", Title: "The City and the City", Genre: "Fiction"},
			wantOk: true,
		},
		{
			name:   "long_title_overflow",
			line:   "06/97   Thomas S. Kuhn          The Structure of Scientific Revolutions N",
			want:   BooklogEntry{Month: "1997-06", Author: "Thomas S. Kuhn", Title: "The Structure of Scientific Revolutions", Genre: "Non-fiction"},
			wantOk: true,
		},
		{
			name:   "author_less",
			line:   "06/00                           VPN                                     I",
			want:   BooklogEntry{Month: "2000-06", Author: "", Title: "VPN", Genre: "IT"},
			wantOk: true,
		},
		{
			name:   "fr_genre_multibyte_author",
			line:   "12/16   Sylvie Lainé            Voyage en France (Part 2)                   FR  9782370610072",
			want:   BooklogEntry{Month: "2016-12", Author: "Sylvie Lainé", Title: "Voyage en France (Part 2)", Genre: "French", ID: "9782370610072", IDType: "isbn"},
			wantOk: true,
		},
		{
			name:   "unknown_genre_passthrough",
			line:   "05/20   Some Author             Some Title                                  Z",
			want:   BooklogEntry{Month: "2020-05", Author: "Some Author", Title: "Some Title", Genre: "Z"},
			wantOk: true,
		},
		{
			name:   "blank_line",
			line:   "   ",
			want:   BooklogEntry{},
			wantOk: false,
		},
		{
			name:   "no_valid_month",
			line:   "notamonth  Some Author             Some Title                              F",
			want:   BooklogEntry{},
			wantOk: false,
		},
		{
			name:   "too_short", // non-blank but shorter than the title column
			line:   "01/26 Short Line",
			want:   BooklogEntry{},
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

func TestLoadBooklog(t *testing.T) {
	want := []BooklogEntry{
		{Month: "2026-05", Author: "Olaf Stapledon", Title: "Last and First Men", Genre: "Fiction", ID: "9781857988062", IDType: "isbn"},
		{Month: "2015-11", Author: "Douglas Hubbard", Title: "How to Measure Anything", Genre: "Business"},
	}
	data := `[
  {"month": "2026-05", "author": "Olaf Stapledon", "title": "Last and First Men", "genre": "Fiction", "id": "9781857988062", "id_type": "isbn"},
  {"month": "2015-11", "author": "Douglas Hubbard", "title": "How to Measure Anything", "genre": "Business"}
]`
	path := filepath.Join(t.TempDir(), "books.json")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadBooklog(path)
	if err != nil {
		t.Fatalf("LoadBooklog() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LoadBooklog() = %#v, want %#v", got, want)
	}
}

func TestLoadBooklogMissing(t *testing.T) {
	if _, err := LoadBooklog(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("LoadBooklog() on a missing file: got nil error, want error")
	}
}

func TestCollateAuthors(t *testing.T) {
	entries := []BooklogEntry{
		{Author: "Iain Banks"},
		{Author: "China Miéville"},
		{Author: "Iain Banks"},
		{Author: ""}, // empty author: not counted
		{Author: "China Miéville"},
		{Author: "Iain Banks"},
		{Author: "Ursula Le Guin"},
		{Author: "Ann Leckie"}, // ties with Le Guin at 1, alphabetical first
	}

	got := CollateAuthors(entries)
	want := []AuthorCount{
		{Author: "Iain Banks", Count: 3},
		{Author: "China Miéville", Count: 2},
		{Author: "Ann Leckie", Count: 1},     // tie broken alphabetically
		{Author: "Ursula Le Guin", Count: 1}, // ...before this
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CollateAuthors() = %#v, want %#v", got, want)
	}
}

func TestCollateAuthorsEmpty(t *testing.T) {
	if got := CollateAuthors(nil); len(got) != 0 {
		t.Errorf("CollateAuthors(nil) = %#v, want empty", got)
	}
}
