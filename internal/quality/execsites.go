// This file backs task-8's mechanical check (spec/plans/coverage-to-100
// task-8, verifies #4): non-test Go code must reach an external program
// through internal/runner or internal/gitcli, not directly, so unit tests
// can substitute their fakes.
//
// The check flags, in every non-test `.go` file outside internal/runner,
// internal/gitcli and internal/process (internal/process joins the
// allow-list as the runner's base, per task-8's own text) and outside the
// fd-inheriting secure git helpers named in execSiteAllowedFunctionNames:
//
//   - a direct exec.Command, exec.CommandContext, os.StartProcess or
//     syscall.Exec call;
//   - a literal "git" argv[0] passed to exec.Command, exec.CommandContext,
//     process.CommandContext or process.CommandContextInteractive;
//   - a call to one of the named git helpers in ExecSiteGitHelperNames.
//
// internal/quality/testdata/exec_sites.pending lists every file this
// detector still matches, following the exact same rules as task-24's
// unit_tier.pending: one line per file with its match count and owning
// task, a local exact-equality guard
// (TestExecSitesPendingDoesNotRegress), and a cross-PR ratchet on the
// list's grand total (ciaudit.CompareExecSitesPendingTotal, wired into
// `wb ci audit --target` through ciaudit.CompareAgainstTarget) that must
// not rise against the base branch's copy unless another entry shrinks by
// at least as much.
package quality

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ExecSitePattern names one direct-process escape shape execSites' detector
// counts.
type ExecSitePattern string

const (
	// ExecSitePatternDirectExec is a direct exec.Command, exec.CommandContext,
	// os.StartProcess or syscall.Exec call.
	ExecSitePatternDirectExec ExecSitePattern = "direct-exec"
	// ExecSitePatternGitLiteral is a literal "git" argv[0] passed to
	// exec.Command, exec.CommandContext, process.CommandContext or
	// process.CommandContextInteractive.
	ExecSitePatternGitLiteral ExecSitePattern = "git-literal"
	// ExecSitePatternGitHelper is a call to one of ExecSiteGitHelperNames.
	ExecSitePatternGitHelper ExecSitePattern = "git-helper-call"
)

// ExecSiteGitHelperNames is task-8's starting list of named git-helper
// functions this repository's production code calls without themselves
// literally spelling "git" at the call site -- the same starting list
// task-24's static check names for test files ("starting with runGit,
// gitOutput, gitRawOutput, runGitIn, runGitWithFilesystemCapability and
// gitWithExtraFiles"), since these same helper names are used from both
// test and production code across this repository.
var ExecSiteGitHelperNames = map[string]bool{
	"runGit": true, "gitOutput": true, "gitRawOutput": true,
	"runGitIn": true, "runGitWithFilesystemCapability": true,
	"gitWithExtraFiles": true,
}

// execSiteAllowedFunctionNames are the fd-inheriting secure git helpers and
// their launchers named in task-8's "Allow-list: the fd-inheriting secure
// git helpers" table: they must keep calling exec.Command/exec.CommandContext
// directly because they rely on fd inheritance, both before and after
// task-11 splits each into a thin shim plus a testable core. A call inside a
// function whose own name is in this set is never flagged, regardless of
// which pattern it would otherwise match; a call to one of these functions
// from elsewhere is unaffected (only their own bodies are exempt).
var execSiteAllowedFunctionNames = map[string]bool{
	"setHooksPathAt": true, "RunSecureHooksGitHelper": true,
	"gitCanonicalBytes": true, "RunSecureCanonicalGitHelper": true,
	"gitCanonicalPolicyBytes": true, "RunSecureCanonicalPolicyGitHelper": true,
	"runSecureStageHelper": true, "RunSecureStageGitHelper": true,
	"runSecureStageCanonicalGitHelper": true, "RunSecureStageCanonicalGitHelper": true,
	"runSecureRenameGitBytesWithHeldWorktree": true, "RunSecureRenameGitHelper": true,
	"runSecureCleanupGitHelper": true, "RunSecureCleanupGitHelper": true,

	// runGitPushDeleteWithLease (internal/orchestrate/pr_land_keep.go,
	// task-17) keeps a possibly credential-bearing push URL out of argv and
	// error text by configuring a throwaway git remote through
	// GIT_CONFIG_COUNT/KEY/VALUE environment variables set only on its own
	// child's environment. internal/runner.Runner.Run has no per-call env
	// override (task-8's four operations take no env parameter), so this one
	// call keeps its direct exec.CommandContext rather than losing that
	// property to route through the runner. Named in task-17's PR per
	// verifies #4's "any addition to the allow-list is named in the PR and
	// approved in review."
	"runGitPushDeleteWithLease": true,
}

// execSiteAllowedDirs are directory prefixes (relative to root, forward
// slashes) FindExecSiteMatches never scans: the runner and adapter
// themselves, which legitimately call exec.Command under the hood, and
// internal/process, which task-8's own text allow-lists as the runner's
// base.
var execSiteAllowedDirs = []string{"internal/runner", "internal/gitcli", "internal/process"}

// ExecSiteMatch is one occurrence FindExecSiteMatches reports.
type ExecSiteMatch struct {
	File    string
	Line    int
	Pattern ExecSitePattern
	Detail  string
}

func (m ExecSiteMatch) String() string {
	return fmt.Sprintf("%s:%d: %s (%s)", m.File, m.Line, m.Detail, m.Pattern)
}

func execSiteAllowedDir(rel string) bool {
	for _, allowed := range execSiteAllowedDirs {
		if rel == allowed || strings.HasPrefix(rel, allowed+"/") {
			return true
		}
	}
	return false
}

// FindExecSiteMatches walks root and reports every occurrence, in every
// non-test `.go` file outside execSiteAllowedDirs, of a pattern task-8's
// check bans. Results are sorted by file, then line.
func FindExecSiteMatches(root string) ([]ExecSiteMatch, error) {
	fset := token.NewFileSet()
	var matches []ExecSiteMatch
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
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := relSlashFor(root, path)
		if execSiteAllowedDir(rel) {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		matches = append(matches, scanExecSiteFile(fset, file, rel)...)
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

// scanExecSiteFile scans every call expression anywhere in file -- not just
// inside a top-level function declaration's body, but also inside a package-
// level var's function-literal value (`var runSystemctl = func(...) {...}`),
// a struct field or map value that is itself a function literal, and any
// closure nested inside any of those -- tracking, at each call, whether the
// innermost enclosing function is exempt via execSiteAllowedFunctionNames.
//
// A plain top-level FuncDecl (including a method: Go represents a method
// declaration as a FuncDecl with a receiver, in file.Decls exactly like an
// ordinary function) is exempt when its own name is listed. A package-level
// var/const-assigned function literal is exempt the same way, keyed off the
// name it was assigned to (execSiteTopLevelFuncLitNames resolves that
// mapping ahead of the walk, since a *ast.FuncLit carries no name of its
// own). Any other function literal -- nested inside a closure, a struct
// field, a map value, or anywhere else with no single declared name --
// simply inherits whichever function it sits inside, the same exemption a
// nested closure inside an allow-listed top-level function already got
// before this walk was rewritten to cover every position, not only
// top-level FuncDecls (review note #764 B2).
func scanExecSiteFile(fset *token.FileSet, file *ast.File, rel string) []ExecSiteMatch {
	aliases := execSitePackageAliases(file)
	litNames := execSiteTopLevelFuncLitNames(file)
	var out []ExecSiteMatch
	ast.Walk(&execSiteVisitor{
		fset:     fset,
		aliases:  aliases,
		rel:      rel,
		litNames: litNames,
		out:      &out,
	}, file)
	return out
}

// execSiteTopLevelFuncLitNames maps a package-level var declaration's
// function-literal value back to the name it was assigned to (`var
// runSystemctl = func(...) {...}` -> "runSystemctl"), the same identifier
// execSiteAllowedFunctionNames would need to exempt that closure the way it
// already exempts a plain top-level FuncDecl of the same name. A literal
// that is instead a struct field, a map value, or nested inside another
// closure has no single declared name of its own and is left out of this
// map -- execSiteVisitor falls back to inheriting its parent's exemption for
// those.
//
// Every *ast.GenDecl with Tok == token.VAR is guaranteed by Go's grammar to
// hold only *ast.ValueSpec entries in Specs (import/type/const-only shapes
// don't arise for VAR), and a ValueSpec's Values never has more entries than
// its Names (the parser rejects any source where they would mismatch outside
// a single multi-value call on the right of `:=`/`=`, which itself always
// yields exactly one Values entry). So neither the ValueSpec type assertion
// nor an index bound on Names needs an explicit check here the way
// unittier.go's more defensive, input-untrusted parsers need one -- both
// would be dead code no test could reach through a real parsed file.
func execSiteTopLevelFuncLitNames(file *ast.File) map[*ast.FuncLit]string {
	names := map[*ast.FuncLit]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec) //nolint:forcetypeassert // guaranteed by go/ast for a VAR GenDecl
			for i, value := range vs.Values {
				lit, ok := value.(*ast.FuncLit)
				if !ok {
					continue
				}
				names[lit] = vs.Names[i].Name
			}
		}
	}
	return names
}

// execSiteVisitor implements ast.Visitor, walking a *ast.File while
// threading each function's exemption down to the calls inside it.
// ast.Walk's contract -- Visit's returned Visitor is used for the node's
// children, and a nil return skips them entirely -- lets each FuncDecl or
// FuncLit hand its children a fresh *execSiteVisitor carrying its own
// exempt value (a value copy, so sibling subtrees never see each other's
// state) without an explicit push/pop stack.
type execSiteVisitor struct {
	fset     *token.FileSet
	aliases  map[string]string
	rel      string
	litNames map[*ast.FuncLit]string
	exempt   bool
	out      *[]ExecSiteMatch
}

func (v *execSiteVisitor) Visit(n ast.Node) ast.Visitor {
	switch node := n.(type) {
	case *ast.FuncDecl:
		if node.Body == nil {
			return nil
		}
		child := *v
		child.exempt = execSiteAllowedFunctionNames[node.Name.Name]
		return &child
	case *ast.FuncLit:
		child := *v
		if name, named := v.litNames[node]; named {
			child.exempt = execSiteAllowedFunctionNames[name]
		}
		return &child
	case *ast.CallExpr:
		if !v.exempt {
			if match, ok := classifyExecSiteCall(node, v.aliases); ok {
				match.File = v.rel
				match.Line = v.fset.Position(node.Pos()).Line
				*v.out = append(*v.out, match)
			}
		}
	}
	return v
}

// execSitePackageAliases resolves file's own imports of "os/exec", "os",
// "syscall" and this repository's internal/process to their canonical short
// name, the same technique unitTierPackageAliases uses.
func execSitePackageAliases(file *ast.File) map[string]string {
	aliases := map[string]string{}
	canonicalByPath := map[string]string{
		"os/exec": "exec",
		"os":      "os",
		"syscall": "syscall",
	}
	const processImportSuffix = "/internal/process"
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		canonical, known := canonicalByPath[path]
		switch {
		case known:
		case strings.HasSuffix(path, processImportSuffix):
			canonical = "process"
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

// classifyExecSiteCall reports the ExecSiteMatch call represents, if any.
func classifyExecSiteCall(call *ast.CallExpr, aliases map[string]string) (ExecSiteMatch, bool) {
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
		case pkgName == "exec" && (fn.Sel.Name == "Command" || fn.Sel.Name == "CommandContext"):
			if literalGitArg(call, 0) || (fn.Sel.Name == "CommandContext" && literalGitArg(call, 1)) {
				return ExecSiteMatch{Pattern: ExecSitePatternGitLiteral, Detail: pkgName + "." + fn.Sel.Name + `("git", ...)`}, true
			}
			return ExecSiteMatch{Pattern: ExecSitePatternDirectExec, Detail: pkgName + "." + fn.Sel.Name}, true
		case pkgName == "os" && fn.Sel.Name == "StartProcess":
			return ExecSiteMatch{Pattern: ExecSitePatternDirectExec, Detail: pkgName + "." + fn.Sel.Name}, true
		case pkgName == "syscall" && fn.Sel.Name == "Exec":
			return ExecSiteMatch{Pattern: ExecSitePatternDirectExec, Detail: pkgName + "." + fn.Sel.Name}, true
		case pkgName == "process" && (fn.Sel.Name == "CommandContext" || fn.Sel.Name == "CommandContextInteractive"):
			nameIndex := 1
			if fn.Sel.Name == "CommandContextInteractive" {
				nameIndex = 2
			}
			if literalGitArg(call, nameIndex) {
				return ExecSiteMatch{Pattern: ExecSitePatternGitLiteral, Detail: pkgName + "." + fn.Sel.Name + `(..., "git", ...)`}, true
			}
		case ExecSiteGitHelperNames[fn.Sel.Name]:
			detail := fn.Sel.Name
			if pkgName != "" {
				detail = pkgName + "." + fn.Sel.Name
			}
			return ExecSiteMatch{Pattern: ExecSitePatternGitHelper, Detail: detail}, true
		}
	case *ast.Ident:
		if ExecSiteGitHelperNames[fn.Name] {
			return ExecSiteMatch{Pattern: ExecSitePatternGitHelper, Detail: fn.Name}, true
		}
	}
	return ExecSiteMatch{}, false
}

// literalGitArg reports whether call's argument at index is the string
// literal "git".
func literalGitArg(call *ast.CallExpr, index int) bool {
	if index >= len(call.Args) {
		return false
	}
	lit, ok := call.Args[index].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(lit.Value)
	return err == nil && value == "git"
}

// CountExecSiteMatchesByFile aggregates matches per file.
func CountExecSiteMatchesByFile(matches []ExecSiteMatch) map[string]int {
	counts := map[string]int{}
	for _, m := range matches {
		counts[m.File]++
	}
	return counts
}

// exec_sites.pending shares unit_tier.pending's exact file format
// (path\tcount\towner) and this package's own parser for it -- these are
// thin, discoverable aliases onto the same implementation, not a second
// format.

// execSitesPendingHeader is written by FormatExecSitesPending and recognised
// (as ordinary "#"-prefixed comment lines) by ParseExecSitesPendingBytes
// (ParseUnitTierPendingBytes underneath). It deliberately does not reuse
// unitTierPendingHeader's text: that header names task-24's detector and
// ciaudit.CompareUnitTierPendingTotal, neither of which owns this file.
const execSitesPendingHeader = "" +
	"# spec/plans/coverage-to-100 task-8: one line per non-test .go file,\n" +
	"# outside internal/runner, internal/gitcli, internal/process and the\n" +
	"# fd-inheriting secure git helpers, where the exec-site detector\n" +
	"# (internal/quality/execsites.go) still matches, beyond zero. Format:\n" +
	"# <path>\\t<count>\\t<owning task>. The grand total may not rise against\n" +
	"# the base branch's copy of this file (ciaudit.CompareExecSitesPendingTotal,\n" +
	"# wired into `wb ci audit --target`) unless the same PR shrinks other\n" +
	"# entries by at least as much; the PR that creates this file is exempt.\n" +
	"# Remove an entry once its count reaches 0, or once the owning task's\n" +
	"# migration lands.\n"

// ParseExecSitesPending parses testdata/exec_sites.pending.
func ParseExecSitesPending(path string) (map[string]UnitTierPendingEntry, error) {
	return ParseUnitTierPending(path)
}

// ParseExecSitesPendingBytes parses exec_sites.pending content already read
// into memory (for example, a fetched target branch's copy).
func ParseExecSitesPendingBytes(data []byte, sourceName string) (map[string]UnitTierPendingEntry, error) {
	return ParseUnitTierPendingBytes(data, sourceName)
}

// FormatExecSitesPending renders entries back into exec_sites.pending's file
// format, sorted by path, under execSitesPendingHeader.
func FormatExecSitesPending(entries map[string]UnitTierPendingEntry) string {
	return formatPendingList(entries, execSitesPendingHeader)
}

// ExecSitesPendingTotal sums every entry's count.
func ExecSitesPendingTotal(entries map[string]UnitTierPendingEntry) int {
	return UnitTierPendingTotal(entries)
}
