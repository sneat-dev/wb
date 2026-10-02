package quality

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// MaxRatchetToleranceStatements is the hard cap on one package's tolerance.
// The tolerance is a stopgap for a handful of timing-dependent branches
// (spec/plans/coverage-to-100/README.md task-3 (b3)); a package that needs
// more than this has a coverage problem a tolerance must not hide.
const MaxRatchetToleranceStatements = 5

// RatchetTolerance is one package's explicitly configured allowance for
// statements that are covered or not depending on timing.
type RatchetTolerance struct {
	// Statements is how many untouched statements may be newly uncovered
	// against the merge base before the package fails.
	Statements int
	// Reason is the configured justification, repeated in every warning and
	// report entry that uses the tolerance.
	Reason string
}

// RatchetTolerances maps a package directory relative to the module root
// (the spelling PackageOf returns, for example "internal/orchestrate") to its
// tolerance. A package without an entry is held to the strict ratchet.
type RatchetTolerances map[string]RatchetTolerance

// ToleratedStatement names one statement EvaluateRatchet let through under a
// package's RatchetTolerance.
type ToleratedStatement struct {
	File   string `yaml:"file" json:"file"`
	Line   int    `yaml:"line" json:"line"`
	Reason string `yaml:"reason" json:"reason"`
}

type ratchetToleranceConfig struct {
	Version         int `yaml:"version"`
	TimingTolerance []struct {
		Package    string `yaml:"package"`
		Statements int    `yaml:"statements"`
		Reason     string `yaml:"reason"`
	} `yaml:"timing_tolerance"`
}

// LoadRatchetTolerances reads root's .wb/coverage-ratchet.yaml. root must be
// the checkout being judged (the pull request's head), never the merge-base
// checkout: the tolerance is policy of the change under review, so a pull
// request that introduces or tightens an entry is judged by its own file. A
// missing file means no tolerance. Anything malformed fails closed: an entry
// without a reason, with a statement count below 1 or above
// MaxRatchetToleranceStatements, with no package, or naming a package twice.
func LoadRatchetTolerances(root string) (RatchetTolerances, error) {
	path := filepath.Join(root, ratchetToleranceConfigPath)
	contents, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read coverage ratchet policy %s: %w", path, err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var config ratchetToleranceConfig
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("decode coverage ratchet policy %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("coverage ratchet policy %s must be exactly one YAML document", path)
	}
	if config.Version != 1 {
		return nil, fmt.Errorf("coverage ratchet policy %s has version %d; want 1", path, config.Version)
	}
	tolerances := make(RatchetTolerances, len(config.TimingTolerance))
	for index, entry := range config.TimingTolerance {
		pkg := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(entry.Package), "./"), "/")
		if pkg == "" {
			return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance[%d] names no package", path, index)
		}
		if _, repeated := tolerances[pkg]; repeated {
			return nil, fmt.Errorf("coverage ratchet policy %s repeats timing_tolerance package %q", path, pkg)
		}
		if entry.Statements < 1 || entry.Statements > MaxRatchetToleranceStatements {
			return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance package %q statements must be between 1 and %d, got %d", path, pkg, MaxRatchetToleranceStatements, entry.Statements)
		}
		reason := strings.TrimSpace(entry.Reason)
		if reason == "" {
			return nil, fmt.Errorf("coverage ratchet policy %s timing_tolerance package %q needs a reason", path, pkg)
		}
		tolerances[pkg] = RatchetTolerance{Statements: entry.Statements, Reason: reason}
	}
	return tolerances, nil
}

// toleratedStatements decides whether a changed package's count rise fits
// its tolerance. It returns the statements to report as tolerated, or nil
// when the tolerance does not apply and the rise must fail as usual. The
// tolerance applies only when all of these hold: the package has an entry,
// the uncovered count is at most the baseline plus the entry's statements,
// the rise is attributed to at least one statement, none of those statements
// is on a line the change added or modified, and together they hold no more
// statements than the entry allows. One statement outside those limits
// withdraws the tolerance for the whole package, so the failure names every
// newly uncovered statement and not just the excess.
func toleratedStatements(tolerance RatchetTolerance, configured bool, count, baselineCount int, newlyUncovered []CoverageBlock, changed ChangedLines, modulePath string) []ToleratedStatement {
	if !configured || count > baselineCount+tolerance.Statements || len(newlyUncovered) == 0 {
		return nil
	}
	statements := 0
	tolerated := make([]ToleratedStatement, 0, len(newlyUncovered))
	for _, block := range newlyUncovered {
		relativeFile := strings.TrimPrefix(block.File, modulePath+"/")
		for line := block.StartLine; line <= block.EndLine; line++ {
			if changed.Contains(relativeFile, line) {
				return nil
			}
		}
		statements += block.Statements
		tolerated = append(tolerated, ToleratedStatement{File: relativeFile, Line: block.StartLine, Reason: tolerance.Reason})
	}
	if statements > tolerance.Statements {
		return nil
	}
	return tolerated
}
