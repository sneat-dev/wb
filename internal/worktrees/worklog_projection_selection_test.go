package worktrees

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestSelectWorkLogProjectionPreservesErrorPrecedence(t *testing.T) {
	t.Parallel()
	current := workLogProjection{Version: 1, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64), Lifecycle: "active"}
	legacy := current
	legacy.Lifecycle = "terminal"
	currentFailure := errors.New("current projection unreadable")
	legacyFailure := errors.New("legacy projection unreadable")
	tests := []struct {
		name       string
		current    workLogProjection
		currentErr error
		legacy     workLogProjection
		legacyErr  error
		want       workLogProjection
		selection  workLogProjectionSelection
		wantErr    error
		wantText   string
	}{
		{name: "both equal", current: current, legacy: current, want: current, selection: workLogProjectionBothEqual},
		{name: "both disagree", current: current, legacy: legacy, wantText: "legacy and current work-log projections disagree"},
		{name: "current only", current: current, legacyErr: os.ErrNotExist, want: current, selection: workLogProjectionCurrentOnly},
		{name: "malformed legacy rejects current", current: current, legacyErr: legacyFailure, wantErr: legacyFailure},
		{name: "current error takes precedence", currentErr: currentFailure, legacyErr: legacyFailure, wantErr: currentFailure},
		{name: "current error rejects valid legacy", currentErr: currentFailure, legacy: legacy, wantErr: currentFailure},
		{name: "both absent", currentErr: os.ErrNotExist, legacyErr: os.ErrNotExist, wantErr: errWorkLogProjectionNotFound},
		{name: "legacy read error", currentErr: os.ErrNotExist, legacyErr: legacyFailure, wantErr: legacyFailure},
		{name: "legacy only", currentErr: os.ErrNotExist, legacy: legacy, want: legacy, selection: workLogProjectionLegacyOnly},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, selection, err := selectWorkLogProjection(test.current, test.currentErr, test.legacy, test.legacyErr)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if test.wantText != "" {
				if err == nil || err.Error() != test.wantText {
					t.Fatalf("error = %v, want %q", err, test.wantText)
				}
				return
			}
			if err != nil || got != test.want || selection != test.selection {
				t.Fatalf("selection = %+v, %d, %v; want %+v, %d", got, selection, err, test.want, test.selection)
			}
		})
	}
}
