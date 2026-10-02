//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
)

func TestRemoteCheckpointNextRefusesNativePushWithoutCheckout(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	root := filepath.Join(parent, "missing-checkout")
	got, err := PushRemoteCheckpoint(context.Background(), PushRemoteCheckpointOptions{Root: root, Task: "checkpoint-next-no-repository", HeadSHA: strings.Repeat("a", 40)})
	if got != (RemoteCheckpointResult{}) || err == nil || !strings.Contains(err.Error(), "push remote checkpoint refs/wb/checkpoints/checkpoint-next-no-repository") {
		t.Fatalf("native missing-checkout push = %+v, %v", got, err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatalf("native push refusal changed contents: %v, %v", entries, err)
	}
}

//nolint:paralleltest // The existing fixture sets process-wide Git/WB environment.
func TestRemoteCheckpointNextRefusesRefRemovedAfterNativeFetch(t *testing.T) {
	fixture, worktree, _ := newSessionCheckpointFixture(t, "checkpoint-next-removed")
	before := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	pushed, err := PushRemoteCheckpoint(context.Background(), PushRemoteCheckpointOptions{Root: worktree, Task: "checkpoint-next-removed", HeadSHA: before})
	if err != nil {
		t.Fatal(err)
	}
	observed := &checkpointNextRemoveFetchedRef{Runner: runner.New(), t: t, root: worktree, ref: pushed.Ref}
	fetched, err := FetchRemoteCheckpoint(withGitRunner(context.Background(), observed), FetchRemoteCheckpointOptions{Root: worktree, Task: "checkpoint-next-removed"})
	if !observed.removed || observed.verifyError == nil || err == nil || fetched != (RemoteCheckpointFetchResult{}) || !strings.Contains(err.Error(), "resolve fetched checkpoint "+pushed.Ref) {
		t.Fatalf("native disappearing fetched ref = %+v, %v; removed=%v verify=%v", fetched, err, observed.removed, observed.verifyError)
	}
	if detail := strings.TrimSpace(observed.verifyOutput); detail == "" || !strings.Contains(err.Error(), detail) {
		t.Fatalf("native verification diagnostic was lost: %q, %v", detail, err)
	}
	if head := gitTestOutput(t, worktree, "rev-parse", "HEAD"); head != before {
		t.Fatalf("fetch refusal changed HEAD: %s -> %s", before, head)
	}
	remote := strings.Fields(gitTestOutput(t, worktree, "ls-remote", "--", fixture.remote, pushed.Ref))
	if len(remote) != 2 || remote[0] != before || remote[1] != pushed.Ref {
		t.Fatalf("fetch refusal changed remote checkpoint: %v", remote)
	}
}

// Every observed command executes native Git. Only after its successful fetch
// does the fixture delete the actual local ref before the native verification.
type checkpointNextRemoveFetchedRef struct {
	runner.Runner
	t            *testing.T
	root, ref    string
	removed      bool
	verifyError  error
	verifyOutput string
}

func (r *checkpointNextRemoveFetchedRef) RunOpts(ctx context.Context, dir string, options runner.RunOptions, name string, args ...string) (runner.Result, error) {
	result, err := r.Runner.RunOpts(ctx, dir, options, name, args...)
	if name == "git" && dir == r.root {
		if err == nil && reflect.DeepEqual(args, []string{"-C", dir, "fetch", "--no-tags", "--", "origin", r.ref + ":" + r.ref}) {
			r.t.Helper()
			gitTest(r.t, r.root, "update-ref", "-d", r.ref)
			r.removed = true
		}
		if reflect.DeepEqual(args, []string{"-C", dir, "rev-parse", "--verify", r.ref + "^{commit}"}) {
			r.verifyError, r.verifyOutput = err, result.CombinedOutput
		}
	}
	return result, err
}
