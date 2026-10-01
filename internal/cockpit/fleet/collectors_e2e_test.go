//go:build e2e

package fleet

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
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

// TestE2ESnapshottingARealRepositoryNeverWritesToItOrFetches proves the snapshot
// is read-only and local (cockpit#req:no-fleet-scan-on-the-request-path), with
// the production wiring: the local collectors and the remote collector over the
// real git provider on a local state clone whose origin is unreachable. With a
// repository origin that has moved ahead, two full snapshots leave every file
// under the repository's .git and under the state clone unchanged (so no ref,
// index, lock or FETCH_HEAD was written or created), leave `git for-each-ref`
// identical, and leave the remote-tracking ref at the old remote state; and
// the other machine in the state clone is read.
func TestE2ESnapshottingARealRepositoryNeverWritesToItOrFetches(t *testing.T) {
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
