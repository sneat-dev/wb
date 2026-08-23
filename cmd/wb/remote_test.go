package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
)

func remoteGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// setGitIdentity gives Publish's commits (made through the process env, not
// remoteGit's explicit env) a valid author/committer so tests work under a
// HOME with no git config.
func setGitIdentity(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
}

// remoteFixture builds a projects root holding one dirty fleet repo, a bare
// state-repo origin, and a wb.yaml pointing at it.
type remoteFixture struct {
	projectsRoot, origin, configPath string
}

func newRemoteFixture(t *testing.T, machine string) remoteFixture {
	t.Helper()
	setGitIdentity(t)
	base := t.TempDir()
	projectsRoot := filepath.Join(base, "projects")
	repo := filepath.Join(projectsRoot, "acme", "widgets")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, repo, "init", "-q", "-b", "main")
	remoteGit(t, repo, "commit", "-q", "--allow-empty", "-m", "seed")
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(base, "origin.git")
	remoteGit(t, base, "init", "-q", "--bare", "-b", "main", origin)
	seed := filepath.Join(base, "seed")
	remoteGit(t, base, "clone", "-q", origin, seed)
	remoteGit(t, seed, "commit", "-q", "--allow-empty", "-m", "init")
	remoteGit(t, seed, "push", "-q", "origin", "main")
	configPath := filepath.Join(base, "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  repo: team/wb-state\n  machine: "+machine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_HOME", filepath.Join(base, "wbhome"))
	return remoteFixture{projectsRoot: projectsRoot, origin: origin, configPath: configPath}
}

func (f remoteFixture) deps(login string, at time.Time) remoteDeps {
	return remoteDeps{
		configPath: f.configPath,
		login:      func() (string, error) { return login, nil },
		open: func(cfg remotestate.Config, projectsRoot string) (remotestate.Provider, error) {
			return gitrepo.New(gitrepo.Options{ClonePath: filepath.Join(projectsRoot, cfg.RepoOwner(), cfg.RepoName()), CloneURL: f.origin}), nil
		},
		now: func() time.Time { return at },
	}
}

func TestRemotePublishUnconfiguredIsUsageError(t *testing.T) {
	deps := defaultRemoteDeps()
	deps.configPath = filepath.Join(t.TempDir(), "none.yaml")
	var out bytes.Buffer
	err := runRemotePublish(deps, t.TempDir(), "", 2, false, false, &out)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || !strings.Contains(err.Error(), "remote:\n  provider: git") {
		t.Fatalf("err = %v, want usage error with snippet", err)
	}
}

func TestRemotePublishWritesSnapshotToStore(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if err := runRemotePublish(f.deps("alice", at), f.projectsRoot, "", 2, false, true, &out); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Key                 string `json:"key"`
		RepositoriesScanned int    `json:"repositories_scanned"`
		Attention           int    `json:"attention"`
		Worktrees           int    `json:"worktrees"`
		Location            string `json:"location"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("json: %v: %s", err, out.String())
	}
	if report.Key != "alice/laptop" || report.RepositoriesScanned != 1 || report.Attention != 1 || len(report.Location) != 40 {
		t.Fatalf("report = %+v", report)
	}
	stored := remoteGit(t, f.origin, "show", "main:machines/alice/laptop/snapshot.yaml")
	if !strings.Contains(stored, "repository: acme/widgets") || !strings.Contains(stored, "- dirty.txt") {
		t.Fatalf("stored snapshot = %s", stored)
	}
	if !strings.Contains(stored, "status: attention") {
		t.Fatalf("stored snapshot should report attention (untracked file, no upstream), not error: %s", stored)
	}
}

func TestRemotePublishDryRunTouchesNothing(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	var out bytes.Buffer
	if err := runRemotePublish(f.deps("alice", time.Now()), f.projectsRoot, "", 2, true, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "acme/widgets") {
		t.Fatalf("dry-run should print the snapshot: %s", out.String())
	}
	if files := remoteGit(t, f.origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(files, "machines/") {
		t.Fatalf("dry-run published: %s", files)
	}
	if _, err := os.Stat(filepath.Join(f.projectsRoot, "team", "wb-state")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run must not clone the state repo")
	}
}

func TestRemotePublishAcceptsProjectsRootAndFilterFlags(t *testing.T) {
	for _, flag := range []string{"projects-root", "filter"} {
		if !persistentFlagSupport[flag]["remote publish"] {
			t.Errorf("remote publish must accept --%s: it scans the fleet under --projects-root honouring --filter", flag)
		}
	}
	if persistentFlagSupport["org"]["remote publish"] {
		t.Error("remote publish must reject --org; it publishes local state, not GitHub-listed repos")
	}
}

func TestPersistentFlagMatrixRemoteCommands(t *testing.T) {
	for _, cmd := range []string{"remote publish", "remote status", "remote machines"} {
		if !persistentFlagSupport["projects-root"][cmd] {
			t.Errorf("%s must accept --projects-root: it locates the state-repo clone", cmd)
		}
	}
	for cmd, want := range map[string]bool{"remote publish": true, "remote status": false, "remote machines": false} {
		if persistentFlagSupport["filter"][cmd] != want {
			t.Errorf("persistentFlagSupport[filter][%q] = %v, want %v", cmd, persistentFlagSupport["filter"][cmd], want)
		}
	}
}

func publishTwo(t *testing.T) (remoteFixture, time.Time) {
	t.Helper()
	f := newRemoteFixture(t, "laptop")
	at := time.Date(2026, 8, 23, 9, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	if err := runRemotePublish(f.deps("alice", at), f.projectsRoot, "", 2, false, false, &out); err != nil {
		t.Fatal(err)
	}
	// Second machine: same store, a different projects root and machine name.
	g := remoteFixture{projectsRoot: filepath.Join(t.TempDir(), "projects"), origin: f.origin, configPath: filepath.Join(t.TempDir(), "wb.yaml")}
	if err := os.MkdirAll(g.projectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(g.configPath, []byte("remote:\n  repo: team/wb-state\n  machine: vm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runRemotePublish(g.deps("bob", at.Add(-48*time.Hour)), g.projectsRoot, "", 2, false, false, &out); err != nil {
		t.Fatal(err)
	}
	return f, at
}

func TestRemoteMachinesFlagsStaleEntries(t *testing.T) {
	f, at := publishTwo(t)
	var out bytes.Buffer
	if err := runRemoteMachines(f.deps("alice", at.Add(time.Hour)), f.projectsRoot, 24*time.Hour, true, &out); err != nil {
		t.Fatal(err)
	}
	var rows []remoteMachineRow
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("json: %v: %s", err, out.String())
	}
	if len(rows) != 2 || rows[0].Key != "alice/laptop" || rows[0].Stale || rows[1].Key != "bob/vm" || !rows[1].Stale {
		t.Fatalf("rows = %+v", rows)
	}
}

// TestRemoteMachinesTableHasPublishedAtColumn proves the human-readable
// table carries the exact RFC3339 UTC publish timestamp, not just the
// coarse relative age: an operator diffing snapshots across machines needs
// the real instant, and "9h" alone cannot be compared across two rows
// published on different days.
func TestRemoteMachinesTableHasPublishedAtColumn(t *testing.T) {
	f, at := publishTwo(t)
	var out bytes.Buffer
	if err := runRemoteMachines(f.deps("alice", at.Add(time.Hour)), f.projectsRoot, 24*time.Hour, false, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	header := strings.SplitN(text, "\n", 2)[0]
	fields := strings.Fields(header)
	atCol, ageCol := -1, -1
	for i, f := range fields {
		switch f {
		case "PUBLISHED_AT":
			atCol = i
		case "PUBLISHED":
			ageCol = i
		}
	}
	if atCol == -1 {
		t.Fatalf("header = %q, want a PUBLISHED_AT column", header)
	}
	if ageCol == -1 || atCol >= ageCol {
		t.Fatalf("header = %q, want PUBLISHED_AT before the PUBLISHED (age) column", header)
	}
	if !strings.Contains(text, at.Format(time.RFC3339)) {
		t.Fatalf("table = %q, want alice's RFC3339 published_at %s", text, at.Format(time.RFC3339))
	}
}

func TestRemoteStatusRendersCrossMachineWorklist(t *testing.T) {
	f, at := publishTwo(t)
	var out, errOut bytes.Buffer
	if err := runRemoteStatus(f.deps("alice", at.Add(time.Hour)), f.projectsRoot, 24*time.Hour, "", false, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"alice/laptop", "acme/widgets", "1 untracked file", "bob/vm", "STALE"} {
		if !strings.Contains(text, want) {
			t.Fatalf("status output lacks %q:\n%s", want, text)
		}
	}
}

func TestRemoteStatusMachineFilterAndErrorRowsDoNotFail(t *testing.T) {
	f, at := publishTwo(t)
	other := filepath.Join(t.TempDir(), "other")
	remoteGit(t, t.TempDir(), "clone", "-q", f.origin, other)
	bad := filepath.Join(other, "machines", "carol", "desk", "snapshot.yaml")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("schema_version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, other, "add", "-A")
	remoteGit(t, other, "commit", "-q", "-m", "corrupt")
	remoteGit(t, other, "push", "-q", "origin", "main")

	var out, errOut bytes.Buffer
	if err := runRemoteStatus(f.deps("alice", at), f.projectsRoot, 24*time.Hour, "", true, &out, &errOut); err != nil {
		t.Fatalf("error rows must not fail the command: %v", err)
	}
	if !strings.Contains(out.String(), "schema_version 99") {
		t.Fatalf("error row missing: %s", out.String())
	}

	out.Reset()
	if err := runRemoteStatus(f.deps("alice", at), f.projectsRoot, 24*time.Hour, "bob/vm", false, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "alice/laptop") || !strings.Contains(out.String(), "bob/vm") {
		t.Fatalf("--machine filter not applied: %s", out.String())
	}
}

// TestRemoteStatusMachineFilterNoMatchWritesToStderr proves an unmatched
// --machine reports on stderr (a typo'd key must not look like "everyone is
// clean") without failing the command: the store itself is fine, only the
// filter matched nothing.
func TestRemoteStatusMachineFilterNoMatchWritesToStderr(t *testing.T) {
	f, at := publishTwo(t)
	var out, errOut bytes.Buffer
	if code := runRemoteStatus(f.deps("alice", at), f.projectsRoot, 24*time.Hour, "carol/desk", false, &out, &errOut); code != nil {
		t.Fatalf("err = %v, want nil (exit code stays 0)", code)
	}
	if out.String() != "" {
		t.Fatalf("stdout = %q, want empty when nothing matched", out.String())
	}
	if !strings.Contains(errOut.String(), "no machine carol/desk in the remote store") {
		t.Fatalf("stderr = %q, want the no-match message", errOut.String())
	}
}

func TestRemotePublishFilterNoMatchStillErrors(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	var out bytes.Buffer
	err := runRemotePublish(f.deps("alice", time.Now().UTC()), f.projectsRoot, "definitely-no-match", 2, false, false, &out)
	if err == nil || !strings.Contains(err.Error(), "no local repositories match") {
		t.Fatalf("err = %v, want unmatched-filter error (a typo must not publish a false clean snapshot)", err)
	}
	if files := remoteGit(t, f.origin, "ls-tree", "-r", "--name-only", "main"); strings.Contains(files, "machines/") {
		t.Fatalf("unmatched filter must not publish: %s", files)
	}
}

func TestMachineRowsTreatsZeroPublishedAtAsError(t *testing.T) {
	entries := []remotestate.Entry{
		{
			Snapshot: remotestate.Snapshot{
				SchemaVersion: 1,
				Login:         "test",
				Machine:       "machine",
				PublishedAt:   time.Time{}, // zero time
			},
			Error: "",
		},
	}
	rows := machineRows(entries, time.Now(), 24*time.Hour)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Error == "" {
		t.Fatalf("row.Error must be non-empty for zero PublishedAt")
	}
	if rows[0].Stale {
		t.Fatalf("row.Stale must be false when there's an error")
	}
	if rows[0].Age != "" {
		t.Fatalf("row.Age must be empty when there's an error")
	}
}

func TestSyncPublishFlagIsRegistered(t *testing.T) {
	cmd := newSyncCmd()
	flag := cmd.Flags().Lookup("publish")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("--publish flag = %+v, want bool default false", flag)
	}
}

func TestFinishSyncPublishFailureIsReportedButExitStaysZero(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	deps := f.deps("alice", time.Now().UTC())
	deps.open = func(remotestate.Config, string) (remotestate.Provider, error) {
		return nil, errors.New("store unreachable")
	}
	var out, errOut bytes.Buffer
	if code := finishSync(nil, true, false, deps, f.projectsRoot, "", 1, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(errOut.String(), "remote publish failed (sync itself succeeded): store unreachable") {
		t.Fatalf("stderr = %q, want the publish failure reported", errOut.String())
	}
}

func TestFinishSyncPublishesAfterCleanSync(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	var out, errOut bytes.Buffer
	if code := finishSync(nil, true, false, f.deps("alice", time.Now().UTC()), f.projectsRoot, "", 1, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "published alice/laptop") {
		t.Fatalf("stdout = %q, want publish confirmation", out.String())
	}
}

func TestFinishSyncSkipsPublishWhenSyncFailed(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	var out, errOut bytes.Buffer
	failed := []fleetsync.Result{{Status: fleetsync.Failed}}
	if code := finishSync(failed, true, false, f.deps("alice", time.Now().UTC()), f.projectsRoot, "", 1, &out, &errOut); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if strings.Contains(out.String(), "published") || strings.Contains(errOut.String(), "publish") {
		t.Fatalf("publish must not run after a failed sync: out=%q err=%q", out.String(), errOut.String())
	}
}

// TestFinishSyncDryRunSkipsPublish proves `wb sync --dry-run --publish` never
// calls the remote publish path at all — deps.open would fail loudly if it
// were invoked, so reaching a clean exit with the "skipping" message and no
// stderr output proves the guard runs before anything provider-shaped.
func TestFinishSyncDryRunSkipsPublish(t *testing.T) {
	f := newRemoteFixture(t, "laptop")
	deps := f.deps("alice", time.Now().UTC())
	deps.open = func(remotestate.Config, string) (remotestate.Provider, error) {
		return nil, errors.New("must not be called")
	}
	var out, errOut bytes.Buffer
	if code := finishSync(nil, true, true, deps, f.projectsRoot, "", 1, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "skipping remote publish") {
		t.Fatalf("stdout = %q, want dry-run skip message", out.String())
	}
	if errOut.String() != "" {
		t.Fatalf("stderr = %q, want empty", errOut.String())
	}
}

var _ = context.Background
