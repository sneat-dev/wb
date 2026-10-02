package quality

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ratchetToleranceConfigPath is the repository-owned list of packages whose
// coverage is known to depend on timing. It is a file of its own, beside
// .wb/quality.yaml and .wb/deadcode-baseline.txt, and not a new key in
// .wb/quality.yaml: that file is decoded with KnownFields(true), so a new key
// there would make every already-installed wb binary refuse the repository's
// lint and merge-validation policy.
const ratchetToleranceConfigPath = ".wb/coverage-ratchet.yaml"

// Hard caps on the tolerance policy. The tolerance is a stopgap for a
// handful of timing-dependent branches (spec/plans/coverage-to-100/README.md
// task-3 (b3)); a repository that needs more than this has a coverage
// problem a tolerance must not hide.
const (
	MaxRatchetToleranceStatements = 5
	MaxRatchetToleranceFunctions  = 5
	MaxRatchetToleranceEntries    = 3
)

// ToleratedFunction is one function or method a tolerance entry names,
// resolved against the head checkout's source.
type ToleratedFunction struct {
	// File is the repository-relative path of the file declaring it.
	File string
	// Name is the declared name, "Func" or "Type.Method".
	Name string
	// StartLine and EndLine bound the declaration in the head file.
	StartLine, EndLine int
}

// RatchetTolerance is one package's explicitly configured allowance for
// statements that are covered or not depending on timing.
type RatchetTolerance struct {
	// Statements is how many untouched statements may be newly uncovered
	// against the merge base before the package fails.
	Statements int
	// Reason is the configured justification, repeated in every warning and
	// report entry that uses the tolerance.
	Reason string
	// Functions are the only declarations a tolerated statement may be in.
	Functions []ToleratedFunction
}

// RatchetTolerances maps a package directory relative to the module root
// (the spelling PackageOf returns, for example "internal/orchestrate") to its
// tolerance. A package without an entry is held to the strict ratchet.
type RatchetTolerances map[string]RatchetTolerance

// ToleratedStatement names one statement EvaluateRatchet let through under a
// package's RatchetTolerance.
type ToleratedStatement struct {
	File     string `yaml:"file" json:"file"`
	Line     int    `yaml:"line" json:"line"`
	Function string `yaml:"function" json:"function"`
	Reason   string `yaml:"reason" json:"reason"`
}

type ratchetToleranceConfig struct {
	Version         int `yaml:"version"`
	TimingTolerance []struct {
		Package    string    `yaml:"package"`
		Statements yaml.Node `yaml:"statements"`
		Functions  []string  `yaml:"functions"`
		Reason     string    `yaml:"reason"`
	} `yaml:"timing_tolerance"`
}

// plainStatementCountRegexp admits only a plain decimal integer: YAML would
// otherwise read 0x5 or 0o5 as 5, and a reviewer should never have to decode
// a number to see how wide a tolerance is.
var plainStatementCountRegexp = regexp.MustCompile(`^[0-9]{1,3}$`)

// LoadRatchetTolerances reads root's .wb/coverage-ratchet.yaml. root must be
// the checkout being judged (the pull request's head), never the merge-base
// checkout: the tolerance is policy of the change under review, and its
// functions are resolved against the source being measured. A missing file
// means no tolerance. Anything malformed fails closed: more than
// MaxRatchetToleranceEntries entries; a package that is not a clean,
// module-relative directory spelled exactly as on disk, or is repeated; a
// statement count that is not a plain decimal integer from 1 to
// MaxRatchetToleranceStatements; no reason; and a functions list that is
// empty, longer than MaxRatchetToleranceFunctions, repeats itself, or names
// a function the head checkout does not declare.
func LoadRatchetTolerances(root string) (RatchetTolerances, error) {
	policyPath := filepath.Join(root, ratchetToleranceConfigPath)
	contents, err := os.ReadFile(policyPath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read coverage ratchet policy %s: %w", policyPath, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var config ratchetToleranceConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode coverage ratchet policy %s: %w", policyPath, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("coverage ratchet policy %s must be exactly one YAML document", policyPath)
	}
	if config.Version != 1 {
		return nil, fmt.Errorf("coverage ratchet policy %s has version %d; want 1", policyPath, config.Version)
	}
	// A null list item ("- ~") decodes to nothing at all, so the typed decode
	// above cannot see it; the raw nodes can.
	var raw struct {
		TimingTolerance []yaml.Node `yaml:"timing_tolerance"`
	}
	_ = yaml.Unmarshal(contents, &raw)
	for index, node := range raw.TimingTolerance {
		if node.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance[%d] must be a mapping with package, statements, functions and reason", policyPath, index)
		}
	}
	if len(config.TimingTolerance) > MaxRatchetToleranceEntries {
		return nil, fmt.Errorf("coverage ratchet policy %s has %d timing_tolerance entries; at most %d are allowed", policyPath, len(config.TimingTolerance), MaxRatchetToleranceEntries)
	}
	tolerances := make(RatchetTolerances, len(config.TimingTolerance))
	for index, entry := range config.TimingTolerance {
		pkg := entry.Package
		if pkg != path.Clean(pkg) || strings.HasPrefix(pkg, "/") || strings.Contains(pkg, "..") || !exactDirectory(root, pkg) {
			return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance[%d] package %q must be a clean directory path relative to the module root, spelled exactly as on disk", policyPath, index, pkg)
		}
		if _, repeated := tolerances[pkg]; repeated {
			return nil, fmt.Errorf("coverage ratchet policy %s repeats timing_tolerance package %q", policyPath, pkg)
		}
		count := entry.Statements
		if count.Kind != yaml.ScalarNode || count.Tag != "!!int" || !plainStatementCountRegexp.MatchString(count.Value) {
			return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance package %q statements must be a plain decimal integer", policyPath, pkg)
		}
		statements, _ := strconv.Atoi(count.Value)
		if statements < 1 || statements > MaxRatchetToleranceStatements {
			return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance package %q statements must be between 1 and %d, got %d", policyPath, pkg, MaxRatchetToleranceStatements, statements)
		}
		reason := strings.TrimSpace(entry.Reason)
		if reason == "" {
			return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance package %q needs a reason", policyPath, pkg)
		}
		if len(entry.Functions) == 0 || len(entry.Functions) > MaxRatchetToleranceFunctions {
			return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance package %q must list between 1 and %d functions, got %d", policyPath, pkg, MaxRatchetToleranceFunctions, len(entry.Functions))
		}
		functions := make([]ToleratedFunction, 0, len(entry.Functions))
		seen := make(map[string]bool, len(entry.Functions))
		for _, spec := range entry.Functions {
			if seen[spec] {
				return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance package %q repeats function %q", policyPath, pkg, spec)
			}
			seen[spec] = true
			function, err := resolveToleratedFunction(root, pkg, spec)
			if err != nil {
				return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance package %q: %w", policyPath, pkg, err)
			}
			functions = append(functions, function)
		}
		tolerances[pkg] = RatchetTolerance{Statements: statements, Reason: reason, Functions: functions}
	}
	return tolerances, nil
}

// exactDirectory reports whether pkg names a real directory under root with
// exactly that spelling. It compares against directory listings and not
// os.Stat, so a case-insensitive file system (macOS) cannot accept
// "internal/Orchestrate" for "internal/orchestrate": PackageOf's spelling
// comes from the coverage profile, and an entry in any other case would
// silently match nothing on Linux CI.
func exactDirectory(root, pkg string) bool {
	if pkg == "." {
		return true
	}
	dir := root
	for _, segment := range strings.Split(pkg, "/") {
		// A listing that cannot be read has no entry to match, which is the
		// same answer as a missing directory.
		entries, _ := os.ReadDir(dir)
		found := false
		for _, entry := range entries {
			if entry.Name() == segment && entry.IsDir() {
				found = true
				break
			}
		}
		if !found {
			return false
		}
		dir = filepath.Join(dir, segment)
	}
	return true
}

// resolveToleratedFunction finds spec ("file.go:Func" or
// "file.go:Type.Method") in root's pkg directory by parsing the file. A name
// is used, and not a line number, because it survives edits elsewhere in the
// file; a function that was renamed or deleted fails here, so a stale entry
// cannot linger.
func resolveToleratedFunction(root, pkg, spec string) (ToleratedFunction, error) {
	file, name, ok := strings.Cut(spec, ":")
	if !ok || name == "" || file != filepath.Base(file) || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
		return ToleratedFunction{}, fmt.Errorf("function %q must be written file.go:Function or file.go:Type.Method, naming a non-test Go file of the package", spec)
	}
	fileSet := token.NewFileSet()
	parsed, err := parser.ParseFile(fileSet, filepath.Join(root, filepath.FromSlash(pkg), file), nil, parser.SkipObjectResolution)
	if err != nil {
		return ToleratedFunction{}, fmt.Errorf("function %q: %w", spec, err)
	}
	for _, declaration := range parsed.Decls {
		function, isFunction := declaration.(*ast.FuncDecl)
		if !isFunction || functionDeclarationName(function) != name {
			continue
		}
		return ToleratedFunction{
			File:      path.Join(pkg, file),
			Name:      name,
			StartLine: fileSet.Position(function.Pos()).Line,
			EndLine:   fileSet.Position(function.End()).Line,
		}, nil
	}
	return ToleratedFunction{}, fmt.Errorf("function %q is not declared in %s", spec, path.Join(pkg, file))
}

// functionDeclarationName is a function's name, or "Type.Method" for a
// method: the receiver's type name without its pointer star or type
// parameters.
func functionDeclarationName(function *ast.FuncDecl) string {
	if function.Recv.NumFields() == 0 {
		return function.Name.Name
	}
	receiver := function.Recv.List[0].Type
	for {
		switch typed := receiver.(type) {
		case *ast.StarExpr:
			receiver = typed.X
			continue
		case *ast.IndexExpr:
			receiver = typed.X
			continue
		case *ast.IndexListExpr:
			receiver = typed.X
			continue
		}
		break
	}
	return types.ExprString(receiver) + "." + function.Name.Name
}

// Added reports whether the diff added line to the file, counting lines git
// would colour as moved. GitChangedLines deliberately leaves moved lines out
// (the moved-code rule), which is right for "must this statement be covered"
// and wrong for "did this change leave this statement alone": text that
// matches a removed line elsewhere is still a statement this change put here.
func (offsets FileLineOffsets) Added(line int) bool {
	for _, hunk := range offsets.hunks {
		if line >= hunk.newStart && line < hunk.newStart+hunk.newCount {
			return true
		}
	}
	return false
}

// toleratedStatements decides whether a changed package's count rise fits
// its tolerance. It returns the statements to report as tolerated, or nil
// when the tolerance does not apply and the rise must fail as usual. The
// tolerance applies only when all of these hold: the package has an entry;
// the uncovered count is at most the baseline plus the entry's statements;
// the rise is attributed to at least one statement; every such statement
// lies wholly inside one of the entry's functions; none of them overlaps a
// line the change added, modified or moved; and together they hold no more
// statements than the entry allows. One statement outside those limits
// withdraws the tolerance for the whole package, so the failure names every
// newly uncovered statement and not just the excess.
func toleratedStatements(tolerance RatchetTolerance, configured bool, count, baselineCount int, newlyUncovered []CoverageBlock, changed ChangedLines, lineOffsets map[string]FileLineOffsets, modulePath string) []ToleratedStatement {
	if !configured || count > baselineCount+tolerance.Statements || len(newlyUncovered) == 0 {
		return nil
	}
	statements := 0
	tolerated := make([]ToleratedStatement, 0, len(newlyUncovered))
	for _, block := range newlyUncovered {
		relativeFile := strings.TrimPrefix(block.File, modulePath+"/")
		function := ""
		for _, candidate := range tolerance.Functions {
			if candidate.File == relativeFile && block.StartLine >= candidate.StartLine && block.EndLine <= candidate.EndLine {
				function = candidate.Name
				break
			}
		}
		if function == "" {
			return nil
		}
		for line := block.StartLine; line <= block.EndLine; line++ {
			if changed.Contains(relativeFile, line) || lineOffsets[relativeFile].Added(line) {
				return nil
			}
		}
		statements += block.Statements
		tolerated = append(tolerated, ToleratedStatement{File: relativeFile, Line: block.StartLine, Function: function, Reason: tolerance.Reason})
	}
	if statements > tolerance.Statements {
		return nil
	}
	return tolerated
}
