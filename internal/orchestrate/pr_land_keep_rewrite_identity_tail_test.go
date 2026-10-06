package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

func TestKeepRewriteScratchDirectoryFailureHasNoGitEffects(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	file := filepath.Join(root, "owned-file")
	const contents = "scratch parent is a file"
	if err := os.WriteFile(file, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	landed, head, refusal, err := rewriteBranchForKeptCommitsInTempDir(context.Background(), file, nil, nil, "", "", "", "", keepPlan{}, githubchecks.PullRequestView{}, nil, "", "", nil)
	var pathErr *os.PathError
	if landed != nil || head != "" || refusal != nil || !errors.As(err, &pathErr) || !strings.Contains(err.Error(), "create landing scratch directory") {
		t.Fatalf("receipt %v %q %+v, error %v", landed, head, refusal, err)
	}
	if pathErr.Op != "mkdir" || filepath.Dir(pathErr.Path) != file || !strings.HasPrefix(filepath.Base(pathErr.Path), "wb-pr-land-") || pathErr.Err == nil {
		t.Fatalf("native allocation error %+v", pathErr)
	}
	got, readErr := os.ReadFile(file)
	entries, listErr := os.ReadDir(root)
	if readErr != nil || string(got) != contents || listErr != nil || len(entries) != 1 || entries[0].Name() != "owned-file" {
		t.Fatalf("scratch failure mutated parent: %q %v %v %v", got, readErr, entries, listErr)
	}
}

type keepRangeFailureGit struct {
	Git
	t     *testing.T
	cause error
	calls int
}

func (g *keepRangeFailureGit) RevListReverseRange(_ context.Context, dir, from, to string) (string, error) {
	g.calls++
	if dir != "owned-canonical" || from != "merge-base" || to != "target" {
		g.t.Fatalf("range %q %q %q", dir, from, to)
	}
	return "must not parse partial output", g.cause
}

func TestKeepRewriteRangeAndPatchFailuresPreserveIdentity(t *testing.T) {
	t.Parallel()
	t.Run("range", func(t *testing.T) {
		t.Parallel()
		cause := errors.New("range failed")
		git := &keepRangeFailureGit{t: t, cause: cause}
		input := []LandedCommit{{SourceSHA: "first", LandedSHA: "existing-pair", Subject: "kept", Kept: true}, {SourceSHA: "second", Subject: "aggregate"}}
		before := append([]LandedCommit(nil), input...)
		got, err := MapLandedCommits(context.Background(), git, nil, "owned-canonical", "target", "merge-base", input)
		if !errors.Is(err, cause) || len(got) != len(input) || &got[0] != &input[0] || !reflect.DeepEqual(got, before) {
			t.Fatalf("partial identities %v, error %v", got, err)
		}
		commits, err := commitsBetween(context.Background(), git, "owned-canonical", "merge-base", "target")
		if commits != nil || !errors.Is(err, cause) || git.calls != 2 {
			t.Fatalf("range result %v %v, calls %d", commits, err, git.calls)
		}
	})
	t.Run("patch", func(t *testing.T) {
		t.Parallel()
		cause := errors.New("pipeline failed")
		canonical, commit := "owned ' canonical", "0123456789abcdef0123456789abcdef01234567'9"
		run := runnertest.New(t)
		script := "git -C " + shellQuote(canonical) + " diff-tree -p --no-color " + shellQuote(commit) + " | git patch-id --stable"
		run.Expect(func(call runnertest.Call) bool {
			return call.Op == "RunOpts" && call.Dir == "" && reflect.DeepEqual(call.Argv(), []string{"sh", "-c", script}) && reflect.DeepEqual(call.Opts.Env, console.Env()) && !call.Opts.CaptureCombined
		}, runner.Result{}, cause)
		got, err := patchIdentity(context.Background(), run, canonical, commit)
		if got != "" || !errors.Is(err, cause) || !strings.Contains(err.Error(), "patch identity of "+shortMergeRevision(commit)) {
			t.Fatalf("patch identity %q %v", got, err)
		}
	})
}

func TestKeepRewriteInventoryErrorsPrecedeGit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	canonical, task, found, err := locateBranchCheckout(ctx, " ", "acme/app", "feature/source", "main")
	if canonical != "" || task != "" || found || err == nil || !strings.Contains(err.Error(), "projects root is required") {
		t.Fatalf("inventory %q %q %v %v", canonical, task, found, err)
	}
	sha := strings.Repeat("a", 40)
	view := githubchecks.PullRequestView{}
	view.Head.Ref, view.Base.Ref = "feature/source", "main"
	landed, head, refusal, landErr := landKeepingCommits(ctx, PullRequestLandOptions{ProjectsRoot: " ", Repository: "acme/app", KeepCommits: []string{sha}}, view, []SourceCommit{{SHA: sha, Subject: "kept"}}, "7", "reviewer")
	if landed != nil || head != "" || refusal != nil || landErr == nil || landErr.Error() != err.Error() {
		t.Fatalf("land inventory %v %q %+v %v", landed, head, refusal, landErr)
	}
}

func TestKeepRewriteUnknownKeptCommitRefusesBeforeInventory(t *testing.T) {
	t.Parallel()
	landed, head, refusal, err := landKeepingCommits(context.Background(), PullRequestLandOptions{Repository: "acme/app", KeepCommits: []string{"unknown"}}, githubchecks.PullRequestView{}, []SourceCommit{{SHA: strings.Repeat("a", 40)}}, "7", "reviewer")
	if landed != nil || head != "" || err != nil || refusal == nil || refusal.code != LandRefusalKeepUnknownCommit || refusal.command != "wb pr land acme/app#7 --keep-commits <a commit on this branch> --reason \"…\"" || !strings.Contains(refusal.reason, "unknown") {
		t.Fatalf("planning refusal %v %q %+v %v", landed, head, refusal, err)
	}
}
