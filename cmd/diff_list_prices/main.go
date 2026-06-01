package main

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"

	"github.com/alecthomas/kong"
	"github.com/gavincarr/kobolt"
	"github.com/gavincarr/kobolt/internal/env"
	helpcolours "github.com/gavincarr/kong-help-colours"
	"github.com/lmittmann/tint"
)

type CLI struct {
	NoColor bool `short:"C" name:"no-color" help:"Disable coloured output"`

	Old string `arg:"" name:"old.json" help:"Earlier snapshot"`
	New string `arg:"" name:"new.json" help:"Later snapshot"`
}

type diff struct {
	order         int
	cc            string
	currency      string
	oldPrice      float64
	newPrice      float64
	abs           float64
	pct           float64
	title, author string
}

const (
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiReset = "\x1b[0m"
)

func main() {
	env.Load()

	var cli CLI
	kong.Parse(&cli,
		kong.Name("diff_list_prices"),
		kong.Description("Show price changes between two snapshots, ranked by percent change."),
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
	oldBooks, err := kobolt.LoadSnapshot(cli.Old)
	if err != nil {
		return fmt.Errorf("load %s: %w", cli.Old, err)
	}
	newBooks, err := kobolt.LoadSnapshot(cli.New)
	if err != nil {
		return fmt.Errorf("load %s: %w", cli.New, err)
	}

	oldByURL := make(map[string]*kobolt.Book, len(oldBooks))
	for _, b := range oldBooks {
		oldByURL[b.URL] = b
	}

	diffs := computeDiffs(newBooks, oldByURL)
	if len(diffs) == 0 {
		fmt.Fprintln(os.Stderr, "no price changes")
		return nil
	}

	sort.SliceStable(diffs, func(i, j int) bool {
		if diffs[i].pct != diffs[j].pct {
			return diffs[i].pct < diffs[j].pct
		}
		if diffs[i].order != diffs[j].order {
			return diffs[i].order < diffs[j].order
		}
		return diffs[i].cc < diffs[j].cc
	})

	useColor := !cli.NoColor
	render(os.Stdout, diffs, useColor)
	return nil
}

func computeDiffs(newBooks []*kobolt.Book, oldByURL map[string]*kobolt.Book) []diff {
	var out []diff
	for i, nb := range newBooks {
		ob := oldByURL[nb.URL]
		if ob == nil {
			continue
		}
		for cc, np := range nb.Regions {
			op := ob.Regions[cc]
			if op == nil {
				continue
			}
			if !isRealPrice(np) || !isRealPrice(op) {
				continue
			}
			if np.Price == op.Price {
				continue
			}
			abs := np.Price - op.Price
			pct := abs / op.Price * 100
			out = append(out, diff{
				order:    i,
				cc:       cc,
				currency: np.Currency,
				oldPrice: op.Price,
				newPrice: np.Price,
				abs:      abs,
				pct:      pct,
				title:    nb.Title,
				author:   nb.Author,
			})
		}
	}
	return out
}

func isRealPrice(rp *kobolt.RegionPrice) bool {
	return rp.Error == "" && rp.Price != 0
}

func render(w *os.File, diffs []diff, useColor bool) {
	formatted := make([][7]string, len(diffs))
	for i, d := range diffs {
		formatted[i] = [7]string{
			d.cc,
			d.currency,
			price(d.oldPrice),
			price(d.newPrice),
			signedAbs(d.abs),
			signedPct(d.pct),
			titleAuthor(d.title, d.author),
		}
	}

	widths := [7]int{}
	for _, row := range formatted {
		for j, cell := range row {
			if len(cell) > widths[j] {
				widths[j] = len(cell)
			}
		}
	}

	for i, row := range formatted {
		absCell := fmt.Sprintf("%*s", widths[4], row[4])
		pctCell := fmt.Sprintf("%-*s", widths[5], row[5])
		if useColor {
			color := ansiGreen
			if diffs[i].abs > 0 {
				color = ansiRed
			}
			absCell = color + absCell + ansiReset
			pctCell = color + pctCell + ansiReset
		}
		line := fmt.Sprintf("%-*s  %-*s  %*s → %*s  %s  %s  %s",
			widths[0], row[0],
			widths[1], row[1],
			widths[2], row[2],
			widths[3], row[3],
			absCell,
			pctCell,
			row[6],
		)
		fmt.Fprintln(w, line)
	}
}

func price(p float64) string {
	return strconv.FormatFloat(p, 'f', 2, 64)
}

func signedAbs(a float64) string {
	if a > 0 {
		return "+" + strconv.FormatFloat(a, 'f', 2, 64)
	}
	return strconv.FormatFloat(a, 'f', 2, 64)
}

func signedPct(p float64) string {
	if p > 0 {
		return fmt.Sprintf("(+%.1f%%)", p)
	}
	return fmt.Sprintf("(%.1f%%)", p)
}

func titleAuthor(title, author string) string {
	if author == "" {
		return title
	}
	return title + " — " + author
}
