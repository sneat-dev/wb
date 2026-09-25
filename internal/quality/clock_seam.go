// This file backs spec/plans/coverage-to-100 task-10: the static check that
// keeps a direct time.Sleep/time.Now/time.After/time.NewTimer/time.Tick call
// out of a retry, timeout or backoff loop.
//
// "Retry, timeout or backoff code path" is defined mechanically here, not by
// judgement: it is exactly the named list below, ClockSeamSites -- one entry
// per (file, function) that owned a direct time.Sleep/time.Now call
// controlling a retry loop, a lock-wait deadline, or a poll-until-ready wait
// before task-10 gave it a clock/sleep seam (a function parameter or struct
// field, defaulting to the real time.Now/time.Sleep/time.After/
// time.NewTimer/time.Tick, that a test replaces with a fake so the wait
// never happens in real time). A function on this list must never again
// call any of those five directly -- every wait it makes must go through
// its own seam. This mirrors task-24's unit-tier pending list
// (internal/quality/unittier.go): a named, reviewed surface, not an
// open-ended repository-wide scan, so a legitimate, unrelated
// `time.Sleep`/`time.Now` elsewhere (a heartbeat ticker, a one-off pause, or
// the single place each seam's own default is seeded, e.g.
// `Sleep: time.Sleep` in a DefaultXxxDeps/New constructor) is never a false
// positive.
//
// Add an entry here, in the same reviewed PR, whenever a new retry/timeout/
// backoff loop is written anywhere in this repository and given a seam the
// same way; FindClockSeamViolations then holds it to the same zero-direct-
// calls rule from that point on. Removing an entry is a mechanical no-op
// once its function is deleted. A site naming a function
// FindClockSeamViolations cannot locate is itself an error (a stale or
// mistyped entry), not a silent pass.
package quality

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
)

// ClockSeamPattern names one banned direct time-package call shape.
type ClockSeamPattern string

const (
	ClockSeamPatternSleep    ClockSeamPattern = "time.Sleep"
	ClockSeamPatternNow      ClockSeamPattern = "time.Now"
	ClockSeamPatternAfter    ClockSeamPattern = "time.After"
	ClockSeamPatternNewTimer ClockSeamPattern = "time.NewTimer"
	ClockSeamPatternTick     ClockSeamPattern = "time.Tick"
)

// clockSeamBannedSelectors maps the time-package selector name to the
// pattern FindClockSeamViolations reports for a direct call to it.
var clockSeamBannedSelectors = map[string]ClockSeamPattern{
	"Sleep":    ClockSeamPatternSleep,
	"Now":      ClockSeamPatternNow,
	"After":    ClockSeamPatternAfter,
	"NewTimer": ClockSeamPatternNewTimer,
	"Tick":     ClockSeamPatternTick,
}

// ClockSeamSite names one retry/timeout/backoff function this detector
// guards: File is relative to the module root, slash-separated; Func is the
// name of the function or method declared in it (a method's receiver is
// ignored -- every entry below names a function whose name is unique within
// its own file, the same pragmatic stance task-24's detector takes rather
// than resolving full method sets).
type ClockSeamSite struct {
	File string
	Func string
}

// ClockSeamSites is the named surface task-10 built while giving each of
// these functions a clock/sleep seam. See the package doc comment above for
// what belongs here and when to add to it.
var ClockSeamSites = []ClockSeamSite{
	{File: "internal/remotestate/gitrepo/clonelock.go", Func: "acquireCloneLock"},
	{File: "internal/remotestate/gitrepo/provider.go", Func: "Fetch"},
	{File: "internal/gitops/gitops.go", Func: "pull"},
	{File: "internal/agents/owner.go", Func: "StopRun"},
	{File: "internal/agents/owner.go", Func: "waitForProcessExit"},
	{File: "internal/orchestrate/pr_create.go", Func: "pinPullRequestViewToHead"},
	{File: "internal/orchestrate/worktree_merge_ack.go", Func: "closeSupersededWorktreeMergePullRequest"},
	{File: "internal/orchestrate/worktree_merge_ack.go", Func: "ensurePreparedWorktreeMergeRebatch"},
	{File: "internal/worktrees/repository_registration_lock.go", Func: "acquireRepositoryRegistrationLock"},
	{File: "cmd/wb/daemon_process_darwin.go", Func: "startDaemonProcessInjected"},
	{File: "cmd/wb/daemon_process_darwin.go", Func: "awaitLaunchdReady"},
	// cmd/wb/daemon.go's own retry/timeout loops (daemonController.stateLock
	// and others) already went through daemonDependencies' now/lockNow/sleep
	// seam before task-10 (see that struct's doc comment); stateLock is
	// listed here so this guard, not just code review, keeps it that way.
	{File: "cmd/wb/daemon.go", Func: "stateLock"},
}

// ClockSeamMatch is one direct, banned time-package call found inside one
// ClockSeamSite's function body.
type ClockSeamMatch struct {
	// File is the path relative to root, slash-separated.
	File string
	// Line is the match's line number.
	Line int
	// Func is the enclosing function's name (the ClockSeamSite.Func that
	// found it).
	Func string
	// Pattern names which time-package call matched.
	Pattern ClockSeamPattern
}

func (m ClockSeamMatch) String() string {
	return fmt.Sprintf("%s:%d: %s calls %s directly, want it routed through its clock/sleep seam", m.File, m.Line, m.Func, m.Pattern)
}

// FindClockSeamViolations parses every file ClockSeamSites names (relative
// to root) and reports every direct time.Sleep/time.Now/time.After/
// time.NewTimer/time.Tick call found inside each named function's body.
// It returns an error, rather than zero matches, when a named function
// cannot be found in its file at all -- a stale or mistyped
// ClockSeamSites entry must fail loudly, never silently pass as clean.
func FindClockSeamViolations(root string) ([]ClockSeamMatch, error) {
	return findClockSeamViolationsAgainst(root, ClockSeamSites)
}

// findClockSeamViolationsAgainst is FindClockSeamViolations' implementation,
// parameterized on the site list. Production always calls it with the
// reviewed ClockSeamSites (via FindClockSeamViolations); tests call it
// directly with a test-local site list pointed at fixture sources, so the
// detector's own matching, sorting and error-handling logic can be exercised
// without mutating the shared, production-only ClockSeamSites slice (which
// parallel tests elsewhere in this package also read).
func findClockSeamViolationsAgainst(root string, sites []ClockSeamSite) ([]ClockSeamMatch, error) {
	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	var matches []ClockSeamMatch
	for _, site := range sites {
		file, ok := parsed[site.File]
		if !ok {
			path := filepath.Join(root, filepath.FromSlash(site.File))
			var parseErr error
			file, parseErr = parser.ParseFile(fset, path, nil, parser.ParseComments)
			if parseErr != nil {
				return nil, fmt.Errorf("parse %s: %w", site.File, parseErr)
			}
			parsed[site.File] = file
		}
		decl := findFuncDecl(file, site.Func)
		if decl == nil {
			return nil, fmt.Errorf("clock seam site %s:%s: function not found (stale ClockSeamSites entry?)", site.File, site.Func)
		}
		timeName, hasTimeImport := timeLocalName(file)
		if !hasTimeImport || decl.Body == nil {
			continue
		}
		ast.Inspect(decl.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := selector.X.(*ast.Ident)
			if !ok || ident.Name != timeName {
				return true
			}
			pattern, banned := clockSeamBannedSelectors[selector.Sel.Name]
			if !banned {
				return true
			}
			matches = append(matches, ClockSeamMatch{
				File: site.File, Line: fset.Position(call.Pos()).Line, Func: site.Func, Pattern: pattern,
			})
			return true
		})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].File != matches[j].File {
			return matches[i].File < matches[j].File
		}
		return matches[i].Line < matches[j].Line
	})
	return matches, nil
}

// findFuncDecl returns the first top-level function or method declaration
// in file whose name is exactly name, or nil when none matches.
func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if funcDecl.Name != nil && funcDecl.Name.Name == name {
			return funcDecl
		}
	}
	return nil
}

// timeLocalName resolves file's own import of the standard library "time"
// package to the local identifier its calls use: the import's explicit
// alias when it has one, "time" otherwise. It reports false when file does
// not import "time" at all, so a function with no time import trivially has
// no banned calls to find.
func timeLocalName(file *ast.File) (string, bool) {
	for _, imp := range file.Imports {
		path, err := stripImportQuotes(imp.Path.Value)
		if err != nil || path != "time" {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name, true
		}
		return "time", true
	}
	return "", false
}

// stripImportQuotes removes the surrounding double quotes go/ast leaves on
// an *ast.ImportSpec's Path.Value (e.g. `"time"` -> `time`).
func stripImportQuotes(value string) (string, error) {
	if len(value) < 2 || value[0] != '"' || value[len(value)-1] != '"' {
		return "", fmt.Errorf("import path %q is not a quoted string literal", value)
	}
	return value[1 : len(value)-1], nil
}
