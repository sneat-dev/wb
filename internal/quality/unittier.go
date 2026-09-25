// This file backs the unit tier / e2e tier split (spec/plans/coverage-to-100
// task-24): the static check that keeps a process-starting or real-git test
// out of the default `go test ./...` tier unless it is named on one of two
// committed lists.
//
// Two lists, two different jobs:
//
//   - internal/quality/testdata/unit_tier.pending names legacy _test.go
//     files that still start a real process or shell out to real git, each
//     with the exact match count this file's detector finds today and the
//     task that will convert or move it. TestUnitTierPendingDoesNotRegress
//     (in this package) fails when a file's live match count exceeds its
//     committed entry -- a fresh violation must update the list in the same
//     reviewed PR, never silently grow past what was recorded. Separately,
//     the CI ratchet (ciaudit.CompareUnitTierPendingTotal, wired into
//     `wb ci audit --target`) fails when the list's grand total rises
//     against the base branch's committed copy, unless the same PR shrank
//     other entries by at least as much.
//   - internal/quality/testdata/unit_tier.allow names files that
//     legitimately re-run the test binary itself (Go's helper-process
//     pattern, `exec.Command(os.Args[0], ...)`) rather than a real external
//     program. These are permanent, reviewed exceptions: a file listed here
//     is not required to also appear on the pending list, and task-20 does
//     not require this list to be empty.
//
// The detector is call-based, over go/ast, not a text/regexp scan (task-24's
// Rework note: "Prefer go/ast over regex where it's cheap"), except for the
// one pattern that is inherently textual: the helper-process re-exec marker
// environment variable (GO_WANT_HELPER_PROCESS and this repository's
// GO_WANT_HELPER_PROCESS_OBSERVE variant), which shows up as a string
// literal value, not a call shape.
//
// A default-tier test file is any `_test.go` file whose own build
// constraint does not require the `e2e` tag (go/build/constraint decides
// this the same way `go build` would, rather than a substring match on the
// `//go:build` line). An e2e-tagged file is out of scope for this detector
// entirely: task-24's Rework note is explicit that "[t]he e2e build tag on
// test files ... only selects a test tier."
package quality

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// UnitTierPattern names one process/real-git escape shape the unit-tier
// detector counts.
type UnitTierPattern string

const (
	// UnitTierPatternExecStart is a direct exec.Command, exec.CommandContext
	// or os.StartProcess call.
	UnitTierPatternExecStart UnitTierPattern = "exec-start"
	// UnitTierPatternWriteExecutable is a call to
	// testenv.WriteExecutableFile, which writes a fake executable a test
	// then runs on PATH.
	UnitTierPatternWriteExecutable UnitTierPattern = "write-executable-file"
	// UnitTierPatternSetenvPath is t.Setenv("PATH", ...): the shape that
	// installs a fake executable on PATH for a subprocess to find.
	UnitTierPatternSetenvPath UnitTierPattern = "setenv-path"
	// UnitTierPatternAllowRealProcess is a call to
	// runnertest.AllowRealProcess, task-8's own escape hatch for a unit test
	// that must start a real process. Only a file on the pending list or
	// the allow list may call it (task-24's Runtime guard section).
	UnitTierPatternAllowRealProcess UnitTierPattern = "allow-real-process"
	// UnitTierPatternGitHelper is an unqualified call to one of
	// UnitTierGitHelperNames: a same-package helper (production or
	// test-local) that shells out to the real git executable itself, so a
	// test file that only calls it carries no exec.Command literal of its
	// own for this detector to see directly.
	UnitTierPatternGitHelper UnitTierPattern = "git-helper-call"
	// UnitTierPatternHelperProcessEnv is a string literal naming the
	// helper-process re-exec environment variable (GO_WANT_HELPER_PROCESS
	// or this repository's GO_WANT_HELPER_PROCESS_OBSERVE variant). It is
	// how the re-exec pattern's own child process finds out it should run
	// as the helper rather than the ordinary test suite, so it is expected,
	// alongside a genuine self-reexec exec-start match, in a file on
	// unit_tier.allow -- TestUnitTierAllowListEntriesAreGenuineHelperProcessReexec
	// permits it there. Outside the allow list it is unreviewed (review
	// note #764 N1/N2) and stays subject to the ordinary pending-count rule
	// like any other match.
	UnitTierPatternHelperProcessEnv UnitTierPattern = "helper-process-env"
)

// UnitTierGitHelperNames is the maintained set of unqualified call names
// task-24's detector treats as "a named git helper call": a same-package
// function, production or test-local, whose own body shells out to the real
// git executable. A test file that calls one of these carries no
// exec.Command/exec.CommandContext literal of its own, so
// UnitTierPatternExecStart cannot see it -- only a name-based check can.
//
// Every name below was found in this repository on 2026-09-25 by grepping
// every function whose body directly invokes exec.Command/exec.CommandContext
// with a literal "git" argv[0] (production helpers), plus every _test.go
// helper conventionally named after the same shape. Task-24's Note is that
// "the check keeps the full list" -- add a name here, with a short comment
// naming where it is defined, whenever a new one is found; removing a name
// is a mechanical no-op once its only caller is gone.
var UnitTierGitHelperNames = map[string]bool{
	// Required by task-24's own Verifies text.
	"runGit":                         true,
	"gitOutput":                      true,
	"gitRawOutput":                   true,
	"runGitIn":                       true,
	"runGitWithFilesystemCapability": true,
	"gitWithExtraFiles":              true,

	// Other production git-invoking helpers, each a same-package
	// unqualified call a test file can reach without its own exec.Command.
	"gitRaw":                         true, // internal/canonicalrescue/canonicalrescue.go
	"gitWithEnvironment":             true, // internal/canonicalrescue/canonicalrescue.go
	"gitTopLevel":                    true, // internal/envguard/envguard.go
	"gitPathExistsAtHEAD":            true, // internal/envguard, internal/locallink
	"gitPathUnchangedFromHEAD":       true, // internal/envguard, internal/locallink
	"ShowFile":                       true, // internal/gitops/gitops.go
	"OriginAddress":                  true, // internal/layout/layout.go
	"OriginSlug":                     true, // internal/layout/layout.go
	"GitMergeBase":                   true, // internal/quality/changed_packages.go
	"GitTopLevel":                    true, // internal/quality/changed_packages.go
	"GitTouchedFiles":                true, // internal/quality/ratchet.go
	"ComputeBaselineAtRef":           true, // internal/quality/ratchet.go
	"resolveRefSHA":                  true, // internal/quality/ratchet.go
	"ConfigureGitAutoMaintenanceOff": true, // internal/testenv/testenv.go
	"runGitPushDeleteWithLease":      true, // internal/orchestrate/pr_land_keep.go
	"commitPatchIDs":                 true, // internal/worktrees/patchid.go
	"isAncestor":                     true, // internal/worktrees/lifecycle.go
	"localBranchExists":              true, // internal/worktrees/worktrees.go
	"atomicLocalBranchRename":        true, // internal/worktrees/branches_quarantine.go
	"mergeResultTree":                true, // internal/worktrees/lifecycle.go
	"retireGitBytes":                 true, // internal/worktrees/retire.go
	"retireGitObjectSHA":             true, // internal/worktrees/retire.go

	// Test-local wrapper helpers: each is defined inside a _test.go file
	// (so its own exec.Command call already trips UnitTierPatternExecStart
	// for that file), but is callable by name from any other test file in
	// the same package that has none of its own.
	"git": true, "gitIn": true, "gitFixture": true, "gitPorcelain": true,
	"gitRefExists": true, "gitStatus": true, "gitTest": true,
	"gitTestOutput": true, "gitTestRun": true, "gitTestRunEnv": true,
	"journeyGit": true, "journeyGitOutput": true, "lgCovRunGit": true,
	"publicationGit": true, "pushTierGit": true, "remoteGit": true,
	"runAbsorbedConflictGit": true, "runCLIWorktreeGit": true,
	"runCampaignGit": true, "runDependencyGit": true, "runEngineGit": true,
	"runFixtureGit": true, "runGitTestOutput": true, "runGuardTestGit": true,
	"runModuleArchiveGit": true, "runQualityGit": true, "runStreamGit": true,
	"runTestGit": true, "runUpgradeGit": true, "scratchGit": true,
	"slCovGit": true, "targetGit": true, "wtLifeCovGit": true,
	"cwDepsGit": true, "gcGit": true, "hkCovGitRepository": true,
	"initGitRepository": true, "initLifecycleGitRepository": true,
	"initTestRepository": true, "agentGuardGit": true,

	// internal/worktrees/wtlife_secure_cov_test.go's own caller-side
	// dispatch helpers: they re-run the test binary (review note #764 B2),
	// but the child they dispatch to (wtlife_secure_helper_test.go's
	// TestWtLifeCovSecureHelperProcess) execs whatever git executable it is
	// given, and most call sites pass wtLifeCovTrustedGit(t) -- real git.
	// Naming these here keeps every caller counted even once their other
	// matches are converted away.
	"wtLifeCovRunSecureHelper": true, "wtLifeCovRunSecureHelperInvocation": true,

	// internal/testenv helpers, called qualified as testenv.Xxx from other
	// packages' test files (review note #764 N1/B1's own fix): the
	// qualified-call fallback in classifyUnitTierCall matches these by
	// Sel.Name regardless of package qualifier, so listing the bare name
	// here is enough for both an unqualified, same-package call and a
	// qualified testenv.Xxx one.
	"InitBareRemoteForTest": true, // internal/testenv/testenv.go

	// Generic process wrappers whose own body invokes exec.Command/
	// exec.CommandContext with the executable name as a parameter, so any
	// unqualified call site starts an unaccounted-for real process (review
	// note #769 N2).
	"runCommand":    true, // cmd/wb/worktree_marker_test.go; also internal/deps/command.go, internal/orchestrate/command.go
	"installFakeGH": true, // internal/orchestrate/ciwait_deadline_unix_test.go
}

// UnitTierMatch is one occurrence of a banned pattern in one default-tier
// test file.
type UnitTierMatch struct {
	// File is the path relative to root, slash-separated.
	File string
	// Line is the match's line number.
	Line int
	// Pattern names which shape matched.
	Pattern UnitTierPattern
	// Detail is a short human-readable identifier for the match (the
	// selector or identifier name), for a diagnostic message.
	Detail string
	// SelfReexec is set only for UnitTierPatternExecStart: it reports
	// whether the program argument (the exec.Command/os.StartProcess
	// argument that names the executable to run) is exactly the expression
	// os.Args[0] -- Go's own helper-process pattern, re-running the test
	// binary itself, rather than a real external program such as git.
	// TestUnitTierAllowListEntriesAreGenuineHelperProcessReexec (review
	// note #764 B1) requires this on every exec-start match in an
	// allow-listed file.
	SelfReexec bool
}

func (m UnitTierMatch) String() string {
	return fmt.Sprintf("%s:%d: %s (%s)", m.File, m.Line, m.Detail, m.Pattern)
}

// unitTierExcludedDirs are directories FindUnitTierMatches never descends
// into: the usual VCS/vendor/dependency noise, and any hidden directory.
func unitTierSkipDir(name string) bool {
	return name == ".git" || name == "node_modules" || name == "vendor" || (strings.HasPrefix(name, ".") && name != ".")
}

// FindUnitTierMatches walks root and reports every occurrence, in every
// default-tier _test.go file (one whose own build constraint does not
// require the e2e tag), of a pattern task-24's unit tier bans. Results are
// sorted by file, then line.
func FindUnitTierMatches(root string) ([]UnitTierMatch, error) {
	fset := token.NewFileSet()
	var matches []UnitTierMatch
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if unitTierSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		if fileRequiresE2ETag(file) {
			return nil
		}
		rel := relSlashFor(root, path)
		matches = append(matches, scanUnitTierFile(fset, file, rel)...)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", root, walkErr)
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].File != matches[j].File {
			return matches[i].File < matches[j].File
		}
		return matches[i].Line < matches[j].Line
	})
	return matches, nil
}

// fileRequiresE2ETag reports whether file's own leading build constraint (a
// `//go:build` or legacy `// +build` comment before the package clause) is
// exactly the bare "e2e" tag -- the one shape task-24 defines the e2e tier
// by ("_test.go files with //go:build e2e"), and the only shape this
// repository's own e2e-tagged files use. A more elaborate constraint that
// merely mentions "e2e" alongside something else (`e2e || windows`, say) is
// deliberately not treated as e2e-only: working out whether every tag
// assignment that satisfies it also sets e2e is a harder, unneeded problem
// while no real file in this repository combines the two, so such a file is
// conservatively still scanned -- the same "detector, not oracle" stance
// FindInlineWriteSequences and the other guards in this package take. A
// file with no build constraint at all is, of course, a default-tier file.
func fileRequiresE2ETag(file *ast.File) bool {
	for _, group := range file.Comments {
		if group.Pos() >= file.Package {
			break
		}
		for _, comment := range group.List {
			if !constraint.IsGoBuild(comment.Text) && !constraint.IsPlusBuild(comment.Text) {
				continue
			}
			expr, err := constraint.Parse(comment.Text)
			if err != nil {
				continue
			}
			if tag, ok := expr.(*constraint.TagExpr); ok && tag.Tag == "e2e" {
				return true
			}
		}
	}
	return false
}

// unitTierAliasedPackages maps the import path of every package this
// detector's qualified-call patterns name explicitly to its canonical short
// name (the identifier its own unaliased import uses): "os/exec" -> "exec",
// "os" -> "os", the repository's internal/testenv -> "testenv", and
// internal/runner/runnertest -> "runnertest". unitTierPackageAliases uses it
// to resolve an aliased import (`osexec "os/exec"`) back to "exec", so
// classifyUnitTierCall's pkgName comparisons still match (review note #764
// N1). It does not handle a dot import of any of these -- see the TODO on
// classifyUnitTierCall.
var unitTierAliasedPackages = map[string]string{
	"os/exec": "exec",
	"os":      "os",
}

const (
	unitTierTestenvImportSuffix    = "/internal/testenv"
	unitTierRunnertestImportSuffix = "/internal/runner/runnertest"
)

// unitTierPackageAliases resolves file's own imports of the packages
// unitTierAliasedPackages names (plus internal/testenv and
// internal/runner/runnertest, matched by path suffix since they are this
// module's own packages) to their canonical short name, keyed by whatever
// local identifier this file actually uses for them -- its explicit import
// alias, or that canonical name itself when the import is unaliased. This
// lets classifyUnitTierCall recognise `osexec.Command(...)` the same way it
// recognises a plain `exec.Command(...)`.
func unitTierPackageAliases(file *ast.File) map[string]string {
	aliases := map[string]string{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		canonical, known := unitTierAliasedPackages[path]
		switch {
		case known:
		case strings.HasSuffix(path, unitTierTestenvImportSuffix):
			canonical = "testenv"
		case strings.HasSuffix(path, unitTierRunnertestImportSuffix):
			canonical = "runnertest"
		default:
			continue
		}
		local := canonical
		if imp.Name != nil {
			local = imp.Name.Name
		}
		aliases[local] = canonical
	}
	return aliases
}

func scanUnitTierFile(fset *token.FileSet, file *ast.File, rel string) []UnitTierMatch {
	aliases := unitTierPackageAliases(file)
	var out []UnitTierMatch
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.CallExpr:
			if match, ok := classifyUnitTierCall(node, aliases); ok {
				match.File = rel
				match.Line = fset.Position(node.Pos()).Line
				out = append(out, match)
			}
		case *ast.BasicLit:
			if node.Kind == token.STRING && strings.Contains(node.Value, "HELPER_PROCESS") {
				out = append(out, UnitTierMatch{
					File:    rel,
					Line:    fset.Position(node.Pos()).Line,
					Pattern: UnitTierPatternHelperProcessEnv,
					Detail:  node.Value,
				})
			}
		}
		return true
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// classifyUnitTierCall reports the UnitTierMatch (File/Line left zero,
// filled in by the caller) call represents, if any. aliases is the file's
// own import-alias resolution from unitTierPackageAliases.
//
// TODO(task-24): known evasions this detector does not catch (review note
// #764 N1), left as documented gaps rather than closed here because none is
// currently used anywhere in this repository and closing all of them would
// cost more than the risk warrants right now:
//   - a dot import of "os/exec" (`. "os/exec"`), so a bare `Command(...)`
//     call is indistinguishable from an unrelated identifier;
//   - `&exec.Cmd{Path: ...}` built as a composite literal and run via
//     `.Run()`/`.Start()`, rather than constructed through exec.Command;
//   - syscall.ForkExec / syscall.Exec called directly;
//   - a git helper called through a method value or a qualified call whose
//     Sel.Name is not in UnitTierGitHelperNames (this repository's one
//     instance, internal/orchestrate/pr_land_review_test.go's
//     worktrees.Create(...), reaches git only through production code the
//     task-8 runtime guard covers, not through a name this detector needs);
//   - a git helper reached through a package-level function variable
//     (`var run = exec.Command; run("git")`).
func classifyUnitTierCall(call *ast.CallExpr, aliases map[string]string) (UnitTierMatch, bool) {
	switch fn := call.Fun.(type) {
	case *ast.SelectorExpr:
		pkgName := ""
		if pkg, ok := fn.X.(*ast.Ident); ok {
			pkgName = pkg.Name
			if canonical, resolved := aliases[pkgName]; resolved {
				pkgName = canonical
			}
		}
		switch {
		case pkgName == "exec" && fn.Sel.Name == "Command":
			return UnitTierMatch{Pattern: UnitTierPatternExecStart, Detail: pkgName + "." + fn.Sel.Name, SelfReexec: callArgIsOSArgsZero(call, 0)}, true
		case pkgName == "exec" && fn.Sel.Name == "CommandContext":
			return UnitTierMatch{Pattern: UnitTierPatternExecStart, Detail: pkgName + "." + fn.Sel.Name, SelfReexec: callArgIsOSArgsZero(call, 1)}, true
		case pkgName == "os" && fn.Sel.Name == "StartProcess":
			return UnitTierMatch{Pattern: UnitTierPatternExecStart, Detail: pkgName + "." + fn.Sel.Name, SelfReexec: callArgIsOSArgsZero(call, 0)}, true
		case pkgName == "testenv" && fn.Sel.Name == "WriteExecutableFile":
			return UnitTierMatch{Pattern: UnitTierPatternWriteExecutable, Detail: pkgName + "." + fn.Sel.Name}, true
		case pkgName == "runnertest" && fn.Sel.Name == "AllowRealProcess":
			return UnitTierMatch{Pattern: UnitTierPatternAllowRealProcess, Detail: pkgName + "." + fn.Sel.Name}, true
		case fn.Sel.Name == "Setenv" && callFirstStringArgEquals(call, "PATH"):
			receiver := pkgName
			if receiver == "" {
				receiver = "t"
			}
			return UnitTierMatch{Pattern: UnitTierPatternSetenvPath, Detail: receiver + `.Setenv("PATH", ...)`}, true
		case UnitTierGitHelperNames[fn.Sel.Name]:
			// A qualified call to a named git helper -- e.g.
			// testenv.ConfigureGitAutoMaintenanceOff(...) -- reached from
			// outside its own package. Matched by Sel.Name alone,
			// regardless of the qualifier, so a helper this repository
			// calls both ways (unqualified in its own package, qualified
			// everywhere else) only needs one entry in the map.
			detail := fn.Sel.Name
			if pkgName != "" {
				detail = pkgName + "." + fn.Sel.Name
			}
			return UnitTierMatch{Pattern: UnitTierPatternGitHelper, Detail: detail}, true
		}
	case *ast.Ident:
		if UnitTierGitHelperNames[fn.Name] {
			return UnitTierMatch{Pattern: UnitTierPatternGitHelper, Detail: fn.Name}, true
		}
	}
	return UnitTierMatch{}, false
}

// callArgIsOSArgsZero reports whether call's argument at index is exactly
// the expression os.Args[0] -- Go's own helper-process pattern for
// re-running the test binary itself, the shape
// TestUnitTierAllowListEntriesAreGenuineHelperProcessReexec (review note
// #764 B1) requires of every exec-start match in an allow-listed file.
func callArgIsOSArgsZero(call *ast.CallExpr, index int) bool {
	if index >= len(call.Args) {
		return false
	}
	indexExpr, ok := call.Args[index].(*ast.IndexExpr)
	if !ok {
		return false
	}
	sel, ok := indexExpr.X.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Args" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "os" {
		return false
	}
	lit, ok := indexExpr.Index.(*ast.BasicLit)
	return ok && lit.Kind == token.INT && lit.Value == "0"
}

// callFirstStringArgEquals reports whether call's first argument is a
// string literal whose unquoted value equals want.
func callFirstStringArgEquals(call *ast.CallExpr, want string) bool {
	if len(call.Args) == 0 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	unquoted, err := strconv.Unquote(lit.Value)
	return err == nil && unquoted == want
}

// CountUnitTierMatchesByFile aggregates matches into a per-file total count,
// the shape the pending list and its guard compare against.
func CountUnitTierMatchesByFile(matches []UnitTierMatch) map[string]int {
	counts := map[string]int{}
	for _, m := range matches {
		counts[m.File]++
	}
	return counts
}

// UnitTierPendingEntry is one committed line of unit_tier.pending: a
// default-tier test file that still trips the unit-tier detector, its exact
// match count as of the commit that added or last updated the entry, and
// the task that owns converting, moving, or deleting it.
type UnitTierPendingEntry struct {
	File  string
	Count int
	Owner string
}

// unitTierPendingHeader is written by FormatUnitTierPending and recognised
// (as ordinary "#"-prefixed comment lines) by ParseUnitTierPendingBytes.
const unitTierPendingHeader = "" +
	"# spec/plans/coverage-to-100 task-24: one line per default-tier test\n" +
	"# file the unit-tier detector (internal/quality/unittier.go) still\n" +
	"# matches, beyond zero. Format: <path>\\t<count>\\t<owning task>. The\n" +
	"# grand total may not rise against the base branch's copy of this file\n" +
	"# (ciaudit.CompareUnitTierPendingTotal, wired into `wb ci audit\n" +
	"# --target`) unless the same PR shrinks other entries by at least as\n" +
	"# much; the PR that creates this file is exempt. Remove an entry once\n" +
	"# its count reaches 0.\n"

// ParseUnitTierPending reads the committed pending list at path.
func ParseUnitTierPending(path string) (map[string]UnitTierPendingEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read pending list %s: %w", path, err)
	}
	return ParseUnitTierPendingBytes(data, path)
}

// ParseUnitTierPendingBytes parses pending-list content already in memory
// (for example the output of `git show <target>:<path>`, which
// ciaudit.CompareUnitTierPendingTotal reads without ever writing a temp
// file). sourceName is used only to build a readable error message.
func ParseUnitTierPendingBytes(data []byte, sourceName string) (map[string]UnitTierPendingEntry, error) {
	out := map[string]UnitTierPendingEntry{}
	var problems []string
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			problems = append(problems, fmt.Sprintf("%s:%d: want 3 tab-separated fields (path, count, owning task), got %d", sourceName, i+1, len(parts)))
			continue
		}
		file := strings.TrimSpace(parts[0])
		count, convErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		owner := strings.TrimSpace(parts[2])
		if convErr != nil || count < 0 {
			problems = append(problems, fmt.Sprintf("%s:%d: %s has an invalid count %q", sourceName, i+1, file, parts[1]))
			continue
		}
		if owner == "" {
			problems = append(problems, fmt.Sprintf("%s:%d: %s has no owning task", sourceName, i+1, file))
			continue
		}
		if _, dup := out[file]; dup {
			problems = append(problems, fmt.Sprintf("%s:%d: duplicate entry for %s", sourceName, i+1, file))
			continue
		}
		out[file] = UnitTierPendingEntry{File: file, Count: count, Owner: owner}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%d pending-list problem(s):\n%s", len(problems), strings.Join(problems, "\n"))
	}
	return out, nil
}

// FormatUnitTierPending renders entries as the committed file format:
// a header comment, then one sorted, tab-separated line per entry.
func FormatUnitTierPending(entries map[string]UnitTierPendingEntry) string {
	sorted := make([]UnitTierPendingEntry, 0, len(entries))
	for _, e := range entries {
		sorted = append(sorted, e)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].File < sorted[j].File })
	var b strings.Builder
	b.WriteString(unitTierPendingHeader)
	for _, e := range sorted {
		fmt.Fprintf(&b, "%s\t%d\t%s\n", e.File, e.Count, e.Owner)
	}
	return b.String()
}

// UnitTierPendingTotal sums every entry's count.
func UnitTierPendingTotal(entries map[string]UnitTierPendingEntry) int {
	total := 0
	for _, e := range entries {
		total += e.Count
	}
	return total
}

// ParseUnitTierAllow reads the committed allow list at path: one
// "<path>\t<reason>" line per permanently exempt helper-process test file.
// It shares ParallelBaseline's file format and validation (a non-empty
// reason on every entry) rather than reimplementing the same parser for a
// second committed key-tab-reason list.
func ParseUnitTierAllow(path string) (map[string]string, error) {
	return ParseParallelBaseline(path)
}
