package kobolt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const frankfurterEndpoint = "https://api.frankfurter.dev/v1/latest"

type ratesFile struct {
	Amount float64            `json:"amount"`
	Base   string             `json:"base"`
	Date   string             `json:"date"`
	Rates  map[string]float64 `json:"rates"`
}

// LoadOrFetchRates returns FX rates with the given base currency. It first
// looks for a same-day cache file at <cacheDir>/rates_<BASE>_<YYYYMMDD>.json;
// on cache miss it fetches from Frankfurter and writes the cache. The
// returned map always contains base itself with rate 1.0 so callers can look
// up the base currency directly.
func LoadOrFetchRates(cacheDir, base string, today time.Time) (map[string]float64, error) {
	base = strings.ToUpper(base)
	path := filepath.Join(cacheDir, fmt.Sprintf("rates_%s_%s.json", base, today.Format("20060102")))

	if data, err := os.ReadFile(path); err == nil {
		rates, perr := parseRates(data, base)
		if perr == nil {
			slog.Info("loaded fx cache", "path", path)
			return rates, nil
		}
		slog.Warn("fx cache parse failed, refetching", "path", path, "error", perr)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read fx cache %s: %w", path, err)
	}

	body, err := fetchRates(base)
	if err != nil {
		return nil, fmt.Errorf("fetch fx rates (base=%s): %w", base, err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return nil, fmt.Errorf("write fx cache %s: %w", path, err)
	}
	slog.Info("fetched fx rates", "base", base, "cache", path)
	return parseRates(body, base)
}

func fetchRates(base string) ([]byte, error) {
	url := fmt.Sprintf("%s?base=%s", frankfurterEndpoint, base)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("frankfurter %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func parseRates(body []byte, base string) (map[string]float64, error) {
	var rf ratesFile
	if err := json.Unmarshal(body, &rf); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if strings.ToUpper(rf.Base) != base {
		return nil, fmt.Errorf("rates base mismatch: want %s, got %s", base, rf.Base)
	}
	out := make(map[string]float64, len(rf.Rates)+1)
	for ccy, r := range rf.Rates {
		out[strings.ToUpper(ccy)] = r
	}
	out[base] = 1.0
	return out, nil
}
