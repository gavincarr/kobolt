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
