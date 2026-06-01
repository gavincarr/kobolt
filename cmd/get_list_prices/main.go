package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alecthomas/kong"
	"github.com/chromedp/chromedp"
	"github.com/gavincarr/kobolt"
	"github.com/gavincarr/kobolt/internal/env"
	helpcolours "github.com/gavincarr/kong-help-colours"
	"github.com/lmittmann/tint"
)

type CLI struct {
	Concurrent int    `short:"c" default:"3" help:"Number of concurrent browser tabs"`
	Timeout    int    `short:"t" default:"90" help:"Per-URL timeout in seconds"`
	CC         string `short:"C" env:"KOBOLT_CC" help:"Comma-separated ISO 3166-1 alpha-2 country codes (e.g. my,au,us). If unset, the region embedded in each input URL is used."`
	Headful    bool   `help:"Run browser in headful (visible) mode for debugging"`
	Verbose    int    `short:"v" type:"counter" help:"Enable debug logging; repeat (-vv) to also dump the raw gizmo config per URL"`

	URLFile string `arg:"" name:"url-file" help:"File containing one Kobo book URL per line"`
}

// Cloudflare blocks the classic --headless mode; the "new" headless and a
// realistic UA pass the challenge automatically.
const realisticUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

func main() {
	env.Load()

	var cli CLI
	kong.Parse(&cli,
		kong.Name("get_list_prices"),
		kong.Description("Scrape Kobo list/sale prices for a file of URLs across one or more regional storefronts."),
		kong.Help(helpcolours.Help),
		kong.ShortHelp(helpcolours.ShortHelp),
	)

	level := slog.LevelInfo
	if cli.Verbose >= 1 {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: level})))

	if err := run(cli); err != nil {
		slog.Error("failed", "error", err)
		os.Exit(1)
	}
}

func run(cli CLI) error {
	urls, err := readURLs(cli.URLFile)
	if err != nil {
		return fmt.Errorf("read url file: %w", err)
	}
	if len(urls) == 0 {
		return errors.New("no URLs found in input file")
	}

	ccs, err := parseCCs(cli.CC)
	if err != nil {
		return fmt.Errorf("invalid --cc: %w", err)
	}

	outPath := kobolt.OutputPath(cli.URLFile, time.Now())
	prior, err := kobolt.LoadSnapshot(outPath)
	if err != nil {
		return fmt.Errorf("load existing output: %w", err)
	}
	existing := make(map[string]*kobolt.Book, len(prior))
	for _, b := range prior {
		existing[b.URL] = b
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
		if err := scrape(cli, books, jobs); err != nil {
			return err
		}
	} else {
		slog.Info("nothing to scrape; all (url, region) pairs already complete")
	}

	if err := kobolt.WriteSnapshot(outPath, books); err != nil {
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
func plan(urls []string, ccs []string, existing map[string]*kobolt.Book) ([]*kobolt.Book, []job, error) {
	books := make([]*kobolt.Book, len(urls))
	var jobs []job

	for i, u := range urls {
		urlCCs := ccs
		if len(urlCCs) == 0 {
			cc, ok := kobolt.ExtractRegion(u)
			if !ok {
				return nil, nil, fmt.Errorf("url %q has no /{cc}/ segment and --cc is not set", u)
			}
			urlCCs = []string{cc}
		}

		b := existing[u]
		if b == nil {
			b = &kobolt.Book{URL: u, Regions: map[string]*kobolt.RegionPrice{}}
		} else if b.Regions == nil {
			b.Regions = map[string]*kobolt.RegionPrice{}
		}
		books[i] = b

		for _, cc := range urlCCs {
			if rp := b.Regions[cc]; rp != nil && rp.Error == "" {
				continue
			}
			jobs = append(jobs, job{i, cc, kobolt.SubstituteRegion(u, cc)})
		}
	}
	return books, jobs, nil
}

func scrape(cli CLI, books []*kobolt.Book, jobs []job) error {
	allocOpts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.UserAgent(realisticUA),
		chromedp.WindowSize(1280, 900),
	)
	if !cli.Headful {
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
	for w := 0; w < cli.Concurrent; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobCh {
				rp, isbn, title, author := scrapeOne(browserCtx, j.url, time.Duration(cli.Timeout)*time.Second, cli.Verbose >= 2)
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

func logResult(url, cc string, rp *kobolt.RegionPrice) {
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

func scrapeOne(browserCtx context.Context, url string, timeout time.Duration, dumpRaw bool) (rp *kobolt.RegionPrice, isbn, title, author string) {
	rp = &kobolt.RegionPrice{URL: url, ScrapedAt: time.Now()}

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
	if dumpRaw {
		slog.Debug("raw gizmo config", "url", url, "config", configJSON)
	}

	isbn, title, author, perr := parseGizmoConfig(configJSON, rp)
	if perr != nil {
		rp.Error = fmt.Sprintf("parse: %v", perr)
		return rp, "", "", ""
	}
	return rp, isbn, title, author
}

func parseGizmoConfig(raw string, rp *kobolt.RegionPrice) (isbn, title, author string, err error) {
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
