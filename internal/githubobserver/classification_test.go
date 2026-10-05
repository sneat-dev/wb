package githubobserver

import (
	"errors"
	"fmt"
	"testing"
)

func TestTransientFailureClassificationPreservesSentinelsAndFlattenedReasons(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		err            error
		read, mutation bool
	}{
		{"nil", nil, false, false},
		{"authoritative", errors.New("HTTP 403"), false, false},
		{"wrapped read", fmt.Errorf("provider: %w", ErrTransientRetriesExhausted), true, true},
		{"flattened read", errors.New("observe: " + ErrTransientRetriesExhausted.Error()), true, true},
		{"unknown mutation", fmt.Errorf("provider: %w", ErrTransientMutationOutcomeUnknown), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsTransientReadFailure(tc.err); got != tc.read {
				t.Fatalf("read=%v want %v", got, tc.read)
			}
			if got := IsTransientGitHubFailure(tc.err); got != tc.mutation {
				t.Fatalf("GitHub=%v want %v", got, tc.mutation)
			}
		})
	}
}
