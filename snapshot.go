package kobolt

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// LoadSnapshot reads a kobolt snapshot file. Records in the v1 flat schema
// (top-level price/currency/scraped_at) are migrated into the v2 region-keyed
// schema, inferring the region from the URL. Returns books in file order; a
// missing file is not an error and returns an empty slice.
func LoadSnapshot(path string) ([]*Book, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
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

	out := make([]*Book, 0, len(raw))
	migrated := 0
	for _, r := range raw {
		b := &Book{URL: r.URL, ISBN: r.ISBN, Title: r.Title, Author: r.Author, Regions: r.Regions}
		if b.Regions == nil {
			b.Regions = map[string]*RegionPrice{}
		}
		hadFlat := r.Currency != "" || r.Price != 0 || r.ListPrice != 0 || r.Error != "" || !r.ScrapedAt.IsZero()
		if len(b.Regions) == 0 && hadFlat {
			if cc, ok := ExtractRegion(r.URL); ok {
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
		out = append(out, b)
	}
	if migrated > 0 {
		slog.Info("migrated v1 records", "count", migrated)
	}
	return out, nil
}

func WriteSnapshot(path string, books []*Book) error {
	data, err := json.MarshalIndent(books, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
