package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestNoPackageLevelFlagBoundGlobalsReappear is the guard spec/plans/
// coverage-to-100 task-5 (sneat-dev/wb#733) asked for: commandStarted,
// extraOrgs, nonInteractive, filterFlag and projectsRoot all moved off
// package-level globals onto *invocation, precisely because a package-level
// global bound to a cobra flag is shared, mutable state that raced across
// concurrent invocations in the same process. This test makes sure the next
// flag never quietly grows a sibling: it fails if any cmd/wb production file
// declares a package-level variable whose address is passed as the target of
// a cobra *Var-family flag-binding call (e.g. StringVar(&x, ...),
// BoolVar(&x, ...)).
//
// It is deliberately narrower than "no package-level var": this package
// keeps several package-level function-typed seams (defaultBranchRead,
// defaultBranchGit, and similar) that exist purely so tests can substitute a
// fake dependency, not to carry per-invocation flag state, and those are not
// what caused #733. The check is specifically for the flag-binding shape.
func TestNoPackageLevelFlagBoundGlobalsReappear(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	fset := token.NewFileSet()
	packageGlobals := map[string]bool{}
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, file)
		for _, decl := range file.Decls {
			genDecl, ok := decl.(*ast.GenDecl)
			if !ok || genDecl.Tok != token.VAR {
				continue
			}
			for _, spec := range genDecl.Specs {
				valueSpec, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range valueSpec.Names {
					if name.Name != "_" {
						packageGlobals[name.Name] = true
					}
				}
			}
		}
	}

	var violations []string
	for _, file := range files {
		relative := fset.Position(file.Pos()).Filename
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// The flag-binding family is *Var(&target, name, default, usage):
			// StringVar, BoolVar, IntVar, StringSliceVar, DurationVar, Int64Var,
			// Float64VarP, StringVarP, and so on (both plain and *P forms).
			if !strings.HasSuffix(selector.Sel.Name, "Var") && !strings.HasSuffix(selector.Sel.Name, "VarP") {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			unary, ok := call.Args[0].(*ast.UnaryExpr)
			if !ok || unary.Op != token.AND {
				return true
			}
			ident, ok := unary.X.(*ast.Ident)
			if !ok {
				return true
			}
			if packageGlobals[ident.Name] {
				position := fset.Position(call.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: %s binds package-level global %q; add the field to *invocation instead",
					filepath.Base(relative), position.Line, selector.Sel.Name, ident.Name))
			}
			return true
		})
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("new package-level flag-bound global(s) reintroduced the #733 data race:\n%s", strings.Join(violations, "\n"))
	}
}

// invocationEntryConstructors names the only production functions allowed to
// build a fresh *invocation from nothing. Every other production function
// must receive inv as a parameter and thread it down, which is the whole
// point of task-5/#733: a function that quietly builds its own throwaway
// invocation instead of using the one the caller threaded is indistinguishable,
// by type, from a function that reads inv correctly, but it silently drops
// whatever --projects-root/--filter/--non-interactive/--org the operator (or
// a test) set. #752's review (N2) found exactly this shape surviving two
// mutations, sync.go and remote_publish.go, with no way to catch it short of
// a real pty; this static check catches it for free instead.
//
//   - newRootCmd is the tree-inspection entry point tests use; it has no
//     production caller.
//   - runWithStdin is the single production execution entry point
//     (main -> run -> runWithStdin), where a fresh invocation is filled in by
//     cobra's flag parsing immediately afterward.
//   - dispatchWithHandlers runs strictly before cobra ever parses a flag, on
//     the hidden pre-cobra protocol paths (agent-remote, session-resolver);
//     none of those paths reads --projects-root or any other flag, so its
//     invocation is deliberately always the zero value, mirroring the
//     zero-value package-level global those paths always saw before #733.
var invocationEntryConstructors = map[string]bool{
	"newRootCmd":           true,
	"runWithStdin":         true,
	"dispatchWithHandlers": true,
}

// findBareInvocationLiterals parses every non-test .go file directly inside
// dir and reports every `invocation{...}` (or `&invocation{...}`) composite
// literal that appears inside a top-level function whose name is not in
// allowed. It is a plain top-level scan (one file, no type-checking), which
// is why it can run against both the real cmd/wb package and an isolated
// single-file fixture in a test.
func findBareInvocationLiterals(t *testing.T, dir string, allowed map[string]bool) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var violations []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			funcDecl, ok := decl.(*ast.FuncDecl)
			if !ok || funcDecl.Body == nil {
				continue
			}
			if allowed[funcDecl.Name.Name] {
				continue
			}
			ast.Inspect(funcDecl.Body, func(n ast.Node) bool {
				composite, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				ident, ok := composite.Type.(*ast.Ident)
				if !ok || ident.Name != "invocation" {
					return true
				}
				position := fset.Position(composite.Pos())
				violations = append(violations, fmt.Sprintf("%s:%d: %s builds a fresh invocation{} instead of threading its caller's inv",
					filepath.Base(name), position.Line, funcDecl.Name.Name))
				return true
			})
		}
	}
	return violations
}

// TestNoBareInvocationLiteralsOutsideEntryConstructors is the #752/N2 AST
// guard: any production function outside invocationEntryConstructors that
// builds its own invocation{} instead of using the inv its caller threaded
// is exactly the #733 regression shape (a stale or wrong invocation, silently
// diverging from the one the operator's flags actually populated).
func TestNoBareInvocationLiteralsOutsideEntryConstructors(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	violations := findBareInvocationLiterals(t, dir, invocationEntryConstructors)
	if len(violations) > 0 {
		sort.Strings(violations)
		t.Fatalf("invocation{} built outside the named entry constructors (%v):\n%s",
			sortedInvocationConstructorNames(), strings.Join(violations, "\n"))
	}
}

// TestBareInvocationLiteralGuardCatchesAStrayLiteral proves
// findBareInvocationLiterals is not vacuously passing: it builds an isolated
// one-file fixture package with a disallowed function that constructs its own
// invocation{}, the same shape #752's review found in sync.go/remote_publish.go
// before they were threaded correctly, and asserts the guard reports it.
func TestBareInvocationLiteralGuardCatchesAStrayLiteral(t *testing.T) {
	fixture := t.TempDir()
	source := `package fixture

func newSyncCmd(inv *invocation) {
	_ = inv
}

func runSyncWithAFreshInvocationInstead() {
	inv := &invocation{}
	_ = inv
}
`
	if err := os.WriteFile(filepath.Join(fixture, "fixture.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	violations := findBareInvocationLiterals(t, fixture, invocationEntryConstructors)
	if len(violations) != 1 || !strings.Contains(violations[0], "runSyncWithAFreshInvocationInstead") {
		t.Fatalf("guard did not catch the stray literal: %v", violations)
	}

	// A literal inside an allowed entry constructor must not be flagged.
	allowedSource := `package fixture

func newRootCmd() {
	inv := &invocation{}
	_ = inv
}
`
	if err := os.WriteFile(filepath.Join(fixture, "fixture.go"), []byte(allowedSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if violations := findBareInvocationLiterals(t, fixture, invocationEntryConstructors); len(violations) != 0 {
		t.Fatalf("guard flagged an allowed entry constructor: %v", violations)
	}
}

func sortedInvocationConstructorNames() []string {
	keys := make([]string, 0, len(invocationEntryConstructors))
	for k := range invocationEntryConstructors {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
