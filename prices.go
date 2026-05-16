package kobolt

import "time"

// Book is the per-URL record in a kobolt snapshot. Common metadata lives at
// the top level; per-region pricing lives under Regions, keyed by 2-letter
// country code.
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
