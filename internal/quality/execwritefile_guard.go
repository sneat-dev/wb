package quality

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
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

// execWriteFileExecutableBits is the set of mode bits (any owner/group/other
// execute bit) that marks a write as producing an executable file.
const execWriteFileExecutableBits = 0o111

// execWriteFileGroupOtherBits is any group or other permission bit. This
// repository's own convention is that a mode meant to make a file runnable
// grants at least group or other access (0o755, the mode every genuine
// fake-executable writer found while building this guard actually uses);
// an owner-only mode such as 0o700 is this repository's convention for
// locking a directory down, not for marking a file executable, and is never
// itself exec'd. isExecWriteFileExecutableMode requires both, so an
// owner-only mode is never misclassified as "will be exec'd" just because
// it happens to also set the owner-execute bit.
const execWriteFileGroupOtherBits = 0o077

// isExecWriteFileExecutableMode reports whether mode marks a write as
// producing a file this guard must protect from the ETXTBSY race.
func isExecWriteFileExecutableMode(mode int64) bool {
	return mode&execWriteFileExecutableBits != 0 && mode&execWriteFileGroupOtherBits != 0
}

// ScanExecWriteFileCallSites walks every .go file under root (skipping
// .git/node_modules/vendor/dot-directories and
// execWriteFileGuardExcludedDirs) and reports one "file:line" entry for each
// call site it can statically prove writes -- or later renders executable --
// a file at its final path without going through
// testenv.WriteExecutableFile. It flags four shapes:
//
//  1. os.WriteFile / ioutil.WriteFile / os.OpenFile with a mode argument
//     that resolves to an executable literal: a bare integer literal, an
//     os.FileMode(literal) conversion, or a local variable/parameter this
//     scan can trace back to one of those within the same function or (for
//     a parameter forwarded straight into the write) at the call site that
//     supplies it.
//  2. os.WriteFile / ioutil.WriteFile followed, later in the same function,
//     by os.Chmod or os.Fchmod on a matching path expression to an
//     executable literal mode -- the write's own mode does not matter,
//     since opening a writable fd at the final path is what races a
//     concurrent fork, regardless of what the file's mode is at that
//     moment (golang/go#22315; task-21, #739).
//
// A call site that computes its mode in a way this scan cannot trace (for
// example, a mode read from a tar header or other runtime value) is not
// flagged: it is not something a static scan can classify, and forcing
// every dynamic-mode writer through testenv.WriteExecutableFile is out of
// scope for this guard. testenv.WriteExecutableFile closes the race window
// by writing to a temporary sibling file and renaming it into place under a
// process-wide fork guard; every fake-executable writer this scan can prove
// unsafe must use it instead.
func ScanExecWriteFileCallSites(root string) ([]string, error) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	order := []string{}

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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		files[path] = file
		order = append(order, path)
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", root, walkErr)
	}

	forwarders := collectExecWriteForwarders(order, files)

	var violations []string
	for _, path := range order {
		violations = append(violations, scanExecWriteFileFile(fset, files[path], forwarders)...)
	}
	return violations, nil
}

// execWriteForwarderKey identifies a function (by its own, best-effort,
// unqualified name -- this scan does not resolve full import paths) that
// forwards one of its own parameters straight into a raw write call's mode
// argument, plus which parameter position that is.
type execWriteForwarderKey struct {
	funcName  string
	paramName string
}

// collectExecWriteForwarders finds every function whose body passes one of
// its own parameters directly (no intervening assignment) as the mode
// argument of an os.WriteFile / ioutil.WriteFile / os.OpenFile call, and
// returns the set of (function name, parameter name) pairs found. A call
// site elsewhere in the scanned tree that invokes such a function with an
// executable-literal argument at that parameter is flagged by
// scanExecWriteFileFile, since that is where the danger actually
// materializes.
func collectExecWriteForwarders(order []string, files map[string]*ast.File) map[execWriteForwarderKey]int {
	forwarders := map[execWriteForwarderKey]int{}
	for _, path := range order {
		file := files[path]
		ast.Inspect(file, func(node ast.Node) bool {
			funcDecl, ok := node.(*ast.FuncDecl)
			if !ok || funcDecl.Body == nil {
				return true
			}
			paramIndex := map[string]int{}
			index := 0
			for _, field := range funcDecl.Type.Params.List {
				for _, name := range field.Names {
					paramIndex[name.Name] = index
					index++
				}
				if len(field.Names) == 0 {
					index++
				}
			}
			ast.Inspect(funcDecl.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}
				modeArg, argIndex := execWriteFileModeArg(call)
				if modeArg == nil {
					return true
				}
				_ = argIndex
				ident, ok := modeArg.(*ast.Ident)
				if !ok {
					return true
				}
				if _, isParam := paramIndex[ident.Name]; isParam {
					forwarders[execWriteForwarderKey{funcName: funcDecl.Name.Name, paramName: ident.Name}] = paramIndex[ident.Name]
				}
				return true
			})
			return true
		})
	}
	return forwarders
}

// execWriteFileModeArg returns the mode-argument expression of call if call
// matches one of the raw-write shapes this guard cares about (os.WriteFile,
// ioutil.WriteFile, os.OpenFile), along with its argument index, or (nil,
// -1) otherwise.
func execWriteFileModeArg(call *ast.CallExpr) (ast.Expr, int) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, -1
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil, -1
	}
	switch {
	case pkgIdent.Name == "os" && sel.Sel.Name == "WriteFile" && len(call.Args) == 3:
		return call.Args[2], 2
	case pkgIdent.Name == "ioutil" && sel.Sel.Name == "WriteFile" && len(call.Args) == 3:
		return call.Args[2], 2
	case pkgIdent.Name == "os" && sel.Sel.Name == "OpenFile" && len(call.Args) == 3:
		return call.Args[2], 2
	default:
		return nil, -1
	}
}

// execWriteFileWritePath returns the path argument of call if it is an
// os.WriteFile or ioutil.WriteFile call (any mode), or nil otherwise.
func execWriteFileWritePath(call *ast.CallExpr) ast.Expr {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok {
		return nil
	}
	if (pkgIdent.Name == "os" || pkgIdent.Name == "ioutil") && sel.Sel.Name == "WriteFile" && len(call.Args) == 3 {
		return call.Args[0]
	}
	return nil
}

// execWriteFileChmodCall returns the path (Chmod) or fd (Fchmod) argument
// and the mode argument of call, and whether it takes a path this scan can
// compare against a written path (true for Chmod, false for Fchmod, which
// takes a file descriptor rather than a path), or (nil, nil, false) if call
// is neither.
func execWriteFileChmodCall(call *ast.CallExpr) (ast.Expr, ast.Expr, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, nil, false
	}
	pkgIdent, ok := sel.X.(*ast.Ident)
	if !ok || pkgIdent.Name != "os" || len(call.Args) != 2 {
		return nil, nil, false
	}
	switch sel.Sel.Name {
	case "Chmod":
		return call.Args[0], call.Args[1], true
	case "Fchmod":
		return call.Args[0], call.Args[1], false
	default:
		return nil, nil, false
	}
}

// resolveExecWriteFileMode reports the literal mode a mode expression
// resolves to, and whether it could be resolved at all. It handles a bare
// integer literal, an os.FileMode(literal) conversion, and (via locals) a
// local variable assigned one of those forms earlier in the same function.
func resolveExecWriteFileMode(expr ast.Expr, locals map[string]int64) (int64, bool) {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind != token.INT {
			return 0, false
		}
		mode, err := strconv.ParseInt(value.Value, 0, 64)
		if err != nil {
			return 0, false
		}
		return mode, true
	case *ast.CallExpr:
		sel, ok := value.Fun.(*ast.SelectorExpr)
		if !ok || len(value.Args) != 1 {
			return 0, false
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "os" || sel.Sel.Name != "FileMode" {
			return 0, false
		}
		return resolveExecWriteFileMode(value.Args[0], locals)
	case *ast.Ident:
		mode, ok := locals[value.Name]
		return mode, ok
	default:
		return 0, false
	}
}

// collectExecWriteFileLocals gathers every simple local variable assignment
// or declaration within body whose right-hand side resolves to a literal
// mode, keyed by variable name. It only understands `name := <mode-expr>`,
// `name = <mode-expr>`, and `var name os.FileMode = <mode-expr>` -- anything
// else is left unresolved, which is safe: an unresolved mode is simply not
// flagged, never misclassified.
func collectExecWriteFileLocals(body *ast.BlockStmt) map[string]int64 {
	locals := map[string]int64{}
	ast.Inspect(body, func(node ast.Node) bool {
		switch stmt := node.(type) {
		case *ast.AssignStmt:
			if len(stmt.Lhs) != len(stmt.Rhs) {
				return true
			}
			for index, lhs := range stmt.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if mode, ok := resolveExecWriteFileMode(stmt.Rhs[index], locals); ok {
					locals[ident.Name] = mode
				}
			}
		case *ast.ValueSpec:
			for index, name := range stmt.Names {
				if index >= len(stmt.Values) {
					continue
				}
				if mode, ok := resolveExecWriteFileMode(stmt.Values[index], locals); ok {
					locals[name.Name] = mode
				}
			}
		}
		return true
	})
	return locals
}

// scanExecWriteFileFile applies every ScanExecWriteFileCallSites detection
// to a single already-parsed file and returns its violations.
func scanExecWriteFileFile(fset *token.FileSet, file *ast.File, forwarders map[execWriteForwarderKey]int) []string {
	var violations []string

	// Direct and locally-traced mode violations, plus write-then-chmod,
	// are scoped per top-level function so the chmod pattern only matches
	// a write in the SAME function.
	ast.Inspect(file, func(node ast.Node) bool {
		funcDecl, ok := node.(*ast.FuncDecl)
		if !ok || funcDecl.Body == nil {
			return true
		}
		locals := collectExecWriteFileLocals(funcDecl.Body)
		writtenPaths := map[string]bool{}
		sawWriteFileCall := false
		ast.Inspect(funcDecl.Body, func(inner ast.Node) bool {
			call, ok := inner.(*ast.CallExpr)
			if !ok {
				return true
			}
			if modeArg, _ := execWriteFileModeArg(call); modeArg != nil {
				if mode, ok := resolveExecWriteFileMode(modeArg, locals); ok && isExecWriteFileExecutableMode(mode) {
					violations = append(violations, fmt.Sprintf(
						"%s: executable-mode write with mode 0o%s", fset.Position(call.Pos()), strconv.FormatInt(mode, 8)))
				}
			}
			if writePath := execWriteFileWritePath(call); writePath != nil {
				writtenPaths[types.ExprString(writePath)] = true
				sawWriteFileCall = true
			}
			if chmodTarget, chmodMode, hasPath := execWriteFileChmodCall(call); chmodTarget != nil {
				mode, resolved := resolveExecWriteFileMode(chmodMode, locals)
				if !resolved || !isExecWriteFileExecutableMode(mode) {
					return true
				}
				// Fchmod takes a file descriptor, not a path, so it cannot be
				// compared against a written path; flag it whenever this
				// function also performed a raw write at all, since a
				// same-function write followed by an Fchmod-to-executable is
				// the pattern this scan exists to catch (task-21, #739).
				matches := writtenPaths[types.ExprString(chmodTarget)]
				if !hasPath {
					matches = sawWriteFileCall
				}
				if matches {
					violations = append(violations, fmt.Sprintf(
						"%s: write followed by chmod to executable mode 0o%s", fset.Position(call.Pos()), strconv.FormatInt(mode, 8)))
				}
			}
			return true
		})
		return true
	})

	// Forwarder call sites: a call to a function this scan proved forwards
	// one of its own parameters straight into a raw write's mode argument,
	// supplied here with an executable-literal argument.
	if len(forwarders) > 0 {
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			funcDecl := findExecWriteFuncDecl(file, ident.Name)
			if funcDecl == nil {
				return true
			}
			paramNames := execWriteFileParamNames(funcDecl)
			for paramIndex, paramName := range paramNames {
				key := execWriteForwarderKey{funcName: ident.Name, paramName: paramName}
				if _, isForwarder := forwarders[key]; !isForwarder {
					continue
				}
				if paramIndex >= len(call.Args) {
					continue
				}
				if mode, ok := resolveExecWriteFileMode(call.Args[paramIndex], nil); ok && isExecWriteFileExecutableMode(mode) {
					violations = append(violations, fmt.Sprintf(
						"%s: call to %s forwards executable mode 0o%s into a raw write",
						fset.Position(call.Pos()), ident.Name, strconv.FormatInt(mode, 8)))
				}
			}
			return true
		})
	}

	return violations
}

// findExecWriteFuncDecl looks up a top-level function declaration by name
// within file.
func findExecWriteFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		if funcDecl, ok := decl.(*ast.FuncDecl); ok && funcDecl.Recv == nil && funcDecl.Name.Name == name {
			return funcDecl
		}
	}
	return nil
}

// execWriteFileParamNames returns funcDecl's parameter names in argument
// order (an unnamed parameter contributes an empty string, keeping
// positions aligned with the call's argument list).
func execWriteFileParamNames(funcDecl *ast.FuncDecl) []string {
	var names []string
	for _, field := range funcDecl.Type.Params.List {
		if len(field.Names) == 0 {
			names = append(names, "")
			continue
		}
		for _, name := range field.Names {
			names = append(names, name.Name)
		}
	}
	return names
}
