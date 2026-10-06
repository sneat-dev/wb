package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestPullRequestUpdateReceiptAllocationPreservesFailuresAndPrivateFiles(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"home", "directory", "create", "close", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			options := PullRequestUpdateOptions{ProjectsRoot: root, Repository: "acme/app", PullRequest: "7"}
			sentinel := errors.New("owned receipt fault")
			var injection *filewrite.Injector
			if stage == "home" {
				loop := filepath.Join(root, "loop")
				if err := os.Symlink(loop, loop); err != nil {
					t.Fatal(err)
				}
				options.ProjectsRoot = loop
			}
			if stage == "directory" {
				if err := os.WriteFile(filepath.Join(root, ".wb"), []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "create" {
				injection = &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: sentinel}
			}
			if stage == "close" {
				injection = &filewrite.Injector{Step: filewrite.StepClose, Err: sentinel}
			}
			got, err := newPullRequestUpdateReceiptPathInjected(options, injection)
			if stage == "success" {
				if err != nil || !strings.HasPrefix(filepath.Base(got), "update-") || filepath.Ext(got) != ".json" {
					t.Fatalf("allocation=%q, %v", got, err)
				}
				data, readErr := os.ReadFile(got)
				info, statErr := os.Stat(got)
				if readErr != nil || statErr != nil || len(data) != 0 || info.Mode().Perm() != 0o600 {
					t.Fatalf("receipt=%q data=%q read=%v stat=%v info=%v", got, data, readErr, statErr, info)
				}
				second, secondErr := newPullRequestUpdateReceiptPath(options)
				if secondErr != nil || second == got {
					t.Fatalf("second allocation=%q, %v", second, secondErr)
				}
				return
			}
			if got != "" || err == nil {
				t.Fatalf("failure allocation=%q, %v", got, err)
			}
			if injection != nil && !errors.Is(err, sentinel) {
				t.Fatalf("lost failure identity: %v", err)
			}
			if stage == "create" || stage == "close" {
				home, homeErr := wbhome.Root(root)
				if homeErr != nil {
					t.Fatal(homeErr)
				}
				files, readErr := os.ReadDir(filepath.Join(home, "reports", "pr-update", "acme", "app", "7"))
				if readErr != nil {
					t.Fatal(readErr)
				}
				want := 0
				if stage == "close" {
					want = 1
				}
				if len(files) != want {
					t.Fatalf("retained allocation files=%v, want %d", files, want)
				}
			}
		})
	}
}

func TestPullRequestUpdateRepositorySegmentsRejectNonCoordinateCharacters(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", ".", "..", "a..b", "a/b", "a b", "é", "a\\b", "a\n"} {
		if validPullRequestUpdateRepositorySegment(value) {
			t.Errorf("accepted repository segment %q", value)
		}
	}
	for _, value := range []string{"a", "ACME-42", "repo_name", "repo.name"} {
		if !validPullRequestUpdateRepositorySegment(value) {
			t.Errorf("rejected repository segment %q", value)
		}
	}
}

func TestPullRequestUpdateLocalLookupReportsFailureWithoutSyncing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	options := PullRequestUpdateOptions{ProjectsRoot: root, Repository: "acme/app"}
	canonical, err := worktrees.CanonicalRepositoryPath(root, options.Repository)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("owned registration lookup fault")
	run := runnertest.New(t).Expect(func(call runnertest.Call) bool {
		return call.Dir == canonical && strings.Join(call.Argv(), " ") == "git worktree list --porcelain"
	}, runner.Result{}, sentinel)
	got := syncOwnedPullRequestUpdateWorktreeWithRunner(context.Background(), options, "feature", "head", run)
	if !strings.Contains(got, "local sync skipped: worktree lookup failed:") || !strings.Contains(got, sentinel.Error()) || run.CallCount() != 1 {
		t.Fatalf("sync note=%q calls=%d", got, run.CallCount())
	}
}

func TestPullRequestUpdateProductionMutationPreservesExpectedHeadAndDiagnostic(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("owned update refusal")
	calls := 0
	ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{
		Execute: func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
			calls++
			want := "api --method PUT repos/acme/app/pulls/7/update-branch -f expected_head_sha=observed-head"
			if dir != "" || strings.Join(args, " ") != want {
				t.Fatalf("mutation dir=%q argv=%q", dir, args)
			}
			return githubobserver.CommandResponse{ExitCode: 1, Err: sentinel, Stderr: []byte("owned refusal diagnostic")}
		},
	})
	head, reason := productionPullRequestUpdateOps().update(ctx, "acme/app", "7", "observed-head")
	if calls != 1 || head != "" || !strings.Contains(reason, "owned refusal diagnostic") {
		t.Fatalf("mutation head=%q reason=%q calls=%d", head, reason, calls)
	}
}
