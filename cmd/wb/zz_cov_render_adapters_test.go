package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/streams"
)

func TestCwCovHumanAgeAndPublishedAgo(t *testing.T) {
	for _, test := range []struct {
		age  time.Duration
		want string
	}{
		{0, "just now"},
		{59 * time.Second, "just now"},
		{90 * time.Second, "1m"},
		{2 * time.Hour, "2h"},
		{47 * time.Hour, "47h"},
		{72 * time.Hour, "3d"},
	} {
		if got := humanAge(test.age); got != test.want {
			t.Errorf("humanAge(%v) = %q, want %q", test.age, got, test.want)
		}
	}
	if got := publishedAgo("just now"); got != "just now" {
		t.Errorf("publishedAgo(just now) = %q", got)
	}
	if got := publishedAgo("2h"); got != "2h ago" {
		t.Errorf("publishedAgo(2h) = %q", got)
	}
}

func TestCwCovMachineRowsClassifyEveryEntry(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []remotestate.Entry{
		{Error: "decode failed", Snapshot: remotestate.Snapshot{Login: "acme", Machine: "broken"}},
		{Snapshot: remotestate.Snapshot{Login: "acme", Machine: "empty"}},
		{Snapshot: remotestate.Snapshot{
			Login: "acme", Machine: "old", PublishedAt: now.Add(-3 * time.Hour), LastSeenAt: now.Add(-time.Minute),
			WBVersion: "v1", Repositories: []remotestate.RepositoryState{{Repository: "acme/a"}},
			Worktrees: []remotestate.WorktreeState{{Task: "t", Repository: "acme/a", Branch: "b"}},
		}},
		{Snapshot: remotestate.Snapshot{
			Login: "acme", Machine: "fresh", LastSeenAt: now.Add(-time.Minute),
			Repositories: []remotestate.RepositoryState{{Repository: "acme/a"}, {Repository: "acme/b"}},
		}},
	}

	rows := machineRows(entries, now, 30*time.Minute)
	if len(rows) != 4 {
		t.Fatalf("rows = %+v, want one per entry", rows)
	}
	if rows[0].Error != "decode failed" || rows[0].Key != "acme/broken" {
		t.Errorf("error row = %+v", rows[0])
	}
	if !strings.Contains(rows[1].Error, "no published_at") {
		t.Errorf("empty snapshot row = %+v, want a stated error", rows[1])
	}
	// The heartbeat is the later of publish and claim activity, and staleness
	// keys off it: a three-hour-old publish with a fresh claim is not stale.
	if rows[2].Age != "3h" || rows[2].Seen != "1m" || rows[2].Stale {
		t.Errorf("heartbeat row = %+v, want age=3h seen=1m not stale", rows[2])
	}
	if rows[2].Attention != 1 || rows[2].Worktrees != 1 || rows[2].WBVersion != "v1" {
		t.Errorf("heartbeat row counts = %+v", rows[2])
	}
	// A snapshot with only claim activity has no publish age at all.
	if rows[3].Age != "" || rows[3].Seen != "1m" || rows[3].Attention != 2 || rows[3].Stale {
		t.Errorf("claim-only row = %+v", rows[3])
	}

	// A zero stale window disables the rule entirely.
	for _, row := range machineRows(entries, now, 0) {
		if row.Stale {
			t.Errorf("row %q is stale with the rule disabled", row.Key)
		}
	}
}

func TestCwCovWriteMachinesTableRendersRowsAndErrors(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	rows := []remoteMachineRow{
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
	rows := machineRows(entries, now, 0)
	claims := []claimRow{
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

func TestCwCovFindSnapshotHolderStaleAndHeartbeatPhrase(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	machines := []remotestate.Entry{
		{Error: "broken", Snapshot: remotestate.Snapshot{Login: "acme", Machine: "one"}},
		{Snapshot: remotestate.Snapshot{
			Login: "acme", Machine: "two", PublishedAt: now.Add(-4 * time.Hour), LastSeenAt: now.Add(-2 * time.Hour),
		}},
	}
	if _, ok := findSnapshot(machines, "acme", "one"); ok {
		t.Error("a corrupt snapshot must not be mistaken for silence")
	}
	snapshot, ok := findSnapshot(machines, "acme", "two")
	if !ok || snapshot.Machine != "two" {
		t.Fatalf("findSnapshot = (%+v, %t)", snapshot, ok)
	}
	if !holderStale(machines, "acme", "absent", now, time.Hour) {
		t.Error("a holder with no snapshot must be stale")
	}
	if !holderStale(machines, "acme", "two", now, time.Hour) {
		t.Error("a holder whose heartbeat is older than the window must be stale")
	}
	if holderStale(machines, "acme", "two", now, 0) {
		t.Error("a zero window disables the staleness rule")
	}
	if holderStale(machines, "acme", "two", now, 3*time.Hour) {
		t.Error("a heartbeat inside the window must be fresh")
	}
	if got := heartbeatPhrase(machines, "acme", "absent", now, "never published"); got != "never published" {
		t.Errorf("missing-snapshot phrase = %q", got)
	}
	if got := heartbeatPhrase(machines, "acme", "two", now, "never published"); got != "2h ago" {
		t.Errorf("heartbeat phrase = %q, want the effective heartbeat age", got)
	}
}

func TestCwCovHolderDescNamesTheCallersOwnLogin(t *testing.T) {
	mine := remotestate.Claim{Login: "acme", Machine: "here"}
	if got := holderDesc(mine, remotestate.Claim{Login: "acme", Machine: "there"}); got != "you on there" {
		t.Errorf("holderDesc(same login, other machine) = %q", got)
	}
	if got := holderDesc(mine, remotestate.Claim{Login: "other", Machine: "there"}); got != "other/there" {
		t.Errorf("holderDesc(other login) = %q", got)
	}
	// The same machine is the bare holder key, not "you on".
	if got := holderDesc(mine, remotestate.Claim{Login: "acme", Machine: "here"}); got != "acme/here" {
		t.Errorf("holderDesc(same machine) = %q", got)
	}
}

func TestCwCovClaimRowsAndClaimsTable(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	machines := []remotestate.Entry{
		{Snapshot: remotestate.Snapshot{Login: "acme", Machine: "one", PublishedAt: now.Add(-time.Hour)}},
	}
	claims := []remotestate.ClaimEntry{
		{Claim: remotestate.Claim{Task: "alpha", Login: "acme", Machine: "one", ClaimedAt: now.Add(-time.Minute), Note: "wip"}},
		{Claim: remotestate.Claim{Task: "beta", Login: "acme", Machine: "absent", ClaimedAt: now.Add(-time.Hour)}},
		{Error: "undecodable", Claim: remotestate.Claim{Task: "broken"}},
	}
	rows := claimRows(claims, machines, now, 2*time.Hour)
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

func TestCwCovProposedTransitiveConsumersWalksTheRecordedGraph(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WB_HOME", home)
	projectsRoot := t.TempDir()

	// No graph evidence at all: found is false rather than guessing.
	consumers, found, err := proposedTransitiveConsumers(projectsRoot, []string{"acme/lib"})
	if err != nil || found || consumers != nil {
		t.Fatalf("missing graph = (%v, %t, %v)", consumers, found, err)
	}

	graph := deps.Graph{
		SchemaVersion: 1,
		Requirements: []deps.GraphRequirement{
			{ProviderRepository: "acme/lib", ConsumerRepository: "acme/mid"},
			{ProviderRepository: "acme/mid", ConsumerRepository: "acme/top"},
			{ProviderRepository: "acme/top", ConsumerRepository: "acme/other"},
			// Self-edges and half-edges are not dependencies.
			{ProviderRepository: "acme/lib", ConsumerRepository: "acme/lib"},
			{ProviderRepository: "", ConsumerRepository: "acme/ignored"},
			{ProviderRepository: "acme/ignored", ConsumerRepository: ""},
		},
	}
	path := filepath.Join(home, "reports", "deps-graph-go", "deps-graph.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}

	consumers, found, err = proposedTransitiveConsumers(projectsRoot, []string{"acme/lib"})
	if err != nil || !found {
		t.Fatalf("graph walk = (%v, %t, %v)", consumers, found, err)
	}
	want := []string{"acme/mid", "acme/other", "acme/top"}
	if strings.Join(consumers, ",") != strings.Join(want, ",") {
		t.Fatalf("consumers = %v, want the transitive closure %v", consumers, want)
	}

	// A malformed graph is not evidence.
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, found, err = proposedTransitiveConsumers(projectsRoot, []string{"acme/lib"}); err != nil || found {
		t.Fatalf("malformed graph = (found=%t, err=%v), want no evidence", found, err)
	}
}

func TestCwCovStreamWorktreesPlannedWorktreeAndRemove(t *testing.T) {
	adapter := &streamWorktrees{projectsRoot: t.TempDir()}
	if _, err := adapter.PlannedWorktree("task", "not-a-slug"); err == nil ||
		!strings.Contains(err.Error(), "must be owner/name") {
		t.Fatalf("malformed repository error = %v", err)
	}
	planned, err := adapter.PlannedWorktree("cw-task", "acme/app")
	if err != nil {
		t.Fatalf("PlannedWorktree: %v", err)
	}
	if !strings.Contains(planned, "cw-task") || !strings.Contains(planned, filepath.FromSlash("acme/app")) {
		t.Fatalf("planned worktree = %q, want a task directory under the canonical clone", planned)
	}

	// Remove with a receipt that matches nothing reports the missing candidate.
	err = adapter.Remove(context.Background(), "cw-task", "acme/app", planned, &streams.SquashAbsorptionReceipt{
		Target: "main", SourceBranch: "task/cw-task", SourceSHA: "s", CandidateSHA: "c", LandingSHA: "l",
	})
	if err == nil || !strings.Contains(err.Error(), "cw-task") || !strings.Contains(err.Error(), "acme/app") {
		t.Fatalf("Remove error = %v, want a refusal naming the task and repository", err)
	}
}

func TestCwCovStreamLeaseIdentity(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  provider: git\n  repo: acme/state\n  machine: cw-machine\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dependencies := remoteDeps{
		configPath: configPath,
		login:      func() (string, error) { return "cw-login", nil },
		open:       func(remotestate.Config, string) (remotestate.Provider, error) { return nil, nil },
	}
	login, machine := streamLeaseIdentity(dependencies, t.TempDir())
	if login != "cw-login" || machine != "cw-machine" {
		t.Fatalf("lease identity = (%q, %q)", login, machine)
	}

	// An unconfigured store degrades to an unattributed lease rather than
	// failing, so a fleet that never opted into wb remote still gets streams.
	dependencies.configPath = filepath.Join(t.TempDir(), "absent.yaml")
	if login, machine := streamLeaseIdentity(dependencies, t.TempDir()); login != "" || machine != "" {
		t.Fatalf("unconfigured identity = (%q, %q), want empty", login, machine)
	}

	// A failing login resolver leaves the login empty but keeps the machine.
	dependencies.configPath = configPath
	dependencies.login = func() (string, error) { return "", context.DeadlineExceeded }
	if login, machine := streamLeaseIdentity(dependencies, t.TempDir()); login != "" || machine != "cw-machine" {
		t.Fatalf("failed login identity = (%q, %q)", login, machine)
	}
}

func TestCwCovStreamSessionIdentityWithoutRegistration(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	if identity := streamSessionIdentity(); identity != "" {
		t.Fatalf("streamSessionIdentity() = %q, want empty with no registered session", identity)
	}
}
