package agents

import "strings"

// Helpers kept for tests only: no production caller remains.

// StripAgentID returns the bare run ID from a possibly machine-qualified
// reference.
func StripAgentID(reference string) string {
	_, agentID, err := SplitAgentRef(reference)
	if err != nil {
		return strings.TrimSpace(reference)
	}
	return agentID
}
