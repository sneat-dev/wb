package orchestrate

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// suffixConstNamePattern matches the naming convention every worktree-merge
// report sidecar/acknowledgement suffix constant follows, e.g.
// worktreeMergeStrandedLandingAcknowledgementSuffix or
// worktreeMergePublishedCandidateAdoptionSuffix. It is deliberately a naming
// pattern, not a hand-copied list of the constants themselves: any suffix
// constant added later that follows the convention is picked up
// automatically by TestWorktreeMergeReportSidecarSuffixParity below.
var suffixConstNamePattern = regexp.MustCompile(`^worktreeMerge[A-Za-z0-9]*Suffix$`)

// packageDecls indexes every top-level function and const/var declaration in
// this package's production sources (test files excluded) by name, so a test
// can walk the real identifier-reachability graph instead of re-typing which
// suffixes each function skips.
type packageDecls struct {
	funcs map[string]*ast.FuncDecl
	exprs map[string]ast.Expr // const/var initializer expressions, by name
}

func loadPackageDecls(t *testing.T) packageDecls {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read internal/orchestrate directory: %v", err)
	}
	fset := token.NewFileSet()
	decls := packageDecls{funcs: map[string]*ast.FuncDecl{}, exprs: map[string]ast.Expr{}}
	parsed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed++
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil { // ignore methods; every function under test is a package-level func
					decls.funcs[d.Name.Name] = d
				}
			case *ast.GenDecl:
				if d.Tok != token.CONST && d.Tok != token.VAR {
					continue
				}
				for _, spec := range d.Specs {
					valueSpec, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					for i, name := range valueSpec.Names {
						if i < len(valueSpec.Values) {
							decls.exprs[name.Name] = valueSpec.Values[i]
						}
					}
				}
			}
		}
	}
	if parsed == 0 {
		t.Fatal("parsed zero non-test .go files in internal/orchestrate")
	}
	return decls
}

// suffixesReachableFrom walks every identifier transitively referenced from
// the named function or declaration: nested calls to other package-level
// functions are followed into their bodies, and references to package-level
// consts/vars are followed into their initializer expressions (which is how
// a shared suffix-list slice, or a var an earlier version of the code might
// introduce, gets resolved down to the literal suffix constants it lists).
// Any identifier matching suffixConstNamePattern encountered along the way is
// recorded as "reachable" from start.
func suffixesReachableFrom(decls packageDecls, start string) map[string]bool {
	found := map[string]bool{}
	visited := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		if visited[name] {
			return
		}
		visited[name] = true
		if suffixConstNamePattern.MatchString(name) {
			found[name] = true
		}
		var node ast.Node
		if fd, ok := decls.funcs[name]; ok {
			node = fd.Body
		} else if expr, ok := decls.exprs[name]; ok {
			node = expr
		}
		if node == nil {
			return
		}
		ast.Inspect(node, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name != name {
				visit(id.Name)
			}
			return true
		})
	}
	visit(start)
	return found
}

// TestWorktreeMergeReportSidecarSuffixParity guards against the recurring
// defect where a worktree-merge report sidecar/acknowledgement suffix
// constant is wired into activeWorktreeMergeLaneReceipt's non-receipt skip
// logic but not resolveWorktreeMergeReceiptPath's (or vice versa). Both
// functions scan the same *.json report directory and must treat the exact
// same set of filename suffixes as "this is a sidecar, not a receipt" -- a
// suffix skipped by only one of them lets that function try to parse the
// sidecar as a receipt (an unskipped acknowledgement can carry receipt-shaped
// fields, including the receipt's own sha256, so a false match is plausible)
// or, for activeWorktreeMergeLaneReceipt, hard-fails the whole lane lookup
// when the sidecar's identity does not check out as a receipt.
//
// This walks the real identifier graph via go/ast rather than comparing two
// hand-copied lists, so it fails automatically the next time a suffix
// constant is registered with one function and not the other -- exactly the
// gap that let four suffixes drift out of sync between the two functions
// after the single-suffix fix on 2026-09-02 added no guarding test.
func TestWorktreeMergeReportSidecarSuffixParity(t *testing.T) {
	decls := loadPackageDecls(t)

	var universe []string
	for name := range decls.exprs {
		if suffixConstNamePattern.MatchString(name) {
			universe = append(universe, name)
		}
	}
	sort.Strings(universe)
	if len(universe) == 0 {
		t.Fatal("discovered zero worktree-merge sidecar suffix constants; suffixConstNamePattern or parsing is broken")
	}

	const resolveFunc = "resolveWorktreeMergeReceiptPath"
	const activeFunc = "activeWorktreeMergeLaneReceipt"
	if _, ok := decls.funcs[resolveFunc]; !ok {
		t.Fatalf("function %s not found by source parsing", resolveFunc)
	}
	if _, ok := decls.funcs[activeFunc]; !ok {
		t.Fatalf("function %s not found by source parsing", activeFunc)
	}

	resolveSet := suffixesReachableFrom(decls, resolveFunc)
	activeSet := suffixesReachableFrom(decls, activeFunc)

	for _, name := range universe {
		name := name
		t.Run(name, func(t *testing.T) {
			inResolve := resolveSet[name]
			inActive := activeSet[name]
			if inResolve != inActive {
				t.Errorf("suffix constant %s is treated as a non-receipt report sidecar by only one scan: %s=%v %s=%v",
					name, resolveFunc, inResolve, activeFunc, inActive)
			}
		})
	}
}
