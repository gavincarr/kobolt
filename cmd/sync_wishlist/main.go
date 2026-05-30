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

	"github.com/gavincarr/kobolt/internal/env"
	"github.com/jessevdk/go-flags"
	"github.com/lmittmann/tint"
)

type Options struct {
	Args struct {
		Output string `positional-arg-name:"output" description:"Output path for the wishlist (default data/wishlist.txt)"`
	} `positional-args:"yes"`
}

const defaultOutput = "data/wishlist.txt"

// fetchTimeout bounds the whole request; Apps Script issues a 302 redirect to
// googleusercontent.com which the default http.Client follows.
const fetchTimeout = 30 * time.Second

func main() {
	env.Load()

	var opts Options
	if _, err := flags.NewParser(&opts, flags.Default).Parse(); err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		}
		os.Exit(1)
	}

	slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: slog.LevelInfo})))

	if err := run(opts); err != nil {
		slog.Error("failed", "error", err)
		os.Exit(1)
	}
}

func run(opts Options) error {
	sheetURL := os.Getenv("KOBOLT_SHEET_URL")
	if sheetURL == "" {
		return errors.New("KOBOLT_SHEET_URL is not set (put it in .env.local)")
	}

	outPath := opts.Args.Output
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

	if dupes > 0 {
		slog.Warn("collapsed duplicate URLs", "count", dupes)
	}

	if err := atomicWrite(outPath, urls); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}

	fmt.Printf("synced %d urls -> %s\n", len(urls), outPath)
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

// parseURLList splits the body on newlines, trims each line, drops blanks,
// dedups, and sorts ascending. It returns the unique sorted URLs and the count
// of duplicate lines that were collapsed.
func parseURLList(body string) (urls []string, dupes int) {
	seen := make(map[string]struct{})
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := seen[line]; ok {
			dupes++
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
