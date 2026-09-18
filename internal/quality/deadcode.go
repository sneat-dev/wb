package quality

// Reachability analysis: prove that every mechanism in a repository has a
// caller on a path that actually runs.
//
// A feature that is absent is loud — nothing compiles, a test fails. A feature
// that is present but unreachable is silent: it can be added without ever
// being wired in, or quietly unwired later, and every other signal stays green.
// Tests do not catch it, because a test calls the mechanism directly and so is
// itself the only caller.
//
// golang.org/x/tools/cmd/deadcode answers exactly this question. It builds a
// call graph from each main package using Rapid Type Analysis and reports the
// functions no path from main can reach. RTA over-approximates reachability —
// for a dynamic call it assumes every method of every type instantiated on a
// reachable path may be called — so a function it reports as unreachable is
// genuinely unreachable, modulo reflection and //go:linkname. That direction of
// error is what makes it safe to gate on: false negatives cost coverage, never
// a broken build.
//
// Real repositories start with existing unreachable code, so a gate that
// demanded zero findings could never be switched on. The baseline is the
// ratchet: today's findings are recorded and tolerated, and only findings
// absent from it fail. Deleting dead code shrinks the baseline and can never
// fail the gate.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultDeadcodeBaseline is the repository-relative baseline path. It sits
// beside .wb/quality.yaml because it is repository-owned policy, reviewed in
// the pull request that changes it, not machine state.
const DefaultDeadcodeBaseline = ".wb/deadcode-baseline.txt"

// DefaultDeadcodeTool pins the analyzer the way .wb/quality.yaml pins
// golangci-lint: an unpinned analyzer silently changes the gate's verdict
// between runs, which is the one thing a ratchet must never do.
var DefaultDeadcodeTool = []string{"go", "run", "golang.org/x/tools/cmd/deadcode@v0.50.0"}

// DeadcodeOptions configures one reachability run.
type DeadcodeOptions struct {
	// Patterns are the main packages to analyze. deadcode only starts from
	// executables, so a pattern matching no main package reports nothing.
	Patterns []string
	// BaselinePath is relative to the repository root when not absolute.
	BaselinePath string
	// Tool overrides the analyzer invocation; nil uses DefaultDeadcodeTool.
	Tool []string
	// Filter is deadcode's -filter regular expression. Empty keeps deadcode's
	// own default, which reports the module of the first listed package.
	Filter string
	// IncludeGenerated reports dead functions in generated files too. Off by
	// default: generated code is not hand-wired, so its reachability is the
	// generator's contract, not this repository's.
	IncludeGenerated bool
	// Timeout bounds the analyzer. Zero disables the bound.
	Timeout time.Duration
}

// DeadcodeFinding is one unreachable function.
type DeadcodeFinding struct {
	// Identity is the baseline key: import path + "." + function name. It
	// deliberately excludes the source position, so moving a function or
	// editing the lines above it does not invalidate the baseline and does not
	// silently re-admit a genuinely new finding.
	Identity string `yaml:"identity" json:"identity"`
	Package  string `yaml:"package" json:"package"`
	Function string `yaml:"function" json:"function"`
	File     string `yaml:"file,omitempty" json:"file,omitempty"`
	Line     int    `yaml:"line,omitempty" json:"line,omitempty"`
}

// DeadcodeReport is the verdict of one run.
type DeadcodeReport struct {
	// Findings is every unreachable function found, baselined or not.
	Findings []DeadcodeFinding `yaml:"findings" json:"findings"`
	// New is the gate: findings absent from the baseline. Non-empty fails.
	New []DeadcodeFinding `yaml:"new,omitempty" json:"new,omitempty"`
	// Fixed lists baseline entries that are now reachable or gone. They never
	// fail the gate; they are what the baseline should shed.
	Fixed []string `yaml:"fixed,omitempty" json:"fixed,omitempty"`
	// BaselinePath is the file consulted, empty when none was configured.
	BaselinePath string `yaml:"baseline_path,omitempty" json:"baseline_path,omitempty"`
	// BaselineMissing distinguishes "no baseline file yet" from "empty
	// baseline". The first is a repository that has not adopted the gate; the
	// second is a repository that has adopted it and is clean.
	BaselineMissing bool `yaml:"baseline_missing,omitempty" json:"baseline_missing,omitempty"`
}

// deadcodePackage mirrors the -json records emitted by x/tools' deadcode.
type deadcodePackage struct {
	Name  string         `json:"Name"`
	Path  string         `json:"Path"`
	Funcs []deadcodeFunc `json:"Funcs"`
}

type deadcodeFunc struct {
	Name      string `json:"Name"`
	Generated bool   `json:"Generated"`
	Position  struct {
		File string `json:"File"`
		Line int    `json:"Line"`
		Col  int    `json:"Col"`
	} `json:"Position"`
}

// Deadcode runs the reachability analysis in repositoryPath and compares it
// against the configured baseline.
func Deadcode(ctx context.Context, repositoryPath string, options DeadcodeOptions) (DeadcodeReport, error) {
	patterns := options.Patterns
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	tool := options.Tool
	if len(tool) == 0 {
		tool = DefaultDeadcodeTool
	}

	arguments := append([]string(nil), tool[1:]...)
	arguments = append(arguments, "-json")
	if options.Filter != "" {
		arguments = append(arguments, "-filter", options.Filter)
	}
	if options.IncludeGenerated {
		arguments = append(arguments, "-generated")
	}
	arguments = append(arguments, patterns...)

	runContext := ctx
	if options.Timeout > 0 {
		var cancel context.CancelFunc
		runContext, cancel = context.WithTimeout(ctx, options.Timeout)
		defer cancel()
	}

	command := exec.CommandContext(runContext, tool[0], arguments...)
	command.Dir = repositoryPath
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		// deadcode exits 0 even when it reports findings, so a non-zero exit
		// is a real failure — a build error, a missing module, a timeout. It
		// must fail the gate rather than be read as "nothing is dead".
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if runContext.Err() != nil {
			return DeadcodeReport{}, fmt.Errorf("deadcode analysis in %s: %w: %s", repositoryPath, runContext.Err(), detail)
		}
		return DeadcodeReport{}, fmt.Errorf("deadcode analysis in %s: %w: %s", repositoryPath, err, detail)
	}

	findings, err := parseDeadcodeOutput(stdout.Bytes())
	if err != nil {
		return DeadcodeReport{}, fmt.Errorf("deadcode analysis in %s: %w", repositoryPath, err)
	}

	report := DeadcodeReport{Findings: findings}
	baselinePath := options.BaselinePath
	if baselinePath == "" {
		return report, nil
	}
	resolved := baselinePath
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(repositoryPath, resolved)
	}
	report.BaselinePath = baselinePath

	baseline, missing, err := LoadDeadcodeBaseline(resolved)
	if err != nil {
		return DeadcodeReport{}, err
	}
	report.BaselineMissing = missing

	seen := make(map[string]bool, len(findings))
	for _, finding := range findings {
		seen[finding.Identity] = true
		if !baseline[finding.Identity] {
			report.New = append(report.New, finding)
		}
	}
	for identity := range baseline {
		if !seen[identity] {
			report.Fixed = append(report.Fixed, identity)
		}
	}
	sort.Strings(report.Fixed)
	return report, nil
}

// parseDeadcodeOutput flattens deadcode's per-package records into findings
// sorted by identity, so a report and a written baseline are byte-stable
// across runs regardless of the order packages were analyzed in.
func parseDeadcodeOutput(output []byte) ([]DeadcodeFinding, error) {
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var packages []deadcodePackage
	if err := json.Unmarshal(trimmed, &packages); err != nil {
		return nil, fmt.Errorf("parse deadcode JSON: %w", err)
	}
	var findings []DeadcodeFinding
	for _, pkg := range packages {
		for _, function := range pkg.Funcs {
			findings = append(findings, DeadcodeFinding{
				Identity: pkg.Path + "." + function.Name,
				Package:  pkg.Path,
				Function: function.Name,
				File:     function.Position.File,
				Line:     function.Position.Line,
			})
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Identity < findings[j].Identity })
	return findings, nil
}

// LoadDeadcodeBaseline reads a baseline file. A missing file is not an error:
// it reports every finding as new, which is what a repository adopting the
// gate should see before it records its starting point.
func LoadDeadcodeBaseline(path string) (entries map[string]bool, missing bool, err error) {
	content, err := os.ReadFile(path) // #nosec G304 -- repository-owned policy path chosen by the caller.
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, true, nil
		}
		return nil, false, fmt.Errorf("read deadcode baseline %s: %w", path, err)
	}
	entries = map[string]bool{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entries[line] = true
	}
	return entries, false, nil
}

// FormatDeadcodeBaseline renders findings as a baseline file. Entries are
// sorted and one per line so that a diff of this file reviews as a list of
// mechanisms that lost or gained a caller.
func FormatDeadcodeBaseline(findings []DeadcodeFinding) string {
	var builder strings.Builder
	builder.WriteString("# wb deadcode baseline — functions unreachable from any main package.\n")
	builder.WriteString("#\n")
	builder.WriteString("# Each line is an import path plus a function name. wb deadcode fails only\n")
	builder.WriteString("# on findings absent from this file, so this list may only shrink without\n")
	builder.WriteString("# review: deleting dead code or wiring it up removes its line.\n")
	builder.WriteString("#\n")
	builder.WriteString("# Regenerate with: wb deadcode --update-baseline\n")
	identities := make([]string, 0, len(findings))
	for _, finding := range findings {
		identities = append(identities, finding.Identity)
	}
	sort.Strings(identities)
	previous := ""
	for _, identity := range identities {
		if identity == previous {
			continue
		}
		builder.WriteString(identity)
		builder.WriteString("\n")
		previous = identity
	}
	return builder.String()
}

// WriteDeadcodeBaseline records findings as the new tolerated set.
func WriteDeadcodeBaseline(path string, findings []DeadcodeFinding) error {
	if directory := filepath.Dir(path); directory != "" && directory != "." {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return fmt.Errorf("create deadcode baseline directory %s: %w", directory, err)
		}
	}
	if err := os.WriteFile(path, []byte(FormatDeadcodeBaseline(findings)), 0o644); err != nil { // #nosec G306 -- reviewed repository policy, not a secret.
		return fmt.Errorf("write deadcode baseline %s: %w", path, err)
	}
	return nil
}
