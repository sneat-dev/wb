// This file backs TestNoInlineWriteSequencesOutsideFilewrite
// (spec/plans/coverage-to-100 task-9): the mechanical check that a
// temp-file write -> sync -> chmod -> close -> publish (rename or link)
// sequence is implemented in exactly one place, internal/filewrite, and
// nowhere else, with a named, shrinking allow-list for the sites task-9's
// own PR series has not yet migrated.
package quality

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// InlineWriteSequenceExemptions lists "relative/path.go:FuncName" sites
// still allowed to implement their own create/write/sync/chmod/publish
// sequence directly, instead of going through internal/filewrite. Every
// entry is a site task-9's own PR series (spec/plans/coverage-to-100)
// has not migrated yet; the PR that migrates one removes its entry here
// in the same commit. The last cutover PR leaves this map empty, turning
// the guard below into a zero-exception gate.
var InlineWriteSequenceExemptions = map[string]string{
	"internal/worktrees/worklog.go:writeBytesImmutableAt":   "cov-t9-cutover: rename-based immutable publish, not yet migrated to internal/filewrite",
	"internal/worktrees/worklog.go:writeBytesAtomicAt":      "cov-t9-cutover: rename-based atomic write, not yet migrated to internal/filewrite",
	"internal/sessionlaunch/state.go:publishLaunchArtifact": "cov-t9-cutover: link-based immutable publish, not yet migrated to internal/filewrite",
}

// inlineWriteSequenceCreateNames are the Openat/OpenFile-family selector
// names FindInlineWriteSequences treats as "this function opens (and
// possibly creates) a file", the first half of the pattern.
var inlineWriteSequenceCreateNames = map[string]bool{
	"Openat":   true,
	"OpenFile": true,
}

// inlineWriteSequencePublishNames are the selector names that identify
// the second half of the pattern: an fd-relative rename-or-link
// publication of a file the same function just created.
var inlineWriteSequencePublishNames = map[string]bool{
	"Renameat":    true,
	"Renameat2":   true,
	"RenameatxNp": true,
	"Linkat":      true,
}

// InlineWriteSequenceViolation names one function outside
// internal/filewrite whose body both opens-or-creates a file with
// O_CREAT and renames or links a name -- the temp-file write/publish
// pattern spec/plans/coverage-to-100 task-9 consolidates into
// internal/filewrite -- and is not in InlineWriteSequenceExemptions.
type InlineWriteSequenceViolation struct {
	// File is the path relative to root, slash-separated.
	File string
	// Func is the offending top-level function's name.
	Func string
	// Line is the function declaration's line number, for a human
	// reading the failure to jump straight to it.
	Line int
}

func (v InlineWriteSequenceViolation) String() string {
	return fmt.Sprintf("%s:%d: func %s implements its own create/write/publish sequence outside internal/filewrite", v.File, v.Line, v.Func)
}

// FindInlineWriteSequences walks root (a module root, typically
// ParallelGuardModuleRoot's result) and reports every non-test,
// non-generated Go function outside internal/filewrite whose body
// contains both an Openat/OpenFile call whose flags mention O_CREAT and
// a Renameat/Renameat2/RenameatxNp/Linkat call, skipping any file:func
// listed in InlineWriteSequenceExemptions. It is a heuristic, not a full
// syscall interpreter: it flags a function by the two calls it contains
// together, not by proving they operate on the same descriptor, which is
// deliberately conservative -- a false positive is a five-minute allow-
// list entry with a reason; a false negative is a second, undetected
// implementation of a security-sensitive sequence.
func FindInlineWriteSequences(root string) ([]InlineWriteSequenceViolation, error) {
	fset := token.NewFileSet()
	var violations []InlineWriteSequenceViolation
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" || (strings.HasPrefix(base, ".") && base != ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel := relSlashFor(root, path)
		if rel == "internal/filewrite" || strings.HasPrefix(rel, "internal/filewrite/") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", path, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !functionHasInlineWriteSequence(fn.Body) {
				continue
			}
			key := rel + ":" + fn.Name.Name
			if _, exempt := InlineWriteSequenceExemptions[key]; exempt {
				continue
			}
			violations = append(violations, InlineWriteSequenceViolation{
				File: rel,
				Func: fn.Name.Name,
				Line: fset.Position(fn.Pos()).Line,
			})
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("walk %s: %w", root, walkErr)
	}
	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Func < violations[j].Func
	})
	return violations, nil
}

// relSlashFor renders path as a slash-separated path relative to root,
// falling back to path itself when it cannot be made relative (mixed
// absolute/relative inputs, which never occurs through
// FindInlineWriteSequences's own filepath.Walk but is exercised directly
// by a unit test) -- the same fallback packageDirFor uses.
func relSlashFor(root, path string) string {
	rel, relErr := filepath.Rel(root, path)
	if relErr != nil {
		rel = path
	}
	return filepath.ToSlash(rel)
}

// functionHasInlineWriteSequence reports whether body contains both an
// O_CREAT-flagged Openat/OpenFile call and a Renameat/Renameat2/
// RenameatxNp/Linkat call.
func functionHasInlineWriteSequence(body *ast.BlockStmt) bool {
	createsWithOCreat := false
	publishes := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := calleeName(call.Fun)
		if inlineWriteSequenceCreateNames[name] && callArgsMentionOCreat(call.Args) {
			createsWithOCreat = true
		}
		if inlineWriteSequencePublishNames[name] {
			publishes = true
		}
		return true
	})
	return createsWithOCreat && publishes
}

// calleeName returns a call expression's selector or identifier name
// ("Openat" for both unix.Openat(...) and a bare Openat(...)), or "" for
// anything else (a function literal, an indexed call, and so on).
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.Ident:
		return f.Name
	default:
		return ""
	}
}

// callArgsMentionOCreat reports whether any argument expression contains
// an identifier or selector named O_CREAT, covering both a bare O_CREAT
// and package-qualified forms (unix.O_CREAT).
func callArgsMentionOCreat(args []ast.Expr) bool {
	found := false
	for _, arg := range args {
		ast.Inspect(arg, func(n ast.Node) bool {
			switch e := n.(type) {
			case *ast.Ident:
				if e.Name == "O_CREAT" {
					found = true
				}
			case *ast.SelectorExpr:
				if e.Sel.Name == "O_CREAT" {
					found = true
				}
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}
