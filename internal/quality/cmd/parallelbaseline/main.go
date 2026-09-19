// Command parallelbaseline is the single source of truth for
// internal/quality/testdata/paralleltest_baseline.txt.
//
// Run it with no flags after a reviewed decision to add a new serial test
// (or after fixing one): it rescans the tree with exactly the same logic
// TestParallelBaselineDoesNotRegress uses (internal/quality's
// ScanSerialTests) and overwrites the baseline file with the current,
// reasoned set of serial tests. Commit the result.
//
// Run it with -check in CI or by hand to verify the committed file matches
// what a regeneration would produce right now, without writing anything:
// it exits 1 if the file is stale (a serial test is missing, a baseline
// entry no longer corresponds to a serial test, or any entry -- committed
// or freshly detected -- carries an empty reason).
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/sneat-dev/wb/internal/quality"
)

func main() {
	check := flag.Bool("check", false, "verify the committed baseline matches the current tree; do not write")
	flag.Parse()

	root, err := quality.ParallelGuardModuleRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "parallelbaseline:", err)
		os.Exit(2)
	}
	baselinePath := filepath.Join(root, "internal", "quality", "testdata", "paralleltest_baseline.txt")

	serial, bareNolint, err := quality.ScanSerialTests(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parallelbaseline:", err)
		os.Exit(2)
	}
	if len(bareNolint) > 0 {
		sort.Strings(bareNolint)
		fmt.Fprintf(os.Stderr, "parallelbaseline: %d //nolint:paralleltest comment(s) carry no reason:\n", len(bareNolint))
		for _, b := range bareNolint {
			fmt.Fprintln(os.Stderr, "  "+b)
		}
		os.Exit(1)
	}

	rendered := quality.FormatParallelBaseline(serial)

	if !*check {
		if err := os.WriteFile(baselinePath, []byte(rendered), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "parallelbaseline:", err)
			os.Exit(2)
		}
		fmt.Printf("parallelbaseline: wrote %d entr(y/ies) to %s\n", len(serial), baselinePath)
		return
	}

	current, err := os.ReadFile(baselinePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parallelbaseline:", err)
		os.Exit(2)
	}
	// -check also rejects an empty reason in the committed file even when
	// the set of keys otherwise matches (ParseParallelBaseline enforces the
	// same rule the guard test does).
	if _, err := quality.ParseParallelBaseline(baselinePath); err != nil {
		fmt.Fprintln(os.Stderr, "parallelbaseline:", err)
		os.Exit(1)
	}
	if string(current) != rendered {
		fmt.Fprintln(os.Stderr, "parallelbaseline: testdata/paralleltest_baseline.txt is stale; run `go run ./internal/quality/cmd/parallelbaseline` and commit the result")
		os.Exit(1)
	}
	fmt.Println("parallelbaseline: up to date")
}
