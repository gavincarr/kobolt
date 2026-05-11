package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	_ "github.com/joho/godotenv/autoload"
	"github.com/jessevdk/go-flags"
)

type Options struct {
	Concurrent int    `short:"c" long:"concurrent" default:"3" description:"Number of concurrent browser tabs"`
	Timeout    int    `short:"t" long:"timeout" default:"90" description:"Per-URL timeout in seconds"`
	CC         string `short:"C" long:"cc" env:"KOBOLT_CC" description:"Comma-separated ISO 3166-1 alpha-2 country codes (e.g. my,au,us). If unset, the region embedded in each input URL is used."`
	Headful    bool   `long:"headful" description:"Run browser in headful (visible) mode for debugging"`
	Verbose    bool   `short:"v" long:"verbose" description:"Enable debug logging"`

	Args struct {
		URLFile string `positional-arg-name:"url-file" description:"File containing one Kobo book URL per line"`
	} `positional-args:"yes" required:"yes"`
}

// Cloudflare blocks the classic --headless mode; the "new" headless and a
// realistic UA pass the challenge automatically.
const realisticUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// Matches https://(www.)kobo.com/{cc}/... so we can swap or extract the region.
var koboURLRegion = regexp.MustCompile(`^(https?://(?:www\.)?kobo\.com/)([a-z]{2})(/.*)$`)

// Book is the per-URL output record. Common metadata lives at the top level;
// per-region pricing lives under Regions, keyed by 2-letter country code.
type Book struct {
	URL     string                  `json:"url"`
	ISBN    string                  `json:"isbn,omitempty"`
	Title   string                  `json:"title,omitempty"`
	Author  string                  `json:"author,omitempty"`
	Regions map[string]*RegionPrice `json:"regions"`
}

type RegionPrice struct {
	URL       string    `json:"url"`
	Price     float64   `json:"price,omitempty"`
	ListPrice float64   `json:"list_price,omitempty"`
	Currency  string    `json:"currency,omitempty"`
	ScrapedAt time.Time `json:"scraped_at"`
	Error     string    `json:"error,omitempty"`
}

func main() {
	var opts Options
	if _, err := flags.NewParser(&opts, flags.Default).Parse(); err != nil {
		if flagsErr, ok := err.(*flags.Error); ok && flagsErr.Type == flags.ErrHelp {
			os.Exit(0)
		}
		os.Exit(1)
	}

	level := slog.LevelInfo
	if opts.Verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	if err := run(opts); err != nil {
		slog.Error("failed", "error", err)
		os.Exit(1)
	}
}

func run(opts Options) error {
	urls, err := readURLs(opts.Args.URLFile)
	if err != nil {
		return fmt.Errorf("read url file: %w", err)
	}
	if len(urls) == 0 {
		return errors.New("no URLs found in input file")
	}

	ccs, err := parseCCs(opts.CC)
	if err != nil {
		return fmt.Errorf("invalid --cc: %w", err)
	}

	outPath := outputPath(opts.Args.URLFile, time.Now())
	existing, err := loadExisting(outPath)
	if err != nil {
		return fmt.Errorf("load existing output: %w", err)
	}
	if len(existing) > 0 {
		slog.Info("loaded existing output", "path", outPath, "records", len(existing))
	}

	books, jobs, err := plan(urls, ccs, existing)
	if err != nil {
		return err
	}
	slog.Info("plan", "urls", len(urls), "regions", ccs, "jobs", len(jobs))

	if len(jobs) > 0 {
		if err := scrape(opts, books, jobs); err != nil {
			return err
		}
	} else {
		slog.Info("nothing to scrape; all (url, region) pairs already complete")
	}

	if err := writeJSON(outPath, books); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	slog.Info("wrote output", "path", outPath, "records", len(books))
	return nil
}

type job struct {
	bookIdx int
	cc      string
	url     string
}

// plan returns the books slice in input order (existing entries reused, new
// ones created) and the list of (book, region) pairs that still need
// scraping (missing entirely or previously errored).
func plan(urls []string, ccs []string, existing map[string]*Book) ([]*Book, []job, error) {
	books := make([]*Book, len(urls))
	var jobs []job

	for i, u := range urls {
		urlCCs := ccs
		if len(urlCCs) == 0 {
			cc, ok := extractRegion(u)
			if !ok {
				return nil, nil, fmt.Errorf("url %q has no /{cc}/ segment and --cc is not set", u)
			}
			urlCCs = []string{cc}
		}

		b := existing[u]
		if b == nil {
			b = &Book{URL: u, Regions: map[string]*RegionPrice{}}
		} else if b.Regions == nil {
			b.Regions = map[string]*RegionPrice{}
		}
		books[i] = b

		for _, cc := range urlCCs {
			if rp := b.Regions[cc]; rp != nil && rp.Error == "" {
				continue
			}
			jobs = append(jobs, job{i, cc, substituteRegion(u, cc)})
		}
	}
	return books, jobs, nil
}

func scrape(opts Options, books []*Book, jobs []job) error {
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.UserAgent(realisticUA),
		chromedp.WindowSize(1280, 900),
	)
	if !opts.Headful {
		allocOpts = append(allocOpts, chromedp.Flag("headless", "new"))
	} else {
		allocOpts = append(allocOpts, chromedp.Flag("headless", false))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), allocOpts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()
	if err := chromedp.Run(browserCtx); err != nil {
		return fmt.Errorf("launch browser: %w", err)
	}

	jobCh := make(chan job)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for w := 0; w < opts.Concurrent; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				rp, isbn, title, author := scrapeOne(browserCtx, j.url, time.Duration(opts.Timeout)*time.Second)
				logResult(j.url, j.cc, rp)
				mu.Lock()
				b := books[j.bookIdx]
				b.Regions[j.cc] = rp
				if rp.Error == "" {
					if isbn != "" {
						b.ISBN = isbn
					}
					if title != "" {
						b.Title = title
					}
					if author != "" {
						b.Author = author
					}
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		jobCh <- j
	}
	close(jobCh)
	wg.Wait()
	return nil
}

func logResult(url, cc string, rp *RegionPrice) {
	if rp.Error != "" {
		slog.Warn("scrape error", "cc", cc, "url", url, "error", rp.Error)
		return
	}
	slog.Info("scraped",
		"cc", cc,
		"url", url,
		"price", rp.Price,
		"list_price", rp.ListPrice,
		"currency", rp.Currency,
	)
}

func parseCCs(s string) ([]string, error) {
	if s == "" {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, c := range strings.Split(s, ",") {
		c = strings.ToLower(strings.TrimSpace(c))
		if c == "" {
			continue
		}
		if len(c) != 2 || !isAlpha(c) {
			return nil, fmt.Errorf("country code %q is not a 2-letter code", c)
		}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out, nil
}

func isAlpha(s string) bool {
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return false
		}
	}
	return true
}

func extractRegion(url string) (string, bool) {
	m := koboURLRegion.FindStringSubmatch(url)
	if m == nil {
		return "", false
	}
	return m[2], true
}

func substituteRegion(url, cc string) string {
	idx := koboURLRegion.FindStringSubmatchIndex(url)
	if idx == nil {
		return url
	}
	return url[:idx[4]] + cc + url[idx[5]:]
}

func readURLs(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var urls []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	return urls, scanner.Err()
}

func outputPath(inputFile string, now time.Time) string {
	dir := filepath.Dir(inputFile)
	base := filepath.Base(inputFile)
	name := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, fmt.Sprintf("%s_%s.json", name, now.Format("20060102")))
}

// loadExisting reads a previous run's output. Records in the v1 flat schema
// (top-level price/currency/scraped_at) are migrated into the v2 region-keyed
// schema, inferring the region from the URL.
func loadExisting(path string) (map[string]*Book, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*Book{}, nil
	}
	if err != nil {
		return nil, err
	}

	type combined struct {
		URL       string                  `json:"url"`
		ISBN      string                  `json:"isbn,omitempty"`
		Title     string                  `json:"title,omitempty"`
		Author    string                  `json:"author,omitempty"`
		Regions   map[string]*RegionPrice `json:"regions,omitempty"`
		Price     float64                 `json:"price,omitempty"`
		ListPrice float64                 `json:"list_price,omitempty"`
		Currency  string                  `json:"currency,omitempty"`
		ScrapedAt time.Time               `json:"scraped_at,omitempty"`
		Error     string                  `json:"error,omitempty"`
	}

	var raw []combined
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	out := make(map[string]*Book, len(raw))
	migrated := 0
	for _, r := range raw {
		b := &Book{URL: r.URL, ISBN: r.ISBN, Title: r.Title, Author: r.Author, Regions: r.Regions}
		if b.Regions == nil {
			b.Regions = map[string]*RegionPrice{}
		}
		hadFlat := r.Currency != "" || r.Price != 0 || r.ListPrice != 0 || r.Error != "" || !r.ScrapedAt.IsZero()
		if len(b.Regions) == 0 && hadFlat {
			if cc, ok := extractRegion(r.URL); ok {
				b.Regions[cc] = &RegionPrice{
					URL:       r.URL,
					Price:     r.Price,
					ListPrice: r.ListPrice,
					Currency:  r.Currency,
					ScrapedAt: r.ScrapedAt,
					Error:     r.Error,
				}
				migrated++
			}
		}
		out[b.URL] = b
	}
	if migrated > 0 {
		slog.Info("migrated v1 records", "count", migrated)
	}
	return out, nil
}

func writeJSON(path string, books []*Book) error {
	data, err := json.MarshalIndent(books, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func scrapeOne(browserCtx context.Context, url string, timeout time.Duration) (rp *RegionPrice, isbn, title, author string) {
	rp = &RegionPrice{URL: url, ScrapedAt: time.Now()}

	// Fresh tab per URL: reusing a tab across navigations hangs on the 2nd page.
	tabCtx, cancelTab := chromedp.NewContext(browserCtx)
	defer cancelTab()
	ctx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	var configJSON string
	var ok bool
	if err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitReady("#ratings-widget-details-wrapper", chromedp.ByID),
		chromedp.AttributeValue("#ratings-widget-details-wrapper", "data-kobo-gizmo-config", &configJSON, &ok, chromedp.ByID),
	); err != nil {
		rp.Error = fmt.Sprintf("navigate/extract: %v", err)
		return rp, "", "", ""
	}
	if !ok || configJSON == "" {
		rp.Error = "data-kobo-gizmo-config attribute missing"
		return rp, "", "", ""
	}
	slog.Debug("raw gizmo config", "url", url, "config", configJSON)

	isbn, title, author, perr := parseGizmoConfig(configJSON, rp)
	if perr != nil {
		rp.Error = fmt.Sprintf("parse: %v", perr)
		return rp, "", "", ""
	}
	return rp, isbn, title, author
}

func parseGizmoConfig(raw string, rp *RegionPrice) (isbn, title, author string, err error) {
	// Outer config is JSON. "googleBook" and "googleProduct" are themselves
	// JSON-encoded strings.
	//   googleBook    -> schema.org Book, Offer price = current (sale) price.
	//   googleProduct -> schema.org Product, Offer price = list price (RRP).
	var outer struct {
		GoogleBook    string `json:"googleBook"`
		GoogleProduct string `json:"googleProduct"`
	}
	if err := json.Unmarshal([]byte(raw), &outer); err != nil {
		return "", "", "", fmt.Errorf("outer: %w", err)
	}
	if outer.GoogleBook == "" {
		return "", "", "", errors.New("googleBook missing in outer config")
	}

	var book struct {
		Name        string          `json:"name"`
		Author      json.RawMessage `json:"author"`
		WorkExample struct {
			ISBN            string `json:"isbn"`
			PotentialAction struct {
				ExpectsAcceptanceOf struct {
					Price         json.Number `json:"price"`
					PriceCurrency string      `json:"priceCurrency"`
				} `json:"expectsAcceptanceOf"`
			} `json:"potentialAction"`
		} `json:"workExample"`
	}
	if err := json.Unmarshal([]byte(outer.GoogleBook), &book); err != nil {
		return "", "", "", fmt.Errorf("googleBook: %w", err)
	}

	offer := book.WorkExample.PotentialAction.ExpectsAcceptanceOf
	title = book.Name
	isbn = book.WorkExample.ISBN
	author = parseAuthor(book.Author)
	rp.Currency = offer.PriceCurrency
	if p := string(offer.Price); p != "" {
		rp.Price, _ = strconv.ParseFloat(p, 64)
	}

	if outer.GoogleProduct != "" {
		var product struct {
			Offers struct {
				Price         json.Number `json:"price"`
				PriceCurrency string      `json:"priceCurrency"`
			} `json:"offers"`
		}
		if err := json.Unmarshal([]byte(outer.GoogleProduct), &product); err == nil {
			if p := string(product.Offers.Price); p != "" {
				rp.ListPrice, _ = strconv.ParseFloat(p, 64)
			}
			if rp.Currency == "" {
				rp.Currency = product.Offers.PriceCurrency
			}
		}
	}
	return isbn, title, author, nil
}

func parseAuthor(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	type person struct {
		Name string `json:"name"`
	}
	var list []person
	if err := json.Unmarshal(raw, &list); err == nil && len(list) > 0 {
		names := make([]string, 0, len(list))
		for _, p := range list {
			if p.Name != "" {
				names = append(names, p.Name)
			}
		}
		return strings.Join(names, ", ")
	}
	var single person
	if err := json.Unmarshal(raw, &single); err == nil {
		return single.Name
	}
	return ""
}
