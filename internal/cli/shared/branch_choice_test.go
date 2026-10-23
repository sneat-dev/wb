package shared

import "testing"

func TestMovementBranchChoicePreservesRefusalPrecedence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, branch   string
		chosen, prefix bool
		want           string
	}{
		{"absent", "", false, false, ""}, {"prefix-only", "", false, true, ""}, {"branch", " topic ", true, false, ""}, {"explicit-empty", "", true, false, "--branch must not be empty when explicitly provided"}, {"whitespace", " \t", true, false, "--branch must not be empty when explicitly provided"}, {"both", "branch", true, true, "--branch and --branch-prefix cannot be used together"}, {"both-empty", "", true, true, "--branch and --branch-prefix cannot be used together"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateBranchChoice(tc.branch, tc.chosen, tc.prefix)
			if tc.want == "" && err != nil || tc.want != "" && (err == nil || err.Error() != tc.want) {
				t.Fatalf("error=%v want=%q", err, tc.want)
			}
		})
	}
}
