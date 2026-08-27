package secretscan

import (
	"fmt"
	"strings"
)

// Overrides is a set of explicitly acknowledged findings, keyed by
// Finding.Key ("<rule-id>:<fingerprint-digest>"). It downgrades one exact
// occurrence from block to warn -- never a rule ID alone, and never a
// blanket "skip scanning" switch. A different secret produces a different
// fingerprint and therefore needs its own explicit override, so this can
// never be reused as a standing bypass.
//
// Overrides must be constructed from caller-supplied --override-secret
// values (see ParseOverrides), which in turn can only be produced by first
// running into the refusal and reading its printed fingerprint. That
// sequencing -- fail, read, decide, re-run with the exact key -- is the
// point: an override is explicit and effortful, never the path of least
// resistance.
type Overrides map[string]bool

// Covers reports whether finding was explicitly acknowledged.
func (o Overrides) Covers(finding Finding) bool {
	return o[finding.Key()]
}

// ParseOverrides parses repeatable "<rule-id>:<fingerprint-digest>" CLI
// values, e.g. "aws-access-token:sha256:4f9c2a1b", as produced verbatim by
// Finding.Key. Every value must be well-formed; a malformed override is a
// hard error rather than a silently ignored no-op, because a bypass an agent
// believes is armed but is not would defeat its own purpose.
func ParseOverrides(raw []string) (Overrides, error) {
	overrides := Overrides{}
	for _, value := range raw {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		ruleID, digest, ok := strings.Cut(value, ":")
		if !ok || strings.TrimSpace(ruleID) == "" || strings.TrimSpace(digest) == "" {
			return nil, fmt.Errorf("--override-secret %q must be exactly <rule-id>:<fingerprint> as printed by the refusal it is acknowledging", value)
		}
		overrides[ruleID+":"+digest] = true
	}
	return overrides, nil
}
