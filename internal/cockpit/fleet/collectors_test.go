package fleet

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// gitEnv makes a fixture's git commands independent of the machine's
// configuration.
func gitEnv() []string {
	return append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
}

// gitIn runs git in dir for a test fixture and returns its output.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", args...)
	command.Dir = dir
	command.Env = gitEnv()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

// realFleet is a projects root holding github.com/acme/widgets, a clone of a
// local bare origin, with a linked WB worktree for task-a, and an origin that
// has moved ahead of the clone's remote-tracking refs.
type realFleet struct {
	root, clone, origin, worktree string
}

func newRealFleet(t *testing.T) realFleet {
	t.Helper()
	f := realFleet{root: realTempDir(t), origin: filepath.Join(realTempDir(t), "origin.git")}
	f.clone = filepath.Join(f.root, "github.com", "acme", "widgets")
	f.worktree = filepath.Join(f.clone, ".worktrees", "task-a")
	if err := os.MkdirAll(f.clone, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.root, "init", "--bare", "--initial-branch=main", f.origin)
	gitIn(t, f.clone, "init", "--initial-branch=main")
	gitIn(t, f.clone, "remote", "add", "origin", f.origin)
	if err := os.WriteFile(filepath.Join(f.clone, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.clone, "add", ".")
	gitIn(t, f.clone, "commit", "-m", "secret subject")
	gitIn(t, f.clone, "push", "-u", "origin", "main")
	gitIn(t, f.clone, "branch", "feature/one")
	gitIn(t, f.clone, "worktree", "add", "-b", "task-a", f.worktree)
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "task-a", EffortKind: worktrees.EffortKindTask, Provenance: worktrees.ProvenanceCreated,
		Repository: "acme/widgets", Branch: "task-a", CreatedAt: time.Now().Add(-time.Hour).UTC(),
	}
	if err := worktrees.WriteManifest(f.worktree, manifest); err != nil {
		t.Fatal(err)
	}
	worktrees.TouchHeartbeat(f.worktree, "test")
	// Move the origin ahead through a second clone, so the first clone's
	// remote-tracking refs are now behind what a fetch would show.
	other := filepath.Join(realTempDir(t), "other")
	gitIn(t, f.root, "clone", f.origin, other)
	if err := os.WriteFile(filepath.Join(other, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", ".")
	gitIn(t, other, "commit", "-m", "origin moved ahead")
	gitIn(t, other, "push", "origin", "main")
	return f
}

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

// fileStates records every file under dir by size and modification time.
func fileStates(t *testing.T, dir string) map[string]string {
	t.Helper()
	states := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, _ := filepath.Rel(dir, path)
		states[relative] = info.ModTime().String() + "/" + info.Mode().String() + "/" + string(rune(info.Size()))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return states
}

// unreachableURL is a remote that fails at once if anything contacts it.
const unreachableURL = "ssh://127.0.0.1:1/never/contacted.git"

// newStateClone makes a local copy of a remote-state store holding one other
// machine's snapshot, whose origin is unreachable.
func newStateClone(t *testing.T, published time.Time) string {
	t.Helper()
	clone := realTempDir(t)
	gitIn(t, clone, "init", "--initial-branch=main")
	gitIn(t, clone, "remote", "add", "origin", unreachableURL)
	data, err := remotestate.Encode(remotestate.Snapshot{
		SchemaVersion: remotestate.SchemaVersion, Login: "alice", Machine: "desk", PublishedAt: published, WBVersion: "v0.9.0",
		KnownRepositories: []string{"acme/widgets"},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(clone, "machines", "alice", "desk", "snapshot.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, clone, "add", ".")
	gitIn(t, clone, "commit", "-m", "snapshot")
	return clone
}

// TestSnapshottingARealRepositoryNeverWritesToItOrFetches proves the snapshot
// is read-only and local (cockpit#req:no-fleet-scan-on-the-request-path), with
// the production wiring: the local collectors and the remote collector over the
// real git provider on a local state clone whose origin is unreachable. With a
// repository origin that has moved ahead, two full snapshots leave every file
// under the repository's .git and under the state clone unchanged (so no ref,
// index, lock or FETCH_HEAD was written or created), leave `git for-each-ref`
// identical, and leave the remote-tracking ref at the old remote state; and
// the other machine in the state clone is read.
func TestSnapshottingARealRepositoryNeverWritesToItOrFetches(t *testing.T) {
	t.Parallel()
	f := newRealFleet(t)
	published := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	stateClone := newStateClone(t, published)
	gitDir := filepath.Join(f.clone, ".git")
	refsBefore := gitIn(t, f.clone, "for-each-ref")
	statesBefore, stateBefore := fileStates(t, gitDir), fileStates(t, stateClone)
	if _, err := os.Stat(filepath.Join(gitDir, "FETCH_HEAD")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the fixture already has a FETCH_HEAD: %v", err)
	}
	trackedBefore := strings.TrimSpace(gitIn(t, f.clone, "rev-parse", "refs/remotes/origin/main"))
	originHead := strings.TrimSpace(gitIn(t, f.clone, "ls-remote", f.origin, "refs/heads/main"))
	if strings.HasPrefix(originHead, trackedBefore) {
		t.Fatal("the origin has not moved ahead of the clone's remote-tracking ref")
	}

	home := t.TempDir()
	remote := LocalStateCollector{Reader: gitrepo.New(gitrepo.Options{ClonePath: stateClone, CloneURL: unreachableURL})}
	collectors := LocalCollectors{ProjectsRoot: f.root, Home: home, IndexCachePath: filepath.Join(home, "index.json")}
	snapshotter := New(Options{Machine: testMachine, Collectors: collectors.Collectors(remote)})
	refreshAndSettle(t, snapshotter)
	refreshAndSettle(t, snapshotter)
	document := snapshotter.Document()

	if _, err := os.Stat(filepath.Join(gitDir, "FETCH_HEAD")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a snapshot created FETCH_HEAD: %v", err)
	}
	if after := gitIn(t, f.clone, "for-each-ref"); after != refsBefore {
		t.Errorf("git for-each-ref changed:\n%s\nthen\n%s", refsBefore, after)
	}
	requireUnchanged(t, ".git", statesBefore, fileStates(t, gitDir))
	requireUnchanged(t, "the state clone", stateBefore, fileStates(t, stateClone))
	if tracked := strings.TrimSpace(gitIn(t, f.clone, "rev-parse", "refs/remotes/origin/main")); tracked != trackedBefore {
		t.Errorf("origin/main moved from %s to %s: something fetched", trackedBefore, tracked)
	}

	// And the snapshot is the real state: the clone, its worktree, its
	// branches and the other machine.
	names := map[string]Branch{}
	for _, branch := range document.Branches {
		names[branch.Scope+":"+branch.Name] = branch
	}
	var local Repository
	for _, repository := range document.Repositories {
		if repository.Route == RouteLocal {
			local = repository
		}
	}
	if document.WarmingUp || local.DefaultBranch != "main" {
		t.Fatalf("document = %+v", document)
	}
	if names["local:feature/one"].Name == "" || names["local:task-a"].Worktree == "" || names["local:task-a"].Task != "task-a" || names["remote:origin/main"].Name == "" || names["local:main"].Upstream != "origin/main" {
		t.Errorf("branches = %+v", names)
	}
	if len(document.Worktrees) != 1 || document.Worktrees[0].Task != "task-a" || document.Worktrees[0].OwnerState != OwnerActive {
		t.Errorf("worktrees = %+v", document.Worktrees)
	}
	if len(document.Machines) != 2 || document.Machines[0].Machine != "desk" || document.Machines[0].Route != RouteCached || !document.Machines[0].ObservedAt.Equal(published) {
		t.Errorf("machines = %+v, want the state clone's machine, cached", document.Machines)
	}
	body, _ := jsonString(document)
	for _, leaked := range []string{"secret subject", f.clone, f.root, f.worktree, "hello", stateClone} {
		if strings.Contains(body, leaked) {
			t.Errorf("the document carries %q", leaked)
		}
	}
}

// requireUnchanged fails for every file that changed, vanished or appeared.
func requireUnchanged(t *testing.T, what string, before, after map[string]string) {
	t.Helper()
	for name, state := range before {
		if after[name] != state {
			t.Errorf("%s: %s changed during a snapshot", what, name)
		}
	}
	for name := range after {
		if _, existed := before[name]; !existed {
			t.Errorf("%s: %s was created during a snapshot", what, name)
		}
	}
}

// TestLocalCollectorsReportFailures covers a cancelled scan, an unreadable
// projects root, a directory that is not a repository and a cancelled read.
func TestLocalCollectorsReportFailures(t *testing.T) {
	t.Parallel()
	collectors := LocalCollectors{ProjectsRoot: filepath.Join(t.TempDir(), "absent"), Home: t.TempDir()}
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
	gitIn(t, dir, "init")
	if _, ok := collectors.Record(dir); ok {
		t.Fatal("a directory with no manifest is a WB worktree")
	}
	created := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "task-q", EffortKind: worktrees.EffortKindTask, Provenance: worktrees.ProvenanceCreated,
		Repository: "acme/widgets", Branch: "task-q", CreatedAt: created,
	}
	if err := worktrees.WriteManifest(dir, manifest); err != nil {
		t.Fatal(err)
	}
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
