package shared

import (
	"fmt"
	"strings"
)

// ValidateBranchChoice validates explicit branch choices without altering their values.
func ValidateBranchChoice(branch string, branchChosen, prefixChosen bool) error {
	if branchChosen && prefixChosen {
		return fmt.Errorf("--branch and --branch-prefix cannot be used together")
	}
	if branchChosen && strings.TrimSpace(branch) == "" {
		return fmt.Errorf("--branch must not be empty when explicitly provided")
	}
	return nil
}
