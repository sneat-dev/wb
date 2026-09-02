package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On 2026-09-02 a reviewer's checkout inside an author's worktree left HEAD on
// a commit no branch reached. `git push` printed "Everything up-to-date" —
// truthfully, because the branch ref it pushed genuinely had not moved — and
// the work was orphaned behind a success message.
//
// Git offers no post-push hook, and it runs pre-push only when it has refs to
// update, so no hook can ever catch this. Only a verification after the push
// can, and these tests pin it.

func TestGuardPublicationConfirmsAPushedWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "publication-ok",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "publication-ok")

	result, err := Guard(context.Background(), worktree, GuardOptions{
		ProjectsRoot: fixture.projectsRoot, CheckPublication: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !PublicationVerified(result.Publication) {
		t.Fatalf("publication = %#v, want published", result.Publication)
	}
	if finding := PublicationFinding(result.Publication, result.Branch); finding != "" {
		t.Fatalf("finding = %q, want none", finding)
	}
}

// The exact incident: a commit exists locally and the branch was never pushed
// since. WB must call that unpublished and name the push that fixes it.
func TestGuardPublicationCatchesAnUnpushedCommit(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "publication-ahead",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "publication-ahead")
	if err := os.WriteFile(filepath.Join(worktree, "orphan.txt"), []byte("work nobody can see\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "orphan.txt")
	gitTest(t, worktree, "commit", "-m", "work that must not be lost")

	result, err := Guard(context.Background(), worktree, GuardOptions{
		ProjectsRoot: fixture.projectsRoot, CheckPublication: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if PublicationVerified(result.Publication) {
		t.Fatalf("publication = %#v, want unpublished", result.Publication)
	}
	if result.Publication.Status != PublicationUnpublished || result.Publication.Ahead != 1 {
		t.Fatalf("publication = %#v, want 1 commit ahead", result.Publication)
	}
	finding := PublicationFinding(result.Publication, result.Branch)
	// The remedy is the point. This failure already printed a success message
	// once; "unpublished" on its own is the same non-answer a second time.
	for _, want := range []string{"NOT on the remote", "git push origin HEAD:publication-ahead", "Everything up-to-date"} {
		if !strings.Contains(finding, want) {
			t.Fatalf("finding = %q, missing %q", finding, want)
		}
	}
}

// A branch that was never pushed at all is a different diagnosis with a
// different remedy (-u), and must not be reported as merely "ahead".
func TestGuardPublicationCatchesANeverPushedBranch(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "publication-unborn",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir

	result, err := Guard(context.Background(), worktree, GuardOptions{
		ProjectsRoot: fixture.projectsRoot, CheckPublication: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if PublicationVerified(result.Publication) {
		t.Fatalf("publication = %#v, want unpublished", result.Publication)
	}
	finding := PublicationFinding(result.Publication, result.Branch)
	if !strings.Contains(finding, "has ever been published") || !strings.Contains(finding, "git push -u origin publication-unborn") {
		t.Fatalf("finding = %q", finding)
	}
}

// Publication stays opt-in: a commit or push hook must never depend on
// reaching origin, so guard without the flag performs no network call and
// reports nothing about publication.
func TestGuardWithoutTheFlagNeverChecksPublication(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "publication-optin",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := Guard(context.Background(), created[0].WorktreeDir, GuardOptions{
		ProjectsRoot: fixture.projectsRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Publication != nil {
		t.Fatalf("publication = %#v, want nothing checked", result.Publication)
	}
}

// "WB could not observe it" must never render as "published". An offline or
// failed check is the state in which an operator is most likely to assume the
// push worked.
func TestPublicationFindingNeverPassesAnUnobservableCheck(t *testing.T) {
	t.Parallel()
	for _, status := range []string{
		CanonicalFreshnessOffline, CanonicalFreshnessFetchError, CanonicalFreshnessDrifted,
	} {
		receipt := &CanonicalFreshness{Status: status, RemoteRef: "origin/feature", Error: "boom"}
		if PublicationVerified(receipt) {
			t.Fatalf("status %q must not count as published", status)
		}
		finding := PublicationFinding(receipt, "feature")
		if !strings.Contains(finding, "could not be verified") || !strings.Contains(finding, "boom") {
			t.Fatalf("status %q finding = %q", status, finding)
		}
	}
	if PublicationVerified(nil) {
		t.Fatal("a nil receipt must not count as published")
	}
	if finding := PublicationFinding(nil, "feature"); finding != "" {
		t.Fatalf("nil receipt finding = %q, want none (nothing was asked)", finding)
	}
}

func TestPublicationFindingExplainsBehindAndDiverged(t *testing.T) {
	t.Parallel()
	behind := PublicationFinding(&CanonicalFreshness{
		Status: PublicationBehind, RemoteRef: "origin/feature", Behind: 2,
	}, "feature")
	if !strings.Contains(behind, "2 commit(s) ahead of HEAD") || !strings.Contains(behind, "Reconcile") {
		t.Fatalf("behind finding = %q", behind)
	}
	diverged := PublicationFinding(&CanonicalFreshness{
		Status: PublicationDiverged, RemoteRef: "origin/feature", Ahead: 3, Behind: 4,
	}, "feature")
	if !strings.Contains(diverged, "diverged (3 local, 4 remote)") {
		t.Fatalf("diverged finding = %q", diverged)
	}
}

// A detached HEAD is where the orphaning starts, so the refusal must say what
// happens next, not merely that the state is disallowed.
func TestGuardDetachedHeadRefusalNamesTheOrphaningHazard(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "publication-detached",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "checkout", "--detach", "HEAD")

	_, err = Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot})
	if err == nil {
		t.Fatal("a detached HEAD must still be refused")
	}
	for _, want := range []string{"detached HEAD", "reachable from no branch", "Everything up-to-date"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, missing %q", err, want)
		}
	}
}

// inspectPublication is inspectCanonicalFreshnessWith aimed at the worktree's
// own branch. This pins that it fetches and compares that branch's ref, not
// the base branch, since aiming it at the wrong ref would silently report a
// feature branch as published whenever main happened to match.
func TestPublicationComparesTheWorktreeBranchNotTheBase(t *testing.T) {
	t.Parallel()
	const head = "1111111111111111111111111111111111111111"
	var fetched []string
	result := inspectCanonicalFreshnessWith(context.Background(), "/repo", "feature/x", func(_ context.Context, _ string, args ...string) (string, error) {
		switch args[0] {
		case "rev-parse":
			if args[1] == "HEAD" {
				return head + "\n", nil
			}
			if args[1] != "origin/feature/x" {
				return "", errors.New("unexpected ref " + args[1])
			}
			return head + "\n", nil
		case "fetch":
			fetched = append(fetched, args[len(args)-1])
			return "", nil
		case "rev-list":
			return "0 0\n", nil
		case "ls-remote":
			if args[len(args)-1] != "refs/heads/feature/x" {
				return "", errors.New("unexpected probe " + args[len(args)-1])
			}
			return head + "\trefs/heads/feature/x\n", nil
		}
		return "", errors.New("unexpected git " + strings.Join(args, " "))
	})
	if result.Status != PublicationPublished || result.Target != "feature/x" || result.RemoteRef != "origin/feature/x" {
		t.Fatalf("receipt = %#v", result)
	}
	if len(fetched) != 1 || fetched[0] != "+refs/heads/feature/x:refs/remotes/origin/feature/x" {
		t.Fatalf("fetched = %v, want the worktree's own branch", fetched)
	}
}
