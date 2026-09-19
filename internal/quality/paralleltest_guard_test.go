package quality

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestParallelBaselineDoesNotRegress guards the payoff of #623's test-speed
// sweep: every Test function and t.Run subtest in scope, at every nesting
// depth, must either call Parallel(), carry a `//nolint:paralleltest // "
// <reason>` with its own reason, or already be recorded -- with a reason --
// in testdata/paralleltest_baseline.txt.
//
// golangci-lint's paralleltest linter runs too (see .golangci.yml), but with
// ignore-missing: true -- annotating each of the roughly 1,900 tests that
// were already, correctly, serial (env/cwd mutation, package-seam
// reassignment, real subprocess fixtures) with an individual nolint comment
// would be a diff explosion unrelated to catching a real mistake. This test
// is the actual regression guard: a newly added serial test that is not
// explained by a nolint comment or a reasoned baseline entry must be a
// deliberate, reviewed choice, not a silent default.
//
// The baseline is keyed by package directory + test name + subtest path
// (never file:line, which shifts on every unrelated edit elsewhere in the
// file) and every entry carries a mandatory reason -- see
// internal/quality/parallelbaseline.go, which this test shares with
// internal/quality/cmd/parallelbaseline (the tool that regenerates the
// committed file after a reviewed decision to add a new serial test).
func TestParallelBaselineDoesNotRegress(t *testing.T) {
	t.Parallel()

	root, err := ParallelGuardModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	baselinePath := filepath.Join(root, "internal", "quality", "testdata", "paralleltest_baseline.txt")
	baseline, err := ParseParallelBaseline(baselinePath)
	if err != nil {
		t.Fatal(err)
	}

	serial, bareNolint, err := ScanSerialTests(root)
	if err != nil {
		t.Fatal(err)
	}

	if len(bareNolint) > 0 {
		sort.Strings(bareNolint)
		t.Errorf("%d //nolint:paralleltest comment(s) carry no reason; write "+
			"//nolint:paralleltest // <reason> instead:\n%s",
			len(bareNolint), strings.Join(bareNolint, "\n"))
	}

	var offenders []string
	for _, entry := range serial {
		if _, ok := baseline[entry.Key]; ok {
			continue
		}
		offenders = append(offenders, fmt.Sprintf("%s (%s)", entry.Key, entry.Reason))
	}
	if len(offenders) == 0 {
		return
	}
	sort.Strings(offenders)
	const maxReported = 25
	if len(offenders) > maxReported {
		offenders = append(offenders[:maxReported], fmt.Sprintf("... and %d more", len(offenders)-maxReported))
	}
	t.Fatalf("%d test(s) are serial without explanation; add t.Parallel(), a "+
		"//nolint:paralleltest // <reason> comment, or -- if reviewed and "+
		"genuinely required to stay serial -- run `go run "+
		"./internal/quality/cmd/parallelbaseline` to add a reasoned entry to "+
		"internal/quality/testdata/paralleltest_baseline.txt:\n%s",
		len(offenders), strings.Join(offenders, "\n"))
}
