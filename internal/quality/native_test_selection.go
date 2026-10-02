package quality

import (
	"fmt"
	"go/ast"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// NativeGoTestSelector is shared by coverage execution and the static guard.
const NativeGoTestSelector = "^Test(E2E|Contract)"

var nativeGoTestName = regexp.MustCompile(NativeGoTestSelector)

// The sole helper exception is child-only: its marker gates the body, and
// selected parents explicitly re-execute os.Args[0] with its literal selector.
const nativeHelperFile = "internal/runner/real_combined_e2e_test.go"
const nativeHelperName = "TestCombinedCaptureHelperProcess"
const nativeHelperMarker = "WB_RUNNER_COMBINED_HELPER"
const nativeHelperSelector = "-test.run=^TestCombinedCaptureHelperProcess$"

// FindNativeTestSelectionProblems rejects silently unselected native tests.
// There are no pending counts or file-wide helper allowances.
func FindNativeTestSelectionProblems(root string) ([]string, error) {
	var problems []string
	helperLive, markerLive, callerLive := false, false, false
	err := walkGoTestFiles(root, func(fset *token.FileSet, file *ast.File, path string) {
		if !fileRequiresE2ETag(file) {
			return
		}
		osName := testImportName(file, "os")
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || !isNativeTestFunction(fn) {
				continue
			}
			if path == nativeHelperFile && fn.Name.Name == nativeHelperName {
				helperLive = true
				markerLive = hasNativeHelperMarker(fn, osName)
				continue
			}
			if !nativeGoTestName.MatchString(fn.Name.Name) {
				problems = append(problems, fmt.Sprintf("%s:%d: %s is not selected by %s", path, fset.Position(fn.Pos()).Line, fn.Name.Name, NativeGoTestSelector))
			} else if path == nativeHelperFile && hasNativeHelperCaller(fn, osName) {
				callerLive = true
			}
		}
	})
	if err != nil {
		return nil, err
	}
	if !helperLive || !markerLive || !callerLive {
		problems = append(problems, fmt.Sprintf("%s: helper exception %s is stale: declaration=%t marker=%t selected self-reexec caller=%t", nativeHelperFile, nativeHelperName, helperLive, markerLive, callerLive))
	}
	sort.Strings(problems)
	return problems, nil
}

func testImportName(file *ast.File, path string) string {
	for _, spec := range file.Imports {
		value, _ := strconv.Unquote(spec.Path.Value)
		if value != path {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return path
	}
	return ""
}

// Match cmd/go's syntactic *T or *anything.T admission; the compiler checks
// type identity, including aliases declared in another file.
func isNativeTestFunction(fn *ast.FuncDecl) bool {
	name := fn.Name.Name
	if fn.Recv != nil || name == "TestMain" || !strings.HasPrefix(name, "Test") || fn.Type.TypeParams != nil || (fn.Type.Results != nil && len(fn.Type.Results.List) != 0) || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
		return false
	}
	if len(name) > 4 {
		first, _ := utf8.DecodeRuneInString(name[4:])
		if unicode.IsLower(first) {
			return false
		}
	}
	parameter := fn.Type.Params.List[0]
	if len(parameter.Names) > 1 {
		return false
	}
	pointer, ok := parameter.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	if identifier, ok := pointer.X.(*ast.Ident); ok {
		return identifier.Name == "T"
	}
	selector, ok := pointer.X.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == "T"
}

func hasNativeHelperMarker(fn *ast.FuncDecl, osName string) bool {
	found := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Getenv" {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || receiver.Name != osName || osName == "" {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if ok && literal.Kind == token.STRING {
			value, _ := strconv.Unquote(literal.Value)
			found = found || value == nativeHelperMarker
		}
		return true
	})
	return found
}

func hasNativeHelperCaller(fn *ast.FuncDecl, osName string) bool {
	found := false
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		self, selector := false, false
		for _, arg := range call.Args {
			if literal, ok := arg.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				value, _ := strconv.Unquote(literal.Value)
				selector = selector || value == nativeHelperSelector
			}
			if index, ok := arg.(*ast.IndexExpr); ok {
				field, ok := index.X.(*ast.SelectorExpr)
				if !ok || field.Sel.Name != "Args" {
					continue
				}
				receiver, ok := field.X.(*ast.Ident)
				if !ok || receiver.Name != osName || osName == "" {
					continue
				}
				zero, ok := index.Index.(*ast.BasicLit)
				self = self || (ok && zero.Kind == token.INT && zero.Value == "0")
			}
		}
		found = found || (self && selector)
		return true
	})
	return found
}
