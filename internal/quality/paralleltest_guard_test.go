package quality

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// parallelGuardExcludedDirs are the subtrees sneat-dev/wb#623's test-speed
// sweep did not touch, because other agent lanes were actively editing their
// test files at the same time (cmd/wb is #623 step 3; hub and
// internal/runqueue belong to other in-flight lanes). Once those get their
// own pass this list should shrink, and eventually disappear.
var parallelGuardExcludedDirs = []string{"cmd/wb", "hub", "internal/runqueue"}

// TestParallelBaselineDoesNotRegress guards the payoff of #623's test-speed
// sweep: every Test function and t.Run subtest in scope must either call
// Parallel(), carry a `//nolint:paralleltest` with its own reason, or already
// have been serial when the baseline in testdata/paralleltest_baseline.txt
// was recorded.
//
// golangci-lint's paralleltest linter runs too (see .golangci.yml), but with
// ignore-missing: true -- annotating each of the roughly 1,900 tests that
// were already, correctly, serial (env/cwd mutation, package-seam
// reassignment, real subprocess fixtures) with an individual nolint comment
// would be a diff explosion unrelated to catching a real mistake. This test
// is the actual regression guard: a newly added serial test that is not
// explained by a nolint comment must be a deliberate, reviewed choice to
// extend the baseline file, not a silent default.
func TestParallelBaselineDoesNotRegress(t *testing.T) {
	t.Parallel()

	root := parallelGuardModuleRoot(t)
	baseline := parallelGuardReadBaseline(t, filepath.Join(root, "internal", "quality", "testdata", "paralleltest_baseline.txt"))

	var offenders []string
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" || (strings.HasPrefix(base, ".") && base != ".") {
				return filepath.SkipDir
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr == nil {
				relSlash := filepath.ToSlash(rel)
				for _, excluded := range parallelGuardExcludedDirs {
					if relSlash == excluded || strings.HasPrefix(relSlash, excluded+"/") {
						return filepath.SkipDir
					}
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		file, parseErr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		offenders = append(offenders, parallelGuardCheckFile(fset, file, rel, baseline)...)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if len(offenders) == 0 {
		return
	}
	sort.Strings(offenders)
	const maxReported = 25
	if len(offenders) > maxReported {
		offenders = append(offenders[:maxReported], fmt.Sprintf("... and %d more", len(offenders)-maxReported))
	}
	t.Fatalf("%d test(s) are serial without explanation; add t.Parallel(), a "+
		"//nolint:paralleltest // <reason> comment, or -- if reviewed and "+
		"genuinely required to stay serial -- add the entry to "+
		"internal/quality/testdata/paralleltest_baseline.txt:\n%s",
		len(offenders), strings.Join(offenders, "\n"))
}

func parallelGuardModuleRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve this test file's own path")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find module root above " + thisFile)
		}
		dir = parent
	}
}

func parallelGuardReadBaseline(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read baseline %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[line] = true
	}
	return out
}

// parallelGuardCheckFile returns "path:line" offenders for every Test
// function and t.Run subtest in file that is serial, unexplained by a
// nolint comment, and absent from baseline.
func parallelGuardCheckFile(fset *token.FileSet, file *ast.File, relPath string, baseline map[string]bool) []string {
	var offenders []string
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Body == nil || fd.Recv != nil {
			continue
		}
		if fd.Name.Name == "TestMain" || !strings.HasPrefix(fd.Name.Name, "Test") {
			continue
		}
		if !parallelGuardSingleTestingTParam(fd.Type) {
			continue
		}
		var subtests []*ast.FuncLit
		record := func(lit *ast.FuncLit) { subtests = append(subtests, lit) }
		if !parallelGuardHasParallelCall(fd.Body, fd.Body, record) {
			key := fmt.Sprintf("%s:%d", relPath, fset.Position(fd.Pos()).Line)
			if !baseline[key] && !parallelGuardHasNolint(fset, file, fd.Pos()) {
				offenders = append(offenders, key)
			}
		}
		for _, lit := range subtests {
			if parallelGuardHasParallelCall(lit.Body, lit.Body, nil) {
				continue
			}
			key := fmt.Sprintf("%s:%d", relPath, fset.Position(lit.Pos()).Line)
			if !baseline[key] && !parallelGuardHasNolint(fset, file, lit.Pos()) {
				offenders = append(offenders, key)
			}
		}
	}
	return offenders
}

func parallelGuardSingleTestingTParam(ft *ast.FuncType) bool {
	if ft.Params == nil || len(ft.Params.List) != 1 {
		return false
	}
	field := ft.Params.List[0]
	star, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	return ok && pkgIdent.Name == "testing" && sel.Sel.Name == "T"
}

// parallelGuardHasParallelCall reports whether node's own statements call
// Parallel(), without descending into nested t.Run subtest closures (each is
// its own unit, reported to record when non-nil).
func parallelGuardHasParallelCall(node, root ast.Node, record func(*ast.FuncLit)) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Run" && len(call.Args) == 2 {
			if lit, ok := call.Args[1].(*ast.FuncLit); ok && parallelGuardSingleTestingTParam(lit.Type) {
				if record != nil {
					record(lit)
				}
				if n != root {
					return false
				}
			}
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Parallel" {
			found = true
			return false
		}
		return true
	})
	return found
}

func parallelGuardHasNolint(fset *token.FileSet, file *ast.File, pos token.Pos) bool {
	posLine := fset.Position(pos).Line
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			cLine := fset.Position(c.Pos()).Line
			if (cLine == posLine-1 || cLine == posLine) && strings.Contains(c.Text, "nolint:paralleltest") {
				return true
			}
		}
	}
	return false
}
