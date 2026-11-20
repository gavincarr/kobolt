package main

import (
	"errors"
	"testing"
)

func TestResolveSettled(t *testing.T) {
	tests := []struct {
		name        string
		st          pageState
		hasMeta     bool
		unavailable bool
		wantErr     error
	}{
		{"dom price without priceDetail", pageState{DOMPrice: true}, true, false, errPriceNotInData},
		{"dom price without metadata", pageState{DOMPrice: true}, false, false, errPriceNotInData},
		{"metadata only", pageState{}, true, true, nil},
		{"neither", pageState{}, false, false, errUnrecognised},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			un, err := resolveSettled(tc.st, tc.hasMeta)
			if un != tc.unavailable || !errors.Is(err, tc.wantErr) {
				t.Errorf("got (%v, %v), want (%v, %v)", un, err, tc.unavailable, tc.wantErr)
			}
		})
	}
}
