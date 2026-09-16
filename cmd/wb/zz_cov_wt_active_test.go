package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// cwWtFakeProvider embeds the remotestate.Provider interface so only List has
// to be implemented for the active-worktree preflight.
type cwWtFakeProvider struct {
	remotestate.Provider
	entries []remotestate.Entry
	err     error
	calls   int
}

func (provider *cwWtFakeProvider) List(context.Context) ([]remotestate.Entry, error) {
	provider.calls++
	return provider.entries, provider.err
}

func cwWtActiveDeps(claims func(string, string) ([]worktrees.ActiveClaimSummary, error),
	sessions func(string) ([]session.View, error),
	remote remoteDeps) activeWorktreeDeps {
	return activeWorktreeDeps{claims: claims, sessions: sessions, remote: remote}
}

func cwWtRemoteDeps(t *testing.T, login string, loginErr error, provider *cwWtFakeProvider, openErr error, now time.Time) remoteDeps {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  provider: git\n  repo: acme/state\n  machine: machine-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return remoteDeps{
		configPath: configPath,
		login:      func() (string, error) { return login, loginErr },
		open: func(remotestate.Config, string) (remotestate.Provider, error) {
			if openErr != nil {
				return nil, openErr
			}
			return provider, nil
		},
		now: func() time.Time { return now },
	}
}

func TestCwWtRunWorktreeActiveLocalPaths(t *testing.T) {
	now := time.Date(2025, 5, 5, 12, 0, 0, 0, time.UTC)
	live := []worktrees.ActiveClaimSummary{
		{Task: "zeta", Repository: "acme/app", Branch: "b1", WBSessionID: "wbs-1", RecordedAt: now.Add(-time.Hour)},
		{Task: "alpha", Repository: "acme/app", Branch: "b2", RecordedAt: now.Add(-2 * time.Hour)},
		{Task: "old", Repository: "acme/app", WBSessionID: "wbs-gone", RecordedAt: now.Add(-72 * time.Hour)},
	}
	deps := cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return live, nil },
		func(string) ([]session.View, error) {
			return []session.View{{Record: session.Record{PID: 1, WBSessionID: "wbs-1"}, State: session.StateLive}}, nil
		},
		cwWtRemoteDeps(t, "me", nil, &cwWtFakeProvider{}, nil, now),
	)
	report, err := runWorktreeActive(context.Background(), deps, "/tmp/projects", "", true, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Local.Status != "incomplete" || report.Local.OmittedUnresolvedClaims != 1 {
		t.Fatalf("local status = %+v", report.Local)
	}
	if report.Remote.Status != "local_only" || report.Remote.StaleAfter != "24h0m0s" {
		t.Fatalf("remote status = %+v", report.Remote)
	}
	if len(report.Worktrees) != 2 {
		t.Fatalf("rows = %+v", report.Worktrees)
	}
	if report.Worktrees[0].Task != "alpha" || report.Worktrees[0].OwnerState != "recent_claim" {
		t.Fatalf("sorted rows = %+v", report.Worktrees)
	}
	if report.Worktrees[1].Task != "zeta" || report.Worktrees[1].OwnerState != "active" {
		t.Fatalf("sorted rows = %+v", report.Worktrees)
	}

	// A zero stale window formats as empty and still short-circuits to local.
	report, err = runWorktreeActive(context.Background(), deps, "/tmp/projects", "", true, 0)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.StaleAfter != "" {
		t.Fatalf("zero stale window = %q", report.Remote.StaleAfter)
	}
}

func TestCwWtRunWorktreeActiveErrors(t *testing.T) {
	now := time.Now().UTC()
	remote := cwWtRemoteDeps(t, "me", nil, &cwWtFakeProvider{}, nil, now)

	_, err := runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, errors.New("boom") },
		func(string) ([]session.View, error) { return nil, nil }, remote), "/tmp", "", true, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "local worktree inventory unavailable") {
		t.Fatalf("claims error = %v", err)
	}

	_, err = runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, errors.New("boom") }, remote), "/tmp", "", true, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "local session inventory unavailable") {
		t.Fatalf("sessions error = %v", err)
	}

	// Remote configuration unavailable.
	badConfig := remote
	badConfig.configPath = filepath.Join(t.TempDir(), "missing.yaml")
	report, err := runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, nil }, badConfig), "/tmp", "", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.Status != "unavailable" || !strings.Contains(report.Remote.Error, "remote configuration unavailable") {
		t.Fatalf("remote config error report = %+v", report.Remote)
	}

	// Local machine identity unavailable.
	noLogin := remote
	noLogin.login = func() (string, error) { return "", errors.New("no login") }
	report, err = runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, nil }, noLogin), "/tmp", "", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.Status != "unavailable" || !strings.Contains(report.Remote.Error, "local machine identity unavailable") {
		t.Fatalf("login error report = %+v", report.Remote)
	}

	// Provider list failure, and provider construction failure.
	failing := &cwWtFakeProvider{err: errors.New("list down")}
	report, err = runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, nil },
		cwWtRemoteDeps(t, "me", nil, failing, nil, now)), "/tmp", "", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.Status != "unavailable" || !strings.Contains(report.Remote.Error, "remote snapshots unavailable") {
		t.Fatalf("list error report = %+v", report.Remote)
	}

	report, err = runWorktreeActive(context.Background(), cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, nil },
		cwWtRemoteDeps(t, "me", nil, nil, errors.New("open failed"), now)), "/tmp", "", false, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.Status != "unavailable" {
		t.Fatalf("open error report = %+v", report.Remote)
	}
}

func TestCwWtRunWorktreeActiveRemoteSnapshots(t *testing.T) {
	now := time.Date(2025, 5, 5, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-48 * time.Hour)
	fresh := now.Add(-time.Hour)
	provider := &cwWtFakeProvider{entries: []remotestate.Entry{
		// The local machine's own old publication is skipped.
		{Snapshot: remotestate.Snapshot{Login: "me", Machine: "machine-a", PublishedAt: fresh}},
		// A snapshot that could not be decoded.
		{Error: "decode failed"},
		// A stale machine carrying one includable and one excluded worktree,
		// plus one whose summary is invalid.
		{Snapshot: remotestate.Snapshot{
			Login: "them", Machine: "machine-b", PublishedAt: stale,
			Worktrees: []remotestate.WorktreeState{
				{Task: "wanted", Repository: "acme/app", Branch: "b", OwnerState: "active", Lifecycle: "working", TaskSummary: "fine", LastActivityAt: fresh},
				{Task: "terminal", Repository: "acme/app", Branch: "b", OwnerState: "active", Lifecycle: "merged"},
				{Task: "idle", Repository: "acme/app", Branch: "b", OwnerState: "orphaned", Lifecycle: "working"},
				{Task: "other-repo", Repository: "acme/elsewhere", Branch: "b", OwnerState: "active", Lifecycle: "working"},
				{Task: "bad-summary", Repository: "acme/app", Branch: "b", OwnerState: "active", Lifecycle: "working", TaskSummary: "two\nlines"},
			},
		}},
		// A fresh machine with no worktrees.
		{Snapshot: remotestate.Snapshot{Login: "them", Machine: "machine-c", PublishedAt: fresh}},
	}}
	deps := cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) { return nil, nil },
		func(string) ([]session.View, error) { return nil, nil },
		cwWtRemoteDeps(t, "me", nil, provider, nil, now),
	)
	report, err := runWorktreeActive(context.Background(), deps, "/tmp", "acme/app", false, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if report.Remote.Status != "unavailable" {
		t.Fatalf("an undecodable snapshot must mark the remote unavailable: %+v", report.Remote)
	}
	if !strings.Contains(report.Remote.Error, "could not be decoded") && !strings.Contains(report.Remote.Error, "invalid task summaries") {
		t.Fatalf("remote error = %q", report.Remote.Error)
	}
	if report.Remote.Machines != 2 {
		t.Fatalf("machines = %d, want 2 (the undecodable entry contributes none)", report.Remote.Machines)
	}
	if report.Remote.StaleMachines != 1 {
		t.Fatalf("stale machines = %d, want 1", report.Remote.StaleMachines)
	}
	if len(report.Remote.Snapshots) != 2 || report.Remote.Snapshots[0].Machine != "them/machine-b" {
		t.Fatalf("snapshots = %+v", report.Remote.Snapshots)
	}
	if len(report.Worktrees) != 2 {
		t.Fatalf("included rows = %+v", report.Worktrees)
	}
	for _, row := range report.Worktrees {
		if row.Locality != "remote" || !row.SnapshotStale || row.Machine != "them/machine-b" {
			t.Fatalf("row = %+v", row)
		}
	}
}

func TestCwWtWriteActiveWorktreeTextBranches(t *testing.T) {
	report := activeWorktreeReport{
		SchemaVersion: 1,
		Local:         activeLocalStatus{Status: "incomplete", OmittedUnresolvedClaims: 2},
		Remote:        activeRemoteStatus{Status: "stale", Error: "one snapshot is stale"},
		Worktrees: []activeWorktreeRow{
			{Locality: "local", Repository: "acme/app", Task: "alpha", Branch: "b", OwnerState: "active", Lifecycle: "working", Summary: "local one"},
			{Locality: "remote", Machine: "them/machine-b", Repository: "acme/app", Task: "beta", Branch: "b", OwnerState: "unknown", Lifecycle: "working", SnapshotStale: true},
		},
	}
	var out bytes.Buffer
	if err := writeActiveWorktreeText(&out, report); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"local: incomplete (2 unresolved claims omitted; inspect wb worktree list)",
		"remote: stale (one snapshot is stale)",
		"local local acme/app alpha b [active/working] — local one",
		"remote them/machine-b acme/app beta b [unknown/working] (STALE SNAPSHOT)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("active text missing %q:\n%s", want, text)
		}
	}

	// A clean report writes just the two status lines.
	out.Reset()
	if err := writeActiveWorktreeText(&out, activeWorktreeReport{Local: activeLocalStatus{Status: "available"}, Remote: activeRemoteStatus{Status: "local_only"}}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "local: available\nremote: local_only\n" {
		t.Fatalf("clean report text = %q", got)
	}

	// Every write failure is propagated.
	for allow := 0; allow < 7; allow++ {
		if err := writeActiveWorktreeText(&cwWtFailWriter{Allow: allow}, report); err == nil {
			t.Fatalf("writeActiveWorktreeText with %d writes allowed returned nil", allow)
		}
	}
}

func TestCwWtActiveHelpers(t *testing.T) {
	if includeRemoteActive(remotestate.WorktreeState{OwnerState: "active", Lifecycle: "working"}) != true {
		t.Fatal("an active working remote worktree must be included")
	}
	if includeRemoteActive(remotestate.WorktreeState{OwnerState: "active", Lifecycle: "MERGED "}) != false {
		t.Fatal("a terminal lifecycle must be excluded")
	}
	if includeRemoteActive(remotestate.WorktreeState{OwnerState: "orphaned", Lifecycle: "working"}) != false {
		t.Fatal("a non-active owner must be excluded")
	}
	for _, value := range []string{"merged", "Superseded", "terminal", "removed", "discarded", "  MERGED  "} {
		if !terminalRemoteLifecycle(value) {
			t.Errorf("terminalRemoteLifecycle(%q) = false", value)
		}
	}
	if terminalRemoteLifecycle("working") {
		t.Fatal("working must not be terminal")
	}

	views := []session.View{
		{Record: session.Record{WBSessionID: "live-1", PID: 1}, State: session.StateLive},
		{Record: session.Record{WBSessionID: "gone-1", PID: 2}, State: session.StateGone},
		{Record: session.Record{WBSessionID: "", PID: 3}, State: session.StateLive},
	}
	ids := activeSessionIDs(views)
	if !ids["live-1"] || ids["gone-1"] || len(ids) != 1 {
		t.Fatalf("active session ids = %v", ids)
	}

	now := time.Now().UTC()
	if state, include := localClaimOwnerState(worktrees.ActiveClaimSummary{WBSessionID: "live-1"}, ids, now); !include || state != "active" {
		t.Fatalf("live claim = (%q, %t)", state, include)
	}
	if state, include := localClaimOwnerState(worktrees.ActiveClaimSummary{RecordedAt: now.Add(-time.Hour)}, ids, now); !include || state != "recent_claim" {
		t.Fatalf("recent claim = (%q, %t)", state, include)
	}
	if _, include := localClaimOwnerState(worktrees.ActiveClaimSummary{RecordedAt: now.Add(-48 * time.Hour)}, ids, now); include {
		t.Fatal("an old unresolved claim must be omitted")
	}
	if _, include := localClaimOwnerState(worktrees.ActiveClaimSummary{}, ids, now); include {
		t.Fatal("a claim with no time and no live session must be omitted")
	}

	if formatActiveTime(time.Time{}) != "" {
		t.Fatal("zero time must format as empty")
	}
	if got := formatActiveTime(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)); got != "2025-01-02T03:04:05Z" {
		t.Fatalf("formatActiveTime = %q", got)
	}
	if formatActiveStaleAfter(0) != "" || formatActiveStaleAfter(-time.Second) != "" {
		t.Fatal("non-positive stale windows must format as empty")
	}
	if got := formatActiveStaleAfter(90 * time.Second); got != "1m30s" {
		t.Fatalf("formatActiveStaleAfter = %q", got)
	}
	if normalizedOwnerState("  ") != "unknown" || normalizedOwnerState("active") != "active" {
		t.Fatal("normalizedOwnerState")
	}
	if normalizedLifecycle("  ") != "working" || normalizedLifecycle("merged") != "merged" {
		t.Fatal("normalizedLifecycle")
	}
	if !matchesWorktreeFilter("acme/app", "") {
		t.Fatal("empty filter must match everything")
	}
	if !matchesWorktreeFilter("Acme/App", "acme") || matchesWorktreeFilter("acme/app", "other") {
		t.Fatal("matchesWorktreeFilter")
	}

	// The final tie-break is the branch, reached only when repository, task,
	// and machine are equal.
	rows := []activeWorktreeRow{
		{Repository: "acme/app", Task: "t", Machine: "m", Branch: "z"},
		{Repository: "acme/app", Task: "t", Machine: "m", Branch: "a"},
	}
	sortActiveWorktrees(rows)
	if rows[0].Branch != "a" || rows[1].Branch != "z" {
		t.Fatalf("branch tie-break = %+v", rows)
	}
}

func TestCwWtActiveSessionListingAndCmd(t *testing.T) {
	// listActiveSessions fails when WB_HOME cannot be resolved: a path whose
	// ancestor is a regular file is not merely absent.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, filepath.Join(blocker, "home"))
	if _, err := listActiveSessions(t.TempDir()); err == nil {
		t.Fatal("listActiveSessions with an unresolvable WB_HOME must fail")
	}

	// Default dependencies against an empty root.
	projects := t.TempDir()
	t.Setenv(wbhome.EnvOverride, filepath.Join(t.TempDir(), "wb-home"))
	stdout, _, err := cwCovExec(t, projects, newWorktreeActiveCmd, "--local-only")
	if err != nil {
		t.Fatalf("active --local-only: %v", err)
	}
	if !strings.Contains(stdout, "local: available") || !strings.Contains(stdout, "remote: local_only") {
		t.Fatalf("active text stdout = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, projects, newWorktreeActiveCmd, "--format", "json", "--local-only")
	if err != nil {
		t.Fatalf("active json: %v", err)
	}
	if !strings.Contains(stdout, "schema_version") {
		t.Fatalf("active json stdout = %q", stdout)
	}

	// Without a configured remote the preflight is incomplete: exit 1.
	_, _, err = cwCovExec(t, projects, newWorktreeActiveCmd)
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("active without a remote exit = %d (%v)", code, err)
	}

	if _, _, err := cwCovExec(t, projects, newWorktreeActiveCmd, "--format", "bogus"); err == nil {
		t.Fatal("active with a bogus format must fail")
	}
}

func TestCwWtWorktreeActiveCmdWithDepsIncompleteIsFindings(t *testing.T) {
	deps := cwWtActiveDeps(
		func(string, string) ([]worktrees.ActiveClaimSummary, error) {
			return []worktrees.ActiveClaimSummary{{Task: "old", Repository: "acme/app", RecordedAt: time.Now().UTC().Add(-72 * time.Hour)}}, nil
		},
		func(string) ([]session.View, error) { return nil, nil },
		cwWtRemoteDeps(t, "me", nil, &cwWtFakeProvider{}, nil, time.Now().UTC()),
	)
	stdout, _, err := cwCovExec(t, t.TempDir(), func() *cobra.Command { return newWorktreeActiveCmdWithDeps(deps) })
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("active with an omitted local claim exit = %d (%v)\n%s", code, err, stdout)
	}
	if !strings.Contains(stdout, "local: incomplete") {
		t.Fatalf("active incomplete stdout = %q", stdout)
	}

	// The same report in json reaches the encoder instead.
	stdout, _, err = cwCovExec(t, t.TempDir(), func() *cobra.Command { return newWorktreeActiveCmdWithDeps(deps) }, "--format", "json")
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("active json exit = %d (%v)", code, err)
	}
	if !strings.Contains(stdout, "\"omitted_unresolved_claims\":1") {
		t.Fatalf("active json stdout = %q", stdout)
	}
}
