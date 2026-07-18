package main

import (
	"fmt"
	"sort"
)

// report accumulates per-repo outcomes for the summary.
type report struct {
	updated, skipped, errors, archived, forked []string
}

// print writes the final summary. Per-repo lines already streamed during the
// run, so this shows counts and re-lists only errors so failures aren't lost in
// scroll.
func (rep *report) print() {
	fmt.Printf("\n━━━ Summary ━━━\n")
	fmt.Printf("Updated  %d\n", len(rep.updated))
	fmt.Printf("Skipped  %d\n", len(rep.skipped))
	fmt.Printf("Forks    %d\n", len(rep.forked))
	fmt.Printf("Archived %d\n", len(rep.archived))
	fmt.Printf("Errors   %d\n", len(rep.errors))
	sort.Strings(rep.errors)
	for _, e := range rep.errors {
		fmt.Printf("  ✗ %s\n", e)
	}
}

// record files an outcome into a bucket and streams a line to stdout
// immediately, so progress is visible as each repo completes.
func (rep *report) record(bucket *[]string, symbol, entry string) {
	*bucket = append(*bucket, entry)
	fmt.Printf("%s %s\n", symbol, entry)
}
