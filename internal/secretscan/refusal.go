package secretscan

import (
	"fmt"
	"strings"
)

// FormatRefusal renders a fail-closed refusal for one or more blocking
// findings. It never contains the matched value -- only rule IDs,
// locations, and Fingerprint's redacted digest -- and always states how to
// proceed, because an agent that cannot tell why it was blocked will work
// around the check instead of fixing the input.
func FormatRefusal(blocking []Finding) error {
	if len(blocking) == 0 {
		return nil
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("refused: %d possible secret(s) matched a named pattern; the write did not happen", len(blocking)))
	for _, finding := range blocking {
		lines = append(lines, "  - "+finding.String())
	}
	lines = append(lines,
		"To proceed: remove or redact the matched text and retry.",
		"If a specific match is a false positive, confirm it is not a real secret and retry with, for each one:",
	)
	for _, finding := range blocking {
		lines = append(lines, fmt.Sprintf("    --override-secret %s", finding.Key()))
	}
	lines = append(lines, "Each --override-secret acknowledges exactly that one finding (its exact digest, not the rule in general) and is recorded in this command's output.")
	return fmt.Errorf("%s", strings.Join(lines, "\n"))
}
