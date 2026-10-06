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
// that was already uncovered there. wait.go's waitLoop spans lines 5-25 and
// merge.go's verify spans lines 28-45; wait.go:50 lies outside both.
func toleranceFixture(uncoveredNow ...int) (current []CoverageBlock, baseline PackageBaseline, touched map[string]bool, tolerances RatchetTolerances) {
	base := []CoverageBlock{
		{File: "m/pkg/wait.go", StartLine: 10, StartCol: 1, EndLine: 12, EndCol: 2, Statements: 1, Count: 1},
		{File: "m/pkg/wait.go", StartLine: 20, StartCol: 1, EndLine: 20, EndCol: 9, Statements: 1, Count: 1},
		{File: "m/pkg/merge.go", StartLine: 30, StartCol: 1, EndLine: 30, EndCol: 9, Statements: 1, Count: 1},
		{File: "m/pkg/merge.go", StartLine: 40, StartCol: 1, EndLine: 40, EndCol: 9, Statements: 1, Count: 0},
		{File: "m/pkg/wait.go", StartLine: 50, StartCol: 1, EndLine: 50, EndCol: 9, Statements: 1, Count: 1},
	}
	current = append([]CoverageBlock(nil), base...)
	for _, index := range uncoveredNow {
		current[index].Count = 0
	}
	tolerances = RatchetTolerances{"pkg": {Statements: 2, Reason: toleranceReason, Functions: []ToleratedFunction{
		{File: "pkg/wait.go", Name: "waitLoop", StartLine: 5, EndLine: 25},
		{File: "pkg/merge.go", Name: "verify", StartLine: 28, EndLine: 45},
	}}}
	return current, BaselineFromProfile(base, "m", "base-sha"), map[string]bool{"pkg/other.go": true}, tolerances
}

func TestEvaluateRatchetToleratesConfiguredTimingDependentStatements(t *testing.T) {
	t.Parallel()
	current, baseline, touched, tolerances := toleranceFixture(2, 0)
	offsets := parseLineOffsets("+++ b/pkg/other.go\n@@ -3 +3 @@\n")

	results, warnings := EvaluateRatchet(current, ChangedLines{"pkg/other.go": {3: true}}, touched, offsets, baseline, "m", tolerances)
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
		{File: "pkg/merge.go", Line: 30, Function: "verify", Reason: toleranceReason},
		{File: "pkg/wait.go", Line: 10, Function: "waitLoop", Reason: toleranceReason},
	}
	if !reflect.DeepEqual(got.Tolerated, want) {
		t.Fatalf("Tolerated = %#v, want %#v", got.Tolerated, want)
	}
}

func TestEvaluateRatchetSortsToleratedStatementsWithinOneFile(t *testing.T) {
	t.Parallel()
	current, baseline, touched, tolerances := toleranceFixture(1, 0)
	current[0], current[1] = current[1], current[0]

	results, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, baseline, "m", tolerances)
	got := results[0].Tolerated
	if len(got) != 2 || got[0].Line != 10 || got[1].Line != 20 {
		t.Fatalf("Tolerated = %#v, want wait.go:10 before wait.go:20", got)
	}
}

func TestEvaluateRatchetFailsAndNamesEveryStatementBeyondTheTolerance(t *testing.T) {
	t.Parallel()
	current, baseline, touched, tolerances := toleranceFixture(0, 1, 2)

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
	current := []CoverageBlock{
		{File: "m/pkg/wait.go", StartLine: 10, StartCol: 1, EndLine: 12, EndCol: 2, Statements: 3, Count: 0},
		{File: "m/pkg/wait.go", StartLine: 20, StartCol: 1, EndLine: 20, EndCol: 9, Statements: 2, Count: 0},
	}
	_, _, touched, tolerances := toleranceFixture()

	results, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, BaselineFromProfile(base, "m", "base-sha"), "m", tolerances)
	got := results[0]
	if got.Pass || len(got.NewlyUncoveredChanged) != 1 || got.NewlyUncoveredChanged[0].Line != 10 {
		t.Fatalf("package ratchet = %#v, want the three-statement block at wait.go:10 to fail a tolerance of 2", got)
	}
}

// Both newly uncovered statements are individually tolerable and together
// fit the allowance, but the count rose by three: one statement of the rise
// has no block to attribute it to. The count limit alone must decide.
func TestEvaluateRatchetFailsWhenTheCountExceedsTheToleranceEvenIfEveryNamedStatementFits(t *testing.T) {
	t.Parallel()
	current, baseline, touched, tolerances := toleranceFixture(0, 2)
	current[3].Statements = 2

	results, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, baseline, "m", tolerances)
	got := results[0]
	if got.Uncovered != 4 || got.BaselineUncovered != 1 {
		t.Fatalf("package ratchet = %#v, want uncovered 4 against baseline 1", got)
	}
	if got.Pass || !got.Rose || got.Tolerated != nil || len(got.NewlyUncoveredChanged) != 2 {
		t.Fatalf("package ratchet = %#v, want a failure naming both attributable statements", got)
	}
}

func TestEvaluateRatchetNeverToleratesAStatementTheChangeTouched(t *testing.T) {
	t.Parallel()
	current, baseline, _, tolerances := toleranceFixture(0)
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

// A change can add an untested branch whose text matches lines it removes
// elsewhere. Git colours those lines as moved, so ChangedLines leaves them
// out and the changed-statement rule does not fire; the tolerance must still
// see that the change put the statement there.
func TestEvaluateRatchetNeverToleratesAStatementOnAMovedLine(t *testing.T) {
	t.Parallel()
	base := []CoverageBlock{
		{File: "m/pkg/wait.go", StartLine: 8, StartCol: 1, EndLine: 8, EndCol: 9, Statements: 1, Count: 1},
	}
	current := []CoverageBlock{
		{File: "m/pkg/wait.go", StartLine: 8, StartCol: 1, EndLine: 8, EndCol: 9, Statements: 1, Count: 1},
		{File: "m/pkg/wait.go", StartLine: 10, StartCol: 1, EndLine: 12, EndCol: 2, Statements: 1, Count: 0},
	}
	_, _, _, tolerances := toleranceFixture()
	diff := "+++ b/pkg/wait.go\n@@ -9,0 +10,3 @@\n\x1b[36m+if late {\x1b[m\n\x1b[36m+\treturn pending\x1b[m\n\x1b[36m+}\x1b[m\n"
	changed := parseColorMovedDiff(diff)
	if changed.Contains("pkg/wait.go", 10) || changed.Contains("pkg/wait.go", 11) {
		t.Fatalf("changed = %#v, want the moved lines left out so this test exercises the hole", changed)
	}
	offsets := parseLineOffsets("+++ b/pkg/wait.go\n@@ -9,0 +10,3 @@\n")

	results, _ := EvaluateRatchet(current, changed, map[string]bool{"pkg/wait.go": true}, offsets, BaselineFromProfile(base, "m", "base-sha"), "m", tolerances)
	got := results[0]
	if got.Pass || !got.Rose || got.Tolerated != nil {
		t.Fatalf("package ratchet = %#v, want the moved-in statement to fail", got)
	}
	if len(got.NewlyUncoveredChanged) != 1 || got.NewlyUncoveredChanged[0].Line != 10 || got.NewlyUncoveredChanged[0].Reason != ReasonNewlyUncoveredAtBase {
		t.Fatalf("findings = %#v, want wait.go:10 named", got.NewlyUncoveredChanged)
	}
}

func TestFileLineOffsetsAddedCoversExactlyTheNewSideOfEachHunk(t *testing.T) {
	t.Parallel()
	offsets := parseLineOffsets("+++ b/a.go\n@@ -9,0 +10,3 @@\n@@ -20,2 +24,0 @@\n")["a.go"]
	for line, want := range map[int]bool{9: false, 10: true, 12: true, 13: false, 24: false} {
		if got := offsets.Added(line); got != want {
			t.Fatalf("Added(%d) = %t, want %t", line, got, want)
		}
	}
	if (FileLineOffsets{}).Added(1) {
		t.Fatal("a file the diff does not touch reports an added line")
	}
}

// Deleting a test can uncover untouched statements anywhere in the package;
// only the listed functions are known to be timing-dependent.
func TestEvaluateRatchetNeverToleratesAStatementOutsideTheListedFunctions(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(RatchetTolerances){
		"another function of a listed file": func(RatchetTolerances) {},
		"a block that runs past the function's end": func(tolerances RatchetTolerances) {
			tolerances["pkg"].Functions[0].EndLine = 49
		},
	} {
		current, baseline, touched, tolerances := toleranceFixture(4)
		mutate(tolerances)
		current[4].EndLine = 51

		results, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, baseline, "m", tolerances)
		got := results[0]
		if got.Pass || got.Tolerated != nil || len(got.NewlyUncoveredChanged) != 1 || got.NewlyUncoveredChanged[0].Line != 50 {
			t.Fatalf("%s: package ratchet = %#v, want wait.go:50 to fail", name, got)
		}
	}
}

func TestEvaluateRatchetWithdrawsTheToleranceWhenAChangedStatementIsAlsoUncovered(t *testing.T) {
	t.Parallel()
	current, baseline, touched, tolerances := toleranceFixture(0)
	current = append(current, CoverageBlock{File: "m/pkg/other.go", StartLine: 3, StartCol: 1, EndLine: 3, EndCol: 9, Statements: 1, Count: 0})

	results, _ := EvaluateRatchet(current, ChangedLines{"pkg/other.go": {3: true}}, touched, nil, baseline, "m", tolerances)
	got := results[0]
	if got.Pass || got.Tolerated != nil || len(got.NewlyUncoveredChanged) != 2 {
		t.Fatalf("package ratchet = %#v, want both the changed and the untouched statement named", got)
	}
}

func TestEvaluateRatchetStaysStrictForPackagesWithoutAToleranceEntry(t *testing.T) {
	t.Parallel()
	current, baseline, touched, tolerances := toleranceFixture(0)
	tolerances["another/pkg"] = tolerances["pkg"]
	delete(tolerances, "pkg")

	withEntryElsewhere, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, baseline, "m", tolerances)
	strict, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, baseline, "m", nil)
	if !reflect.DeepEqual(withEntryElsewhere, strict) {
		t.Fatalf("results differ:\n with other entry: %#v\n strict: %#v", withEntryElsewhere, strict)
	}
	if strict[0].Pass || len(strict[0].NewlyUncoveredChanged) != 1 {
		t.Fatalf("package ratchet = %#v, want the strict failure", strict[0])
	}
}

// A listed package the change does not touch keeps the existing
// warn-only behaviour: the tolerance neither hides the warning nor is
// reported as used.
func TestEvaluateRatchetOnlyWarnsForAListedPackageTheChangeDoesNotTouch(t *testing.T) {
	t.Parallel()
	current, baseline, _, tolerances := toleranceFixture(0)

	results, warnings := EvaluateRatchet(current, ChangedLines{}, map[string]bool{"elsewhere/x.go": true}, nil, baseline, "m", tolerances)
	got := results[0]
	if !got.Pass || !got.Rose || got.Changed || got.Tolerance != 0 || got.Tolerated != nil {
		t.Fatalf("package ratchet = %#v, want an unchanged package that rose, passed and used no tolerance", got)
	}
	if len(warnings) != 1 || warnings[0].File != "pkg/wait.go" || warnings[0].Line != 10 {
		t.Fatalf("warnings = %#v, want wait.go:10", warnings)
	}
}

func TestEvaluateRatchetReportsNoToleranceWhenNoneWasNeeded(t *testing.T) {
	t.Parallel()
	current, baseline, touched, tolerances := toleranceFixture()

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
	_, _, touched, tolerances := toleranceFixture()

	results, _ := EvaluateRatchet(current, ChangedLines{}, touched, nil, BaselineFromProfile(base, "m", "base-sha"), "m", tolerances)
	got := results[0]
	if got.Pass || !got.Rose || got.Tolerated != nil {
		t.Fatalf("package ratchet = %#v, want the count rise to fail", got)
	}
}

const tolerancePolicySource = `package orchestrate

type waiter struct{}

type queue[T any] struct{}

type pair[K comparable, V any] struct{}

func waitLoop() int {
	return 1
}

func (w *waiter) Wait() int { return 2 }

func (q queue[T]) Len() int { return 3 }

func (p *pair[K, V]) Swap() {}
`

// writeRatchetPolicy builds a checkout with internal/orchestrate/wait.go and
// the given policy.
func writeRatchetPolicy(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		ratchetToleranceConfigPath:        contents,
		"internal/orchestrate/wait.go":    tolerancePolicySource,
		"internal/orchestrate/broken.go":  "package orchestrate\n\nfunc (\n",
		"internal/orchestrate/a_test.go":  "package orchestrate\n\nfunc helper() {}\n",
		"internal/orchestrate/notes.txt":  "func waitLoop\n",
		"internal/orchestrate/sub/sub.go": "package sub\n\nfunc Deep() {}\n",
		"internal/plainfile":              "not a directory\n",
		"cmd/wb/main.go":                  "package main\n\nfunc main() {}\n",
		"cmd/other/main.go":               "package main\n\nfunc main() {}\n",
		"cmd/third/main.go":               "package main\n\nfunc main() {}\n",
		"root.go":                         "package root\n\nfunc Root() {}\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func tolerancePolicyEntry(pkg, statements, functions, reason string) string {
	entry := "  - package: " + pkg + "\n"
	if statements != "" {
		entry += "    statements: " + statements + "\n"
	}
	if functions != "" {
		entry += "    functions: " + functions + "\n"
	}
	if reason != "" {
		entry += "    reason: " + reason + "\n"
	}
	return entry
}

func TestLoadRatchetTolerancesResolvesFunctionsAndMethodsAgainstTheCheckout(t *testing.T) {
	t.Parallel()
	root := writeRatchetPolicy(t, "version: 1\ntiming_tolerance:\n"+
		tolerancePolicyEntry("internal/orchestrate", "2", `["wait.go:waitLoop", "wait.go:waiter.Wait", "wait.go:queue.Len", "wait.go:pair.Swap"]`, `" timing "`)+
		tolerancePolicyEntry("cmd/wb", "5", `["main.go:main"]`, "cap")+
		tolerancePolicyEntry(".", "1", `["root.go:Root"]`, "module root"))
	got, err := LoadRatchetTolerances(root)
	if err != nil {
		t.Fatal(err)
	}
	want := RatchetTolerances{
		"internal/orchestrate": {Statements: 2, Reason: "timing", Functions: []ToleratedFunction{
			{File: "internal/orchestrate/wait.go", Name: "waitLoop", StartLine: 9, EndLine: 11},
			{File: "internal/orchestrate/wait.go", Name: "waiter.Wait", StartLine: 13, EndLine: 13},
			{File: "internal/orchestrate/wait.go", Name: "queue.Len", StartLine: 15, EndLine: 15},
			{File: "internal/orchestrate/wait.go", Name: "pair.Swap", StartLine: 17, EndLine: 17},
		}},
		"cmd/wb": {Statements: 5, Reason: "cap", Functions: []ToleratedFunction{{File: "cmd/wb/main.go", Name: "main", StartLine: 3, EndLine: 3}}},
		".":      {Statements: 1, Reason: "module root", Functions: []ToleratedFunction{{File: "root.go", Name: "Root", StartLine: 3, EndLine: 3}}},
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
	const pkg, functions = "internal/orchestrate", `["wait.go:waitLoop"]`
	header := "version: 1\ntiming_tolerance:\n"
	entry := func(pkg, statements, functions, reason string) string {
		return header + tolerancePolicyEntry(pkg, statements, functions, reason)
	}
	for name, test := range map[string]struct{ contents, want string }{
		"missing reason":               {entry(pkg, "2", functions, ""), "needs a reason"},
		"blank reason":                 {entry(pkg, "2", functions, `"  "`), "needs a reason"},
		"missing statements":           {entry(pkg, "", functions, "r"), "plain decimal integer"},
		"zero statements":              {entry(pkg, "0", functions, "r"), "between 1 and 5, got 0"},
		"negative statements":          {entry(pkg, "-1", functions, "r"), "plain decimal integer"},
		"above the cap":                {entry(pkg, "6", functions, "r"), "between 1 and 5, got 6"},
		"fractional statements":        {entry(pkg, "2.9", functions, "r"), "plain decimal integer"},
		"hexadecimal statements":       {entry(pkg, "0x5", functions, "r"), "plain decimal integer"},
		"octal statements":             {entry(pkg, "0o5", functions, "r"), "plain decimal integer"},
		"quoted statements":            {entry(pkg, `"2"`, functions, "r"), "plain decimal integer"},
		"list of statements":           {entry(pkg, "[2]", functions, "r"), "plain decimal integer"},
		"null entry":                   {header + "  - ~\n", "timing_tolerance[0] must be a mapping"},
		"null entry after a valid one": {entry(pkg, "2", functions, "r") + "  - ~\n", "timing_tolerance[1] must be a mapping"},
		"scalar entry":                 {header + "  - internal/orchestrate\n", "decode coverage ratchet policy"},
		"no package":                   {header + "  - statements: 1\n    reason: r\n", "must be a clean directory path"},
		"dot-slash package":            {entry("./internal/orchestrate", "2", functions, "r"), "must be a clean directory path"},
		"trailing slash":               {entry("internal/orchestrate/", "2", functions, "r"), "must be a clean directory path"},
		"absolute package":             {entry("/internal/orchestrate", "2", functions, "r"), "must be a clean directory path"},
		"parent segment":               {entry("../internal/orchestrate", "2", functions, "r"), "must be a clean directory path"},
		"package pattern":              {entry("internal/...", "2", functions, "r"), "must be a clean directory path"},
		"module-qualified package":     {entry("example.test/app/internal/orchestrate", "2", functions, "r"), "must be a clean directory path"},
		"wrong case":                   {entry("internal/Orchestrate", "2", functions, "r"), "must be a clean directory path"},
		"wrong case in the parent":     {entry("Internal/orchestrate", "2", functions, "r"), "must be a clean directory path"},
		"missing directory":            {entry("internal/absent", "2", functions, "r"), "must be a clean directory path"},
		"a file, not a directory":      {entry("internal/plainfile", "2", functions, "r"), "must be a clean directory path"},
		"below a file":                 {entry("internal/plainfile/x", "2", functions, "r"), "must be a clean directory path"},
		"repeated package":             {entry(pkg, "1", functions, "r") + tolerancePolicyEntry(pkg, "1", functions, "r"), "repeats timing_tolerance package"},
		"no functions":                 {entry(pkg, "2", "", "r"), "between 1 and 5 functions, got 0"},
		"empty functions":              {entry(pkg, "2", "[]", "r"), "between 1 and 5 functions, got 0"},
		"too many functions":           {entry(pkg, "2", `["wait.go:a", "wait.go:b", "wait.go:c", "wait.go:d", "wait.go:e", "wait.go:f"]`, "r"), "between 1 and 5 functions, got 6"},
		"repeated function":            {entry(pkg, "2", `["wait.go:waitLoop", "wait.go:waitLoop"]`, "r"), `repeats function "wait.go:waitLoop"`},
		"unknown function":             {entry(pkg, "2", `["wait.go:gone"]`, "r"), `function "wait.go:gone" is not declared in internal/orchestrate/wait.go`},
		"method without its type":      {entry(pkg, "2", `["wait.go:Wait"]`, "r"), "is not declared"},
		"a type, not a function":       {entry(pkg, "2", `["wait.go:waiter"]`, "r"), "is not declared"},
		"unknown file":                 {entry(pkg, "2", `["absent.go:waitLoop"]`, "r"), `function "absent.go:waitLoop"`},
		"unparseable file":             {entry(pkg, "2", `["broken.go:waitLoop"]`, "r"), `function "broken.go:waitLoop"`},
		"function without a file":      {entry(pkg, "2", `["waitLoop"]`, "r"), "must be written file.go:Function"},
		"file without a function":      {entry(pkg, "2", `["wait.go:"]`, "r"), "must be written file.go:Function"},
		"file in another directory":    {entry(pkg, "2", `["sub/sub.go:Deep"]`, "r"), "must be written file.go:Function"},
		"test file":                    {entry(pkg, "2", `["a_test.go:helper"]`, "r"), "must be written file.go:Function"},
		"not a Go file":                {entry(pkg, "2", `["notes.txt:waitLoop"]`, "r"), "must be written file.go:Function"},
		"more than three entries": {header + tolerancePolicyEntry(pkg, "1", functions, "r") + tolerancePolicyEntry("cmd/wb", "1", `["main.go:main"]`, "r") +
			tolerancePolicyEntry("cmd/other", "1", `["main.go:main"]`, "r") + tolerancePolicyEntry("cmd/third", "1", `["main.go:main"]`, "r"), "4 timing_tolerance entries; at most 3"},
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

// TestRepositoryRatchetPolicyIsExactlyTheReviewedTolerance pins this
// repository's own .wb/coverage-ratchet.yaml. The tolerance is a stopgap
// that must only shrink: adding a package or a function, or raising a
// statement count, has to change this test in the same pull request, so a
// reviewer sees the widening twice. Removing an entry needs the same edit
// and is always welcome.
func TestRepositoryRatchetPolicyIsExactlyTheReviewedTolerance(t *testing.T) {
	t.Parallel()
	tolerances, err := LoadRatchetTolerances(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for pkg, tolerance := range tolerances {
		var functions []string
		for _, function := range tolerance.Functions {
			functions = append(functions, function.File+":"+function.Name)
		}
		got[pkg] = fmt.Sprintf("%d statement(s) in %s", tolerance.Statements, strings.Join(functions, ", "))
	}
	want := map[string]string{
		"internal/githubchecks": "1 statement(s) in internal/githubchecks/ciwait.go:waitForCommitChecksWith",
		"internal/orchestrate":  "1 statement(s) in internal/orchestrate/worktree_merge.go:verifyWorktreeMergeTargetChecks",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repository tolerance policy = %#v, want exactly the reviewed %#v", got, want)
	}
}
