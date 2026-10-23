package policy

import (
	"fmt"
	"strings"
)

// Helpers kept for tests only: no production caller remains.

// Summary counts findings by rule for a one-line report.
func (r Result) Summary() string {
	counts := map[string]int{}
	for _, finding := range r.Findings {
		counts[finding.Rule]++
	}
	if len(counts) == 0 {
		return "no violations"
	}
	parts := make([]string, 0, len(counts))
	for _, rule := range sortedKeys(counts) {
		parts = append(parts, fmt.Sprintf("%d %s", counts[rule], rule))
	}
	return strings.Join(parts, ", ")
}
