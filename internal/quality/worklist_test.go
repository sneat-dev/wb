package quality

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeWorklistFixture writes go.mod plus every relativePath -> contents
// entry into a fresh temp module root and returns its path.
func writeWorklistFixture(t *testing.T, modulePath string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for relativePath, contents := range files {
		full := filepath.Join(dir, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const worklistFixtureA = `package pkg

func FuncA1() int {
	return 1
}

func FuncA2() int {
	return 2
}
`

const worklistFixtureB = `package pkg

func FuncB1() int {
	return 1
}
`

func TestBuildWorklistRejectsUnitSizeBelowOne(t *testing.T) {
	t.Parallel()
	if _, err := BuildWorklist(nil, "fixture.test/worklist", t.TempDir(), 0); err == nil {
		t.Fatal("want error for --unit-size 0")
	}
}

func TestBuildWorklistWithNoUncoveredBlocksReturnsEmptyWorklist(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "fixture.test/worklist/pkg/a.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 10, Statements: 1, Count: 1},
	}
	// moduleRoot need not exist: an all-covered profile never opens a source
	// file, so a missing/garbage root must not turn into an error here.
	worklist, err := BuildWorklist(blocks, "fixture.test/worklist", "/does/not/exist", 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(worklist.Units) != 0 || worklist.TotalUncoveredStatements != 0 || worklist.UnitSize != 300 {
		t.Fatalf("worklist = %#v, want empty", worklist)
	}
}

func TestBuildWorklistGroupsWholeFunctionsAndMarksSharedFiles(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/worklist"
	moduleRoot := writeWorklistFixture(t, modulePath, map[string]string{
		"pkg/a.go": worklistFixtureA,
		"pkg/b.go": worklistFixtureB,
	})
	blocks := []CoverageBlock{
		// Deliberately out of file/line order: BuildWorklist must sort before
		// grouping, not rely on profile order.
		{File: modulePath + "/pkg/b.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 10, Statements: 5, Count: 0},
		{File: modulePath + "/pkg/a.go", StartLine: 8, StartCol: 2, EndLine: 8, EndCol: 10, Statements: 10, Count: 0},
		{File: modulePath + "/pkg/a.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 10, Statements: 10, Count: 0},
		// A covered block in the same function range must never appear.
		{File: modulePath + "/pkg/a.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 10, Statements: 10, Count: 1},
	}

	worklist, err := BuildWorklist(blocks, modulePath, moduleRoot, 12)
	if err != nil {
		t.Fatal(err)
	}
	if worklist.TotalUncoveredStatements != 25 {
		t.Fatalf("TotalUncoveredStatements = %d, want 25", worklist.TotalUncoveredStatements)
	}
	if len(worklist.Units) != 3 {
		t.Fatalf("units = %#v, want 3", worklist.Units)
	}

	unit0, unit1, unit2 := worklist.Units[0], worklist.Units[1], worklist.Units[2]
	if unit0.Statements != 10 || unit1.Statements != 10 || unit2.Statements != 5 {
		t.Fatalf("unit statement totals = %d,%d,%d, want 10,10,5", unit0.Statements, unit1.Statements, unit2.Statements)
	}
	if len(unit0.Blocks) != 1 || unit0.Blocks[0].Function != "FuncA1" {
		t.Fatalf("unit0.Blocks = %#v, want one FuncA1 block", unit0.Blocks)
	}
	if len(unit1.Blocks) != 1 || unit1.Blocks[0].Function != "FuncA2" {
		t.Fatalf("unit1.Blocks = %#v, want one FuncA2 block", unit1.Blocks)
	}
	if len(unit2.Blocks) != 1 || unit2.Blocks[0].Function != "FuncB1" {
		t.Fatalf("unit2.Blocks = %#v, want one FuncB1 block", unit2.Blocks)
	}
	if !reflect.DeepEqual(unit0.Files, []string{modulePath + "/pkg/a.go"}) {
		t.Fatalf("unit0.Files = %#v", unit0.Files)
	}
	if !reflect.DeepEqual(unit2.Files, []string{modulePath + "/pkg/b.go"}) {
		t.Fatalf("unit2.Files = %#v", unit2.Files)
	}
	if !reflect.DeepEqual(unit0.SharesFileWith, []int{1}) {
		t.Fatalf("unit0.SharesFileWith = %#v, want [1]", unit0.SharesFileWith)
	}
	if !reflect.DeepEqual(unit1.SharesFileWith, []int{0}) {
		t.Fatalf("unit1.SharesFileWith = %#v, want [0]", unit1.SharesFileWith)
	}
	if len(unit2.SharesFileWith) != 0 {
		t.Fatalf("unit2.SharesFileWith = %#v, want none", unit2.SharesFileWith)
	}

	// Every uncovered block appears in exactly one unit (task-25 Verifies 3).
	seen := map[string]int{}
	for _, unit := range worklist.Units {
		for _, block := range unit.Blocks {
			key := block.File + ":" + block.Function
			seen[key]++
		}
	}
	for key, count := range seen {
		if count != 1 {
			t.Fatalf("block %q appears in %d units, want exactly 1", key, count)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("saw %d distinct uncovered blocks, want 3", len(seen))
	}
}

func TestBuildWorklistIsDeterministicRegardlessOfProfileOrder(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/worklist"
	moduleRoot := writeWorklistFixture(t, modulePath, map[string]string{
		"pkg/a.go": worklistFixtureA,
		"pkg/b.go": worklistFixtureB,
	})
	forward := []CoverageBlock{
		{File: modulePath + "/pkg/a.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 10, Statements: 10, Count: 0},
		{File: modulePath + "/pkg/a.go", StartLine: 8, StartCol: 2, EndLine: 8, EndCol: 10, Statements: 10, Count: 0},
		{File: modulePath + "/pkg/b.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 10, Statements: 5, Count: 0},
	}
	reversed := []CoverageBlock{forward[2], forward[1], forward[0]}

	first, err := BuildWorklist(forward, modulePath, moduleRoot, 12)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildWorklist(reversed, modulePath, moduleRoot, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("worklist depends on profile order:\nforward:  %#v\nreversed: %#v", first, second)
	}
}

func TestBuildWorklistAttributesUnenclosedBlockAsFileLevel(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/worklist"
	moduleRoot := writeWorklistFixture(t, modulePath, map[string]string{
		"pkg/init.go": "package pkg\n\nvar Global = 1\n\nfunc Covered() int { return 1 }\n",
	})
	blocks := []CoverageBlock{
		{File: modulePath + "/pkg/init.go", StartLine: 3, StartCol: 1, EndLine: 3, EndCol: 15, Statements: 1, Count: 0},
	}
	worklist, err := BuildWorklist(blocks, modulePath, moduleRoot, 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(worklist.Units) != 1 || len(worklist.Units[0].Blocks) != 1 {
		t.Fatalf("units = %#v", worklist.Units)
	}
	if got := worklist.Units[0].Blocks[0].Function; got != "" {
		t.Fatalf("Function = %q, want empty (file-level)", got)
	}
}

func TestBuildWorklistErrorsWhenSourceFileIsMissing(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/worklist"
	moduleRoot := writeWorklistFixture(t, modulePath, map[string]string{})
	blocks := []CoverageBlock{
		{File: modulePath + "/pkg/missing.go", StartLine: 3, StartCol: 1, EndLine: 3, EndCol: 2, Statements: 1, Count: 0},
	}
	if _, err := BuildWorklist(blocks, modulePath, moduleRoot, 300); err == nil {
		t.Fatal("want error when the coverage profile names a file absent from moduleRoot")
	}
}

func TestBuildWorklistErrorsWhenSourceFileHasInvalidSyntax(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/worklist"
	moduleRoot := writeWorklistFixture(t, modulePath, map[string]string{
		"pkg/broken.go": "package pkg\n\nfunc Broken( {\n",
	})
	blocks := []CoverageBlock{
		{File: modulePath + "/pkg/broken.go", StartLine: 3, StartCol: 1, EndLine: 3, EndCol: 2, Statements: 1, Count: 0},
	}
	if _, err := BuildWorklist(blocks, modulePath, moduleRoot, 300); err == nil {
		t.Fatal("want error when the source file does not parse")
	}
}

func TestSourceFilePathMapsModuleQualifiedFileUnderModuleRoot(t *testing.T) {
	t.Parallel()
	got := sourceFilePath("fixture.test/worklist/pkg/a.go", "fixture.test/worklist", "/root")
	want := filepath.Join("/root", "pkg", "a.go")
	if got != want {
		t.Fatalf("sourceFilePath = %q, want %q", got, want)
	}
}

func TestEnclosingFunctionReturnsEmptyWhenNoRangeContains(t *testing.T) {
	t.Parallel()
	if got := enclosingFunction(nil, 1, 1); got != "" {
		t.Fatalf("enclosingFunction(nil, ...) = %q, want empty", got)
	}
	ranges := []funcRange{{name: "Other", startLine: 10, endLine: 20}}
	if got := enclosingFunction(ranges, 1, 5); got != "" {
		t.Fatalf("enclosingFunction = %q, want empty for a non-overlapping range", got)
	}
}

const worklistFixtureReceivers = `package receivers

func Plain() int { return 1 }

type Box struct{ v int }

func (b *Box) SetPointer(v int) { b.v = v }

func (b Box) GetValue() int { return b.v }

type Generic[T any] struct{ v T }

func (g Generic[T]) GetGeneric() T { return g.v }

func (g *Generic[T]) SetGeneric(v T) { g.v = v }
`

func TestParseFunctionRangesNamesPlainPointerValueAndGenericReceivers(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "receivers.go")
	if err := os.WriteFile(path, []byte(worklistFixtureReceivers), 0o644); err != nil {
		t.Fatal(err)
	}
	ranges, err := parseFunctionRanges(path)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, r := range ranges {
		names = append(names, r.name)
	}
	want := []string{"Plain", "(*Box).SetPointer", "Box.GetValue", "GetGeneric", "SetGeneric"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %#v, want %#v", names, want)
	}
}

func TestParseFunctionRangesErrorsOnMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := parseFunctionRanges(filepath.Join(t.TempDir(), "missing.go")); err == nil {
		t.Fatal("want error for a missing source file")
	}
}

func TestBlockSortsBeforeOrdersByEveryPositionFieldInTurn(t *testing.T) {
	t.Parallel()
	a := CoverageBlock{File: "a.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 5}
	b := CoverageBlock{File: "b.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 5}
	if !blockSortsBefore(a, b) || blockSortsBefore(b, a) {
		t.Fatal("want file to break the tie first")
	}
	sameFileLaterLine := CoverageBlock{File: "a.go", StartLine: 5, StartCol: 2, EndLine: 5, EndCol: 5}
	if !blockSortsBefore(a, sameFileLaterLine) || blockSortsBefore(sameFileLaterLine, a) {
		t.Fatal("want start line to break the tie second")
	}
	laterCol := CoverageBlock{File: "a.go", StartLine: 4, StartCol: 3, EndLine: 4, EndCol: 5}
	if !blockSortsBefore(a, laterCol) || blockSortsBefore(laterCol, a) {
		t.Fatal("want start column to break the tie third")
	}
	laterEndLine := CoverageBlock{File: "a.go", StartLine: 4, StartCol: 2, EndLine: 5, EndCol: 5}
	if !blockSortsBefore(a, laterEndLine) || blockSortsBefore(laterEndLine, a) {
		t.Fatal("want end line to break the tie fourth")
	}
	laterEndCol := CoverageBlock{File: "a.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 9}
	if !blockSortsBefore(a, laterEndCol) || blockSortsBefore(laterEndCol, a) {
		t.Fatal("want end column to break the tie last")
	}
}

func TestBuildWorklistMergesEveryBlockOfTheSameFunctionIntoOneGroup(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/worklist"
	moduleRoot := writeWorklistFixture(t, modulePath, map[string]string{
		"pkg/multi.go": "package pkg\n\nfunc FuncMulti() int {\n\tx := 1\n\treturn x\n}\n",
	})
	blocks := []CoverageBlock{
		{File: modulePath + "/pkg/multi.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 8, Statements: 3, Count: 0},
		{File: modulePath + "/pkg/multi.go", StartLine: 5, StartCol: 2, EndLine: 5, EndCol: 10, Statements: 4, Count: 0},
	}
	// unitSize (6) is deliberately smaller than the two blocks' combined 7
	// statements: if the two blocks were not merged into one funcGroup before
	// packing, the packer would flush between them into two separate units
	// (this is what pins the merge, not just the final counts).
	worklist, err := BuildWorklist(blocks, modulePath, moduleRoot, 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(worklist.Units) != 1 {
		t.Fatalf("units = %#v, want 1 (both blocks belong to one function and must never split across units)", worklist.Units)
	}
	unit := worklist.Units[0]
	if unit.Statements != 7 || len(unit.Blocks) != 2 {
		t.Fatalf("unit = %#v, want both FuncMulti blocks merged into one group", unit)
	}
	for _, block := range unit.Blocks {
		if block.Function != "FuncMulti" {
			t.Fatalf("block.Function = %q, want FuncMulti", block.Function)
		}
	}
}

func TestBuildWorklistKeepsWholeFileInOneUnitWhenItFits(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/worklist"
	moduleRoot := writeWorklistFixture(t, modulePath, map[string]string{
		"pkg/a.go": worklistFixtureA,
	})
	blocks := []CoverageBlock{
		{File: modulePath + "/pkg/a.go", StartLine: 4, StartCol: 2, EndLine: 4, EndCol: 10, Statements: 10, Count: 0},
		{File: modulePath + "/pkg/a.go", StartLine: 8, StartCol: 2, EndLine: 8, EndCol: 10, Statements: 10, Count: 0},
	}
	worklist, err := BuildWorklist(blocks, modulePath, moduleRoot, 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(worklist.Units) != 1 {
		t.Fatalf("units = %#v, want 1 (both functions fit in one unit)", worklist.Units)
	}
	unit := worklist.Units[0]
	if unit.Statements != 20 {
		t.Fatalf("unit.Statements = %d, want 20", unit.Statements)
	}
	// sortedUniqueFiles must dedupe: two functions, same file, one entry.
	if !reflect.DeepEqual(unit.Files, []string{modulePath + "/pkg/a.go"}) {
		t.Fatalf("unit.Files = %#v, want a single deduped entry", unit.Files)
	}
	if len(unit.SharesFileWith) != 0 {
		t.Fatalf("unit.SharesFileWith = %#v, want none (only one unit exists)", unit.SharesFileWith)
	}
}

func TestPackWorklistUnitsWithNoBlocksReturnsNoUnits(t *testing.T) {
	t.Parallel()
	if units := packWorklistUnits(nil, 300); len(units) != 0 {
		t.Fatalf("units = %#v, want none", units)
	}
}
