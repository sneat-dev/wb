package policy

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

// Reference is one dependency edge found in a module: an import in a source
// file, or a requirement in go.mod.
type Reference struct {
	// Import is the imported path, or the required module path for a manifest
	// requirement.
	Import string
	// File is slash-separated and relative to the module directory.
	File string
	// Line is 1-indexed; 0 for a manifest requirement with no useful position.
	Line int
	// Package is the directory the importing file lives in, relative to the
	// module directory. Empty means the module root.
	Package string
	// Scope is source or tests.
	Scope string
	// Manifest marks a go.mod requirement rather than a source import.
	Manifest bool
}

// Module is the lexical evidence gathered from one Go module.
//
// Nothing here is resolved or type-checked: the scan reads import blocks and
// go.mod, and never downloads a dependency. That is deliberate — it means the
// check still reports when the build itself cannot start, which is exactly
// when an architecture boundary is most likely to be under discussion.
type Module struct {
	Path string
	Dir  string

	References []Reference

	// Unparseable lists files that could not be parsed. They are reported
	// rather than swallowed: a file the scanner cannot read is a hole in the
	// check, and a hole that stays quiet is worse than one that does not.
	Unparseable []string
}

// ScanModule reads the module rooted at dir.
func ScanModule(dir string) (Module, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return Module{}, err
	}
	manifestPath := filepath.Join(absolute, "go.mod")
	contents, err := os.ReadFile(manifestPath)
	if err != nil {
		return Module{}, fmt.Errorf("read go.mod in %s: %w", absolute, err)
	}
	parsed, err := modfile.Parse("go.mod", contents, nil)
	if err != nil {
		return Module{}, fmt.Errorf("parse go.mod in %s: %w", absolute, err)
	}
	if parsed.Module == nil || parsed.Module.Mod.Path == "" {
		return Module{}, fmt.Errorf("%s/go.mod declares no module path", absolute)
	}

	module := Module{Path: parsed.Module.Mod.Path, Dir: absolute}

	fileSet := token.NewFileSet()
	walkErr := filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path == absolute {
				return nil
			}
			if skipDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".go") {
			return nil
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		parsedFile, err := parser.ParseFile(fileSet, path, nil, parser.ImportsOnly)
		if err != nil {
			module.Unparseable = append(module.Unparseable, relative)
			return nil
		}
		scope := ScopeSource
		switch {
		case strings.HasSuffix(entry.Name(), "_test.go"):
			scope = ScopeTests
		case parsedFile.Name != nil && parsedFile.Name.Name == "main":
			scope = ScopeMain
		}
		packageDir := filepath.ToSlash(filepath.Dir(relative))
		if packageDir == "." {
			packageDir = ""
		}
		for _, spec := range parsedFile.Imports {
			if spec.Path == nil {
				continue
			}
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				continue
			}
			module.References = append(module.References, Reference{
				Import:  importPath,
				File:    relative,
				Line:    fileSet.Position(spec.Path.Pos()).Line,
				Package: packageDir,
				Scope:   scope,
			})
		}
		return nil
	})
	if walkErr != nil {
		return Module{}, walkErr
	}

	// Manifest requirements are attributed to the scope they are actually used
	// in. A module required directly but imported only from _test.go is a test
	// dependency, and holding it to production rules would report a violation
	// the repository cannot act on without deleting a legitimate test.
	usage := map[string]string{}
	for _, reference := range module.References {
		for _, requirement := range parsed.Require {
			if requirement.Indirect || !coversModule(requirement.Mod.Path, reference.Import) {
				continue
			}
			usage[requirement.Mod.Path] = widerScope(usage[requirement.Mod.Path], reference.Scope)
		}
	}
	for _, requirement := range parsed.Require {
		// An indirect requirement is in go.mod because something else needed
		// it, not because this module chose it. Holding a repository to a rule
		// it cannot satisfy without rewriting its dependencies' dependencies
		// would make the check unactionable, so only direct requirements count.
		if requirement.Indirect {
			continue
		}
		line := 0
		if requirement.Syntax != nil {
			line = requirement.Syntax.Start.Line
		}
		scope := usage[requirement.Mod.Path]
		if scope == "" {
			// Required but imported nowhere. Judge it by production rules:
			// a stale requirement should be reported, not excused.
			scope = ScopeSource
		}
		module.References = append(module.References, Reference{
			Import:   requirement.Mod.Path,
			File:     "go.mod",
			Line:     line,
			Scope:    scope,
			Manifest: true,
		})
	}

	sort.Slice(module.References, func(i, j int) bool {
		left, right := module.References[i], module.References[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.Line != right.Line {
			return left.Line < right.Line
		}
		return left.Import < right.Import
	})
	sort.Strings(module.Unparseable)
	return module, nil
}

// skipDirectory mirrors what the go command itself ignores, so the scan sees
// the same tree the compiler would.
func skipDirectory(name string) bool {
	switch name {
	case "vendor", "node_modules", "testdata":
		return true
	}
	return strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// coversModule reports whether an import path belongs to a required module.
func coversModule(modulePath, importPath string) bool {
	return importPath == modulePath || strings.HasPrefix(importPath, modulePath+"/")
}

// widerScope keeps the strictest scope a dependency is used in, so a module
// used in both production and test code is judged by production rules.
func widerScope(current, candidate string) string {
	rank := map[string]int{ScopeTests: 1, ScopeMain: 2, ScopeSource: 3}
	if rank[candidate] > rank[current] {
		return candidate
	}
	return current
}
