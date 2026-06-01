package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/gavincarr/kobolt/internal/env"
	helpcolours "github.com/gavincarr/kong-help-colours"
	"github.com/lmittmann/tint"
)

type CLI struct {
	Verbose int `short:"v" type:"counter" help:"Verbose output: -v logs the sync summary (info), -vv also itemises duplicate URLs (debug)"`

	Output string `arg:"" optional:"" help:"Output path for the wishlist (default data/wishlist.txt)"`
}

const defaultOutput = "data/wishlist.txt"

// fetchTimeout bounds the whole request; Apps Script issues a 302 redirect to
// googleusercontent.com which the default http.Client follows.
const fetchTimeout = 30 * time.Second

func main() {
	env.Load()

	var cli CLI
	kong.Parse(&cli,
		kong.Name("sync_wishlist"),
		kong.Description("Pull the wishlist URL list down from the Google Sheet into a local file."),
		kong.Help(helpcolours.Help),
		kong.ShortHelp(helpcolours.ShortHelp),
	)

	// Silent by default: only warnings (duplicate URLs) and errors surface.
	// -v lifts to info (the sync summary), -vv to debug (itemised duplicates).
	level := slog.LevelWarn
	switch {
	case cli.Verbose >= 2:
		level = slog.LevelDebug
	case cli.Verbose >= 1:
		level = slog.LevelInfo
	}
	slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: level})))

	if err := run(cli); err != nil {
		slog.Error("failed", "error", err)
		os.Exit(1)
	}
}

func run(cli CLI) error {
	sheetURL := os.Getenv("KOBOLT_SHEET_URL")
	if sheetURL == "" {
		return errors.New("KOBOLT_SHEET_URL is not set (put it in .env.local)")
	}

	outPath := cli.Output
	if outPath == "" {
		outPath = defaultOutput
	}

	client := &http.Client{Timeout: fetchTimeout}
	body, err := fetchURLList(client, sheetURL)
	if err != nil {
		return fmt.Errorf("fetch sheet: %w", err)
	}

	urls, dupes := parseURLList(body)
	if len(urls) == 0 {
		return fmt.Errorf("sheet returned no URLs; refusing to overwrite %s", outPath)
	}

	if len(dupes) > 0 {
		total := 0
		for _, n := range dupes {
			total += n
		}
		slog.Warn("collapsed duplicate URLs", "count", total)
		// Itemise which URLs were duplicated (sorted) so they're easy to
		// find and clean up in the sheet; visible at -vv.
		dupURLs := make([]string, 0, len(dupes))
		for u := range dupes {
			dupURLs = append(dupURLs, u)
		}
		sort.Strings(dupURLs)
		for _, u := range dupURLs {
			slog.Debug("duplicate url", "url", u, "count", dupes[u]+1)
		}
	}

	if err := atomicWrite(outPath, urls); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	slog.Info("synced wishlist", "count", len(urls), "path", outPath)
	return nil
}

// fetchURLList GETs the sheet's action=list endpoint and returns the raw body.
// The action=list query param is appended to whatever query string base
// already carries. A non-200 status is surfaced as an error.
func fetchURLList(client *http.Client, base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid KOBOLT_SHEET_URL: %w", err)
	}
	q := u.Query()
	q.Set("action", "list")
	u.RawQuery = q.Encode()

	resp, err := client.Get(u.String())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %s", resp.Status)
	}
	return string(body), nil
}

// parseURLList splits the body on newlines, trims each line, lowercases it,
// drops blanks, dedups, and sorts ascending. It returns the unique sorted URLs
// and, for each URL that appeared more than once, the number of collapsed
// (extra) occurrences. dupes is nil when there were no duplicates.
//
// Kobo URLs are case-insensitive but the sheet sometimes carries uppercase
// country/language codes (e.g. /AU/EN/), which would otherwise sort and dedup
// as distinct from /au/en/; lowercasing normalises them.
func parseURLList(body string) (urls []string, dupes map[string]int) {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(body, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			if dupes == nil {
				dupes = make(map[string]int)
			}
			dupes[line]++
			continue
		}
		seen[line] = struct{}{}
		urls = append(urls, line)
	}
	sort.Strings(urls)
	return urls, dupes
}

// atomicWrite writes urls (one per line, trailing newline) to a temp file in
// the same directory as path, then renames it over path. On any failure the
// existing file at path is left untouched.
func atomicWrite(path string, urls []string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".wishlist-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if we don't make it to the rename.
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	var b strings.Builder
	for _, u := range urls {
		b.WriteString(u)
		b.WriteByte('\n')
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
