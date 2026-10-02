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

func TestE2ERemoteCheckpointNextRefusesNativePushWithoutCheckout(t *testing.T) {
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
func TestE2ERemoteCheckpointNextRefusesRefRemovedAfterNativeFetch(t *testing.T) {
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

//nolint:paralleltest // The native repository fixture and Git tracing mutate process-wide environment.
func TestE2ERemoteCheckpointRefusesInvalidNativeVerificationOutput(t *testing.T) {
	fixture, worktree, _ := newSessionCheckpointFixture(t, "checkpoint-traced-verification")
	before := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	pushed, err := PushRemoteCheckpoint(context.Background(), PushRemoteCheckpointOptions{Root: worktree, Task: "checkpoint-traced-verification", HeadSHA: before})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := FetchRemoteCheckpoint(context.Background(), FetchRemoteCheckpointOptions{Root: worktree, Task: "checkpoint-traced-verification"})
	if err != nil || initial.SHA != before {
		t.Fatalf("initial native checkpoint retrieval = %+v, %v", initial, err)
	}
	t.Setenv("GIT_TRACE", "1")
	// The real Git command succeeds, but its stderr trace accompanies its stdout
	// SHA in the existing combined-output observation. No query output is forged.
	output, err := git(context.Background(), worktree, "rev-parse", "--verify", pushed.Ref+"^{commit}")
	if err != nil || isGitObjectID(output) || !strings.Contains(output, before) || !strings.Contains(output, "trace:") {
		t.Fatalf("native traced verification prerequisite = %q, %v", output, err)
	}
	fetched, err := FetchRemoteCheckpoint(context.Background(), FetchRemoteCheckpointOptions{Root: worktree, Task: "checkpoint-traced-verification"})
	if fetched != (RemoteCheckpointFetchResult{}) || err == nil || !strings.Contains(err.Error(), "resolve fetched checkpoint "+pushed.Ref+": Git returned an invalid commit object ID") || strings.Contains(err.Error(), "%!") {
		t.Fatalf("invalid native verification output admission = %+v, %v", fetched, err)
	}
	t.Setenv("GIT_TRACE", "")
	if head := gitTestOutput(t, worktree, "rev-parse", "HEAD"); head != before {
		t.Fatalf("verification refusal changed HEAD: %s -> %s", before, head)
	}
	if local := gitTestOutput(t, worktree, "rev-parse", "--verify", pushed.Ref+"^{commit}"); local != before {
		t.Fatalf("verification refusal changed local checkpoint: %s", local)
	}
	remote := strings.Fields(gitTestOutput(t, worktree, "ls-remote", "--", fixture.remote, pushed.Ref))
	if len(remote) != 2 || remote[0] != before || remote[1] != pushed.Ref {
		t.Fatalf("verification refusal changed remote checkpoint: %v", remote)
	}
}
