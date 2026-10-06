package cmdremote

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remoterun"
	"github.com/sneat-dev/wb/internal/remotestate"
)

func TestWriteRemoteStatusDiagnosticsRendersEachMismatch(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	writeRemoteStatusDiagnostics(&out, remoterun.StatusDiagnostics{
		Provider: "hub", Store: "memory",
		Mismatches: []string{"laptop-a", "laptop-b"},
	})
	rendered := out.String()
	for _, want := range []string{
		"warning: remote provider mismatch: laptop-a; configured store is memory",
		"warning: remote provider mismatch: laptop-b; configured store is memory",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing %q: %q", want, rendered)
		}
	}
}

func TestWriteClaimOutcomeSkipsEmptyText(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := writeClaimOutcome(&out, false, remotestate.ClaimOutcome{}, "")
	if err != nil {
		t.Fatalf("writeClaimOutcome returned %v, want nil", err)
	}
	if out.Len() != 0 {
		t.Fatalf("writeClaimOutcome wrote %q, want nothing", out.String())
	}
}

func TestCwCovWriteMachinesTableRendersRowsAndErrors(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rows := []remoterun.MachineRow{
		{Key: "acme/fresh", PublishedAt: now.Add(-2 * time.Hour), Age: "2h", Seen: "2h", WBVersion: "v1", Attention: 1, Worktrees: 2},
		{Key: "acme/stale", PublishedAt: now.Add(-72 * time.Hour), Age: "3d", Seen: "3d", Stale: true},
		{Key: "acme/broken", Error: "decode failed"},
	}
	var out bytes.Buffer
	writeMachinesTable(&out, rows)
	text := out.String()
	for _, want := range []string{
		"MACHINE", "PUBLISHED_AT", "ATTENTION", "WORKTREES",
		"acme/fresh", "2026-01-02T01:04:05Z", "v1",
		"STALE", "error: decode failed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("machines table missing %q:\n%s", want, text)
		}
	}
	// The error row carries nothing but the message.
	if strings.Contains(text, "acme/broken 2026") {
		t.Errorf("an error row must not render snapshot columns:\n%s", text)
	}
}

func TestCwCovWriteStatusWorklistRendersEverySectionShape(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []remotestate.Entry{
		{Snapshot: remotestate.Snapshot{
			Login: "acme", Machine: "one", PublishedAt: now.Add(-2 * time.Hour), RepositoriesScanned: 7,
			Repositories: []remotestate.RepositoryState{
				{Repository: "acme/dirty", Branch: "main", Upstream: "origin/main", Ahead: 2, Behind: 1, Summary: "2 modified"},
				{Repository: "acme/no-upstream", Branch: "task/x", Summary: "untracked"},
				{Repository: "acme/failed", Error: "not a repository"},
			},
			Worktrees: []remotestate.WorktreeState{
				{Task: "alpha", Repository: "acme/a", Branch: "task/alpha", OwnerState: "active"},
				{Task: "beta", Repository: "acme/b", Branch: "task/beta"},
			},
		}},
		{Snapshot: remotestate.Snapshot{Login: "acme", Machine: "clean", PublishedAt: now.Add(-time.Hour)}},
		{Error: "snapshot truncated", Snapshot: remotestate.Snapshot{Login: "acme", Machine: "broken"}},
	}
	rows := remoterun.MachineRows(entries, now, 0)
	claims := []remoterun.ClaimRow{
		{Task: "alpha", Holder: "acme/one"},
		{Task: "beta", Holder: "acme/other"},
		{Task: "broken", Holder: "acme/one", Error: "undecodable"},
	}
	var out bytes.Buffer
	writeStatusWorklist(&out, entries, rows, claims)
	text := out.String()
	for _, want := range []string{
		"## acme/one (published 2h ago, 7 scanned)",
		"acme/dirty", "+2/-1", "acme/no-upstream", "(no upstream)",
		"error: not a repository",
		"worktree alpha", "(active)", "worktree beta",
		"remote claims: alpha",
		"## acme/clean", "clean",
		"## acme/broken", "error: snapshot truncated",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status worklist missing %q:\n%s", want, text)
		}
	}
	// A claim held elsewhere must not appear under this machine, and an
	// undecodable claim is never listed: exactly one claims line, naming only
	// the task this machine holds.
	if strings.Count(text, "remote claims:") != 1 || !strings.Contains(text, "remote claims: alpha\n") {
		t.Errorf("claims were misattributed:\n%s", text)
	}
}

func TestCwCovClaimRowsAndClaimsTable(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	machines := []remotestate.Entry{
		{Snapshot: remotestate.Snapshot{Login: "acme", Machine: "one", PublishedAt: now.Add(-time.Hour)}},
	}
	claims := []remotestate.ClaimEntry{
		{Claim: remotestate.Claim{Task: "alpha", Login: "acme", Machine: "one", ClaimedAt: now.Add(-time.Minute), Note: "wip"}},
		{Claim: remotestate.Claim{Task: "beta", Login: "acme", Machine: "absent", ClaimedAt: now.Add(-time.Hour)}},
		{Error: "undecodable", Claim: remotestate.Claim{Task: "broken"}},
	}
	rows := remoterun.ClaimRows(claims, machines, now, 2*time.Hour)
	if len(rows) != 3 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Holder != "acme/one" || rows[0].Stale || rows[0].HeartbeatAge != "1h ago" || rows[0].Note != "wip" {
		t.Errorf("fresh claim row = %+v", rows[0])
	}
	if !rows[1].Stale || rows[1].HeartbeatAge != "never published" {
		t.Errorf("holder with no snapshot = %+v, want stale/never published", rows[1])
	}
	if rows[2].Error != "undecodable" || rows[2].Task != "broken" {
		t.Errorf("error claim row = %+v", rows[2])
	}

	var out bytes.Buffer
	writeClaimsTable(&out, rows)
	text := out.String()
	for _, want := range []string{"TASK", "HOLDER", "CLAIMED_AT", "HEARTBEAT", "STALE", "NOTE", "alpha", "acme/one", "wip", "never published", "error: undecodable"} {
		if !strings.Contains(text, want) {
			t.Errorf("claims table missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "STALE") {
		t.Errorf("a stale claim must be marked:\n%s", text)
	}
}
