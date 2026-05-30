package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseURLList(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantURLs  []string
		wantDupes map[string]int
	}{
		{
			name:      "empty input",
			body:      "",
			wantURLs:  nil,
			wantDupes: nil,
		},
		{
			name:      "whitespace only",
			body:      "  \n\t\n   \n",
			wantURLs:  nil,
			wantDupes: nil,
		},
		{
			name:      "single url",
			body:      "https://www.kobo.com/au/en/ebook/a",
			wantURLs:  []string{"https://www.kobo.com/au/en/ebook/a"},
			wantDupes: nil,
		},
		{
			name:      "already sorted",
			body:      "https://www.kobo.com/au/en/ebook/a\nhttps://www.kobo.com/au/en/ebook/b\nhttps://www.kobo.com/au/en/ebook/c",
			wantURLs:  []string{"https://www.kobo.com/au/en/ebook/a", "https://www.kobo.com/au/en/ebook/b", "https://www.kobo.com/au/en/ebook/c"},
			wantDupes: nil,
		},
		{
			name:      "unsorted gets sorted",
			body:      "https://www.kobo.com/au/en/ebook/c\nhttps://www.kobo.com/au/en/ebook/a\nhttps://www.kobo.com/au/en/ebook/b",
			wantURLs:  []string{"https://www.kobo.com/au/en/ebook/a", "https://www.kobo.com/au/en/ebook/b", "https://www.kobo.com/au/en/ebook/c"},
			wantDupes: nil,
		},
		{
			name:      "blank lines dropped",
			body:      "https://www.kobo.com/au/en/ebook/a\n\n\nhttps://www.kobo.com/au/en/ebook/b\n",
			wantURLs:  []string{"https://www.kobo.com/au/en/ebook/a", "https://www.kobo.com/au/en/ebook/b"},
			wantDupes: nil,
		},
		{
			name:      "trailing newline",
			body:      "https://www.kobo.com/au/en/ebook/a\nhttps://www.kobo.com/au/en/ebook/b\n",
			wantURLs:  []string{"https://www.kobo.com/au/en/ebook/a", "https://www.kobo.com/au/en/ebook/b"},
			wantDupes: nil,
		},
		{
			name:      "leading and trailing whitespace trimmed",
			body:      "  https://www.kobo.com/au/en/ebook/a  \n\thttps://www.kobo.com/au/en/ebook/b\t\n",
			wantURLs:  []string{"https://www.kobo.com/au/en/ebook/a", "https://www.kobo.com/au/en/ebook/b"},
			wantDupes: nil,
		},
		{
			name:     "duplicates collapsed",
			body:     "https://www.kobo.com/au/en/ebook/b\nhttps://www.kobo.com/au/en/ebook/a\nhttps://www.kobo.com/au/en/ebook/b\nhttps://www.kobo.com/au/en/ebook/a\nhttps://www.kobo.com/au/en/ebook/a",
			wantURLs: []string{"https://www.kobo.com/au/en/ebook/a", "https://www.kobo.com/au/en/ebook/b"},
			wantDupes: map[string]int{
				"https://www.kobo.com/au/en/ebook/a": 2,
				"https://www.kobo.com/au/en/ebook/b": 1,
			},
		},
		{
			name:     "uppercase cc/lang lowercased and deduped",
			body:     "https://www.kobo.com/AU/EN/ebook/a\nhttps://www.kobo.com/au/en/ebook/a\nhttps://www.kobo.com/AU/en/ebook/b",
			wantURLs: []string{"https://www.kobo.com/au/en/ebook/a", "https://www.kobo.com/au/en/ebook/b"},
			wantDupes: map[string]int{
				"https://www.kobo.com/au/en/ebook/a": 1,
			},
		},
		{
			name:      "duplicate only after trim",
			body:      "https://www.kobo.com/au/en/ebook/a\n  https://www.kobo.com/au/en/ebook/a  ",
			wantURLs:  []string{"https://www.kobo.com/au/en/ebook/a"},
			wantDupes: map[string]int{"https://www.kobo.com/au/en/ebook/a": 1},
		},
		{
			name:      "crlf line endings",
			body:      "https://www.kobo.com/au/en/ebook/b\r\nhttps://www.kobo.com/au/en/ebook/a\r\n",
			wantURLs:  []string{"https://www.kobo.com/au/en/ebook/a", "https://www.kobo.com/au/en/ebook/b"},
			wantDupes: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURLs, gotDupes := parseURLList(tt.body)
			if !reflect.DeepEqual(gotURLs, tt.wantURLs) {
				t.Errorf("parseURLList() urls = %#v, want %#v", gotURLs, tt.wantURLs)
			}
			if !reflect.DeepEqual(gotDupes, tt.wantDupes) {
				t.Errorf("parseURLList() dupes = %#v, want %#v", gotDupes, tt.wantDupes)
			}
		})
	}
}

func TestAtomicWriteSuccess(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "wishlist.txt")
	urls := []string{
		"https://www.kobo.com/au/en/ebook/a",
		"https://www.kobo.com/au/en/ebook/b",
		"https://www.kobo.com/au/en/ebook/c",
	}

	if err := atomicWrite(dest, urls); err != nil {
		t.Fatalf("atomicWrite() error = %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want := "https://www.kobo.com/au/en/ebook/a\nhttps://www.kobo.com/au/en/ebook/b\nhttps://www.kobo.com/au/en/ebook/c\n"
	if string(got) != want {
		t.Errorf("content = %q, want %q", string(got), want)
	}

	// No temp files should be left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected only the destination file, got %d entries", len(entries))
	}
}

func TestAtomicWriteFailureLeavesOriginalIntact(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "wishlist.txt")
	original := "https://www.kobo.com/au/en/ebook/original\n"
	if err := os.WriteFile(dest, []byte(original), 0o644); err != nil {
		t.Fatalf("seed WriteFile() error = %v", err)
	}

	// Make the destination directory read-only so the temp-file create fails.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := atomicWrite(dest, []string{"https://www.kobo.com/au/en/ebook/new"})
	if err == nil {
		t.Fatalf("atomicWrite() expected error when dir is read-only, got nil")
	}

	// Restore write perms to read the file back.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(got) != original {
		t.Errorf("original file was modified: got %q, want %q", string(got), original)
	}
}

func TestFetchURLListSuccess(t *testing.T) {
	body := "https://www.kobo.com/au/en/ebook/c\nhttps://www.kobo.com/au/en/ebook/a\nhttps://www.kobo.com/au/en/ebook/b\n"
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := fetchURLList(srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("fetchURLList() error = %v", err)
	}
	if gotQuery != "action=list" {
		t.Errorf("query = %q, want %q", gotQuery, "action=list")
	}
	want := "https://www.kobo.com/au/en/ebook/c\nhttps://www.kobo.com/au/en/ebook/a\nhttps://www.kobo.com/au/en/ebook/b\n"
	if got != want {
		t.Errorf("body = %q, want %q", got, want)
	}

	urls, dupes := parseURLList(got)
	wantURLs := []string{
		"https://www.kobo.com/au/en/ebook/a",
		"https://www.kobo.com/au/en/ebook/b",
		"https://www.kobo.com/au/en/ebook/c",
	}
	if !reflect.DeepEqual(urls, wantURLs) {
		t.Errorf("parsed urls = %#v, want %#v", urls, wantURLs)
	}
	if len(dupes) != 0 {
		t.Errorf("dupes = %#v, want none", dupes)
	}
}

func TestFetchURLListAppendsToExistingQuery(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		_, _ = w.Write([]byte("https://www.kobo.com/au/en/ebook/a\n"))
	}))
	defer srv.Close()

	if _, err := fetchURLList(srv.Client(), srv.URL+"?foo=bar"); err != nil {
		t.Fatalf("fetchURLList() error = %v", err)
	}
	vals, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("ParseQuery() error = %v", err)
	}
	if vals.Get("action") != "list" {
		t.Errorf("action = %q, want %q (query=%q)", vals.Get("action"), "list", gotQuery)
	}
	if vals.Get("foo") != "bar" {
		t.Errorf("foo = %q, want %q (query=%q)", vals.Get("foo"), "bar", gotQuery)
	}
}

func TestFetchURLListNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := fetchURLList(srv.Client(), srv.URL)
	if err == nil {
		t.Fatalf("fetchURLList() expected error on 500, got nil")
	}
}

func TestFetchURLListNetworkError(t *testing.T) {
	// Server is closed immediately so the connection fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()

	if _, err := fetchURLList(http.DefaultClient, url); err == nil {
		t.Fatalf("fetchURLList() expected network error, got nil")
	}
}
