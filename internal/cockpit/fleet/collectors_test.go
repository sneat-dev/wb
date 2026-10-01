package fleet

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// realTempDir is a temporary directory with no symbolic link in its path, which
// WB's worktree journal requires (macOS keeps /var behind one).
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestLocalCollectorsReportFailures covers a cancelled scan, an unreadable
// projects root, a directory that is not a repository and a cancelled read.
func TestLocalCollectorsReportFailures(t *testing.T) {
	t.Parallel()
	collectors := LocalCollectors{ProjectsRoot: filepath.Join(t.TempDir(), "absent"), Home: t.TempDir(), Runner: newFakeGit(t)}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := collectors.Repositories(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled scan = %v", err)
	}
	if _, err := collectors.Repositories(t.Context()); err == nil {
		t.Error("a missing projects root scanned cleanly")
	}
	if _, err := collectors.PullRequests(cancelled); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled pull request records = %v", err)
	}
	notARepository := discover.Repo{Org: "acme", Name: "widgets", Path: t.TempDir()}
	if _, err := collectors.Branches(t.Context(), notARepository); !errors.Is(err, errGit) || strings.Contains(err.Error(), notARepository.Path) {
		t.Errorf("branches of a directory that is no repository = %v, want a message with no path", err)
	}
	if bindings, err := collectors.PullRequests(t.Context()); err != nil || len(bindings) != 0 {
		t.Errorf("pull request records of an empty root = %v, %v", bindings, err)
	}
}

// TestWorktreesAreReadFromGitsAdministrativeFiles covers the linked-worktree
// listing with no Git command: none, a normal entry, a relative gitdir resolved
// against its own entry, a detached HEAD, a half-created entry, a worktree whose
// directory is missing or outside the projects root, a cancelled read and an
// unreadable directory.
func TestWorktreesAreReadFromGitsAdministrativeFiles(t *testing.T) {
	t.Parallel()
	root := realTempDir(t)
	clone := filepath.Join(root, "clone")
	collectors := LocalCollectors{ProjectsRoot: root}
	repo := discover.Repo{Path: clone}
	if linked, err := collectors.Worktrees(t.Context(), repo); err != nil || linked != nil {
		t.Fatalf("worktrees of a clone with none = %v, %v", linked, err)
	}
	outside := realTempDir(t)
	for _, dir := range []string{"tasks/a", "tasks/b", "tasks/rel"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	admin := filepath.Join(clone, ".git", "worktrees")
	for name, files := range map[string]map[string]string{
		"a":       {"gitdir": filepath.Join(root, "tasks/a/.git") + "\n", "HEAD": "ref: refs/heads/feature/a\n"},
		"b":       {"gitdir": filepath.Join(root, "tasks/b/.git") + "\n", "HEAD": "0123456789abcdef\n"},
		"rel":     {"gitdir": "../../../../tasks/rel/.git\n", "HEAD": "ref: refs/heads/rel\n"},
		"half":    {"HEAD": "ref: refs/heads/half\n"},
		"missing": {"gitdir": filepath.Join(root, "tasks/vanished/.git") + "\n", "HEAD": "ref: refs/heads/gone\n"},
		"foreign": {"gitdir": filepath.Join(outside, ".git") + "\n", "HEAD": "ref: refs/heads/foreign\n"},
	} {
		for file, content := range files {
			path := filepath.Join(admin, name, file)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(admin, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	notADirectory := filepath.Join(root, "tasks", "plain")
	if err := os.WriteFile(notADirectory, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(admin, "plain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(admin, "plain", "gitdir"), []byte(filepath.Join(notADirectory, ".git")), 0o644); err != nil {
		t.Fatal(err)
	}
	linked, err := collectors.Worktrees(t.Context(), repo)
	want := []LinkedWorktree{
		{Path: filepath.Join(root, "tasks/a"), Branch: "feature/a"}, {Path: filepath.Join(root, "tasks/b")},
		{Path: filepath.Join(root, "tasks/rel"), Branch: "rel"},
	}
	if err != nil || len(linked) != len(want) {
		t.Fatalf("worktrees = %+v, %v, want %+v: only existing directories under the projects root", linked, err, want)
	}
	for index := range want {
		if linked[index] != want[index] {
			t.Errorf("worktree %d = %+v, want %+v", index, linked[index], want[index])
		}
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := collectors.Worktrees(cancelled, repo); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled read = %v", err)
	}
	if err := os.Chmod(admin, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(admin, 0o755) })
	if _, err := os.ReadDir(admin); err == nil {
		t.Skip("running with privileges that read a mode 000 directory")
	}
	if _, err := collectors.Worktrees(t.Context(), repo); err == nil {
		t.Error("an unreadable administrative directory listed cleanly")
	}
}

// TestRefsAreParsedFromForEachRefOutput covers the format: a local and a remote
// branch, the remote HEAD and tags skipped, a malformed line, tracking states
// and a bad date.
func TestRefsAreParsedFromForEachRefOutput(t *testing.T) {
	t.Parallel()
	output := strings.Join([]string{
		"refs/heads/main\x00origin/main\x00[ahead 2, behind 3]\x001700000000",
		"refs/heads/gone\x00origin/gone\x00[gone]\x00notadate",
		"refs/heads/plain\x00\x00\x001700000001",
		"refs/remotes/origin/main\x00\x00\x001700000002",
		"refs/remotes/origin/HEAD\x00\x00\x001700000003",
		"refs/tags/v1\x00\x00\x001700000004",
		"malformed",
		"",
	}, "\n")
	refs := parseRefs(output)
	if len(refs) != 4 {
		t.Fatalf("refs = %+v", refs)
	}
	main, gone, plain, remote := refs[0], refs[1], refs[2], refs[3]
	if main.Name != "main" || main.Scope != BranchLocal || main.Upstream != "origin/main" || main.Ahead != 2 || main.Behind != 3 || !main.CommittedAt.Equal(time.Unix(1700000000, 0)) {
		t.Errorf("main = %+v", main)
	}
	if !gone.UpstreamGone || !gone.CommittedAt.IsZero() || plain.Upstream != "" || plain.Ahead != 0 || remote.Name != "origin/main" || remote.Scope != BranchRemote {
		t.Errorf("gone = %+v, plain = %+v, remote = %+v", gone, plain, remote)
	}
}

// TestRecordReadsTheManifestAndHeartbeatAndNothingElse covers a WB worktree,
// one with no heartbeat and a directory that is no WB worktree.
func TestRecordReadsTheManifestAndHeartbeatAndNothingElse(t *testing.T) {
	t.Parallel()
	collectors := LocalCollectors{}
	dir := realTempDir(t)
	if _, ok := collectors.Record(dir); ok {
		t.Fatal("a directory with no manifest is a WB worktree")
	}
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "task-q", EffortKind: worktrees.EffortKindTask, Provenance: worktrees.ProvenanceCreated,
		Repository: "acme/widgets", Branch: "task-q", CreatedAt: created,
	}
	writeManifestFile(t, dir, manifest)
	record, ok := collectors.Record(dir)
	if !ok || record.Task != "task-q" || record.Branch != "task-q" || !record.CreatedAt.Equal(created) || !record.HeartbeatAt.IsZero() {
		t.Fatalf("record without a heartbeat = %+v, %v", record, ok)
	}
	worktrees.TouchHeartbeat(dir, "test")
	if record, _ := collectors.Record(dir); record.HeartbeatAt.IsZero() {
		t.Error("the heartbeat was not read")
	}
}

// TestLocalCollectorsReadSessionsAndRuns reads real records from a WB home:
// a registered session and a run whose owner never started, which renders as
// abandoned rather than running.
func TestLocalCollectorsReadSessionsAndRuns(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	collectors := LocalCollectors{Home: home}
	if views, err := collectors.Sessions(t.Context()); err != nil || len(views) != 0 {
		t.Fatalf("sessions of an empty home = %v, %v", views, err)
	}
	if results, err := collectors.Runs(t.Context()); err != nil || len(results) != 0 {
		t.Fatalf("runs of an empty home = %v, %v", results, err)
	}
	if _, err := session.Register(filepath.Join(home, session.DirName), session.Record{PID: os.Getpid(), WBSessionID: "wbs-test", Runtime: "claude", Model: "opus"}); err != nil {
		t.Fatal(err)
	}
	record := agents.Record{
		SchemaVersion: 1, AgentID: "agt-0123456789abcdef0123456789abcdef", State: agents.StateRunning, Repository: "acme/widgets",
		Resolved: agents.Resolved{Harness: "codex", Model: "gpt"}, StartedAt: time.Now().Add(-time.Hour), Task: "private task",
	}
	if err := agents.NewStore(home).Create(record); err != nil {
		t.Fatal(err)
	}
	views, err := collectors.Sessions(t.Context())
	if err != nil || len(views) != 1 || views[0].WBSessionID != "wbs-test" || views[0].State != session.StateLive {
		t.Fatalf("sessions = %+v, %v", views, err)
	}
	results, err := collectors.Runs(t.Context())
	if err != nil || len(results) != 1 || results[0].State != agents.StateAbandoned {
		t.Fatalf("runs = %+v, %v", results, err)
	}

	broken := filepath.Join(home, agents.DirName, "agt-broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "run.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := collectors.Runs(t.Context()); err == nil {
		t.Error("a corrupt run record did not fail the listing")
	}
}

// fakeReader is a local state reader that serves fixed machines.
type fakeReader struct {
	machines []remotestate.Entry
	err      error
}

func (r fakeReader) ReadLocal(context.Context) ([]remotestate.Entry, error) { return r.machines, r.err }

// TestLocalStateCollectorReadsMachinesThroughTheReader covers a reader that
// answers and one that fails.
func TestLocalStateCollectorReadsMachinesThroughTheReader(t *testing.T) {
	t.Parallel()
	machines := []remotestate.Entry{{Snapshot: remotestate.Snapshot{Machine: "desk"}}}
	got, err := LocalStateCollector{Reader: fakeReader{machines: machines}}.Machines(t.Context())
	if err != nil || len(got) != 1 || got[0].Snapshot.Machine != "desk" {
		t.Errorf("machines = %+v, %v", got, err)
	}
	if _, err := (LocalStateCollector{Reader: fakeReader{err: errBoom}}).Machines(t.Context()); !errors.Is(err, errBoom) {
		t.Errorf("a failing reader = %v", err)
	}
}

// TestBranchesReadForEachRefThroughTheHardenedCommand pins what the collectors
// hand the runner: the Git binary, the repository named by -C, the read-only
// command, and the allow-listed environment.
func TestBranchesReadForEachRefThroughTheHardenedCommand(t *testing.T) {
	t.Parallel()
	git := newFakeGit(t)
	dir, repo := indexedCheckout(t, git, 1)
	repo.refs = "refs/heads/main\x00origin/main\x00[ahead 1]\x001700000000\nrefs/remotes/origin/main\x00\x00\x001700000001\n"
	refs, err := (LocalCollectors{Git: "/opt/git", Runner: git}).Branches(t.Context(), discover.Repo{Path: dir})
	if err != nil || len(refs) != 2 || refs[0].Ahead != 1 || refs[1].Scope != BranchRemote {
		t.Fatalf("branches = %+v, %v", refs, err)
	}
	calls := git.running()
	if len(calls) != 1 || calls[0].Binary != "/opt/git" || calls[0].Dir != dir {
		t.Fatalf("calls = %+v", calls)
	}
	want := []string{"for-each-ref", "--format=" + refFormat, "refs/heads", "refs/remotes"}
	if !slices.Equal(calls[0].Args, want) {
		t.Errorf("arguments = %q, want %q", calls[0].Args, want)
	}
	if opts := calls[0].Opts; !opts.DiscardStderr || opts.StdoutLimit != maxGitOutput || opts.WaitDelay != gitWaitDelay {
		t.Errorf("options = %+v, want stderr discarded, the output cap and the wait delay", opts)
	}
	if !slices.Equal(calls[0].Env, gitEnvironment(os.Environ())) {
		t.Errorf("environment = %v, want the allow-list built from the parent's", calls[0].Env)
	}
}

// TestEveryGitCommandCarriesTheHardeningSettingsLiterally spells out the
// settings a repository's own configuration cannot override: the fake runner
// compares each command's prefix with gitArguments, and this test pins
// gitArguments itself.
func TestEveryGitCommandCarriesTheHardeningSettingsLiterally(t *testing.T) {
	t.Parallel()
	want := []string{
		"--no-optional-locks", "-C", "/repo",
		"-c", "protocol.allow=never", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null", "-c", "uploadpack.allowFilter=false",
		"-c", "protocol.ext.allow=never", "-c", "protocol.file.allow=never", "-c", "protocol.git.allow=never",
		"-c", "protocol.ssh.allow=never", "-c", "protocol.http.allow=never", "-c", "protocol.https.allow=never",
		"status",
	}
	if got := gitArguments("/repo", []string{"status"}); !slices.Equal(got, want) {
		t.Errorf("hardened arguments = %q, want %q", got, want)
	}
}
