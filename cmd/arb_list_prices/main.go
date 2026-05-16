package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gavincarr/kobolt"
	"github.com/jessevdk/go-flags"
	_ "github.com/joho/godotenv/autoload"
	"github.com/lmittmann/tint"
	"golang.org/x/term"
)

type Options struct {
	Top       int     `short:"n" long:"top" default:"20" description:"Show at most N books with the largest cross-region spreads"`
	MinSpread float64 `short:"m" long:"min-spread" default:"5" description:"Skip books whose max cross-region spread is below this percent"`
	Base      string  `short:"b" long:"base" default:"AUD" description:"Base currency for normalization (ISO 4217)"`
	NoColor   bool    `long:"no-color" description:"Disable coloured output even on a TTY"`

	Args struct {
		Snapshot string `positional-arg-name:"snapshot.json" description:"Snapshot to analyze"`
	} `positional-args:"yes" required:"yes"`
}

type regionEntry struct {
	cc       string
	currency string
	price    float64
	inBase   float64
}

type bookArb struct {
	order   int
	title   string
	author  string
	regions []regionEntry
	spread  float64
}

const (
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiReset = "\x1b[0m"
)

func main() {
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
	books, err := kobolt.LoadSnapshot(opts.Args.Snapshot)
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}
	if len(books) == 0 {
		return fmt.Errorf("snapshot %s is empty", opts.Args.Snapshot)
	}

	base := strings.ToUpper(opts.Base)
	cacheDir := filepath.Dir(opts.Args.Snapshot)
	rates, err := kobolt.LoadOrFetchRates(cacheDir, base, time.Now())
	if err != nil {
		return err
	}

	arbs := computeArbs(books, rates, base)
	arbs = filterAndRank(arbs, opts.MinSpread, opts.Top)

	if len(arbs) == 0 {
		fmt.Fprintf(os.Stderr, "no books with cross-region spread ≥ %g%%\n", opts.MinSpread)
		return nil
	}

	useColor := !opts.NoColor && term.IsTerminal(int(os.Stdout.Fd()))
	render(os.Stdout, arbs, base, useColor)
	return nil
}

func computeArbs(books []*kobolt.Book, rates map[string]float64, base string) []bookArb {
	out := make([]bookArb, 0, len(books))
	unknownCCY := map[string]bool{}
	for i, b := range books {
		var regions []regionEntry
		for cc, rp := range b.Regions {
			if rp.Error != "" || rp.Price == 0 {
				continue
			}
			ccy := strings.ToUpper(rp.Currency)
			rate, ok := rates[ccy]
			if !ok || rate == 0 {
				if !unknownCCY[ccy] {
					slog.Warn("no fx rate for currency, skipping region", "currency", ccy, "base", base)
					unknownCCY[ccy] = true
				}
				continue
			}
			regions = append(regions, regionEntry{
				cc:       cc,
				currency: ccy,
				price:    rp.Price,
				inBase:   rp.Price / rate,
			})
		}
		if len(regions) < 2 {
			continue
		}
		sort.Slice(regions, func(i, j int) bool { return regions[i].inBase < regions[j].inBase })
		minP, maxP := regions[0].inBase, regions[len(regions)-1].inBase
		spread := (maxP - minP) / minP * 100
		out = append(out, bookArb{
			order:   i,
			title:   b.Title,
			author:  b.Author,
			regions: regions,
			spread:  spread,
		})
	}
	return out
}

func filterAndRank(arbs []bookArb, minSpread float64, top int) []bookArb {
	filtered := arbs[:0]
	for _, a := range arbs {
		if a.spread >= minSpread {
			filtered = append(filtered, a)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].spread != filtered[j].spread {
			return filtered[i].spread > filtered[j].spread
		}
		return filtered[i].order < filtered[j].order
	})
	if top > 0 && len(filtered) > top {
		filtered = filtered[:top]
	}
	return filtered
}

func render(w *os.File, arbs []bookArb, base string, useColor bool) {
	ccW, ccyW, priceW, baseW := 0, 0, 0, 0
	for _, a := range arbs {
		for _, r := range a.regions {
			if len(r.cc) > ccW {
				ccW = len(r.cc)
			}
			if len(r.currency) > ccyW {
				ccyW = len(r.currency)
			}
			if n := len(formatPrice(r.price)); n > priceW {
				priceW = n
			}
			if n := len(formatPrice(r.inBase)); n > baseW {
				baseW = n
			}
		}
	}

	baseLower := strings.ToLower(base)

	for i, a := range arbs {
		fmt.Fprintf(w, "%s  (spread %.1f%%)\n", titleAuthor(a.title, a.author), a.spread)
		minBase := a.regions[0].inBase
		maxIdx := len(a.regions) - 1
		for j, r := range a.regions {
			var suffix string
			if j == 0 {
				suffix = "— (cheapest)"
			} else {
				pct := (r.inBase - minBase) / minBase * 100
				suffix = fmt.Sprintf("+%.1f%%", pct)
			}
			line := fmt.Sprintf("  %-*s  %-*s  %*s  (%s %*s)  %s",
				ccW, r.cc,
				ccyW, r.currency,
				priceW, formatPrice(r.price),
				baseLower, baseW, formatPrice(r.inBase),
				suffix,
			)
			if useColor {
				switch j {
				case 0:
					line = ansiGreen + line + ansiReset
				case maxIdx:
					line = ansiRed + line + ansiReset
				}
			}
			fmt.Fprintln(w, line)
		}
		if i < len(arbs)-1 {
			fmt.Fprintln(w)
		}
	}
}

func formatPrice(p float64) string {
	return strconv.FormatFloat(p, 'f', 2, 64)
}

func titleAuthor(title, author string) string {
	if author == "" {
		return title
	}
	return title + " — " + author
}
