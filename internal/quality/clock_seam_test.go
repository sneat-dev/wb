package quality

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeClockSeamFixture writes a small, self-contained Go source file under
// dir for findClockSeamViolationsAgainst to parse. Each fixture is valid
// enough on its own (parser.ParseFile parses one file at a time, with no
// package-wide type-checking), so the fixtures never need to compile as a
// real package.
func writeClockSeamFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
}

// TestFindClockSeamViolationsReportsADirectBannedCall is B1's core positive
// case: a fixture function that still calls time.Sleep directly must be
// reported, with the exact file/line/func/pattern and rendered String() the
// real guard test (TestClockSeamSitesHaveNoDirectTimeCalls) relies on to
// name a real violation. Mutation-check: flipping the detector's banned-
// selector check to always skip recording (`if !banned || true`) makes this
// test fail, because matches would come back empty instead of holding this
// one match.
func TestFindClockSeamViolationsReportsADirectBannedCall(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeClockSeamFixture(t, dir, "retry.go", `package fixture

import "time"

func retryLoop() {
	time.Sleep(10 * time.Millisecond)
}
`)

	matches, err := findClockSeamViolationsAgainst(dir, []ClockSeamSite{{File: "retry.go", Func: "retryLoop"}})
	if err != nil {
		t.Fatalf("findClockSeamViolationsAgainst: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("got %d matches, want 1: %+v", len(matches), matches)
	}

	want := ClockSeamMatch{File: "retry.go", Line: 6, Func: "retryLoop", Pattern: ClockSeamPatternSleep}
	if matches[0] != want {
		t.Fatalf("match = %+v, want %+v", matches[0], want)
	}

	wantString := "retry.go:6: retryLoop calls time.Sleep directly, want it routed through its clock/sleep seam"
	if got := matches[0].String(); got != wantString {
		t.Fatalf("String() = %q, want %q", got, wantString)
	}
}

// TestFindClockSeamViolationsResolvesAnAliasedTimeImport proves the detector
// follows a file's own import alias (e.g. `import t "time"`) rather than
// hard-coding the identifier "time", by matching a call written as
// t.Sleep(...).
func TestFindClockSeamViolationsResolvesAnAliasedTimeImport(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeClockSeamFixture(t, dir, "aliased.go", `package fixture

import t "time"

func aliasedRetry() {
	t.Sleep(5 * t.Millisecond)
}
`)

	matches, err := findClockSeamViolationsAgainst(dir, []ClockSeamSite{{File: "aliased.go", Func: "aliasedRetry"}})
	if err != nil {
		t.Fatalf("findClockSeamViolationsAgainst: %v", err)
	}
	if len(matches) != 1 || matches[0].Pattern != ClockSeamPatternSleep || matches[0].Line != 6 {
		t.Fatalf("got %+v, want a single Sleep match at line 6", matches)
	}
}

// TestFindClockSeamViolationsSortsByFileThenLine proves the reported matches
// are sorted by file then line, not left in ClockSeamSites' or ast.Inspect's
// traversal order: the sites here are listed with retry.go first, but
// poll.go sorts before it, so a correct result reorders them.
func TestFindClockSeamViolationsSortsByFileThenLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeClockSeamFixture(t, dir, "retry.go", `package fixture

import "time"

func retryLoop() {
	time.Sleep(10 * time.Millisecond)
}
`)
	writeClockSeamFixture(t, dir, "poll.go", `package fixture

import "time"

func pollNow() {
	_ = time.Now()
}
`)

	sites := []ClockSeamSite{
		{File: "retry.go", Func: "retryLoop"},
		{File: "poll.go", Func: "pollNow"},
	}
	matches, err := findClockSeamViolationsAgainst(dir, sites)
	if err != nil {
		t.Fatalf("findClockSeamViolationsAgainst: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2: %+v", len(matches), matches)
	}
	if matches[0].File != "poll.go" || matches[0].Pattern != ClockSeamPatternNow {
		t.Fatalf("matches[0] = %+v, want poll.go's time.Now match first", matches[0])
	}
	if matches[1].File != "retry.go" || matches[1].Pattern != ClockSeamPatternSleep {
		t.Fatalf("matches[1] = %+v, want retry.go's time.Sleep match second", matches[1])
	}
}

// TestFindClockSeamViolationsIgnoresANonBannedTimeCallAndSortsSameFileMatchesByLine
// covers two branches the earlier, cross-file tests above don't reach: a
// time-package call that is not one of the five banned selectors (e.g. the
// time.Duration(0) conversion here) must be skipped rather than reported,
// and two matches within the *same* file must still come out ordered by
// line (the sort comparator's tie-break once File is equal).
func TestFindClockSeamViolationsIgnoresANonBannedTimeCallAndSortsSameFileMatchesByLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeClockSeamFixture(t, dir, "multi.go", `package fixture

import "time"

func multiCalls() {
	time.Sleep(1 * time.Millisecond)
	_ = time.Duration(0)
	time.Now()
}
`)

	matches, err := findClockSeamViolationsAgainst(dir, []ClockSeamSite{{File: "multi.go", Func: "multiCalls"}})
	if err != nil {
		t.Fatalf("findClockSeamViolationsAgainst: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("got %d matches, want 2 (time.Duration is not a banned selector): %+v", len(matches), matches)
	}
	if matches[0].Pattern != ClockSeamPatternSleep || matches[0].Line != 6 {
		t.Fatalf("matches[0] = %+v, want the Sleep match at line 6 first", matches[0])
	}
	if matches[1].Pattern != ClockSeamPatternNow || matches[1].Line != 8 {
		t.Fatalf("matches[1] = %+v, want the Now match at line 8 second", matches[1])
	}
}

// TestFindClockSeamViolationsSkipsAFileWithNoTimeImport proves a listed
// function that never imports "time" at all trivially has zero violations
// instead of erroring or false-matching on an unrelated selector.
func TestFindClockSeamViolationsSkipsAFileWithNoTimeImport(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeClockSeamFixture(t, dir, "notime.go", `package fixture

import "fmt"

func noTimeFunc() {
	fmt.Println("hello")
}
`)

	matches, err := findClockSeamViolationsAgainst(dir, []ClockSeamSite{{File: "notime.go", Func: "noTimeFunc"}})
	if err != nil {
		t.Fatalf("findClockSeamViolationsAgainst: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("got %d matches, want 0 (file has no time import): %+v", len(matches), matches)
	}
}

// TestFindClockSeamViolationsErrorsOnAStaleSite proves a ClockSeamSites-style
// entry naming a function that does not exist in its file fails loudly
// (a stale or mistyped entry must never silently pass as clean).
func TestFindClockSeamViolationsErrorsOnAStaleSite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeClockSeamFixture(t, dir, "retry.go", `package fixture

import "time"

func retryLoop() {
	time.Sleep(10 * time.Millisecond)
}
`)

	_, err := findClockSeamViolationsAgainst(dir, []ClockSeamSite{{File: "retry.go", Func: "doesNotExist"}})
	if err == nil {
		t.Fatal("want an error for a stale site, got nil")
	}
	if !strings.Contains(err.Error(), "function not found") {
		t.Fatalf("err = %v, want it to mention a missing function", err)
	}
}

// TestFindClockSeamViolationsErrorsOnAParseFailure proves a site naming a
// file that fails to parse surfaces that failure as an error rather than
// silently reporting zero matches.
func TestFindClockSeamViolationsErrorsOnAParseFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeClockSeamFixture(t, dir, "broken.go", `package fixture

func broken( {
`)

	_, err := findClockSeamViolationsAgainst(dir, []ClockSeamSite{{File: "broken.go", Func: "broken"}})
	if err == nil {
		t.Fatal("want a parse error, got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Fatalf("err = %v, want it to mention parsing", err)
	}
}

// TestStripImportQuotesRejectsAMalformedValue directly exercises
// stripImportQuotes' error branch. go/parser always hands ImportSpec.Path a
// properly double-quoted string, so a real source file can never drive this
// branch end to end; it is tested in isolation instead.
func TestStripImportQuotesRejectsAMalformedValue(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", `"time`, `time"`, "time"} {
		if _, err := stripImportQuotes(value); err == nil {
			t.Fatalf("stripImportQuotes(%q): want an error, got nil", value)
		}
	}
}

// TestStripImportQuotesAcceptsAQuotedValue is stripImportQuotes' success
// path, matching what go/parser actually produces for an import path.
func TestStripImportQuotesAcceptsAQuotedValue(t *testing.T) {
	t.Parallel()

	got, err := stripImportQuotes(`"time"`)
	if err != nil {
		t.Fatalf("stripImportQuotes: %v", err)
	}
	if got != "time" {
		t.Fatalf("got %q, want %q", got, "time")
	}
}
