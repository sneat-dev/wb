package quality

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// DeadcodeFailureEvidence is the complete set of newly unreachable functions
// from a failed `go run ./cmd/wb deadcode` check. Count is independently
// declared by the command; Complete is false when its output could not be
// verified. The ordinary Detail remains bounded for human-facing reports.
type DeadcodeFailureEvidence struct {
	Count      int      `yaml:"count" json:"count"`
	Identities []string `yaml:"identities,omitempty" json:"identities,omitempty"`
	Complete   bool     `yaml:"complete" json:"complete"`
}

// Valid also checks evidence after a receipt has been deserialized; a stored
// count/list mismatch or duplicate must not authorize a baseline match.
func (e *DeadcodeFailureEvidence) Valid() bool {
	if e == nil || !e.Complete || e.Count <= 0 || e.Count != len(e.Identities) {
		return false
	}
	seen := make(map[string]bool, len(e.Identities))
	for _, identity := range e.Identities {
		if identity == "" || len(strings.Fields(identity)) != 1 || seen[identity] {
			return false
		}
		seen[identity] = true
	}
	return true
}

var (
	deadcodeHeaderPattern  = regexp.MustCompile(`^New unreachable functions \(([0-9]+)\):$`)
	deadcodeFindingPattern = regexp.MustCompile(`^  (.+):([1-9][0-9]*): (\S+)$`)
	deadcodeFixedPattern   = regexp.MustCompile(`^Baseline entries now reachable or gone \(([0-9]+)\) — rerun with --update-baseline to drop them:$`)
)

func isDeadcodeVerificationCommand(language string, check Check, command []string) bool {
	return language == "go" && check == CheckLint && len(command) == 4 &&
		command[0] == "go" && command[1] == "run" && command[2] == "./cmd/wb" && command[3] == "deadcode"
}

// parseDeadcodeFailureEvidence consumes the untruncated combined command
// output. It accepts only WB's complete text report plus go run's exit line;
// analyzer errors, a partial report, or future output shapes stay failures.
func parseDeadcodeFailureEvidence(output string) *DeadcodeFailureEvidence {
	evidence := &DeadcodeFailureEvidence{}
	lines := strings.Split(strings.TrimSuffix(output, "\n"), "\n")
	if len(lines) < 3 || strings.Contains(output, "\r") {
		return evidence
	}
	header := deadcodeHeaderPattern.FindStringSubmatch(lines[0])
	if len(header) != 2 {
		return evidence
	}
	count, err := strconv.Atoi(header[1])
	if err != nil || count <= 0 || count > len(lines)-3 {
		return evidence
	}
	evidence.Count = count
	for _, line := range lines[1 : count+1] {
		finding := deadcodeFindingPattern.FindStringSubmatch(line)
		if len(finding) != 4 {
			return evidence
		}
		evidence.Identities = append(evidence.Identities, finding[3])
	}
	index := count + 1
	if index < len(lines) && lines[index] == "" {
		index++
		if index >= len(lines) {
			return evidence
		}
		fixed := deadcodeFixedPattern.FindStringSubmatch(lines[index])
		if len(fixed) != 2 {
			return evidence
		}
		fixedCount, err := strconv.Atoi(fixed[1])
		if err != nil || fixedCount <= 0 || fixedCount > len(lines)-index-3 {
			return evidence
		}
		index++
		for _, line := range lines[index : index+fixedCount] {
			if !strings.HasPrefix(line, "  ") || len(strings.Fields(strings.TrimPrefix(line, "  "))) != 1 {
				return evidence
			}
		}
		index += fixedCount
	}
	footer := fmt.Sprintf("error: %d function(s) are unreachable from main and are not in %s; wire them up, delete them, or record them with --update-baseline", count, DefaultDeadcodeBaseline)
	if index+2 != len(lines) || lines[index] != footer || lines[index+1] != "exit status 1" || !evidence.validWithoutComplete() {
		return evidence
	}
	evidence.Complete = true
	return evidence
}

func (e *DeadcodeFailureEvidence) validWithoutComplete() bool {
	copy := *e
	copy.Complete = true
	return copy.Valid()
}
