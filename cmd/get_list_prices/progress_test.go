package main

import (
	"sync"
	"testing"

	"github.com/gavincarr/kobolt"
)

func TestProgressConcurrent(t *testing.T) {
	p := &progress{total: 100}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
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
	if done, failed := p.counts(); done != 100 || failed != 25 {
		t.Fatalf("got done=%d failed=%d, want 100/25", done, failed)
	}
}
