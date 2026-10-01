//go:build e2e

package streams

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestE2EPushPreservesAncestorCommandFailures(t *testing.T) {
	t.Parallel()
	for _, at := range []int{1, 2} {
		t.Run(map[int]string{1: "local ancestor", 2: "remote ancestor"}[at], func(t *testing.T) {
			t.Parallel()
			local, _ := newPublishedStreamFixture(t)
			commitStreamFile(t, local, "ahead.txt", "ahead\n", "feat: ahead")
			before := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "refs/remotes/origin/stream/recovery"))
			native := ExecGit{Timeout: 30 * time.Second}.commands()
			git := native
			calls := 0
			cause := errors.New("ancestry unavailable")
			git.command = func(ctx context.Context, dir, input string, args ...string) (string, error) {
				if args[0] == "merge-base" {
					calls++
					if calls == at {
						return "", cause
					}
				}
				return native.command(ctx, dir, input, args...)
			}
			_, err := git.PushBranch(context.Background(), local, "stream/recovery")
			if !errors.Is(err, cause) || calls != at {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			after := strings.Fields(runStreamGit(t, local, "ls-remote", "origin", "refs/heads/stream/recovery"))[0]
			if before != after {
				t.Fatal("failed ancestry verification published a branch")
			}
		})
	}
}

func TestE2EPushRejectsMissingPostPublicationTrackingRef(t *testing.T) {
	t.Parallel()
	local, _ := newPublishedStreamFixture(t)
	commitStreamFile(t, local, "ahead.txt", "ahead\n", "feat: ahead")
	head := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD"))
	native := ExecGit{Timeout: 30 * time.Second}.commands()
	git := native
	reads := 0
	git.command = func(ctx context.Context, dir, input string, args ...string) (string, error) {
		if strings.Join(args, " ") == "rev-parse --verify --quiet refs/remotes/origin/stream/recovery" {
			reads++
			if reads == 2 {
				runStreamGit(t, dir, "update-ref", "-d", "refs/remotes/origin/stream/recovery")
			}
		}
		return native.command(ctx, dir, input, args...)
	}
	_, err := git.PushBranch(context.Background(), local, "stream/recovery")
	if err == nil || !strings.Contains(err.Error(), "does not resolve") || reads != 2 {
		t.Fatalf("reads=%d error=%v", reads, err)
	}
	if got := strings.Fields(runStreamGit(t, local, "ls-remote", "origin", "refs/heads/stream/recovery"))[0]; got != head {
		t.Fatalf("remote=%s want=%s", got, head)
	}
}

func TestE2EFastForwardVerifiesMergeAndResultingHead(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"merge", "head", "mismatch"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			local, other := newPublishedStreamFixture(t)
			old := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD"))
			runStreamGit(t, other, "checkout", "stream/recovery")
			commitStreamFile(t, other, "ahead.txt", "ahead\n", "feat: ahead")
			runStreamGit(t, other, "push", "origin", "stream/recovery")
			runStreamGit(t, local, "fetch", "origin")
			remote := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "refs/remotes/origin/stream/recovery"))
			if stage == "mismatch" {
				remote = old
			}
			native := ExecGit{Timeout: 30 * time.Second}.commands()
			git := native
			merged := false
			cause := errors.New("verification command failed")
			git.command = func(ctx context.Context, dir, input string, args ...string) (string, error) {
				if args[0] == "merge" {
					if stage == "merge" {
						return "", cause
					}
					merged = true
				}
				if merged && stage == "head" && strings.Join(args, " ") == "rev-parse HEAD" {
					return "", cause
				}
				return native.command(ctx, dir, input, args...)
			}
			err := git.fastForwardBranch(context.Background(), local, "stream/recovery", remote)
			if err == nil {
				t.Fatal("verification succeeded")
			}
			if stage != "mismatch" && !errors.Is(err, cause) {
				t.Fatalf("cause lost: %v", err)
			}
			if stage == "mismatch" && !strings.Contains(err.Error(), "want fetched origin") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestE2EDeleteVerifiesFetchAndAbsenceAfterPublication(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"fetch", "still present"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			local, _ := newPublishedStreamFixture(t)
			head := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD"))
			native := ExecGit{Timeout: 30 * time.Second}.commands()
			git := native
			cause := errors.New("fetch unavailable")
			git.command = func(ctx context.Context, dir, input string, args ...string) (string, error) {
				if strings.Join(args, " ") == "fetch --quiet --prune origin" {
					if stage == "fetch" {
						return "", cause
					}
					out, err := native.command(ctx, dir, input, args...)
					if err == nil {
						runStreamGit(t, dir, "update-ref", "refs/remotes/origin/stream/recovery", head)
					}
					return out, err
				}
				return native.command(ctx, dir, input, args...)
			}
			err := git.DeleteRemoteBranch(context.Background(), local, "stream/recovery", head)
			if err == nil {
				t.Fatal("deletion verification succeeded")
			}
			if stage == "fetch" && !errors.Is(err, cause) {
				t.Fatalf("cause lost: %v", err)
			}
			if stage != "fetch" && !strings.Contains(err.Error(), "still resolves") {
				t.Fatalf("error=%v", err)
			}
			if got := runStreamGit(t, local, "ls-remote", "origin", "refs/heads/stream/recovery"); strings.TrimSpace(got) != "" {
				t.Fatalf("deletion did not land: %s", got)
			}
		})
	}
}

func TestE2ECommitDiscoveryRetainsSubjectsAndPatchFailureBoundaries(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"subjects", "patch"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root, execGit := gitFixture(t)
			native := execGit.commands()
			git := native
			cause := errors.New("commit evidence unavailable")
			hit := false
			git.command = func(ctx context.Context, dir, input string, args ...string) (string, error) {
				if stage == "subjects" && args[0] == "log" && args[1] == "--no-walk" || stage == "patch" && args[0] == "patch-id" {
					hit = true
					if stage == "patch" && strings.TrimSpace(input) == "" {
						t.Fatal("missing native patch stream")
					}
					return "", cause
				}
				return native.command(ctx, dir, input, args...)
			}
			_, err := git.CommitsNotIn(context.Background(), root, "stream/fixture", "main")
			if !hit || !errors.Is(err, cause) {
				t.Fatalf("hit=%v error=%v", hit, err)
			}
		})
	}
	root, git := gitFixture(t)
	runStreamGit(t, root, "commit", "--allow-empty", "--allow-empty-message", "-m", "")
	head := strings.TrimSpace(runStreamGit(t, root, "rev-parse", "HEAD"))
	subjects, err := git.commands().commitSubjects(context.Background(), root, []string{head})
	if err != nil || len(subjects) != 0 {
		t.Fatalf("subjects=%v error=%v", subjects, err)
	}
}

func TestE2EPushDestinationEvidenceRejectsEmptyAndUnexpectedResults(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"empty", "unexpected"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			local, _ := newPublishedStreamFixture(t)
			native := ExecGit{Timeout: 30 * time.Second}.commands()
			git := native
			hit := false
			git.command = func(ctx context.Context, dir, input string, args ...string) (string, error) {
				if stage == "empty" && strings.Join(args, " ") == "remote get-url --push --all origin" {
					// Verify the native destination lookup before exercising an empty
					// successful response at the existing command boundary.
					out, err := native.command(ctx, dir, input, args...)
					if err != nil || strings.TrimSpace(out) == "" {
						t.Fatalf("native destination lookup: %q, %v", out, err)
					}
					hit = true
					return " \n", nil
				}
				if stage == "unexpected" && args[0] == "ls-remote" {
					hit = true
					return "012345 refs/heads/wrong\n", nil
				}
				return native.command(ctx, dir, input, args...)
			}
			_, err := git.remoteHeadsOnOriginPushDestinations(context.Background(), local, "stream/recovery")
			if err == nil || !hit || stage == "empty" && !strings.Contains(err.Error(), "no URLs") || stage == "unexpected" && (!hit || !strings.Contains(err.Error(), "unexpected response")) {
				t.Fatalf("hit=%v error=%v", hit, err)
			}
		})
	}
}
