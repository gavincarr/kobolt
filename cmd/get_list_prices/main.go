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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/jessevdk/go-flags"
)

type Options struct {
	Concurrent int  `short:"c" long:"concurrent" default:"3" description:"Number of concurrent browser tabs"`
	Timeout    int  `short:"t" long:"timeout" default:"90" description:"Per-URL timeout in seconds"`
	Headful    bool `long:"headful" description:"Run browser in headful (visible) mode for debugging"`
	Verbose    bool `short:"v" long:"verbose" description:"Enable debug logging"`

	Args struct {
		URLFile string `positional-arg-name:"url-file" description:"File containing one Kobo book URL per line"`
	} `positional-args:"yes" required:"yes"`
}

// Cloudflare blocks the classic --headless mode; the "new" headless and a
// realistic UA pass the challenge automatically.
const realisticUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

type BookPrice struct {
	URL       string    `json:"url"`
	ISBN      string    `json:"isbn,omitempty"`
	Title     string    `json:"title,omitempty"`
	Author    string    `json:"author,omitempty"`
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
	slog.Info("loaded urls", "count", len(urls), "file", opts.Args.URLFile)

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

	results := make([]BookPrice, len(urls))
	type job struct {
		idx int
		url string
	}
	jobs := make(chan job)

	var wg sync.WaitGroup
	for i := 0; i < opts.Concurrent; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for j := range jobs {
				bp := scrapeOne(browserCtx, j.url, time.Duration(opts.Timeout)*time.Second)
				if bp.Error != "" {
					slog.Warn("scrape error", "url", bp.URL, "error", bp.Error)
				} else {
					slog.Info("scraped",
						"url", bp.URL,
						"isbn", bp.ISBN,
						"price", bp.Price,
						"list_price", bp.ListPrice,
						"currency", bp.Currency,
					)
				}
				results[j.idx] = bp
			}
		}(i)
	}

	for i, u := range urls {
		jobs <- job{i, u}
	}
	close(jobs)
	wg.Wait()

	outPath := outputPath(opts.Args.URLFile, time.Now())
	if err := writeJSON(outPath, results); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	slog.Info("wrote output", "path", outPath, "records", len(results))
	return nil
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

func writeJSON(path string, results []BookPrice) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func scrapeOne(browserCtx context.Context, url string, timeout time.Duration) BookPrice {
	bp := BookPrice{URL: url, ScrapedAt: time.Now()}

	// Fresh tab per URL: avoids stale-DOM issues from reusing the same tab
	// across navigations. The browser process (and its CF cookies) is shared.
	tabCtx, cancelTab := chromedp.NewContext(browserCtx)
	defer cancelTab()
	ctx, cancel := context.WithTimeout(tabCtx, timeout)
	defer cancel()

	var configJSON string
	var ok bool
	err := chromedp.Run(ctx,
		chromedp.Navigate(url),
		chromedp.WaitReady("#ratings-widget-details-wrapper", chromedp.ByID),
		chromedp.AttributeValue("#ratings-widget-details-wrapper", "data-kobo-gizmo-config", &configJSON, &ok, chromedp.ByID),
	)
	if err != nil {
		bp.Error = fmt.Sprintf("navigate/extract: %v", err)
		return bp
	}
	if !ok || configJSON == "" {
		bp.Error = "data-kobo-gizmo-config attribute missing"
		return bp
	}

	slog.Debug("raw gizmo config", "url", url, "config", configJSON)

	if err := parseGizmoConfig(configJSON, &bp); err != nil {
		bp.Error = fmt.Sprintf("parse: %v", err)
		return bp
	}
	return bp
}

func parseGizmoConfig(raw string, bp *BookPrice) error {
	// The outer config is JSON. The "googleBook" and "googleProduct" values
	// are themselves JSON-encoded strings.
	//   googleBook   -> schema.org Book, Offer price = current (sale) price.
	//   googleProduct-> schema.org Product, Offer price = list price (RRP).
	var outer struct {
		GoogleBook    string `json:"googleBook"`
		GoogleProduct string `json:"googleProduct"`
	}
	if err := json.Unmarshal([]byte(raw), &outer); err != nil {
		return fmt.Errorf("outer: %w", err)
	}
	if outer.GoogleBook == "" {
		return errors.New("googleBook missing in outer config")
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
		return fmt.Errorf("googleBook: %w", err)
	}

	offer := book.WorkExample.PotentialAction.ExpectsAcceptanceOf
	bp.Title = book.Name
	bp.ISBN = book.WorkExample.ISBN
	bp.Currency = offer.PriceCurrency
	if p := string(offer.Price); p != "" {
		bp.Price, _ = strconv.ParseFloat(p, 64)
	}
	bp.Author = parseAuthor(book.Author)

	if outer.GoogleProduct != "" {
		var product struct {
			Offers struct {
				Price         json.Number `json:"price"`
				PriceCurrency string      `json:"priceCurrency"`
			} `json:"offers"`
		}
		if err := json.Unmarshal([]byte(outer.GoogleProduct), &product); err == nil {
			if p := string(product.Offers.Price); p != "" {
				bp.ListPrice, _ = strconv.ParseFloat(p, 64)
			}
			if bp.Currency == "" {
				bp.Currency = product.Offers.PriceCurrency
			}
		}
	}
	return nil
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
