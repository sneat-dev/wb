package quality

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const toleranceReason = "wait-loop deadline branch is covered only when the timer wins"

// toleranceFixture models internal/orchestrate on a pull request that edits
// pkg/other.go: three statements that were covered at the merge base and one
// that was already uncovered there.
func toleranceFixture(uncoveredNow ...int) (current []CoverageBlock, baseline PackageBaseline, touched map[string]bool) {
	base := []CoverageBlock{
		{File: "m/pkg/wait.go", StartLine: 10, StartCol: 1, EndLine: 12, EndCol: 2, Statements: 1, Count: 1},
		{File: "m/pkg/wait.go", StartLine: 20, StartCol: 1, EndLine: 20, EndCol: 9, Statements: 1, Count: 1},
		{File: "m/pkg/merge.go", StartLine: 30, StartCol: 1, EndLine: 30, EndCol: 9, Statements: 1, Count: 1},
		{File: "m/pkg/merge.go", StartLine: 40, StartCol: 1, EndLine: 40, EndCol: 9, Statements: 1, Count: 0},
	}
	current = append([]CoverageBlock(nil), base...)
	for _, index := range uncoveredNow {
		current[index].Count = 0
	}
	return current, BaselineFromProfile(base, "m", "base-sha"), map[string]bool{"pkg/other.go": true}
}

func TestEvaluateRatchetToleratesConfiguredTimingDependentStatements(t *testing.T) {
	t.Parallel()
	current, baseline, touched := toleranceFixture(2, 0)
	tolerances := RatchetTolerances{"pkg": {Statements: 2, Reason: toleranceReason}}

	results, warnings := EvaluateRatchet(current, ChangedLines{"pkg/other.go": {3: true}}, touched, nil, baseline, "m", tolerances)
	if len(results) != 1 || len(warnings) != 0 {
		t.Fatalf("results = %#v, warnings = %#v", results, warnings)
	}
	got := results[0]
	if !got.Pass || got.Rose || len(got.NewlyUncoveredChanged) != 0 {
		t.Fatalf("package ratchet = %#v, want a pass with no findings", got)
	}
	if got.Uncovered != 3 || got.BaselineUncovered != 1 || got.Tolerance != 2 {
		t.Fatalf("package ratchet = %#v, want uncovered 3 against baseline 1 under tolerance 2", got)
	}
	want := []ToleratedStatement{
		{File: "pkg/merge.go", Line: 30, Reason: toleranceReason},
		{File: "pkg/wait.go", Line: 10, Reason: toleranceReason},
	}
	if !reflect.DeepEqual(got.Tolerated, want) {
		t.Fatalf("Tolerated = %#v, want %#v", got.Tolerated, want)
	}
}

func TestEvaluateRatchetSortsToleratedStatementsWithinOneFile(t *testing.T) {
	t.Parallel()
	current, baseline, touched := toleranceFixture(1, 0)
	current[0], current[1] = current[1], current[0]
	tolerances := RatchetTolerances{"pkg": {Statements: 2, Reason: toleranceReason}}

	results, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, baseline, "m", tolerances)
	got := results[0].Tolerated
	if len(got) != 2 || got[0].Line != 10 || got[1].Line != 20 {
		t.Fatalf("Tolerated = %#v, want wait.go:10 before wait.go:20", got)
	}
}

func TestEvaluateRatchetFailsAndNamesEveryStatementBeyondTheTolerance(t *testing.T) {
	t.Parallel()
	current, baseline, touched := toleranceFixture(0, 1, 2)
	tolerances := RatchetTolerances{"pkg": {Statements: 2, Reason: toleranceReason}}

	results, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, baseline, "m", tolerances)
	got := results[0]
	if got.Pass || !got.Rose || got.Tolerance != 0 || got.Tolerated != nil {
		t.Fatalf("package ratchet = %#v, want a failure that used no tolerance", got)
	}
	var named []string
	for _, finding := range got.NewlyUncoveredChanged {
		if finding.Reason != ReasonNewlyUncoveredAtBase {
			t.Fatalf("finding = %#v, want reason %q", finding, ReasonNewlyUncoveredAtBase)
		}
		named = append(named, fmt.Sprintf("%s:%d", finding.File, finding.Line))
	}
	if strings.Join(named, " ") != "pkg/merge.go:30 pkg/wait.go:10 pkg/wait.go:20" {
		t.Fatalf("findings = %#v, want all three newly uncovered statements", got.NewlyUncoveredChanged)
	}
}

func TestEvaluateRatchetCountsStatementsNotBlocksAgainstTheTolerance(t *testing.T) {
	t.Parallel()
	base := []CoverageBlock{
		{File: "m/pkg/wait.go", StartLine: 10, StartCol: 1, EndLine: 12, EndCol: 2, Statements: 3, Count: 1},
		{File: "m/pkg/wait.go", StartLine: 20, StartCol: 1, EndLine: 20, EndCol: 9, Statements: 4, Count: 0},
	}
	// The count rises by only one (4 to 5), which the count rule alone would
	// allow, but the newly uncovered block holds three statements.
	baseline := BaselineFromProfile(base, "m", "base-sha")
	current := []CoverageBlock{
		{File: "m/pkg/wait.go", StartLine: 10, StartCol: 1, EndLine: 12, EndCol: 2, Statements: 3, Count: 0},
		{File: "m/pkg/wait.go", StartLine: 20, StartCol: 1, EndLine: 20, EndCol: 9, Statements: 2, Count: 0},
	}
	tolerances := RatchetTolerances{"pkg": {Statements: 2, Reason: toleranceReason}}

	results, _ := EvaluateRatchet(current, ChangedLines{}, map[string]bool{"pkg/other.go": true}, nil, baseline, "m", tolerances)
	got := results[0]
	if got.Pass || len(got.NewlyUncoveredChanged) != 1 || got.NewlyUncoveredChanged[0].Line != 10 {
		t.Fatalf("package ratchet = %#v, want the three-statement block at wait.go:10 to fail a tolerance of 2", got)
	}
}

func TestEvaluateRatchetNeverToleratesAStatementTheChangeTouched(t *testing.T) {
	t.Parallel()
	current, baseline, _ := toleranceFixture(0)
	tolerances := RatchetTolerances{"pkg": {Statements: 2, Reason: toleranceReason}}
	// Line 11 is inside the block that starts at line 10: the change edited
	// the statement, so it is the author's to cover.
	changed := ChangedLines{"pkg/wait.go": {11: true}}

	results, _ := EvaluateRatchet(current, changed, map[string]bool{"pkg/wait.go": true}, nil, baseline, "m", tolerances)
	got := results[0]
	if got.Pass || !got.Rose || got.Tolerated != nil {
		t.Fatalf("package ratchet = %#v, want a failure with nothing tolerated", got)
	}
	reasons := map[string]bool{}
	for _, finding := range got.NewlyUncoveredChanged {
		reasons[finding.Reason] = true
	}
	if !reasons[ReasonChangedStatementUncovered] {
		t.Fatalf("findings = %#v, want the changed-statement rule to fire", got.NewlyUncoveredChanged)
	}
}

func TestEvaluateRatchetWithdrawsTheToleranceWhenAChangedStatementIsAlsoUncovered(t *testing.T) {
	t.Parallel()
	current, baseline, touched := toleranceFixture(0)
	current = append(current, CoverageBlock{File: "m/pkg/other.go", StartLine: 3, StartCol: 1, EndLine: 3, EndCol: 9, Statements: 1, Count: 0})
	tolerances := RatchetTolerances{"pkg": {Statements: 2, Reason: toleranceReason}}

	results, _ := EvaluateRatchet(current, ChangedLines{"pkg/other.go": {3: true}}, touched, nil, baseline, "m", tolerances)
	got := results[0]
	if got.Pass || got.Tolerated != nil || len(got.NewlyUncoveredChanged) != 2 {
		t.Fatalf("package ratchet = %#v, want both the changed and the untouched statement named", got)
	}
}

func TestEvaluateRatchetStaysStrictForPackagesWithoutAToleranceEntry(t *testing.T) {
	t.Parallel()
	current, baseline, touched := toleranceFixture(0)
	tolerances := RatchetTolerances{"another/pkg": {Statements: 2, Reason: toleranceReason}}

	withEntryElsewhere, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, baseline, "m", tolerances)
	strict, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, baseline, "m", nil)
	if !reflect.DeepEqual(withEntryElsewhere, strict) {
		t.Fatalf("results differ:\n with other entry: %#v\n strict: %#v", withEntryElsewhere, strict)
	}
	if strict[0].Pass || len(strict[0].NewlyUncoveredChanged) != 1 {
		t.Fatalf("package ratchet = %#v, want the strict failure", strict[0])
	}
}

func TestEvaluateRatchetReportsNoToleranceWhenNoneWasNeeded(t *testing.T) {
	t.Parallel()
	current, baseline, touched := toleranceFixture()
	tolerances := RatchetTolerances{"pkg": {Statements: 2, Reason: toleranceReason}}

	results, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, baseline, "m", tolerances)
	got := results[0]
	if !got.Pass || got.Tolerance != 0 || got.Tolerated != nil {
		t.Fatalf("package ratchet = %#v, want a plain pass", got)
	}
}

func TestEvaluateRatchetDoesNotTolerateARiseItCannotAttribute(t *testing.T) {
	t.Parallel()
	base := []CoverageBlock{{File: "m/pkg/wait.go", StartLine: 10, StartCol: 1, EndLine: 10, EndCol: 9, Statements: 1, Count: 0}}
	current := []CoverageBlock{{File: "m/pkg/wait.go", StartLine: 10, StartCol: 1, EndLine: 10, EndCol: 9, Statements: 2, Count: 0}}
	tolerances := RatchetTolerances{"pkg": {Statements: 2, Reason: toleranceReason}}

	results, _ := EvaluateRatchet(current, ChangedLines{}, map[string]bool{"pkg/other.go": true}, nil, BaselineFromProfile(base, "m", "base-sha"), "m", tolerances)
	got := results[0]
	if got.Pass || !got.Rose || got.Tolerated != nil {
		t.Fatalf("package ratchet = %#v, want the count rise to fail", got)
	}
}

func writeRatchetPolicy(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, ratchetToleranceConfigPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadRatchetTolerancesReadsEntriesAndNormalisesThePackage(t *testing.T) {
	t.Parallel()
	root := writeRatchetPolicy(t, "version: 1\ntiming_tolerance:\n  - package: ./internal/orchestrate/\n    statements: 2\n    reason: \" timing \"\n  - package: cmd/wb\n    statements: 5\n    reason: cap\n")
	got, err := LoadRatchetTolerances(root)
	if err != nil {
		t.Fatal(err)
	}
	want := RatchetTolerances{
		"internal/orchestrate": {Statements: 2, Reason: "timing"},
		"cmd/wb":               {Statements: 5, Reason: "cap"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tolerances = %#v, want %#v", got, want)
	}
}

func TestLoadRatchetTolerancesTreatsAMissingFileAsNoTolerance(t *testing.T) {
	t.Parallel()
	got, err := LoadRatchetTolerances(t.TempDir())
	if err != nil || got != nil {
		t.Fatalf("tolerances = %#v, err = %v, want none", got, err)
	}
}

func TestLoadRatchetTolerancesFailsClosed(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct{ contents, want string }{
		"missing reason":    {"version: 1\ntiming_tolerance:\n  - package: pkg\n    statements: 2\n", "needs a reason"},
		"blank reason":      {"version: 1\ntiming_tolerance:\n  - package: pkg\n    statements: 2\n    reason: \"  \"\n", "needs a reason"},
		"zero statements":   {"version: 1\ntiming_tolerance:\n  - package: pkg\n    reason: r\n", "between 1 and 5, got 0"},
		"negative":          {"version: 1\ntiming_tolerance:\n  - package: pkg\n    statements: -1\n    reason: r\n", "between 1 and 5, got -1"},
		"above the cap":     {"version: 1\ntiming_tolerance:\n  - package: pkg\n    statements: 6\n    reason: r\n", "between 1 and 5, got 6"},
		"no package":        {"version: 1\ntiming_tolerance:\n  - statements: 1\n    reason: r\n", "names no package"},
		"repeated package":  {"version: 1\ntiming_tolerance:\n  - package: pkg\n    statements: 1\n    reason: r\n  - package: ./pkg\n    statements: 1\n    reason: r\n", "repeats timing_tolerance package"},
		"wrong version":     {"version: 2\n", "has version 2; want 1"},
		"unknown field":     {"version: 1\nallow: everything\n", "decode coverage ratchet policy"},
		"several documents": {"version: 1\n---\nversion: 1\n", "exactly one YAML document"},
	} {
		_, err := LoadRatchetTolerances(writeRatchetPolicy(t, test.contents))
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: err = %v, want it to contain %q", name, err, test.want)
		}
	}
}

func TestLoadRatchetTolerancesReportsAnUnreadablePolicy(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ratchetToleranceConfigPath), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRatchetTolerances(root)
	if err == nil || !strings.Contains(err.Error(), "read coverage ratchet policy") {
		t.Fatalf("err = %v, want a read failure", err)
	}
}

// TestRepositoryRatchetPolicyIsValid keeps this repository's own
// .wb/coverage-ratchet.yaml loadable, so a bad edit fails here and not in
// every pull request's coverage job.
func TestRepositoryRatchetPolicyIsValid(t *testing.T) {
	t.Parallel()
	tolerances, err := LoadRatchetTolerances(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for pkg := range tolerances {
		if info, err := os.Stat(filepath.Join("..", "..", filepath.FromSlash(pkg))); err != nil || !info.IsDir() {
			t.Fatalf("tolerance names %q, which is not a package directory in this repository", pkg)
		}
	}
}
