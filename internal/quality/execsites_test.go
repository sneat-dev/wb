package quality

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

func execSiteFixtureModule(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

func TestFindExecSiteMatchesFindsDirectExecCommand(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import "os/exec"

func run() { exec.Command("ls", "-la") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternDirectExec || matches[0].File != "pkg/thing.go" {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestFindExecSiteMatchesFindsDirectExecCommandContextOSStartProcessAndSyscallExec(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

func run(ctx context.Context) {
	exec.CommandContext(ctx, "ls")
	os.StartProcess("/bin/ls", nil, nil)
	syscall.Exec("/bin/ls", nil, nil)
}
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 3 {
		t.Fatalf("matches = %+v, want 3", matches)
	}
	for _, m := range matches {
		if m.Pattern != ExecSitePatternDirectExec {
			t.Errorf("pattern = %s, want direct-exec", m.Pattern)
		}
	}
}

func TestFindExecSiteMatchesFindsGitLiteralThroughExecCommand(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import "os/exec"

func run() { exec.Command("git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternGitLiteral {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestFindExecSiteMatchesFindsGitLiteralThroughExecCommandContext(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import (
	"context"
	"os/exec"
)

func run(ctx context.Context) { exec.CommandContext(ctx, "git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternGitLiteral {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestFindExecSiteMatchesFindsGitLiteralThroughProcessCommandContext(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import (
	"context"

	"github.com/sneat-dev/wb/internal/process"
)

func run(ctx context.Context) { process.CommandContext(ctx, "git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternGitLiteral {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestFindExecSiteMatchesFindsGitLiteralThroughProcessCommandContextInteractive(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import (
	"context"

	"github.com/sneat-dev/wb/internal/process"
)

func run(ctx context.Context) { process.CommandContextInteractive(ctx, true, "git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternGitLiteral {
		t.Fatalf("matches = %+v", matches)
	}
}

func TestFindExecSiteMatchesIgnoresProcessCommandContextOfANonGitProgram(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import (
	"context"

	"github.com/sneat-dev/wb/internal/process"
)

func run(ctx context.Context) { process.CommandContext(ctx, "gh", "pr", "list") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none for a non-git process.CommandContext call", matches)
	}
}

func TestFindExecSiteMatchesFindsNamedGitHelperCallUnqualifiedAndQualified(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

func run() { runGit("status") }

func runGit(args ...string) (string, error) { return "", nil }
`)
	writeQualityFile(t, filepath.Join(root, "other", "thing.go"), `package other

import "github.com/sneat-dev/wb/pkg"

func run() { pkg.RunGit() }

`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternGitHelper || matches[0].Detail != "runGit" {
		t.Fatalf("matches = %+v, want exactly one git-helper-call match (RunGit is not a listed name)", matches)
	}
}

func TestFindExecSiteMatchesResolvesAliasedImports(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import (
	osexec "os/exec"
	osx "os"
	sc "syscall"
	proc "github.com/sneat-dev/wb/internal/process"
	"context"
)

func run(ctx context.Context) {
	osexec.Command("git", "status")
	osx.StartProcess("/bin/ls", nil, nil)
	sc.Exec("/bin/ls", nil, nil)
	proc.CommandContext(ctx, "git", "fetch")
}
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 4 {
		t.Fatalf("matches = %+v, want 4 (aliased imports resolved)", matches)
	}
}

func TestFindExecSiteMatchesSkipsAnAllowListedFunctionEntirely(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import "os/exec"

func RunSecureHooksGitHelper() { exec.Command("git", "hook-run") }

func ordinary() { exec.Command("git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Line != 7 {
		t.Fatalf("matches = %+v, want only the ordinary() call (line 7), not RunSecureHooksGitHelper's", matches)
	}
}

// TestFindExecSiteMatchesFindsCallInsideAPackageLevelVarFuncLiteral is
// review note #764 B2's own reproduction: a package-level `var name =
// func(...) {...}` is a *ast.GenDecl, not a *ast.FuncDecl, so the detector
// must walk its function-literal value the same way it already walks a
// plain top-level function's body -- exactly the shape
// cmd/wb/fleet_default_branch.go's defaultBranchGit and cmd/wb/daemon.go's
// runSystemctl already used, invisibly, before this fix.
func TestFindExecSiteMatchesFindsCallInsideAPackageLevelVarFuncLiteral(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import "os/exec"

var runSystemctl = func(args ...string) ([]byte, error) {
	return exec.Command("systemctl", args...).CombinedOutput()
}
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternDirectExec {
		t.Fatalf("matches = %+v, want one direct-exec match inside the var-assigned closure", matches)
	}
}

// TestFindExecSiteMatchesRespectsAllowListedNameForAPackageLevelVarFuncLiteral
// proves the fix's exemption is keyed off the var's own name, not just a
// plain FuncDecl's: a closure assigned to an allow-listed name is exempt the
// same way a plain function of that name already is.
func TestFindExecSiteMatchesRespectsAllowListedNameForAPackageLevelVarFuncLiteral(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import "os/exec"

var RunSecureHooksGitHelper = func() { exec.Command("git", "hook-run") }

var ordinary = func() { exec.Command("git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Line != 7 {
		t.Fatalf("matches = %+v, want only the ordinary closure's call (line 7), not RunSecureHooksGitHelper's", matches)
	}
}

// TestFindExecSiteMatchesFindsCallInsideAStructFieldFuncLiteral covers a
// function literal with no single declared name at all -- a map value, here
// -- which the fix's exemption inherits from its enclosing context (module
// scope, so not exempt) rather than trying to name.
func TestFindExecSiteMatchesFindsCallInsideAStructFieldFuncLiteral(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import "os/exec"

var handlers = map[string]func(){
	"start": func() { exec.Command("git", "status") },
}
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternGitLiteral {
		t.Fatalf("matches = %+v, want one git-literal match inside the map-value closure", matches)
	}
}

// TestFindExecSiteMatchesFindsCallInsideFuncInit covers Go's special init
// position: a plain *ast.FuncDecl named "init", already walked by the
// original per-FuncDecl loop, but worth pinning explicitly since it is one
// of the non-FuncDecl-body shapes review note #764 B2 named.
func TestFindExecSiteMatchesFindsCallInsideFuncInit(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import "os/exec"

func init() { exec.Command("git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternGitLiteral {
		t.Fatalf("matches = %+v, want one git-literal match inside func init()", matches)
	}
}

// TestFindExecSiteMatchesFindsCallInsideAMethodDeclaration covers a method
// (Go represents it as a *ast.FuncDecl with a receiver, listed in
// file.Decls exactly like an ordinary function): already worked before this
// fix, pinned here as one of review note #764 B2's named shapes ("method
// values").
func TestFindExecSiteMatchesFindsCallInsideAMethodDeclaration(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import "os/exec"

type Runner struct{}

func (r Runner) Run() { exec.Command("git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternGitLiteral {
		t.Fatalf("matches = %+v, want one git-literal match inside the method", matches)
	}
}

// TestFindExecSiteMatchesFindsCallInsideAClosureNestedInAnAllowListedFunction
// pins the allow-listed-function exemption still reaches a closure nested
// inside it (the review's own adversarial check, confirmed correct before
// this fix and re-confirmed here now that the walk was rewritten).
func TestFindExecSiteMatchesFindsCallInsideAClosureNestedInAnAllowListedFunction(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), `package pkg

import "os/exec"

func RunSecureHooksGitHelper() {
	run := func() { exec.Command("git", "hook-run") }
	run()
}
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none: the nested closure inherits its allow-listed enclosing function's exemption", matches)
	}
}

func TestFindExecSiteMatchesSkipsAllowListedDirectories(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "internal", "runner", "real.go"), `package runner

import "os/exec"

func run() { exec.Command("git", "status") }
`)
	writeQualityFile(t, filepath.Join(root, "internal", "gitcli", "gitcli.go"), `package gitcli

import "os/exec"

func run() { exec.Command("git", "status") }
`)
	writeQualityFile(t, filepath.Join(root, "internal", "process", "command.go"), `package process

import "os/exec"

func run() { exec.Command("git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none under internal/runner, internal/gitcli or internal/process", matches)
	}
}

func TestFindExecSiteMatchesIgnoresTestFiles(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing_test.go"), `package pkg

import "os/exec"

func run() { exec.Command("git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none for a _test.go file", matches)
	}
}

func TestFindExecSiteMatchesIgnoresNonGoFiles(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "README.md"), "exec.Command(\"git\", \"status\")\n")
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none for a non-.go file", matches)
	}
}

func TestFindExecSiteMatchesSkipsVCSAndVendorDirectories(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, ".git", "hooks", "thing.go"), `package pkg

import "os/exec"

func run() { exec.Command("git", "status") }
`)
	writeQualityFile(t, filepath.Join(root, "vendor", "thing.go"), `package pkg

import "os/exec"

func run() { exec.Command("git", "status") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none under .git or vendor", matches)
	}
}

func TestFindExecSiteMatchesReturnsParseError(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "broken.go"), "package pkg\n\nfunc broken( {\n")
	if _, err := FindExecSiteMatches(root); err == nil {
		t.Fatal("want a parse error for invalid Go source")
	}
}

func TestFindExecSiteMatchesReturnsWalkError(t *testing.T) {
	t.Parallel()
	if _, err := FindExecSiteMatches(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Fatal("want an error for a root that does not exist")
	}
}

func TestFindExecSiteMatchesIgnoresAFuncDeclWithNoBody(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "thing.go"), "package pkg\n\nfunc external()\n")
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("matches = %+v, want none for a body-less func decl", matches)
	}
}

func TestCountExecSiteMatchesByFileAggregatesPerFile(t *testing.T) {
	t.Parallel()
	counts := CountExecSiteMatchesByFile([]ExecSiteMatch{
		{File: "a.go", Pattern: ExecSitePatternDirectExec},
		{File: "a.go", Pattern: ExecSitePatternGitLiteral},
		{File: "b.go", Pattern: ExecSitePatternDirectExec},
	})
	if counts["a.go"] != 2 || counts["b.go"] != 1 || len(counts) != 2 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestExecSiteMatchStringNamesFileLineDetailAndPattern(t *testing.T) {
	t.Parallel()
	m := ExecSiteMatch{File: "a.go", Line: 12, Pattern: ExecSitePatternDirectExec, Detail: "exec.Command"}
	got := m.String()
	if !strings.Contains(got, "a.go:12") || !strings.Contains(got, "exec.Command") || !strings.Contains(got, string(ExecSitePatternDirectExec)) {
		t.Fatalf("String() = %q", got)
	}
}

func TestLiteralGitArgRejectsShortArgList(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: nil}
	if literalGitArg(call, 0) {
		t.Fatal("want false for a call with no arguments")
	}
}

func TestLiteralGitArgRejectsNonStringLiteralArg(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.Ident{Name: "name"}}}
	if literalGitArg(call, 0) {
		t.Fatal("want false for a non-literal argument")
	}
}

func TestLiteralGitArgRejectsAnUnquotableLiteral(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"unterminated`}}}
	if literalGitArg(call, 0) {
		t.Fatal("want false for a literal strconv.Unquote cannot parse")
	}
}

func TestLiteralGitArgRejectsANonGitLiteral(t *testing.T) {
	t.Parallel()
	call := &ast.CallExpr{Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"gh"`}}}
	if literalGitArg(call, 0) {
		t.Fatal("want false for a literal that is not \"git\"")
	}
}

// TestFindExecSiteMatchesSortsAcrossFilesByFileThenLine exercises the
// comparator's file-differs branch (matches in more than one file), not just
// its line-differs branch (which the aliased-imports fixture already
// exercises with several matches inside one file).
func TestFindExecSiteMatchesSortsAcrossFilesByFileThenLine(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "zzz", "thing.go"), `package zzz

import "os/exec"

func run() { exec.Command("ls") }
`)
	writeQualityFile(t, filepath.Join(root, "aaa", "thing.go"), `package aaa

import "os/exec"

func run() { exec.Command("ls") }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %+v, want 2", matches)
	}
	if matches[0].File > matches[1].File {
		t.Fatalf("matches not sorted by file: %+v", matches)
	}
}

// TestFindExecSiteMatchesFindsAQualifiedGitHelperCall exercises the
// SelectorExpr git-helper case with a non-empty package name (a call like
// hp.gitOutput(...), as opposed to the unqualified runGit(...) case another
// test already covers).
func TestFindExecSiteMatchesFindsAQualifiedGitHelperCall(t *testing.T) {
	t.Parallel()
	root := execSiteFixtureModule(t)
	writeQualityFile(t, filepath.Join(root, "pkg", "helper.go"), `package pkg

func GitOutputHelper() {}
`)
	writeQualityFile(t, filepath.Join(root, "other", "thing.go"), `package other

import hp "github.com/sneat-dev/wb/pkg"

func run() { hp.gitOutput() }
`)
	matches, err := FindExecSiteMatches(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Pattern != ExecSitePatternGitHelper || matches[0].Detail != "hp.gitOutput" {
		t.Fatalf("matches = %+v, want exactly one qualified git-helper-call match", matches)
	}
}

// TestExecSitePackageAliasesSkipsAnUnquotableImportPath exercises
// execSitePackageAliases' error path for an ImportSpec whose Path.Value is
// not a validly quoted Go string literal -- unreachable through a real
// parsed file (the parser rejects that source), so it is exercised directly
// against a hand-built *ast.File.
func TestExecSitePackageAliasesSkipsAnUnquotableImportPath(t *testing.T) {
	t.Parallel()
	file := &ast.File{Imports: []*ast.ImportSpec{
		{Path: &ast.BasicLit{Kind: token.STRING, Value: "not-quoted"}},
	}}
	aliases := execSitePackageAliases(file)
	if len(aliases) != 0 {
		t.Fatalf("aliases = %+v, want empty for an unquotable import path", aliases)
	}
}

func TestParseExecSitesPendingRoundTripsThroughFormat(t *testing.T) {
	t.Parallel()
	entries := map[string]UnitTierPendingEntry{
		"pkg/a.go": {File: "pkg/a.go", Count: 2, Owner: "task-8"},
	}
	formatted := FormatExecSitesPending(entries)
	root := t.TempDir()
	path := filepath.Join(root, "exec_sites.pending")
	writeQualityFile(t, path, formatted)
	roundTripped, err := ParseExecSitesPending(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(roundTripped) != 1 || roundTripped["pkg/a.go"] != entries["pkg/a.go"] {
		t.Fatalf("roundTripped = %+v", roundTripped)
	}
}

func TestParseExecSitesPendingBytesRejectsInvalidCount(t *testing.T) {
	t.Parallel()
	if _, err := ParseExecSitesPendingBytes([]byte("a.go\tnotanumber\ttask-8\n"), "source"); err == nil {
		t.Fatal("want an error for a non-numeric count")
	}
}

func TestExecSitesPendingTotalSumsEveryEntry(t *testing.T) {
	t.Parallel()
	total := ExecSitesPendingTotal(map[string]UnitTierPendingEntry{
		"a.go": {Count: 2},
		"b.go": {Count: 5},
	})
	if total != 7 {
		t.Fatalf("total = %d, want 7", total)
	}
}

// TestFormatExecSitesPendingUsesItsOwnHeaderNotUnitTierPendings pins that
// FormatExecSitesPending writes execSitesPendingHeader, not
// unitTierPendingHeader -- formatPendingList is shared between the two
// Format functions (unittier.go), and a header naming task-24's detector or
// ciaudit.CompareUnitTierPendingTotal in a committed exec_sites.pending
// would describe the wrong detector and the wrong ratchet.
func TestFormatExecSitesPendingUsesItsOwnHeaderNotUnitTierPendings(t *testing.T) {
	t.Parallel()
	entries := map[string]UnitTierPendingEntry{"pkg/a.go": {File: "pkg/a.go", Count: 1, Owner: "task-8"}}
	formatted := FormatExecSitesPending(entries)
	if !strings.Contains(formatted, "task-8") || !strings.Contains(formatted, "CompareExecSitesPendingTotal") {
		t.Fatalf("FormatExecSitesPending output missing its own header:\n%s", formatted)
	}
	if strings.Contains(formatted, "task-24") || strings.Contains(formatted, "CompareUnitTierPendingTotal") {
		t.Fatalf("FormatExecSitesPending output uses unit_tier.pending's header:\n%s", formatted)
	}
	if formatted == FormatUnitTierPending(entries) {
		t.Fatal("FormatExecSitesPending must not render identically to FormatUnitTierPending")
	}
}
