package quality

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CoverageBlock is one statement-range entry from a Go coverage profile
// (`go test -coverprofile`). Count is the number of times the profiled binary
// executed every statement in the block; Count == 0 means the block is
// uncovered.
type CoverageBlock struct {
	File       string
	StartLine  int
	EndLine    int
	Statements int
	Count      int
}

// ParseCoverageProfile reads a Go coverage profile (`mode: ...` header
// followed by `file:startLine.startCol,endLine.endCol numStmt count` rows)
// into its constituent blocks, preserving line ranges that profileTotals
// collapses into a single aggregate.
func ParseCoverageProfile(profilePath string) ([]CoverageBlock, error) {
	file, err := os.Open(profilePath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var blocks []CoverageBlock
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		lineNumber++
		if line == "" {
			continue
		}
		if lineNumber == 1 && strings.HasPrefix(line, "mode: ") {
			continue
		}
		block, err := parseCoverageProfileLine(line)
		if err != nil {
			return nil, fmt.Errorf("invalid coverage profile %s at line %d: %w", profilePath, lineNumber, err)
		}
		blocks = append(blocks, block)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return blocks, nil
}

func parseCoverageProfileLine(line string) (CoverageBlock, error) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return CoverageBlock{}, fmt.Errorf("expected 3 fields, got %d", len(fields))
	}
	colon := strings.LastIndex(fields[0], ":")
	if colon < 0 {
		return CoverageBlock{}, fmt.Errorf("missing file:range separator")
	}
	fileName := fields[0][:colon]
	rangePart := fields[0][colon+1:]
	rangeFields := strings.SplitN(rangePart, ",", 2)
	if len(rangeFields) != 2 {
		return CoverageBlock{}, fmt.Errorf("missing start,end range")
	}
	startLine, err := parsePosition(rangeFields[0])
	if err != nil {
		return CoverageBlock{}, fmt.Errorf("start position: %w", err)
	}
	endLine, err := parsePosition(rangeFields[1])
	if err != nil {
		return CoverageBlock{}, fmt.Errorf("end position: %w", err)
	}
	statements, err := strconv.Atoi(fields[1])
	if err != nil {
		return CoverageBlock{}, fmt.Errorf("statement count: %w", err)
	}
	count, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return CoverageBlock{}, fmt.Errorf("hit count: %w", err)
	}
	return CoverageBlock{
		File:       fileName,
		StartLine:  startLine,
		EndLine:    endLine,
		Statements: statements,
		Count:      int(count),
	}, nil
}

func parsePosition(position string) (line int, err error) {
	dot := strings.Index(position, ".")
	if dot < 0 {
		return 0, fmt.Errorf("missing line.column")
	}
	return strconv.Atoi(position[:dot])
}

// PackageOf maps a coverage profile's module-qualified file path (for example
// "github.com/sneat-dev/wb/internal/quality/ratchet.go") to the package
// directory relative to the module root ("internal/quality"). modulePath is
// the module declaration from go.mod (for example
// "github.com/sneat-dev/wb"); the module root package itself maps to ".".
func PackageOf(file, modulePath string) string {
	trimmed := strings.TrimPrefix(file, modulePath+"/")
	trimmed = path.Dir(trimmed)
	if trimmed == "." || trimmed == "" {
		return "."
	}
	return trimmed
}

// PackageUncoveredCounts sums the uncovered statement count per package.
func PackageUncoveredCounts(blocks []CoverageBlock, modulePath string) map[string]int {
	counts := make(map[string]int)
	for _, block := range blocks {
		pkg := PackageOf(block.File, modulePath)
		if block.Count == 0 {
			counts[pkg] += block.Statements
		} else if _, ok := counts[pkg]; !ok {
			counts[pkg] = 0
		}
	}
	return counts
}

// PackageBaseline is the per-package uncovered-statement-count baseline the
// ratchet compares against. It has no committed file of its own: go-ci's
// coverage job publishes it as a build artifact on every push to the default
// branch (spec/plans/coverage-to-100/README.md task-3(b)).
type PackageBaseline struct {
	SchemaVersion int            `json:"schema_version"`
	SHA           string         `json:"sha,omitempty"`
	Packages      map[string]int `json:"packages"`
}

// BaselineFromProfile builds a PackageBaseline from a measured coverage
// profile, for publishing as the baseline artifact.
func BaselineFromProfile(blocks []CoverageBlock, modulePath, sha string) PackageBaseline {
	return PackageBaseline{
		SchemaVersion: 1,
		SHA:           sha,
		Packages:      PackageUncoveredCounts(blocks, modulePath),
	}
}

// LoadBaseline reads a PackageBaseline written by WriteBaseline. A missing
// file is reported as os.ErrNotExist so callers can distinguish "no baseline
// available yet" from a malformed one.
func LoadBaseline(path string) (PackageBaseline, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return PackageBaseline{}, err
	}
	var baseline PackageBaseline
	if err := json.Unmarshal(contents, &baseline); err != nil {
		return PackageBaseline{}, fmt.Errorf("parse coverage baseline %s: %w", path, err)
	}
	return baseline, nil
}

// WriteBaseline writes baseline as deterministic, indented JSON.
func WriteBaseline(path string, baseline PackageBaseline) error {
	encoded, err := json.MarshalIndent(baseline, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(path, encoded, 0o644)
}

// ChangedLines is the set of new-file line numbers a diff added or modified
// against a merge base, excluding lines git identifies as moved-but-unmodified
// (spec/plans/coverage-to-100/README.md task-3(a)). Keys are file paths
// relative to the repository root, matching `git diff`'s `b/<path>` spelling.
type ChangedLines map[string]map[int]bool

// Contains reports whether line in file was added or changed (not moved).
func (c ChangedLines) Contains(file string, line int) bool {
	lines, ok := c[file]
	if !ok {
		return false
	}
	return lines[line]
}

// add records file:line as changed.
func (c ChangedLines) add(file string, line int) {
	lines, ok := c[file]
	if !ok {
		lines = make(map[int]bool)
		c[file] = lines
	}
	lines[line] = true
}

// sgrCodeRegexp matches one ANSI SGR escape sequence, e.g. "\x1b[1;32m".
var sgrCodeRegexp = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// hunkHeaderRegexp matches a unified-diff hunk header's new-file range, e.g.
// "@@ -12,0 +13,2 @@ func Foo()" -> start=13, count=2.
var hunkHeaderRegexp = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// GitChangedLines computes ChangedLines for repoRoot against mergeBase,
// running `git diff --merge-base <mergeBase> -U0 --color-moved=plain` with
// explicit color assignments so moved lines are distinguishable from plain
// additions regardless of the caller's git config.
func GitChangedLines(ctx context.Context, repoRoot, mergeBase string) (ChangedLines, error) {
	args := []string{
		"-c", "color.diff.new=green",
		"-c", "color.diff.newMoved=cyan",
		"-c", "color.diff.newMovedDim=cyan",
		"-c", "color.diff.newMovedAlternative=cyan",
		"-c", "color.diff.newMovedAlternativeDim=cyan",
		"diff", "--merge-base", mergeBase, "-U0", "--color-moved=plain", "--color=always", "--no-ext-diff",
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git diff --merge-base %s: %w: %s", mergeBase, err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git diff --merge-base %s: %w", mergeBase, err)
	}
	return parseColorMovedDiff(string(output)), nil
}

func parseColorMovedDiff(raw string) ChangedLines {
	changed := make(ChangedLines)
	currentFile := ""
	newLine := 0
	for _, rawLine := range strings.Split(raw, "\n") {
		codes := leadingSGRCodes(rawLine)
		stripped := sgrCodeRegexp.ReplaceAllString(rawLine, "")
		switch {
		case strings.HasPrefix(stripped, "+++ "):
			currentFile = strings.TrimPrefix(stripped, "+++ ")
			currentFile = strings.TrimPrefix(currentFile, "b/")
			if currentFile == "/dev/null" {
				currentFile = ""
			}
			newLine = 0
		case hunkHeaderRegexp.MatchString(stripped):
			match := hunkHeaderRegexp.FindStringSubmatch(stripped)
			start, _ := strconv.Atoi(match[1])
			newLine = start
		case currentFile != "" && strings.HasPrefix(stripped, "+") && !strings.HasPrefix(stripped, "+++"):
			if !containsCode(codes, "36") { // not the moved color
				changed.add(currentFile, newLine)
			}
			newLine++
		case currentFile != "" && strings.HasPrefix(stripped, "-") && !strings.HasPrefix(stripped, "---"):
			// Removed lines never advance the new-file counter.
		}
	}
	return changed
}

// leadingSGRCodes collects every SGR code from escape sequences preceding the
// line's first non-escape rune, so "\x1b[1m\x1b[32m+foo" yields ["1","32"].
func leadingSGRCodes(line string) []string {
	var codes []string
	remaining := line
	for {
		match := sgrCodeRegexp.FindStringIndex(remaining)
		if match == nil || match[0] != 0 {
			break
		}
		body := sgrCodeRegexp.FindStringSubmatch(remaining)[1]
		if body != "" {
			codes = append(codes, strings.Split(body, ";")...)
		}
		remaining = remaining[match[1]:]
	}
	return codes
}

func containsCode(codes []string, want string) bool {
	for _, code := range codes {
		if code == want {
			return true
		}
	}
	return false
}

// RatchetFinding names one newly uncovered, changed statement.
type RatchetFinding struct {
	File string
	Line int
}

// PackageRatchet is one package's ratchet verdict.
type PackageRatchet struct {
	Package               string
	Uncovered             int
	BaselineUncovered     int
	HasBaseline           bool
	Rose                  bool
	NewlyUncoveredChanged []RatchetFinding
	Pass                  bool
}

// EvaluateRatchet applies the per-change coverage ratchet
// (spec/plans/coverage-to-100/README.md task-3): a package fails when its
// uncovered-statement count rises against baseline, or when a changed,
// non-moved line is uncovered.
func EvaluateRatchet(blocks []CoverageBlock, changed ChangedLines, baseline PackageBaseline, modulePath string) []PackageRatchet {
	uncovered := PackageUncoveredCounts(blocks, modulePath)
	findingsByPackage := make(map[string][]RatchetFinding)
	for _, block := range blocks {
		if block.Count != 0 {
			continue
		}
		pkg := PackageOf(block.File, modulePath)
		relativeFile := strings.TrimPrefix(block.File, modulePath+"/")
		for line := block.StartLine; line <= block.EndLine; line++ {
			if changed.Contains(relativeFile, line) {
				findingsByPackage[pkg] = append(findingsByPackage[pkg], RatchetFinding{File: block.File, Line: block.StartLine})
				break
			}
		}
	}

	packages := make(map[string]bool)
	for pkg := range uncovered {
		packages[pkg] = true
	}
	for pkg := range baseline.Packages {
		packages[pkg] = true
	}
	names := make([]string, 0, len(packages))
	for pkg := range packages {
		names = append(names, pkg)
	}
	sort.Strings(names)

	results := make([]PackageRatchet, 0, len(names))
	for _, pkg := range names {
		baselineCount, hasBaseline := baseline.Packages[pkg]
		count := uncovered[pkg]
		findings := append([]RatchetFinding(nil), findingsByPackage[pkg]...)
		sort.Slice(findings, func(i, j int) bool {
			if findings[i].File != findings[j].File {
				return findings[i].File < findings[j].File
			}
			return findings[i].Line < findings[j].Line
		})
		rose := hasBaseline && count > baselineCount
		results = append(results, PackageRatchet{
			Package:               pkg,
			Uncovered:             count,
			BaselineUncovered:     baselineCount,
			HasBaseline:           hasBaseline,
			Rose:                  rose,
			NewlyUncoveredChanged: findings,
			Pass:                  !rose && len(findings) == 0,
		})
	}
	return results
}

// ComputeBaselineAtRef checks out ref into a throwaway git worktree and
// measures its per-package uncovered-statement counts, for the fallback path
// when no published baseline artifact exists yet
// (spec/plans/coverage-to-100/README.md task-3(b)). It is bounded by timeout
// so a missing artifact cannot make every PR pay for an open-ended run.
func ComputeBaselineAtRef(ctx context.Context, repoRoot, ref string, timeout time.Duration) (PackageBaseline, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	sha, err := resolveRefSHA(ctx, repoRoot, ref)
	if err != nil {
		return PackageBaseline{}, refMeasurementError(ctx, ref, timeout, err)
	}
	// The module declaration does not change per-commit for this ratchet's
	// purposes, so it is read once from the working tree rather than requiring
	// every historical ref to carry a readable go.mod in the throwaway
	// worktree below.
	modulePath, err := ReadModulePath(repoRoot)
	if err != nil {
		return PackageBaseline{}, err
	}

	worktreeDir, err := os.MkdirTemp("", "wb-coverage-baseline-*")
	if err != nil {
		return PackageBaseline{}, err
	}
	defer func() { _ = os.RemoveAll(worktreeDir) }()

	addCmd := exec.CommandContext(ctx, "git", "worktree", "add", "--detach", "--force", worktreeDir, sha)
	addCmd.Dir = repoRoot
	if output, err := addCmd.CombinedOutput(); err != nil {
		return PackageBaseline{}, refMeasurementError(ctx, ref, timeout, fmt.Errorf("git worktree add %s: %w: %s", sha, err, string(output)))
	}
	defer func() {
		removeCmd := exec.Command("git", "worktree", "remove", "--force", worktreeDir)
		removeCmd.Dir = repoRoot
		_ = removeCmd.Run()
	}()

	profilePath := filepath.Join(worktreeDir, "wb-coverage-baseline.out")
	testCmd := exec.CommandContext(ctx, "go", "test", "-coverprofile="+profilePath, "./...")
	testCmd.Dir = worktreeDir
	if output, err := testCmd.CombinedOutput(); err != nil {
		return PackageBaseline{}, refMeasurementError(ctx, ref, timeout, fmt.Errorf("go test -coverprofile at merge base %s: %w: %s", sha, err, string(output)))
	}

	blocks, err := ParseCoverageProfile(profilePath)
	if err != nil {
		return PackageBaseline{}, fmt.Errorf("no coverage profile produced measuring merge base %s (a module with no test files produces none): %w", sha, err)
	}
	return BaselineFromProfile(blocks, modulePath, sha), nil
}

// refMeasurementError reports plain as-is unless ctx's own deadline is what
// actually stopped the command, in which case it reports the wall-time
// budget by name instead of the killed subprocess's ambiguous "signal:
// killed" — every failing step in ComputeBaselineAtRef shares this so a
// missing baseline artifact fails loud with one consistent message rather
// than hanging or a per-step guess.
func refMeasurementError(ctx context.Context, ref string, timeout time.Duration, plain error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("measuring merge base %s exceeded its %s wall-time budget", ref, timeout)
	}
	return plain
}

func resolveRefSHA(ctx context.Context, repoRoot, ref string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "rev-parse", ref)
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %w", ref, err)
	}
	return strings.TrimSpace(string(output)), nil
}

func ReadModulePath(moduleRoot string) (string, error) {
	contents, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module ")), nil
		}
	}
	return "", fmt.Errorf("no module declaration in %s/go.mod", moduleRoot)
}
