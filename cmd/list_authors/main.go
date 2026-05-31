// Command list_authors is a throwaway tool that prints reading-log authors and
// their entry counts in decreasing-popularity order. It reads a parsed-booklog
// JSON file (the output of parse_booklog).
//
//	go run ./cmd/list_authors data/books.json
package main

import (
	"fmt"
	"os"

	"github.com/gavincarr/kobolt"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <booklog.json>\n", os.Args[0])
		os.Exit(2)
	}

	entries, err := kobolt.LoadBooklog(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	for _, a := range kobolt.CollateAuthors(entries) {
		fmt.Printf("%5d  %s\n", a.Count, a.Author)
	}
}
