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
// The analysis is per platform. deadcode type-checks one GOOS at a time, so a
// Linux-only file (procfs readers, cgroup supervision) is invisible on macOS
// and a Windows-only helper is invisible on Linux: the same tree gave a
// different verdict depending on the host that ran the gate. The gate now
// analyses every supported platform (DefaultDeadcodePlatforms) with a fixed
// GOARCH and CGO setting, whatever host runs it, and calls a function dead
// only when it is unreachable on every one of them. A function reachable on
// any supported platform is in use. A function that exists on one platform
// only cannot be told apart from "absent" on the others in a per-platform
// report, so it is never reported.
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
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
)

// DefaultDeadcodeBaseline is the repository-relative baseline path. It sits
// beside .wb/quality.yaml because it is repository-owned policy, reviewed in
// the pull request that changes it, not machine state.
const DefaultDeadcodeBaseline = ".wb/deadcode-baseline.txt"

// DefaultDeadcodeTool pins the analyzer the way .wb/quality.yaml pins
// golangci-lint: an unpinned analyzer silently changes the gate's verdict
// between runs, which is the one thing a ratchet must never do.
var DefaultDeadcodeTool = []string{"go", "run", deadcodeToolPackage}

// DefaultDeadcodePlatforms is the supported GOOS set the gate analyses. A
// function is reported only when it is unreachable on every entry, so the
// verdict does not depend on which of them the gate happens to run on.
var DefaultDeadcodePlatforms = []string{"linux", "darwin", "windows"}

// deadcodeAnalysisArch fixes GOARCH for every platform run, so the verdict
// does not change between an arm64 laptop and an amd64 runner.
const deadcodeAnalysisArch = "amd64"

// deadcodeToolPackage is the pinned analyzer, installed once per run so the
// per-platform runs execute the host binary with GOOS set for the analysis
// only. `go run` cannot do this: it would build the analyzer itself for the
// target platform and then fail to execute it.
const deadcodeToolPackage = "golang.org/x/tools/cmd/deadcode@v0.50.0"

// DeadcodeOptions configures one reachability run.
type DeadcodeOptions struct {
	// Patterns are the main packages to analyze. deadcode only starts from
	// executables, so a pattern matching no main package reports nothing.
	Patterns []string
	// BaselinePath is relative to the repository root when not absolute.
	BaselinePath string
	// Tool overrides the analyzer invocation. When set, the analyzer runs once
	// per Platforms entry, or once with the host environment when Platforms is
	// empty. When nil, the pinned analyzer is installed and run once per
	// platform; Platforms empty then means DefaultDeadcodePlatforms.
	Tool []string
	// Platforms lists the GOOS values to analyse. A function is reported only
	// when it is unreachable on all of them.
	Platforms []string
	// ToolDirectory is the parent directory the analyzer is installed under;
	// empty means the operating system's temporary directory.
	ToolDirectory string
	// Runner starts the analyzer and the go tool; nil uses the real runner.
	Runner runner.Runner
	// GoCommand is the go tool used to install the pinned analyzer; empty
	// means "go".
	GoCommand string
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
	// Platforms lists the GOOS values analysed; Findings, New and Fixed refer
	// to functions unreachable on all of them. Empty when one unscoped run was
	// made with an explicit analyzer.
	Platforms []string `yaml:"platforms,omitempty" json:"platforms,omitempty"`
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
	run := options.Runner
	if run == nil {
		run = runner.New()
	}
	runContext := ctx
	if options.Timeout > 0 {
		var cancel context.CancelFunc
		runContext, cancel = context.WithTimeout(ctx, options.Timeout)
		defer cancel()
	}

	tool := options.Tool
	platforms := options.Platforms
	if len(tool) == 0 {
		binary, cleanup, err := installDeadcodeTool(runContext, run, repositoryPath, options.GoCommand, options.ToolDirectory)
		if err != nil {
			return DeadcodeReport{}, err
		}
		defer cleanup()
		tool = []string{binary}
		if len(platforms) == 0 {
			platforms = DefaultDeadcodePlatforms
		}
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

	var findings []DeadcodeFinding
	if len(platforms) == 0 {
		var err error
		findings, err = runDeadcodeAnalyzer(runContext, run, repositoryPath, tool[0], arguments, nil)
		if err != nil {
			return DeadcodeReport{}, err
		}
	} else {
		perPlatform := make([][]DeadcodeFinding, 0, len(platforms))
		for _, platform := range platforms {
			// Sequential on purpose: each analysis type-checks the whole
			// module, and running them together would multiply the peak load.
			found, err := runDeadcodeAnalyzer(runContext, run, repositoryPath, tool[0], arguments, deadcodePlatformEnv(platform))
			if err != nil {
				return DeadcodeReport{}, fmt.Errorf("platform %s: %w", platform, err)
			}
			perPlatform = append(perPlatform, found)
		}
		findings = intersectDeadcodeFindings(perPlatform)
	}

	report := DeadcodeReport{Findings: findings, Platforms: append([]string(nil), platforms...)}
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

// deadcodePlatformEnv is the analysis environment for one GOOS: the platform,
// a fixed architecture, and cgo off so a cgo file cannot make the analysis
// differ between a host with a C toolchain and one without.
func deadcodePlatformEnv(platform string) []string {
	return []string{"GOOS=" + platform, "GOARCH=" + deadcodeAnalysisArch, "CGO_ENABLED=0"}
}

// runDeadcodeAnalyzer runs the analyzer once and parses its findings. extraEnv
// is appended to the process environment, so its entries win.
func runDeadcodeAnalyzer(ctx context.Context, run runner.Runner, repositoryPath, program string, arguments, extraEnv []string) ([]DeadcodeFinding, error) {
	var options runner.RunOptions
	if len(extraEnv) > 0 {
		options.Env = append(os.Environ(), extraEnv...)
	}
	result, err := run.RunOpts(ctx, repositoryPath, options, program, arguments...)
	if err != nil {
		// deadcode exits 0 even when it reports findings, so a non-zero exit
		// is a real failure — a build error, a missing module, a timeout. It
		// must fail the gate rather than be read as "nothing is dead".
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("deadcode analysis in %s: %w: %s", repositoryPath, ctx.Err(), detail)
		}
		return nil, fmt.Errorf("deadcode analysis in %s: %w: %s", repositoryPath, err, detail)
	}
	findings, err := parseDeadcodeOutput([]byte(result.Stdout))
	if err != nil {
		return nil, fmt.Errorf("deadcode analysis in %s: %w", repositoryPath, err)
	}
	return findings, nil
}

// intersectDeadcodeFindings keeps the functions present in every platform's
// findings, in identity order. The first platform's position is kept.
func intersectDeadcodeFindings(perPlatform [][]DeadcodeFinding) []DeadcodeFinding {
	counts := make(map[string]int)
	for _, findings := range perPlatform {
		seen := make(map[string]bool, len(findings))
		for _, finding := range findings {
			if !seen[finding.Identity] {
				seen[finding.Identity] = true
				counts[finding.Identity]++
			}
		}
	}
	var common []DeadcodeFinding
	for _, finding := range perPlatform[0] {
		if counts[finding.Identity] == len(perPlatform) {
			common = append(common, finding)
			counts[finding.Identity] = 0 // a duplicate identity is reported once
		}
	}
	return common
}

// installDeadcodeTool installs the pinned analyzer for the host into a
// temporary directory and returns the binary and a cleanup. GOOS and GOARCH
// are pinned to the host so an ambient cross-compilation setting cannot
// produce a binary this machine cannot execute.
func installDeadcodeTool(ctx context.Context, run runner.Runner, repositoryPath, goCommand, parent string) (string, func(), error) {
	if goCommand == "" {
		goCommand = "go"
	}
	directory, err := os.MkdirTemp(parent, "wb-deadcode-tool-")
	if err != nil {
		return "", nil, fmt.Errorf("create deadcode analyzer directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(directory) }
	env := append(os.Environ(), "GOBIN="+directory, "GOOS="+runtime.GOOS, "GOARCH="+runtime.GOARCH, "CGO_ENABLED=0", "GOFLAGS=")
	result, err := run.RunOpts(ctx, repositoryPath, runner.RunOptions{Env: env, CaptureCombined: true}, goCommand, "install", deadcodeToolPackage)
	if err != nil {
		cleanup()
		output := result.CombinedOutput
		if output == "" {
			output = result.Stdout + result.Stderr
		}
		return "", nil, fmt.Errorf("install deadcode analyzer %s: %w: %s", deadcodeToolPackage, err, strings.TrimSpace(output))
	}
	return filepath.Join(directory, deadcodeBinaryName(runtime.GOOS)), cleanup, nil
}

// deadcodeBinaryName is the file `go install` writes for the analyzer on goos.
func deadcodeBinaryName(goos string) string {
	if goos == "windows" {
		return "deadcode.exe"
	}
	return "deadcode"
}
