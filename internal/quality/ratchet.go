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
	StartCol   int
	EndLine    int
	EndCol     int
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
	startLine, startCol, err := parsePosition(rangeFields[0])
	if err != nil {
		return CoverageBlock{}, fmt.Errorf("start position: %w", err)
	}
	endLine, endCol, err := parsePosition(rangeFields[1])
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
		StartCol:   startCol,
		EndLine:    endLine,
		EndCol:     endCol,
		Statements: statements,
		Count:      int(count),
	}, nil
}

func parsePosition(position string) (line, col int, err error) {
	dot := strings.Index(position, ".")
	if dot < 0 {
		return 0, 0, fmt.Errorf("missing line.column")
	}
	line, err = strconv.Atoi(position[:dot])
	if err != nil {
		return 0, 0, err
	}
	col, err = strconv.Atoi(position[dot+1:])
	if err != nil {
		return 0, 0, err
	}
	return line, col, nil
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

// UncoveredBlock names one uncovered statement range in the baseline,
// keyed the same way a Go coverage profile line names it, so a later
// EvaluateRatchet run can tell which of a package's currently-uncovered
// blocks are new against the baseline and which already existed there
// (spec/plans/coverage-to-100/README.md task-3, review item B2: a
// count-only failure must still name file:line).
type UncoveredBlock struct {
	File      string `json:"file"`
	StartLine int    `json:"start_line"`
	StartCol  int    `json:"start_col"`
	EndLine   int    `json:"end_line"`
	EndCol    int    `json:"end_col"`
}

// PackageBaseline is the per-package uncovered-statement baseline the
// ratchet compares against. It has no committed file of its own: go-ci's
// coverage job publishes it as a build artifact on every push to the default
// branch (spec/plans/coverage-to-100/README.md task-3(b)). Packages holds
// each package's uncovered statement count for the rise check;
// UncoveredBlocks holds the exact uncovered statement ranges behind that
// count, so a rise can be attributed to specific newly-uncovered
// statements instead of only reported as a number.
type PackageBaseline struct {
	SchemaVersion   int                         `json:"schema_version"`
	SHA             string                      `json:"sha,omitempty"`
	Packages        map[string]int              `json:"packages"`
	UncoveredBlocks map[string][]UncoveredBlock `json:"uncovered_blocks,omitempty"`
}

// BaselineFromProfile builds a PackageBaseline from a measured coverage
// profile, for publishing as the baseline artifact.
func BaselineFromProfile(blocks []CoverageBlock, modulePath, sha string) PackageBaseline {
	uncoveredBlocks := make(map[string][]UncoveredBlock)
	for _, block := range blocks {
		if block.Count != 0 {
			continue
		}
		pkg := PackageOf(block.File, modulePath)
		uncoveredBlocks[pkg] = append(uncoveredBlocks[pkg], UncoveredBlock{
			File: block.File, StartLine: block.StartLine, StartCol: block.StartCol,
			EndLine: block.EndLine, EndCol: block.EndCol,
		})
	}
	for pkg := range uncoveredBlocks {
		sort.Slice(uncoveredBlocks[pkg], func(i, j int) bool {
			a, b := uncoveredBlocks[pkg][i], uncoveredBlocks[pkg][j]
			if a.File != b.File {
				return a.File < b.File
			}
			if a.StartLine != b.StartLine {
				return a.StartLine < b.StartLine
			}
			return a.StartCol < b.StartCol
		})
	}
	return PackageBaseline{
		SchemaVersion:   baselineSchemaVersion,
		SHA:             sha,
		Packages:        PackageUncoveredCounts(blocks, modulePath),
		UncoveredBlocks: uncoveredBlocks,
	}
}

// baselineSchemaVersion is the only PackageBaseline schema ValidateBaseline
// accepts. It is 2, not 1: schema 1 baselines (published before
// UncoveredBlocks existed) cannot attribute a count rise to specific
// statements, so ValidateBaseline treats them the same as any other
// unusable baseline — fall back to measuring the merge base directly.
const baselineSchemaVersion = 2

// ValidateBaseline reports whether baseline is usable against expectedSHA. A
// wrong schema version, an empty package map, or (when expectedSHA is
// non-empty) a SHA that does not match expectedSHA each make the baseline
// unusable: EvaluateRatchet treats a package missing from Packages as having
// no baseline and passes the count rule for it by design, so a baseline that
// silently lost its contents (an empty `{}` artifact, a schema drift, or one
// published for the wrong commit) would otherwise disable the per-package
// count rule for every package without failing anything
// (spec/plans/coverage-to-100/README.md task-3(b)). Callers must treat a
// non-nil error as "this baseline cannot be trusted", not "no baseline
// available" — the caller falls back to measuring the merge base directly,
// or fails loudly, either way never using the untrusted baseline as-is.
func ValidateBaseline(baseline PackageBaseline, expectedSHA string) error {
	if baseline.SchemaVersion != baselineSchemaVersion {
		return fmt.Errorf("baseline schema_version %d is not the supported %d", baseline.SchemaVersion, baselineSchemaVersion)
	}
	if len(baseline.Packages) == 0 {
		return fmt.Errorf("baseline has no package counts")
	}
	if expectedSHA != "" && baseline.SHA != expectedSHA {
		return fmt.Errorf("baseline sha %q does not match merge base %q", baseline.SHA, expectedSHA)
	}
	return nil
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
	// PackageBaseline holds only an int, a string, and a map[string]int, all
	// directly JSON-marshalable; MarshalIndent fails only on cycles,
	// channels/funcs, or NaN/Inf floats, none of which this type can hold,
	// so its error is discarded rather than kept as an untestable dead
	// branch.
	encoded, _ := json.MarshalIndent(baseline, "", "  ")
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
		// core.quotePath=false and an explicit a/ b/ prefix pair keep file
		// names parseable regardless of the caller's own git config: a
		// quoted path (spaces, non-ASCII) would otherwise come back
		// C-style-escaped, and diff.mnemonicPrefix=true would rename the
		// "b/" prefix this parser strips to "w/" (or "i/"/"c/"/"o/").
		"-c", "core.quotePath=false",
		"-c", "diff.mnemonicPrefix=false",
		"diff", "--merge-base", mergeBase, "-U0", "--color-moved=plain", "--color=always", "--no-ext-diff",
		"--src-prefix=a/", "--dst-prefix=b/",
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

// GitTouchedFiles reports every repository-relative file path a diff touches
// against mergeBase — added, modified, deleted, or renamed — independent of
// whether the diff added any line at all. A pure deletion (for example
// removing a test function) never appears in ChangedLines (it added no
// line), but it must still count as "the PR changed this package"
// (founder decision 2026-09-23, review B1): the caller uses this, not
// ChangedLines, to decide per-package ratchet ownership.
func GitTouchedFiles(ctx context.Context, repoRoot, mergeBase string) (map[string]bool, error) {
	cmd := exec.CommandContext(ctx, "git",
		"-c", "core.quotePath=false",
		// --no-renames: with rename detection on (git's default for `diff`
		// once similarity is high enough), --name-only prints only a
		// rename's destination path, silently dropping the source path —
		// so `git mv a/ext_test.go c/ext_test.go` never marked package a as
		// touched (review-696 blocking #1). --no-renames always lists both
		// the old (now-deleted) and new paths as separate entries.
		"diff", "--merge-base", mergeBase, "--no-renames", "--name-only", "--no-ext-diff")
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git diff --merge-base %s --name-only: %w: %s", mergeBase, err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git diff --merge-base %s --name-only: %w", mergeBase, err)
	}
	files := make(map[string]bool)
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files[line] = true
		}
	}
	return files, nil
}

// FileLineOffsets maps one file's merge-base (old) line numbers to its
// current (new) line numbers, built from that file's own unified-diff
// hunks. EvaluateRatchet uses it (review-696 non-blocking #3) to find the
// current position of a baseline-recorded statement whose file was edited
// elsewhere, instead of treating every uncovered block in a touched file as
// unattributable.
type FileLineOffsets struct {
	hunks []lineOffsetHunk
}

type lineOffsetHunk struct {
	oldStart, oldCount, newStart, newCount int
}

// Map translates a merge-base line to its current line. ok is false when
// oldLine falls inside a hunk's old range: the diff itself touched that
// exact line, so there is no single current line to point to for it — the
// direct changed-line rule (GitChangedLines) already covers whatever
// replaced it.
func (offsets FileLineOffsets) Map(oldLine int) (newLine int, ok bool) {
	offset := 0
	for _, hunk := range offsets.hunks {
		if oldLine < hunk.oldStart {
			break
		}
		if oldLine < hunk.oldStart+hunk.oldCount {
			return 0, false
		}
		offset += hunk.newCount - hunk.oldCount
	}
	return oldLine + offset, true
}

// lineOffsetHunkRegexp matches one unified-diff hunk header's full range on
// both sides, e.g. "@@ -12,3 +13,2 @@ func Foo()" -> (12,3,13,2). A count
// omitted from the header (bare "-12" or "+13") means 1, and an explicit
// ",0" means an empty range (a pure insertion has old count 0; a pure
// deletion has new count 0).
var lineOffsetHunkRegexp = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// GitLineOffsets computes FileLineOffsets for every file a diff against
// mergeBase touches (review-696 non-blocking #3). --no-renames matches
// GitTouchedFiles: a rename is a plain delete-then-add pair, so its old
// path's hunks (an entire "delete everything") never falsely offset the new
// path's line numbers.
func GitLineOffsets(ctx context.Context, repoRoot, mergeBase string) (map[string]FileLineOffsets, error) {
	args := []string{
		"-c", "core.quotePath=false",
		"-c", "diff.mnemonicPrefix=false",
		"diff", "--merge-base", mergeBase, "-U0", "--no-renames", "--no-ext-diff",
		"--src-prefix=a/", "--dst-prefix=b/",
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("git diff --merge-base %s -U0: %w: %s", mergeBase, err, string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git diff --merge-base %s -U0: %w", mergeBase, err)
	}
	return parseLineOffsets(string(output)), nil
}

func parseLineOffsets(raw string) map[string]FileLineOffsets {
	result := make(map[string]FileLineOffsets)
	currentFile := ""
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "):
			currentFile = strings.TrimPrefix(line, "+++ ")
			currentFile = strings.TrimSuffix(currentFile, "\t")
			currentFile = strings.TrimPrefix(currentFile, "b/")
			if currentFile == "/dev/null" {
				currentFile = ""
			}
		case lineOffsetHunkRegexp.MatchString(line):
			if currentFile == "" {
				continue
			}
			match := lineOffsetHunkRegexp.FindStringSubmatch(line)
			entry := result[currentFile]
			entry.hunks = append(entry.hunks, lineOffsetHunk{
				oldStart: atoiOrDefault(match[1], 1),
				oldCount: atoiOrDefault(match[2], 1),
				newStart: atoiOrDefault(match[3], 1),
				newCount: atoiOrDefault(match[4], 1),
			})
			result[currentFile] = entry
		}
	}
	return result
}

// atoiOrDefault parses s as a unified-diff hunk-header count, returning def
// (1, always, at every call site) when s is empty because the header omitted
// it (a count of exactly 1).
func atoiOrDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
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
			// git appends a trailing "\t" to a path containing a space (or
			// other characters unified diff needs to disambiguate), even
			// with core.quotePath=false; stripping it keeps a spaced path a
			// clean key instead of silently exempting the whole file from
			// the ratchet. TestGitChangedLinesHandlesPathsWithSpaces covers
			// this.
			currentFile = strings.TrimSuffix(currentFile, "\t")
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
			// Removed ("-") lines never advance the new-file counter, and no
			// other line kind needs handling under -U0 (no unprefixed context
			// lines), so every other case is a deliberate no-op.
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

// Ratchet finding/warning reasons (review-696 non-blocking #4): a genuinely
// added or changed line and a pre-existing statement whose *coverage*
// changed are different situations and must read differently, since the
// first names something the diff shows and the second does not.
const (
	// ReasonChangedStatementUncovered is a statement the diff itself adds or
	// modifies, found via GitChangedLines.
	ReasonChangedStatementUncovered = "added or changed statement is not covered by a test"
	// ReasonNewlyUncoveredAtBase is a statement the diff does not touch at
	// all, found only because the package's uncovered count rose (review
	// B2): it was covered when baseline was measured and is not now.
	ReasonNewlyUncoveredAtBase = "newly uncovered (was covered at base)"
)

// RatchetFinding names one newly uncovered statement that fails the ratchet.
type RatchetFinding struct {
	File   string
	Line   int
	Reason string
}

// RatchetWarning names one newly uncovered statement in a package the PR did
// not itself change (review B1, founder decision 2026-09-23: "only packages
// the PR changes" are hard-gated on their uncovered count; every other
// package's count rise is reported, never failed on).
type RatchetWarning struct {
	Package string `json:"package"`
	File    string `json:"file"`
	Line    int    `json:"line"`
	Reason  string `json:"reason"`
}

// PackageRatchet is one package's ratchet verdict.
type PackageRatchet struct {
	Package               string
	Uncovered             int
	BaselineUncovered     int
	HasBaseline           bool
	Rose                  bool
	Changed               bool // true when the PR itself touches a file (including _test.go) in this package
	NewlyUncoveredChanged []RatchetFinding
	Pass                  bool
}

// EvaluateRatchet applies the per-change coverage ratchet
// (spec/plans/coverage-to-100/README.md task-3, founder decisions 11 and 12):
// a package the PR changes fails when its uncovered-statement count rises
// against baseline, or when a changed, non-moved line is uncovered anywhere.
// A package the PR does not change only ever warns on a count rise —
// touchedFiles (repository-relative paths, including files the diff only
// deletes lines from, deletes entirely, or a rename's old AND new path)
// decides package ownership; a changed package with no baseline entry at
// all is treated as a baseline of 0 (review-696 blocking #2: the merge-base
// measurement covers every package that existed then, so a missing entry
// means the package did not exist, not that it is exempt).
//
// Attributing a count-only rise to an exact statement (uncoveredBlockKey
// against baseline.UncoveredBlocks) can land on a block inside a file the
// PR itself touched: editing a file shifts every later line's position, so
// the baseline's raw stored position there does not directly compare. Set
// lineOffsets (from GitLineOffsets) maps each baseline block's stored line
// through that file's own diff hunks to its current line first: a current
// block landing on that mapped line is the same pre-existing statement,
// just shifted, not new (review-696 non-blocking #3) — pass nil to skip
// this mapping entirely, which means no touched-file block can be matched
// to a shifted baseline position, so every uncovered block in a touched
// file is reported as attributable (the original, pre-5a7d0df6 behavior,
// before the blanket touched-file skip existed — this is the LEAST
// conservative option, not a conservative one).
func EvaluateRatchet(blocks []CoverageBlock, changed ChangedLines, touchedFiles map[string]bool, lineOffsets map[string]FileLineOffsets, baseline PackageBaseline, modulePath string) ([]PackageRatchet, []RatchetWarning) {
	uncovered := PackageUncoveredCounts(blocks, modulePath)
	findingsByPackage := make(map[string][]RatchetFinding)
	reported := make(map[string]map[string]bool) // pkg -> "file:line" already reported
	report := func(pkg, file string, line int, reason string) {
		key := file + ":" + strconv.Itoa(line)
		if reported[pkg] == nil {
			reported[pkg] = make(map[string]bool)
		}
		if reported[pkg][key] {
			return
		}
		reported[pkg][key] = true
		findingsByPackage[pkg] = append(findingsByPackage[pkg], RatchetFinding{File: file, Line: line, Reason: reason})
	}

	// changedPackages reuses the same file->package-directory mapping
	// ChangedPackages (changed_packages.go) uses to build `wb run
	// --changed`'s package list (task-19 cutover: one computation, not two).
	// It is narrower than the naive "every touched file's directory" used
	// before it: only *.go files decide package ownership here, matching
	// the pre-commit hook's own scope. Dropping a non-Go touched file never
	// changes this ratchet's outcome either way — no CoverageBlock is ever
	// produced for a file that is not Go source, so a directory whose only
	// touched files are non-Go never has anything to report or warn about
	// regardless of whether it counts as "changed".
	changedPackages := goPackageDirsFromFiles(touchedFiles)

	blocksByPackage := make(map[string][]CoverageBlock)
	for _, block := range blocks {
		if block.Count != 0 {
			continue
		}
		pkg := PackageOf(block.File, modulePath)
		blocksByPackage[pkg] = append(blocksByPackage[pkg], block)
		relativeFile := strings.TrimPrefix(block.File, modulePath+"/")
		for line := block.StartLine; line <= block.EndLine; line++ {
			if changed.Contains(relativeFile, line) {
				// Report the first changed, uncovered line inside this
				// block (one finding per uncovered block, as before), not
				// the block's StartLine, and the repo-relative path git
				// diff uses, not the module-qualified import path — so a
				// finding can be matched against `git diff` output and
				// opened directly.
				report(pkg, relativeFile, line, ReasonChangedStatementUncovered)
				break
			}
		}
	}

	baselineBlocks := make(map[string]map[string]bool)
	// shiftedLines[file] holds every CURRENT line a baseline-uncovered block
	// in that same file maps to through the diff's own hunks: a currently
	// uncovered block landing there is that same pre-existing statement,
	// not a new one (review-696 non-blocking #3).
	shiftedLines := make(map[string]map[int]bool)
	for pkg, pkgBlocks := range baseline.UncoveredBlocks {
		set := make(map[string]bool, len(pkgBlocks))
		for _, block := range pkgBlocks {
			set[uncoveredBlockKey(block.File, block.StartLine, block.StartCol, block.EndLine, block.EndCol)] = true
			relativeFile := strings.TrimPrefix(block.File, modulePath+"/")
			if offsets, ok := lineOffsets[relativeFile]; ok {
				if newLine, ok := offsets.Map(block.StartLine); ok {
					if shiftedLines[relativeFile] == nil {
						shiftedLines[relativeFile] = make(map[int]bool)
					}
					shiftedLines[relativeFile][newLine] = true
				}
			}
		}
		baselineBlocks[pkg] = set
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

	var warnings []RatchetWarning
	results := make([]PackageRatchet, 0, len(names))
	for _, pkg := range names {
		baselineCount, hasBaseline := baseline.Packages[pkg]
		count := uncovered[pkg]
		isChangedPkg := changedPackages[pkg]
		if !hasBaseline && isChangedPkg {
			// The merge base was measured for every package that existed
			// then; a changed package absent from it is new, and a new
			// package is held to the same "must not rise" rule as any
			// other changed package, starting from 0 (review-696 blocking
			// #2) — otherwise moving covered code into a brand-new package
			// and dropping its test would pass silently.
			baselineCount, hasBaseline = 0, true
		}
		rose := hasBaseline && count > baselineCount
		if rose {
			// A count-only rise still needs a file:line an author can act
			// on (review item B2): every currently-uncovered block in this
			// package that the baseline did not already record as
			// uncovered is one of the statements behind the rise.
			base := baselineBlocks[pkg]
			for _, block := range blocksByPackage[pkg] {
				relativeFile := strings.TrimPrefix(block.File, modulePath+"/")
				if base[uncoveredBlockKey(block.File, block.StartLine, block.StartCol, block.EndLine, block.EndCol)] {
					continue
				}
				if touchedFiles[relativeFile] && shiftedLines[relativeFile][block.StartLine] {
					continue
				}
				if isChangedPkg {
					report(pkg, relativeFile, block.StartLine, ReasonNewlyUncoveredAtBase)
				} else {
					warnings = append(warnings, RatchetWarning{Package: pkg, File: relativeFile, Line: block.StartLine, Reason: ReasonNewlyUncoveredAtBase})
				}
			}
		}
		findings := append([]RatchetFinding(nil), findingsByPackage[pkg]...)
		sort.Slice(findings, func(i, j int) bool {
			if findings[i].File != findings[j].File {
				return findings[i].File < findings[j].File
			}
			return findings[i].Line < findings[j].Line
		})
		results = append(results, PackageRatchet{
			Package:               pkg,
			Uncovered:             count,
			BaselineUncovered:     baselineCount,
			HasBaseline:           hasBaseline,
			Rose:                  rose,
			Changed:               isChangedPkg,
			NewlyUncoveredChanged: findings,
			Pass:                  (!rose || !isChangedPkg) && len(findings) == 0,
		})
	}
	sort.Slice(warnings, func(i, j int) bool {
		if warnings[i].Package != warnings[j].Package {
			return warnings[i].Package < warnings[j].Package
		}
		if warnings[i].File != warnings[j].File {
			return warnings[i].File < warnings[j].File
		}
		return warnings[i].Line < warnings[j].Line
	})
	return results, warnings
}

// uncoveredBlockKey identifies one uncovered statement range the same way
// EvaluateRatchet and BaselineFromProfile both derive it from a
// CoverageBlock, so the two can be compared for exact identity.
func uncoveredBlockKey(file string, startLine, startCol, endLine, endCol int) string {
	return fmt.Sprintf("%s:%d.%d,%d.%d", file, startLine, startCol, endLine, endCol)
}

// ComputeBaselineAtRef checks out ref into a throwaway git worktree and
// measures its per-package uncovered-statement counts, for the fallback path
// when no published baseline artifact exists yet
// (spec/plans/coverage-to-100/README.md task-3(b)). It is bounded by timeout
// so a missing artifact cannot make every PR pay for an open-ended run.
// options carries the same Retry/CoverageDiagnosticsDir a normal `wb
// coverage` run uses; RepositoryRunOptions is applied to the checked-out
// worktree so the merge base is measured through the identical
// .wb/quality.yaml-aware sharded, retried CoverWithOptions runner the head
// measurement uses (review item 5/non-blocking #1), not a bare `go test`.
func ComputeBaselineAtRef(ctx context.Context, repoRoot, ref string, timeout time.Duration, options RunOptions) (PackageBaseline, error) {
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
	runOptions := options
	runOptions.CoverageProfile = profilePath
	if runOptions.Timeout <= 0 || runOptions.Timeout > timeout {
		runOptions.Timeout = timeout
	}
	repoOptions, err := RepositoryRunOptions(worktreeDir, runOptions)
	if err != nil {
		return PackageBaseline{}, err
	}
	report := CoverWithOptions(ctx, "merge-base", worktreeDir, repoOptions)
	if report.Status == StatusFailed {
		return PackageBaseline{}, refMeasurementError(ctx, ref, timeout, fmt.Errorf("measure coverage at merge base %s: %s", sha, report.Error))
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
