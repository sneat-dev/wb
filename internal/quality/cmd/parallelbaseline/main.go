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
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/sneat-dev/wb/internal/quality"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run implements the whole command against injected argv and output
// streams, following the repo's run(args, stdout, stderr) seam convention
// so every exit path is directly testable without a subprocess.
func run(args []string, stdout, stderr io.Writer) int {
	flagSet := flag.NewFlagSet("parallelbaseline", flag.ContinueOnError)
	flagSet.SetOutput(stderr)
	check := flagSet.Bool("check", false, "verify the committed baseline matches the current tree; do not write")
	if err := flagSet.Parse(args); err != nil {
		return 2
	}

	root, err := quality.ParallelGuardModuleRoot()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "parallelbaseline:", err)
		return 2
	}
	baselinePath := filepath.Join(root, "internal", "quality", "testdata", "paralleltest_baseline.txt")
	return runAgainstBaseline(root, baselinePath, *check, stdout, stderr)
}

// runAgainstBaseline is run's testable core: given an already-resolved
// module root and baseline file path, it scans the tree and either writes
// or checks. Splitting this out lets a test point baselinePath at a
// disposable file instead of the real committed one -- every other test in
// the suite may read that real path concurrently, in its own process, so a
// test must never write to it even briefly.
func runAgainstBaseline(root, baselinePath string, check bool, stdout, stderr io.Writer) int {
	serial, bareNolint, err := quality.ScanSerialTests(root)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "parallelbaseline:", err)
		return 2
	}
	if len(bareNolint) > 0 {
		sort.Strings(bareNolint)
		_, _ = fmt.Fprintf(stderr, "parallelbaseline: %d //nolint:paralleltest comment(s) carry no reason:\n", len(bareNolint))
		for _, b := range bareNolint {
			_, _ = fmt.Fprintln(stderr, "  "+b)
		}
		return 1
	}

	rendered := quality.FormatParallelBaseline(serial)

	if !check {
		if err := os.WriteFile(baselinePath, []byte(rendered), 0o644); err != nil {
			_, _ = fmt.Fprintln(stderr, "parallelbaseline:", err)
			return 2
		}
		_, _ = fmt.Fprintf(stdout, "parallelbaseline: wrote %d entr(y/ies) to %s\n", len(serial), baselinePath)
		return 0
	}

	current, err := os.ReadFile(baselinePath)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "parallelbaseline:", err)
		return 2
	}
	// -check also rejects an empty reason in the committed file even when
	// the set of keys otherwise matches (ParseParallelBaseline enforces the
	// same rule the guard test does).
	if _, err := quality.ParseParallelBaseline(baselinePath); err != nil {
		_, _ = fmt.Fprintln(stderr, "parallelbaseline:", err)
		return 1
	}
	if string(current) != rendered {
		_, _ = fmt.Fprintln(stderr, "parallelbaseline: testdata/paralleltest_baseline.txt is stale; run `go run ./internal/quality/cmd/parallelbaseline` and commit the result")
		return 1
	}
	_, _ = fmt.Fprintln(stdout, "parallelbaseline: up to date")
	return 0
}
