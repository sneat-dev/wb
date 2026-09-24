package quality

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// execWriteFileGuardExcludedDirs are the subtrees ScanExecWriteFileCallSites
// does not scan. internal/execfile is excluded because it is the one place
// permitted to write an executable file directly -- it is where
// execfile.WriteExecutableFile (re-exported by internal/testenv for callers
// that already import that package) itself lives, and its own tests
// exercise the literal write-then-rename sequence the guard exists to keep
// everyone else away from.
var execWriteFileGuardExcludedDirs = []string{"internal/execfile"}

// ScanExecWriteFileCallSites walks every _test.go file under root (skipping
// .git/node_modules/vendor/dot-directories and
// execWriteFileGuardExcludedDirs) and reports one "file:line" entry for
// every os.WriteFile call whose mode argument is a literal integer with any
// executable bit set (0o111).
//
// A test that writes an executable file this way and then execs it by path
// races any concurrent fork elsewhere in the same parallel test binary: the
// write leaves a writable file descriptor open at the final path for a
// window a fork can inherit into a child before that child's own exec
// replaces its image, which fails the child's exec with "text file busy"
// (golang/go#22315; task-21, #739). testenv.WriteExecutableFile closes that
// window by writing to a temporary sibling file and renaming it into place
// under a process-wide fork guard; every fake-executable writer must use it
// instead of os.WriteFile.
//
// This check is necessarily limited to a literal mode argument -- a call
// site that computes its mode at runtime cannot be classified statically --
// but every fake-executable writer found in this repository as of #739
// used a literal 0o755/0755, so that is the pattern this check enforces.
func ScanExecWriteFileCallSites(root string) ([]string, error) {
	var violations []string
	fset := token.NewFileSet()
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
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
				for _, excluded := range execWriteFileGuardExcludedDirs {
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
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 3 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "WriteFile" {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok || pkgIdent.Name != "os" {
				return true
			}
			lit, ok := call.Args[2].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				return true
			}
			mode, convErr := strconv.ParseInt(lit.Value, 0, 64)
			if convErr != nil {
				return true
			}
			if mode&0o111 == 0 {
				return true
			}
			violations = append(violations, fmt.Sprintf("%s: os.WriteFile with executable mode %s", fset.Position(lit.Pos()), lit.Value))
			return true
		})
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", root, walkErr)
	}
	return violations, nil
}
