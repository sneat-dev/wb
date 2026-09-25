// This file backs task-25 (spec/plans/coverage-to-100/README.md, "Coverage
// worklists"): it turns a measured Go coverage profile into the deterministic
// unit list a coverage-to-100 lane's brief carries instead of exploration.
// The coordinator regenerates the list from the latest cov/integration
// profile before cutting new lane briefs, so the same profile must always
// produce the same units in the same order.
package quality

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
)

// WorklistBlock is one uncovered statement range from a coverage profile,
// attributed to its enclosing top-level function declaration. Function is
// empty when no top-level func/method declaration in the file contains the
// block (for example a package-level var initializer that calls a
// function): the block still gets its own singleton group, keyed by file.
type WorklistBlock struct {
	File       string `json:"file"`
	StartLine  int    `json:"start_line"`
	StartCol   int    `json:"start_col"`
	EndLine    int    `json:"end_line"`
	EndCol     int    `json:"end_col"`
	Statements int    `json:"statements"`
	Function   string `json:"function"`
}

// WorklistUnit is one group of uncovered blocks, sized to about the
// unitSize BuildWorklist was called with, that keeps whole functions (and,
// where the running total allows it, whole files) together. SharesFileWith
// names every other unit that also holds a block from one of Files, sorted
// and deduplicated, so the coordinator never schedules two lanes that would
// edit the same file at the same time.
type WorklistUnit struct {
	Index          int             `json:"index"`
	Statements     int             `json:"statements"`
	Files          []string        `json:"files"`
	Blocks         []WorklistBlock `json:"blocks"`
	SharesFileWith []int           `json:"shares_file_with,omitempty"`
}

// Worklist is the deterministic grouping BuildWorklist produces from one
// coverage profile.
type Worklist struct {
	UnitSize                 int            `json:"unit_size"`
	TotalUncoveredStatements int            `json:"total_uncovered_statements"`
	Units                    []WorklistUnit `json:"units"`
}

// BuildWorklist reads every uncovered block in blocks (Count == 0), maps
// each one to its enclosing top-level function by parsing the module source
// under moduleRoot with go/ast, and groups them into units of about
// unitSize statements: a function's blocks are never split across units,
// and consecutive functions from the same file stay in the same unit while
// the running total allows it. The result is deterministic for a given
// profile and module tree: same input, same units, same order, every
// uncovered block in exactly one unit.
func BuildWorklist(blocks []CoverageBlock, modulePath, moduleRoot string, unitSize int) (Worklist, error) {
	if unitSize < 1 {
		return Worklist{}, fmt.Errorf("unit size must be at least 1 statement, got %d", unitSize)
	}

	uncovered := make([]CoverageBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.Count == 0 {
			uncovered = append(uncovered, block)
		}
	}
	sort.Slice(uncovered, func(i, j int) bool { return blockSortsBefore(uncovered[i], uncovered[j]) })

	worklist := Worklist{UnitSize: unitSize}
	if len(uncovered) == 0 {
		return worklist, nil
	}

	functionRangesByFile := map[string][]funcRange{}
	worklistBlocks := make([]WorklistBlock, 0, len(uncovered))
	for _, block := range uncovered {
		ranges, ok := functionRangesByFile[block.File]
		if !ok {
			var err error
			ranges, err = parseFunctionRanges(sourceFilePath(block.File, modulePath, moduleRoot))
			if err != nil {
				return Worklist{}, fmt.Errorf("map %s to its enclosing function: %w", block.File, err)
			}
			functionRangesByFile[block.File] = ranges
		}
		worklist.TotalUncoveredStatements += block.Statements
		worklistBlocks = append(worklistBlocks, WorklistBlock{
			File:       block.File,
			StartLine:  block.StartLine,
			StartCol:   block.StartCol,
			EndLine:    block.EndLine,
			EndCol:     block.EndCol,
			Statements: block.Statements,
			Function:   enclosingFunction(ranges, block.StartLine, block.EndLine),
		})
	}

	worklist.Units = packWorklistUnits(worklistBlocks, unitSize)
	return worklist, nil
}

// blockSortsBefore orders uncovered blocks by file, then position within the
// file, so BuildWorklist's grouping is deterministic regardless of the
// input profile's own line order (a merged profile need not be sorted).
func blockSortsBefore(a, b CoverageBlock) bool {
	if a.File != b.File {
		return a.File < b.File
	}
	if a.StartLine != b.StartLine {
		return a.StartLine < b.StartLine
	}
	if a.StartCol != b.StartCol {
		return a.StartCol < b.StartCol
	}
	if a.EndLine != b.EndLine {
		return a.EndLine < b.EndLine
	}
	return a.EndCol < b.EndCol
}

// funcGroup accumulates every uncovered block this profile attributes to one
// top-level function declaration (or, when Function is empty, one
// unattributed block run in a file), so packWorklistUnits can place a
// function's blocks in exactly one unit.
type funcGroup struct {
	file       string
	function   string
	statements int
	blocks     []WorklistBlock
}

// packWorklistUnits groups sorted, function-attributed blocks into
// funcGroups (blocks are contiguous per function: Go forbids nested
// top-level func declarations, so a single function's blocks can never be
// separated by another function's blocks once sorted by file then line),
// then greedily bins whole funcGroups into units of about unitSize
// statements, and finally marks every unit that shares a file with another
// unit.
func packWorklistUnits(sortedBlocks []WorklistBlock, unitSize int) []WorklistUnit {
	var groups []funcGroup
	for _, block := range sortedBlocks {
		if n := len(groups); n > 0 && groups[n-1].file == block.File && groups[n-1].function == block.Function {
			groups[n-1].statements += block.Statements
			groups[n-1].blocks = append(groups[n-1].blocks, block)
			continue
		}
		groups = append(groups, funcGroup{file: block.File, function: block.Function, statements: block.Statements, blocks: []WorklistBlock{block}})
	}

	var units []WorklistUnit
	fileUnits := map[string]map[int]bool{}
	var current WorklistUnit
	flush := func() {
		if len(current.Blocks) == 0 {
			return
		}
		current.Index = len(units)
		current.Files = sortedUniqueFiles(current.Blocks)
		for _, file := range current.Files {
			if fileUnits[file] == nil {
				fileUnits[file] = map[int]bool{}
			}
			fileUnits[file][current.Index] = true
		}
		units = append(units, current)
		current = WorklistUnit{}
	}
	for _, group := range groups {
		if current.Statements > 0 && current.Statements+group.statements > unitSize {
			flush()
		}
		current.Blocks = append(current.Blocks, group.blocks...)
		current.Statements += group.statements
	}
	flush()

	for i := range units {
		shared := map[int]bool{}
		for _, file := range units[i].Files {
			for other := range fileUnits[file] {
				if other != i {
					shared[other] = true
				}
			}
		}
		if len(shared) == 0 {
			continue
		}
		var indexes []int
		for other := range shared {
			indexes = append(indexes, other)
		}
		sort.Ints(indexes)
		units[i].SharesFileWith = indexes
	}
	return units
}

func sortedUniqueFiles(blocks []WorklistBlock) []string {
	seen := map[string]bool{}
	var files []string
	for _, block := range blocks {
		if seen[block.File] {
			continue
		}
		seen[block.File] = true
		files = append(files, block.File)
	}
	sort.Strings(files)
	return files
}

// sourceFilePath maps a coverage profile's module-qualified file path back
// to its path on disk under moduleRoot, the inverse of the module-qualified
// naming go test -coverprofile writes.
func sourceFilePath(file, modulePath, moduleRoot string) string {
	relative := strings.TrimPrefix(file, modulePath+"/")
	return filepath.Join(moduleRoot, filepath.FromSlash(relative))
}

// funcRange is one top-level function or method declaration's line range in
// a parsed source file, named the way the enclosing function should read in
// a worklist: "Name" for a plain function, "(*Type).Name" or "Type.Name" for
// a method.
type funcRange struct {
	name      string
	startLine int
	endLine   int
}

// parseFunctionRanges parses sourcePath and returns every top-level
// function/method declaration's name and line range, sorted by start line.
// Go forbids nested top-level func declarations (only func literals nest),
// so these ranges never overlap.
func parseFunctionRanges(sourcePath string) ([]funcRange, error) {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, sourcePath, nil, 0)
	if err != nil {
		return nil, err
	}
	var ranges []funcRange
	for _, decl := range file.Decls {
		funcDecl, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ranges = append(ranges, funcRange{
			name:      funcDeclName(funcDecl),
			startLine: fileSet.Position(funcDecl.Pos()).Line,
			endLine:   fileSet.Position(funcDecl.End()).Line,
		})
	}
	sort.Slice(ranges, func(i, j int) bool { return ranges[i].startLine < ranges[j].startLine })
	return ranges, nil
}

// funcDeclName renders a function declaration's name the way an enclosing
// function should read in a worklist: the bare name for a plain function,
// "(*Type).Name" for a pointer-receiver method, and "Type.Name" for a
// value-receiver method.
func funcDeclName(funcDecl *ast.FuncDecl) string {
	if funcDecl.Recv == nil || len(funcDecl.Recv.List) == 0 {
		return funcDecl.Name.Name
	}
	switch receiver := funcDecl.Recv.List[0].Type.(type) {
	case *ast.StarExpr:
		if ident, ok := receiver.X.(*ast.Ident); ok {
			return "(*" + ident.Name + ")." + funcDecl.Name.Name
		}
	case *ast.Ident:
		return receiver.Name + "." + funcDecl.Name.Name
	}
	return funcDecl.Name.Name
}

// enclosingFunction returns the name of the funcRange that fully contains
// [startLine, endLine], or "" when no top-level function declaration does.
func enclosingFunction(ranges []funcRange, startLine, endLine int) string {
	for _, candidate := range ranges {
		if candidate.startLine <= startLine && endLine <= candidate.endLine {
			return candidate.name
		}
	}
	return ""
}
