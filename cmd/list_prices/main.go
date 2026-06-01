// Command list_prices lists each book in a snapshot on a single line, with all
// of its regional prices normalised to a single base currency, cheapest first.
// Books are ordered by their cheapest (minimum) normalised price ascending.
// Unlike arb_list_prices it does not require a book to be available in more
// than one region — single-region books are listed too.
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

	"github.com/alecthomas/kong"
	"github.com/gavincarr/kobolt"
	"github.com/gavincarr/kobolt/internal/env"
	helpcolours "github.com/gavincarr/kong-help-colours"
	"github.com/lmittmann/tint"
)

type CLI struct {
	Top        int    `short:"n" default:"0" help:"Show at most N books (0 = no limit)"`
	Base       string `short:"b" default:"AUD" help:"Base currency for normalization (ISO 4217)"`
	NoColor    bool   `name:"no-color" help:"Disable coloured output"`

	Snapshot string `arg:"" name:"snapshot.json" help:"Snapshot to analyze"`
}

type regionEntry struct {
	cc     string
	inBase float64
}

type bookPrices struct {
	order   int
	title   string
	author  string
	regions []regionEntry
	minBase float64
}

// Colour the pricing section by the book's cheapest base-currency price:
// green under 10, yellow under 20, cyan otherwise.
const (
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	ansiReset  = "\x1b[0m"
)

func main() {
	env.Load()

	var cli CLI
	kong.Parse(&cli,
		kong.Name("list_prices"),
		kong.Description("List each book's regional prices on one line, normalised to a base currency, cheapest first."),
		kong.Help(helpcolours.Help),
		kong.ShortHelp(helpcolours.ShortHelp),
	)

	slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{Level: slog.LevelInfo})))

	if err := run(cli); err != nil {
		slog.Error("failed", "error", err)
		os.Exit(1)
	}
}

func run(cli CLI) error {
	books, err := kobolt.LoadSnapshot(cli.Snapshot)
	if err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}
	if len(books) == 0 {
		return fmt.Errorf("snapshot %s is empty", cli.Snapshot)
	}

	base := strings.ToUpper(cli.Base)
	cacheDir := filepath.Dir(cli.Snapshot)
	rates, err := kobolt.LoadOrFetchRates(cacheDir, base, time.Now())
	if err != nil {
		return err
	}

	priced := computePrices(books, rates, base)
	priced = rank(priced, cli.Top)

	if len(priced) == 0 {
		fmt.Fprintln(os.Stderr, "no books with a priced region")
		return nil
	}

	useColor := !cli.NoColor
	render(os.Stdout, priced, base, useColor)
	return nil
}

func computePrices(books []*kobolt.Book, rates map[string]float64, base string) []bookPrices {
	out := make([]bookPrices, 0, len(books))
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
				cc:     cc,
				inBase: rp.Price / rate,
			})
		}
		if len(regions) == 0 {
			continue
		}
		sort.Slice(regions, func(i, j int) bool { return regions[i].inBase < regions[j].inBase })
		out = append(out, bookPrices{
			order:   i,
			title:   b.Title,
			author:  b.Author,
			regions: regions,
			minBase: regions[0].inBase,
		})
	}
	return out
}

func rank(priced []bookPrices, top int) []bookPrices {
	sort.SliceStable(priced, func(i, j int) bool {
		if priced[i].minBase != priced[j].minBase {
			return priced[i].minBase < priced[j].minBase
		}
		return priced[i].order < priced[j].order
	})
	if top > 0 && len(priced) > top {
		priced = priced[:top]
	}
	return priced
}

// render prints one line per book:
//
//	Title — Author (AUD 0.99 AU, 1.36 MY, 1.38 US)
//
// All prices are in the base currency (named once, up front); each is followed
// by its uppercased region/store code, cheapest first. The parenthetical
// pricing section is coloured by the book's cheapest price (see colourFor).
func render(w *os.File, priced []bookPrices, base string, useColor bool) {
	for _, p := range priced {
		var b strings.Builder
		for i, r := range p.regions {
			if i == 0 {
				fmt.Fprintf(&b, "%s %s %s", base, formatPrice(r.inBase), strings.ToUpper(r.cc))
			} else {
				fmt.Fprintf(&b, ", %s %s", formatPrice(r.inBase), strings.ToUpper(r.cc))
			}
		}
		section := "(" + b.String() + ")"
		if useColor {
			section = colourFor(p.minBase) + section + ansiReset
		}
		fmt.Fprintf(w, "%s %s\n", titleAuthor(p.title, p.author), section)
	}
}

func colourFor(minBase float64) string {
	switch {
	case minBase < 10:
		return ansiGreen
	case minBase < 20:
		return ansiYellow
	default:
		return ansiCyan
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
