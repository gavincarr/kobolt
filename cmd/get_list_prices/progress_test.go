package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/gavincarr/kobolt"
)

// captureHandler records every slog record's attributes.
type captureHandler struct {
	mu   sync.Mutex
	recs []map[string]any
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *captureHandler) WithGroup(string) slog.Handler            { return h }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	m := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	h.mu.Lock()
	h.recs = append(h.recs, m)
	h.mu.Unlock()
	return nil
}

func TestProgressConcurrent(t *testing.T) {
	const total, wantFailed = 100, 25
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	p := &progress{total: total}
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rp := &kobolt.RegionPrice{}
			if i%4 == 0 {
				rp.Error = "boom"
			}
			p.logResult("u", "my", rp)
		}(i)
	}
	wg.Wait()
	if done, failed := p.counts(); done != total || failed != wantFailed {
		t.Fatalf("got done=%d failed=%d, want %d/%d", done, failed, total, wantFailed)
	}

	if len(h.recs) != total {
		t.Fatalf("got %d log records, want %d", len(h.recs), total)
	}
	// Records are appended under progress's lock, so slice order is log order.
	seen := map[int]bool{}
	prevFailed, maxFailed := 0, 0
	for i, r := range h.recs {
		var n, tot int
		if _, err := fmt.Sscanf(fmt.Sprint(r["req"]), "%d/%d", &n, &tot); err != nil || tot != total {
			t.Fatalf("record %d: bad req %v", i, r["req"])
		}
		if n < 1 || n > total || seen[n] {
			t.Fatalf("record %d: req N=%d out of range or duplicate", i, n)
		}
		seen[n] = true
		failed, ok := r["failed"].(int64)
		if !ok {
			t.Fatalf("record %d: failed = %v (%T), want int", i, r["failed"], r["failed"])
		}
		f := int(failed)
		if f > n {
			t.Errorf("record %d: failed=%d exceeds N=%d", i, f, n)
		}
		if f < prevFailed {
			t.Errorf("record %d: failed=%d decreased from %d", i, f, prevFailed)
		}
		prevFailed = f
		maxFailed = max(maxFailed, f)
	}
	if maxFailed != wantFailed {
		t.Errorf("highest failed = %d, want %d", maxFailed, wantFailed)
	}
}
