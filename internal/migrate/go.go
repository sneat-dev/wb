package migrate

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"path"
	"strconv"
	"strings"
)

// transformGo performs syntax-aware Go changes. It deliberately never matches
// comments or string literals, unlike a source-text substitution.
func transformGo(source []byte, filename string, step Step) ([]byte, bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, source, parser.ParseComments)
	if err != nil {
		return nil, false, fmt.Errorf("parse Go source: %w", err)
	}
	changed := false
	switch step.Kind {
	case "import.replace":
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return nil, false, fmt.Errorf("unquote import: %w", err)
			}
			if importPath == step.From {
				spec.Path.Value = strconv.Quote(step.To)
				changed = true
			}
		}
	case "selector.rewrite":
		aliases := importAliases(file, step.Import)
		if len(aliases) == 0 {
			return source, false, nil
		}
		needsRewrite := false
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			prefix, ok := selector.X.(*ast.Ident)
			if ok && aliases[prefix.Name] {
				_, needsRewrite = step.Rewrites[selector.Sel.Name]
			}
			return !needsRewrite
		})
		if !needsRewrite {
			return source, false, nil
		}
		packageName, importChanged := ensureGoImport(file, fset, step.AddImport, step.AddImportAs)
		changed = importChanged
		ast.Inspect(file, func(node ast.Node) bool {
			selector, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			prefix, ok := selector.X.(*ast.Ident)
			if !ok || !aliases[prefix.Name] {
				return true
			}
			target, ok := step.Rewrites[selector.Sel.Name]
			if !ok {
				return true
			}
			_, member, ok := strings.Cut(target, ".")
			if !ok || member == "" {
				return true
			}
			prefix.Name = packageName
			selector.Sel.Name = member
			changed = true
			return true
		})
	default:
		return nil, false, fmt.Errorf("unsupported Go step %q", step.Kind)
	}
	if !changed {
		return source, false, nil
	}
	var out bytes.Buffer
	if err := format.Node(&out, fset, file); err != nil {
		return nil, false, fmt.Errorf("format Go source: %w", err)
	}
	return out.Bytes(), true, nil
}

func importAliases(file *ast.File, importPath string) map[string]bool {
	aliases := map[string]bool{}
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil || value != importPath {
			continue
		}
		name := path.Base(value)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name != "_" && name != "." {
			aliases[name] = true
		}
	}
	return aliases
}

// ensureGoImport returns the package identifier that is safe to use in this
// file. A migration never assumes that `record` is available: local variables
// and declarations can shadow it, so the runner picks a deterministic alias.
func ensureGoImport(file *ast.File, fset *token.FileSet, importPath, preferred string) (string, bool) {
	for _, spec := range file.Imports {
		value, err := strconv.Unquote(spec.Path.Value)
		if err != nil || value != importPath {
			continue
		}
		currentName := path.Base(value)
		if spec.Name != nil {
			currentName = spec.Name.Name
		}
		name := availableGoIdentifier(file, preferred)
		if name == currentName {
			return name, false
		}
		renameGoImport(file, fset, spec, currentName, name)
		return name, true
	}
	if preferred == "" {
		preferred = path.Base(importPath)
	}
	name := availableGoIdentifier(file, preferred)
	newSpec := &ast.ImportSpec{Path: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(importPath)}}
	if name != path.Base(importPath) {
		newSpec.Name = ast.NewIdent(name)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		if !gen.Lparen.IsValid() {
			// Keep a single declaration and let go/format render it as a group.
			gen.Lparen, gen.Rparen = gen.TokPos, gen.TokPos
		}
		gen.Specs = append(gen.Specs, newSpec)
		file.Imports = append(file.Imports, newSpec)
		return name, true
	}
	decl := &ast.GenDecl{Tok: token.IMPORT, Specs: []ast.Spec{newSpec}}
	file.Decls = append([]ast.Decl{decl}, file.Decls...)
	file.Imports = append(file.Imports, newSpec)
	return name, true
}

// renameGoImport renames only selectors bound to spec's imported package. A
// source file may also have a local variable named record; changing every
// textual "record." occurrence would be incorrect.
func renameGoImport(file *ast.File, fset *token.FileSet, spec *ast.ImportSpec, oldName, newName string) {
	importPath, err := strconv.Unquote(spec.Path.Value)
	if err != nil {
		return
	}
	info := goTypeInfo(fset, file)
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok || ident.Name != oldName {
			return true
		}
		pkgName, ok := info.Uses[ident].(*types.PkgName)
		if ok && pkgName.Imported().Path() == importPath {
			ident.Name = newName
		}
		return true
	})
	if newName == path.Base(importPath) {
		spec.Name = nil
	} else {
		spec.Name = ast.NewIdent(newName)
	}
}

func goTypeInfo(fset *token.FileSet, file *ast.File) *types.Info {
	info := &types.Info{Uses: map[*ast.Ident]types.Object{}}
	config := types.Config{
		Importer: stubImporter{},
		Error:    func(error) {}, // Syntax-aware rewrites tolerate unknown dependencies.
	}
	_, _ = config.Check(file.Name.Name, fset, []*ast.File{file}, info)
	return info
}

// stubImporter gives go/types enough package identity to bind import aliases.
// It intentionally does not load external packages: migration planning must be
// possible before dependencies are upgraded or downloaded.
type stubImporter struct{}

func (stubImporter) Import(importPath string) (*types.Package, error) {
	return types.NewPackage(importPath, path.Base(importPath)), nil
}

func availableGoIdentifier(file *ast.File, preferred string) string {
	used := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		if ident, ok := node.(*ast.Ident); ok && ident.Name != "_" {
			used[ident.Name] = true
		}
		return true
	})
	if !used[preferred] {
		return preferred
	}
	base := preferred
	if base == "record" {
		base = "dalrecord"
	}
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s%d", base, i)
		if !used[candidate] {
			return candidate
		}
	}
}
