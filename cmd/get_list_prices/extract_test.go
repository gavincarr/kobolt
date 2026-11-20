package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gavincarr/kobolt"
)

// Fixtures in testdata/ are trimmed excerpts of real pages saved 2026-11-20:
//   - gizmo_us_fundamentals.json: the decoded data-kobo-gizmo-config attribute
//     from the legacy-template US page.
//   - rsc_*.txt: whole RSC rows from the decoded self.__next_f.push payload of
//     new-template pages, in original order. The AU/MY files keep the
//     product's itemDetails row, the priceDetail row and decoy rows (header
//     nav titles, RelatedReads books with their own titles/authors).
//   - rsc_my_oathbringer_midstream.txt: the first rows of a page captured
//     before itemDetails/priceDetail had streamed in.
func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// dropRows removes RSC rows (lines) containing substr.
func dropRows(text, substr string) string {
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, l := range lines {
		if !strings.Contains(l, substr) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func TestParseGizmoConfig(t *testing.T) {
	raw := readFixture(t, "gizmo_us_fundamentals.json")
	rp := &kobolt.RegionPrice{}
	isbn, title, author, err := parseGizmoConfig(raw, rp)
	if err != nil {
		t.Fatal(err)
	}
	if isbn != "9781098108250" || title != "Fundamentals of Data Engineering" || author != "Joe Reis, Matt Housley" {
		t.Errorf("got isbn=%q title=%q author=%q", isbn, title, author)
	}
	if rp.Price != 48.99 || rp.ListPrice != 67.99 || rp.Currency != "USD" {
		t.Errorf("got price=%v list=%v currency=%q", rp.Price, rp.ListPrice, rp.Currency)
	}
}

func TestParseRSC(t *testing.T) {
	tests := []struct {
		fixture   string
		price     float64
		listPrice float64
		currency  string
	}{
		{"rsc_au_oathbringer.txt", 14.99, 14.99, "AUD"},
		{"rsc_my_oathbringer_sale.txt", 55.49, 65.23, "MYR"},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			rp := &kobolt.RegionPrice{}
			isbn, title, author, err := parseRSC(readFixture(t, tt.fixture), rp)
			if err != nil {
				t.Fatal(err)
			}
			if isbn != "9780575093355" || title != "Oathbringer" || author != "Brandon Sanderson" {
				t.Errorf("got isbn=%q title=%q author=%q", isbn, title, author)
			}
			if rp.Price != tt.price || rp.ListPrice != tt.listPrice || rp.Currency != tt.currency {
				t.Errorf("got price=%v list=%v currency=%q, want %v %v %q",
					rp.Price, rp.ListPrice, rp.Currency, tt.price, tt.listPrice, tt.currency)
			}
		})
	}
}

func TestParseRSCNoPriceDetail(t *testing.T) {
	au := readFixture(t, "rsc_au_oathbringer.txt")
	i := strings.Index(au, `"priceDetail":`)
	if i < 0 {
		t.Fatal("fixture has no priceDetail")
	}
	tests := []struct {
		name string
		text string
	}{
		{"midstream", readFixture(t, "rsc_my_oathbringer_midstream.txt")},
		{"itemDetails without priceDetail", dropRows(au, `"priceDetail":`)},
		{"priceDetail truncated mid-stream", au[:i+40]},
		{"empty", ""},
		{"null value", `1:{"priceDetail":null}`},
		{"undefined string", `1:{"priceDetail":"$undefined"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rp := &kobolt.RegionPrice{}
			_, _, _, err := parseRSC(tt.text, rp)
			if !errors.Is(err, errNoPriceDetail) {
				t.Fatalf("err = %v, want errNoPriceDetail", err)
			}
			if rp.Price != 0 || rp.ListPrice != 0 || rp.Currency != "" {
				t.Errorf("rp modified: %+v", rp)
			}
		})
	}
}

func TestRSCMetadata(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		wantOK bool
	}{
		{"au without priceDetail", dropRows(readFixture(t, "rsc_au_oathbringer.txt"), `"priceDetail":`), true},
		{"my sale", readFixture(t, "rsc_my_oathbringer_sale.txt"), true},
		{"midstream", readFixture(t, "rsc_my_oathbringer_midstream.txt"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isbn, title, author, ok := rscMetadata(tt.text)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if isbn != "9780575093355" || title != "Oathbringer" || author != "Brandon Sanderson" {
				t.Errorf("got isbn=%q title=%q author=%q", isbn, title, author)
			}
		})
	}
}

func TestRSCAuthors(t *testing.T) {
	tests := []struct {
		name string
		cs   []rscContributor
		want string
	}{
		{"single", []rscContributor{{Name: "Brandon Sanderson", Type: "Author"}}, "Brandon Sanderson"},
		{"multiple, narrator excluded", []rscContributor{
			{Name: "Joe Reis", Type: "Author"},
			{Name: "Kate Reading", Type: "Narrator", IsNarrator: true},
			{Name: "Matt Housley", Type: "Author"},
		}, "Joe Reis, Matt Housley"},
		{"none", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := rscAuthors(tt.cs); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
