package quality

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// unitTierFixtureModule writes a minimal module at t.TempDir() so
// FindUnitTierMatches has a go.mod-free directory tree to walk (it never
// needs go.mod itself -- unlike ParallelGuardModuleRoot, it just walks
// root), and returns that root.
func unitTierFixtureModule(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func TestFindUnitTierMatchesFindsExecStart(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import "os/exec"

func TestSomething(t *testing.T) {
	exec.Command("ls", "-la")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want exactly 1", matches)
	}
	if matches[0].Pattern != UnitTierPatternExecStart || matches[0].File != "pkg/thing_test.go" || matches[0].Line != 6 {
		t.Fatalf("match = %+v", matches[0])
	}
}

func TestFindUnitTierMatchesFindsExecCommandContextAndStartProcess(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import (
	"context"
	"os"
	"os/exec"
)

func TestSomething(t *testing.T) {
	exec.CommandContext(context.Background(), "ls")
	os.StartProcess("/bin/ls", nil, nil)
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %+v, want exactly 2", matches)
	}
	for _, m := range matches {
		if m.Pattern != UnitTierPatternExecStart {
			t.Errorf("pattern = %s, want exec-start", m.Pattern)
		}
	}
}

func TestFindUnitTierMatchesFindsWriteExecutableFile(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import "github.com/sneat-dev/wb/internal/testenv"

func TestSomething(t *testing.T) {
	testenv.WriteExecutableFile("path", []byte("#!/bin/sh\n"))
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != UnitTierPatternWriteExecutable {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestFindUnitTierMatchesFindsAllowRealProcess(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import "github.com/sneat-dev/wb/internal/runner/runnertest"

func TestSomething(t *testing.T) {
	runnertest.AllowRealProcess(t)
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != UnitTierPatternAllowRealProcess {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestFindUnitTierMatchesFindsSetenvPathExactlyAndOnlyPATH(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

func TestSomething(t *testing.T) {
	t.Setenv("PATH", "/fake/bin")
	t.Setenv("HOME", "/fake/home")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != UnitTierPatternSetenvPath {
		t.Fatalf("matches = %+v, want exactly one setenv-path match (HOME must not match)", matches)
	}
}

func TestFindUnitTierMatchesFindsNamedGitHelperCall(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

func TestSomething(t *testing.T) {
	runGit(t, "/tmp/repo", "status")
	notAHelperCall(t)
}

func runGit(t interface{ Helper() }, dir string, args ...string) {}
func notAHelperCall(t interface{ Helper() })                     {}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	// runGit's own definition is a bare func decl with no banned call inside
	// it, so only the call site in TestSomething should match.
	if len(matches) != 1 || matches[0].Pattern != UnitTierPatternGitHelper || matches[0].Detail != "runGit" {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestFindUnitTierMatchesFindsHelperProcessEnvMarker(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	// The marker name is assembled at runtime, rather than written as one
	// contiguous "GO_WANT_HELPER_PROCESS" literal in this file's own
	// source: this test file is itself a _test.go file FindUnitTierMatches
	// would otherwise also scan, and a literal match here would make this
	// test fixture flag itself as a HELPER_PROCESS-env violation.
	marker := "GO_WANT_HELPER" + "_PROCESS"
	fixture := "package pkg\n\nimport \"os\"\n\nfunc TestSomething(t *testing.T) {\n" +
		"\tif os.Getenv(\"" + marker + "\") == \"1\" {\n\t\treturn\n\t}\n}\n"
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), fixture)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != UnitTierPatternHelperProcessEnv {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestFindUnitTierMatchesSkipsE2ETaggedFiles(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `//go:build e2e

package pkg

import "os/exec"

func TestSomething(t *testing.T) {
	exec.Command("git", "status")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none for an e2e-tagged file", matches)
	}
}

func TestFindUnitTierMatchesDoesNotSkipAFileWhoseConstraintAlsoBuildsWithoutE2E(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	// A constraint of "e2e || linux" still builds without the e2e tag on a
	// linux GOOS, so this file is not e2e-only and must still be scanned.
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `//go:build e2e || linux

package pkg

import "os/exec"

func TestSomething(t *testing.T) {
	exec.Command("git", "status")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want the exec.Command site scanned", matches)
	}
}

func TestFindUnitTierMatchesIgnoresNonTestFiles(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import "os/exec"

func run() { exec.Command("git", "status") }
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none for a non-test file", matches)
	}
}

func TestFindUnitTierMatchesSkipsVCSAndVendorDirectories(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, ".git", "hooks", "thing_test.go"), `package pkg

import "os/exec"

func TestSomething(t *testing.T) { exec.Command("git", "status") }
`)
	writeQualityFile(t, filepath.Join(root, "vendor", "thing_test.go"), `package pkg

import "os/exec"

func TestSomething(t *testing.T) { exec.Command("git", "status") }
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none under .git or vendor", matches)
	}
}

func TestFindUnitTierMatchesReturnsParseError(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "broken_test.go"), "package pkg\n\nfunc TestBroken( {\n")
	if _, err := FindUnitTierMatches(root); err == nil {
		t.Fatal("want a parse error for invalid Go source")
	}
}

func TestFindUnitTierMatchesReturnsWalkError(t *testing.T) {
	t.Parallel()
	if _, err := FindUnitTierMatches(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("want an error for a root that does not exist")
	}
}

func TestCountUnitTierMatchesByFileAggregatesPerFile(t *testing.T) {
	t.Parallel()
	counts := CountUnitTierMatchesByFile([]UnitTierMatch{
		{File: "a_test.go", Pattern: UnitTierPatternExecStart},
		{File: "a_test.go", Pattern: UnitTierPatternGitHelper},
		{File: "b_test.go", Pattern: UnitTierPatternExecStart},
	})
	if counts["a_test.go"] != 2 || counts["b_test.go"] != 1 || len(counts) != 2 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestUnitTierMatchStringNamesFileLineDetailAndPattern(t *testing.T) {
	t.Parallel()
	m := UnitTierMatch{File: "a_test.go", Line: 12, Pattern: UnitTierPatternExecStart, Detail: "exec.Command"}
	got := m.String()
	if !strings.Contains(got, "a_test.go:12") || !strings.Contains(got, "exec.Command") || !strings.Contains(got, string(UnitTierPatternExecStart)) {
		t.Fatalf("String() = %q", got)
	}
}

func TestParseUnitTierPendingParsesValidFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "unit_tier.pending")
	writeQualityFile(t, path, "# comment\n\ncmd/wb/a_test.go\t3\ttask-22\ninternal/worktrees/b_test.go\t5\ttask-18\n")
	entries, err := ParseUnitTierPending(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries["cmd/wb/a_test.go"].Count != 3 || entries["cmd/wb/a_test.go"].Owner != "task-22" {
		t.Fatalf("entries = %+v", entries)
	}
	if entries["internal/worktrees/b_test.go"].Count != 5 || entries["internal/worktrees/b_test.go"].Owner != "task-18" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseUnitTierPendingMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := ParseUnitTierPending(filepath.Join(t.TempDir(), "missing.pending")); err == nil {
		t.Fatal("want an error reading a missing file")
	}
}

func TestParseUnitTierPendingBytesRejectsWrongFieldCount(t *testing.T) {
	t.Parallel()
	if _, err := ParseUnitTierPendingBytes([]byte("only-one-field\n"), "source"); err == nil {
		t.Fatal("want an error for a line without 3 tab-separated fields")
	}
}

func TestParseUnitTierPendingBytesRejectsInvalidCount(t *testing.T) {
	t.Parallel()
	if _, err := ParseUnitTierPendingBytes([]byte("a_test.go\tnotanumber\ttask-1\n"), "source"); err == nil {
		t.Fatal("want an error for a non-numeric count")
	}
}

func TestParseUnitTierPendingBytesRejectsNegativeCount(t *testing.T) {
	t.Parallel()
	if _, err := ParseUnitTierPendingBytes([]byte("a_test.go\t-1\ttask-1\n"), "source"); err == nil {
		t.Fatal("want an error for a negative count")
	}
}

func TestParseUnitTierPendingBytesRejectsMissingOwner(t *testing.T) {
	t.Parallel()
	if _, err := ParseUnitTierPendingBytes([]byte("a_test.go\t2\t\n"), "source"); err == nil {
		t.Fatal("want an error for an empty owning task")
	}
}

func TestParseUnitTierPendingBytesRejectsDuplicateEntry(t *testing.T) {
	t.Parallel()
	if _, err := ParseUnitTierPendingBytes([]byte("a_test.go\t2\ttask-1\na_test.go\t3\ttask-1\n"), "source"); err == nil {
		t.Fatal("want an error for a duplicate file entry")
	}
}

func TestParseUnitTierPendingBytesReportsEveryProblemAtOnce(t *testing.T) {
	t.Parallel()
	_, err := ParseUnitTierPendingBytes([]byte("bad-line\na_test.go\tnotanumber\ttask-1\n"), "source")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "2 pending-list problem(s)") {
		t.Fatalf("err = %v, want it to name both problems", err)
	}
}

func TestFormatUnitTierPendingRoundTripsThroughParse(t *testing.T) {
	t.Parallel()
	entries := map[string]UnitTierPendingEntry{
		"b_test.go": {File: "b_test.go", Count: 2, Owner: "task-2"},
		"a_test.go": {File: "a_test.go", Count: 1, Owner: "task-1"},
	}
	formatted := FormatUnitTierPending(entries)
	lines := strings.Split(strings.TrimRight(formatted, "\n"), "\n")
	var dataLines []string
	for _, l := range lines {
		if !strings.HasPrefix(l, "#") && l != "" {
			dataLines = append(dataLines, l)
		}
	}
	if len(dataLines) != 2 || dataLines[0] != "a_test.go\t1\ttask-1" || dataLines[1] != "b_test.go\t2\ttask-2" {
		t.Fatalf("formatted data lines = %+v (from %q)", dataLines, formatted)
	}

	root := t.TempDir()
	path := filepath.Join(root, "unit_tier.pending")
	writeQualityFile(t, path, formatted)
	roundTripped, err := ParseUnitTierPending(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(roundTripped) != 2 || roundTripped["a_test.go"] != entries["a_test.go"] || roundTripped["b_test.go"] != entries["b_test.go"] {
		t.Fatalf("round-tripped = %+v, want %+v", roundTripped, entries)
	}
}

func TestUnitTierPendingTotalSumsEveryEntry(t *testing.T) {
	t.Parallel()
	total := UnitTierPendingTotal(map[string]UnitTierPendingEntry{
		"a_test.go": {Count: 2},
		"b_test.go": {Count: 5},
	})
	if total != 7 {
		t.Fatalf("total = %d, want 7", total)
	}
}

func TestUnitTierPendingTotalOfEmptyMapIsZero(t *testing.T) {
	t.Parallel()
	if total := UnitTierPendingTotal(map[string]UnitTierPendingEntry{}); total != 0 {
		t.Fatalf("total = %d, want 0", total)
	}
}

func TestParseUnitTierAllowSharesParallelBaselineFormat(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "unit_tier.allow")
	writeQualityFile(t, path, "internal/process/command_unix_test.go\tre-runs the test binary itself\n")
	entries, err := ParseUnitTierAllow(path)
	if err != nil {
		t.Fatal(err)
	}
	if entries["internal/process/command_unix_test.go"] != "re-runs the test binary itself" {
		t.Fatalf("entries = %+v", entries)
	}
}

func TestParseUnitTierAllowRejectsEmptyReason(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "unit_tier.allow")
	writeQualityFile(t, path, "internal/process/command_unix_test.go\t\n")
	if _, err := ParseUnitTierAllow(path); err == nil {
		t.Fatal("want an error for an allow-list entry with no reason")
	}
}

func TestFileRequiresE2ETagIgnoresOtherSingleTag(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `//go:build linux

package pkg

import "os/exec"

func TestSomething(t *testing.T) {
	exec.Command("git", "status")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want the file still scanned (only \"e2e\" alone is e2e-only)", matches)
	}
}

func TestFileRequiresE2ETagIgnoresAnUnparsableBuildComment(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `//go:build (e2e

package pkg

import "os/exec"

func TestSomething(t *testing.T) {
	exec.Command("git", "status")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v, want an unparsable //go:build comment to leave the file scanned, not skipped", matches)
	}
}

func TestCallFirstStringArgEqualsRejectsNonStringFirstArg(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.Ident{Name: "key"}}}
	if callFirstStringArgEquals(call, "PATH") {
		t.Fatal("want false for a non-literal first argument")
	}
}

func TestCallFirstStringArgEqualsRejectsUnquotableLiteral(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"unterminated`}}}
	if callFirstStringArgEquals(call, "PATH") {
		t.Fatal("want false for a literal strconv.Unquote cannot parse")
	}
}

func TestCallFirstStringArgEqualsRejectsNoArguments(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: nil}
	if callFirstStringArgEquals(call, "PATH") {
		t.Fatal("want false for a call with no arguments")
	}
}

func TestFindUnitTierMatchesMarksSelfReexecForExecCommandOSArgsZero(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import "os/exec"
import "os"

func TestSomething(t *testing.T) {
	exec.Command(os.Args[0], "-test.run=Helper")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || !matches[0].SelfReexec {
		t.Fatalf("matches = %+v, want exactly one self-reexec match", matches)
	}
}

func TestFindUnitTierMatchesDoesNotMarkSelfReexecForExecCommandOfGit(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import "os/exec"

func TestSomething(t *testing.T) {
	exec.Command("git", "status")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].SelfReexec {
		t.Fatalf("matches = %+v, want exactly one non-self-reexec match", matches)
	}
}

func TestFindUnitTierMatchesMarksSelfReexecForCommandContextArgOne(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import (
	"context"
	"os"
	"os/exec"
)

func TestSomething(t *testing.T) {
	exec.CommandContext(context.Background(), os.Args[0])
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || !matches[0].SelfReexec {
		t.Fatalf("matches = %+v, want the CommandContext call flagged self-reexec", matches)
	}
}

func TestFindUnitTierMatchesMarksSelfReexecForStartProcess(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import "os"

func TestSomething(t *testing.T) {
	os.StartProcess(os.Args[0], nil, nil)
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || !matches[0].SelfReexec {
		t.Fatalf("matches = %+v, want the StartProcess call flagged self-reexec", matches)
	}
}

func TestFindUnitTierMatchesFindsQualifiedGitHelperCall(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import "github.com/sneat-dev/wb/internal/testenv"

func TestSomething(t *testing.T) {
	testenv.ConfigureGitAutoMaintenanceOff(t, "/tmp/repo")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != UnitTierPatternGitHelper || matches[0].Detail != "testenv.ConfigureGitAutoMaintenanceOff" {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestFindUnitTierMatchesResolvesAliasedExecImport(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import osexec "os/exec"

func TestSomething(t *testing.T) {
	osexec.Command("git", "status")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != UnitTierPatternExecStart || matches[0].Detail != "exec.Command" {
		t.Fatalf("matches = %+v, want the aliased import resolved to exec.Command", matches)
	}
}

func TestFindUnitTierMatchesResolvesAliasedTestenvImport(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import te "github.com/sneat-dev/wb/internal/testenv"

func TestSomething(t *testing.T) {
	te.WriteExecutableFile("path", []byte("#!/bin/sh\n"))
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != UnitTierPatternWriteExecutable {
		t.Fatalf("matches = %+v, want the aliased testenv import still recognised", matches)
	}
}

func TestFindUnitTierMatchesResolvesAliasedRunnertestImport(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import rt "github.com/sneat-dev/wb/internal/runner/runnertest"

func TestSomething(t *testing.T) {
	rt.AllowRealProcess(t)
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != UnitTierPatternAllowRealProcess {
		t.Fatalf("matches = %+v, want the aliased runnertest import still recognised", matches)
	}
}

func TestFindUnitTierMatchesIgnoresUnrelatedImportPath(t *testing.T) {
	t.Parallel()
	root := unitTierFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import "fmt"

func TestSomething(t *testing.T) {
	fmt.Println("hello")
}
`)
	matches, err := FindUnitTierMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none for an unrelated import", matches)
	}
}

func TestCallArgIsOSArgsZeroRejectsShortArgList(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: nil}
	if callArgIsOSArgsZero(call, 0) {
		t.Fatal("want false when the call has no argument at that index")
	}
}

func TestCallArgIsOSArgsZeroRejectsNonIndexExpr(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.Ident{Name: "path"}}}
	if callArgIsOSArgsZero(call, 0) {
		t.Fatal("want false for a plain identifier argument")
	}
}

func TestCallArgIsOSArgsZeroRejectsIndexOfNonSelector(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.IndexExpr{
		X:     &ast.Ident{Name: "args"},
		Index: &ast.BasicLit{Kind: token.INT, Value: "0"},
	}}}
	if callArgIsOSArgsZero(call, 0) {
		t.Fatal("want false when the indexed expression is not a selector")
	}
}

func TestCallArgIsOSArgsZeroRejectsWrongSelectorField(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.IndexExpr{
		X:     &ast.SelectorExpr{X: &ast.Ident{Name: "os"}, Sel: &ast.Ident{Name: "Environ"}},
		Index: &ast.BasicLit{Kind: token.INT, Value: "0"},
	}}}
	if callArgIsOSArgsZero(call, 0) {
		t.Fatal("want false for os.Environ[0], not os.Args[0]")
	}
}

func TestCallArgIsOSArgsZeroRejectsWrongPackage(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.IndexExpr{
		X:     &ast.SelectorExpr{X: &ast.Ident{Name: "notos"}, Sel: &ast.Ident{Name: "Args"}},
		Index: &ast.BasicLit{Kind: token.INT, Value: "0"},
	}}}
	if callArgIsOSArgsZero(call, 0) {
		t.Fatal("want false for notos.Args[0]")
	}
}

func TestCallArgIsOSArgsZeroRejectsNonZeroIndex(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.IndexExpr{
		X:     &ast.SelectorExpr{X: &ast.Ident{Name: "os"}, Sel: &ast.Ident{Name: "Args"}},
		Index: &ast.BasicLit{Kind: token.INT, Value: "1"},
	}}}
	if callArgIsOSArgsZero(call, 0) {
		t.Fatal("want false for os.Args[1]")
	}
}

func TestUnitTierPackageAliasesIgnoresUnquotableImportPath(t *testing.T) {
	t.Parallel()
	file := &ast.File{Imports: []*ast.ImportSpec{
		{Path: &ast.BasicLit{Kind: token.STRING, Value: `"unterminated`}},
	}}
	aliases := unitTierPackageAliases(file)
	if len(aliases) != 0 {
		t.Fatalf("aliases = %+v, want none for an unquotable import path", aliases)
	}
}

func TestCallArgIsOSArgsZeroRejectsNonIntIndexLiteral(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.IndexExpr{
		X:     &ast.SelectorExpr{X: &ast.Ident{Name: "os"}, Sel: &ast.Ident{Name: "Args"}},
		Index: &ast.BasicLit{Kind: token.STRING, Value: `"0"`},
	}}}
	if callArgIsOSArgsZero(call, 0) {
		t.Fatal("want false when the index literal is not an INT token")
	}
}
