//go:build e2e

package streamsync

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestE2EFastForwardRefusesUnavailableOrChangedGitState(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"remote head", "compare local", "checkout", "merge", "advanced head", "changed advanced head", "compare remote"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			fixture := newRemoteStreamFixture(t)
			native := ExecGit{Timeout: time.Minute}
			if operation == "compare remote" {
				commitFile(t, fixture.local, "local.txt", "local\n", "local advance")
			} else {
				runGit(t, fixture.other, "checkout", "stream/fixture")
				commitFile(t, fixture.other, "remote.txt", "remote\n", "remote advance")
				runGit(t, fixture.other, "push", "origin", "stream/fixture")
				if err := native.Fetch(context.Background(), fixture.local); err != nil {
					t.Fatal(err)
				}
			}
			before := stCovHead(t, fixture.local)
			failure := errors.New("Git command became unavailable")
			localHeads, comparisons, injections := 0, 0, 0
			commands := gitCommands{run: func(ctx context.Context, dir string, args ...string) (string, error) {
				if args[0] == "rev-parse" && args[1] == "stream/fixture" {
					localHeads++
				}
				if args[0] == "merge-base" {
					comparisons++
				}
				fail := operation == "remote head" && args[0] == "rev-parse" && args[1] == "origin/stream/fixture" ||
					operation == "compare local" && args[0] == "merge-base" && comparisons == 1 ||
					operation == "compare remote" && args[0] == "merge-base" && comparisons == 2 ||
					operation == "checkout" && args[0] == "checkout" ||
					operation == "merge" && args[0] == "merge" ||
					operation == "advanced head" && args[0] == "rev-parse" && localHeads == 2
				if operation == "changed advanced head" && args[0] == "rev-parse" && localHeads == 2 {
					// Another writer moves the ref after the native merge succeeds.
					runGit(t, dir, "update-ref", "refs/heads/stream/fixture", before)
					injections++
				}
				if fail {
					injections++
					return "", failure
				}
				return native.run(ctx, dir, args...)
			}}
			head, present, advanced, err := commands.FastForwardToRemote(context.Background(), fixture.local, "stream/fixture", "origin/stream/fixture")
			refused := errors.Is(err, failure)
			if operation == "changed advanced head" {
				refused = err != nil && strings.Contains(err.Error(), "fast-forwarded")
			}
			if !refused || head != "" || present || advanced || injections != 1 {
				t.Fatalf("result=%q,%t,%t,%v; injections=%d", head, present, advanced, err, injections)
			}
			if operation == "advanced head" {
				remote := strings.TrimSpace(runGit(t, fixture.local, "rev-parse", "origin/stream/fixture"))
				if stCovHead(t, fixture.local) != remote {
					t.Fatal("native merge did not complete before verification failed")
				}
			} else if stCovHead(t, fixture.local) != before {
				t.Fatal("failed command changed branch head")
			}
		})
	}
}

func TestE2ERebaseAndCommitPropagateIndexReadFailures(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"rebase", "commit"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			root := stCovScratchRepo(t)
			before := stCovHead(t, root)
			native := ExecGit{Timeout: time.Minute}
			failure := errors.New("index read failed")
			injections := 0
			commands := gitCommands{run: func(ctx context.Context, dir string, args ...string) (string, error) {
				if args[0] == "diff" {
					injections++
					return "", failure
				}
				return native.run(ctx, dir, args...)
			}}
			if operation == "rebase" {
				if paths, err := commands.Rebase(context.Background(), root, "main", "missing-upstream"); !errors.Is(err, failure) || paths != nil {
					t.Fatalf("rebase=%v,%v", paths, err)
				}
			} else {
				if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("new\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if head, committed, err := commands.CommitAll(context.Background(), root, "new"); !errors.Is(err, failure) || head != "" || committed {
					t.Fatalf("commit=%q,%t,%v", head, committed, err)
				}
			}
			if injections != 1 || stCovHead(t, root) != before {
				t.Fatal("failed index read changed commit")
			}
		})
	}
}

func TestE2EPushRejectsUnreadableRemoteHeadAfterPublishing(t *testing.T) {
	t.Parallel()
	fixture := newRemoteStreamFixture(t)
	commitFile(t, fixture.local, "local.txt", "local\n", "local advance")
	native := ExecGit{Timeout: time.Minute}
	failure := errors.New("remote ref read failed")
	injections := 0
	commands := gitCommands{run: func(ctx context.Context, dir string, args ...string) (string, error) {
		if args[0] == "rev-parse" && strings.HasPrefix(args[1], "refs/remotes/") {
			injections++
			return "", failure
		}
		return native.run(ctx, dir, args...)
	}}
	if head, err := commands.PushWithLease(context.Background(), fixture.local, "stream/fixture", ""); !errors.Is(err, failure) || head != "" || injections != 1 {
		t.Fatalf("push=%q,%v; injections=%d", head, err, injections)
	}
	remote := strings.TrimSpace(runGit(t, fixture.other, "ls-remote", "origin", "refs/heads/stream/fixture"))
	if !strings.HasPrefix(remote, stCovHead(t, fixture.local)+"\t") {
		t.Fatalf("native push did not publish: %q", remote)
	}
}
