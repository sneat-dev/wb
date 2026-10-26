package worktrees

import (
	"strings"
	"testing"
	"time"
)

var canonicalClaimEpoch = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func TestClaimModeAndLeaseValidation(t *testing.T) {
	t.Parallel()
	now := canonicalClaimEpoch
	future := now.Add(time.Hour)
	cases := []struct {
		name    string
		mode    string
		lease   time.Time
		wantErr string
	}{
		{"default mode without lease", "", time.Time{}, ""},
		{"explicit worktree without lease", ClaimModeWorktree, time.Time{}, ""},
		{"canonical with future lease", ClaimModeCanonical, future, ""},
		{"unknown mode", "shared", time.Time{}, "claim mode"},
		{"canonical without lease", ClaimModeCanonical, time.Time{}, "lease_expires_at"},
		{"canonical with lease not after record time", ClaimModeCanonical, now, "lease_expires_at"},
		{"worktree with lease", ClaimModeWorktree, future, "valid only for a canonical claim"},
		{"default with lease", "", future, "valid only for a canonical claim"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateClaimModeLease(tc.mode, tc.lease, now)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}
