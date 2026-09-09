package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAbortDiscardedAcceptsStrictSquashAbsorptionPullRequest(t *testing.T) {
	fixture, created, _, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "abort-absorbed-squash")
	integrationHead := gitTestOutput(t, fixture.canonical, "rev-parse", "integration/abort-absorbed-squash")
	installAbsorbingPullRequestFixture(t, integrationHead, squashSHA, mergedAt)

	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "abort-absorbed-squash",
		Disposition: AbortDiscarded, AbsorbedBy: "77", DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || !results[0].WorktreeGone || !results[0].BranchDeleted ||
		!results[0].AbsorbedAtOrigin || results[0].AbsorbedBySHA != squashSHA {
		t.Fatalf("strict absorbed abort = %#v", results)
	}
	proof := results[0].MergedPullRequest
	if proof == nil || proof.Number != 77 || proof.Repository != "acme/app" || proof.HeadSHA != integrationHead || proof.MergeSHA != squashSHA || proof.Base != "main" {
		t.Fatalf("persisted pull request proof = %#v", proof)
	}
	if _, err := os.Stat(created.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("absorbed worktree remains: %v", err)
	}
}

func TestAbortDiscardedRefusesInvalidSquashAbsorptionProofs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture *gitFixture, created CreateResult, squashSHA, integrationHead string)
		want   string
	}{
		{
			name: "missing PR head metadata",
			mutate: func(t *testing.T, fixture *gitFixture, created CreateResult, squashSHA, _ string) {
				installAbsorbingPullRequestFixture(t, "", squashSHA, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
			},
			want: "invalid head commit",
		},
		{
			name: "merge is absent from fetched target",
			mutate: func(t *testing.T, fixture *gitFixture, created CreateResult, squashSHA, integrationHead string) {
				gitTest(t, fixture.canonical, "push", "--force", "origin", squashSHA+"^:refs/heads/main")
				installAbsorbingPullRequestFixture(t, integrationHead, squashSHA, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
			},
			want: "not contained in the exact fetched origin/main target",
		},
		{
			name: "source head is not in the PR head",
			mutate: func(t *testing.T, fixture *gitFixture, created CreateResult, squashSHA, integrationHead string) {
				if err := os.WriteFile(filepath.Join(created.WorktreeDir, "after-integration.txt"), []byte("not in PR\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitTest(t, created.WorktreeDir, "add", "after-integration.txt")
				gitTest(t, created.WorktreeDir, "commit", "-m", "advance source outside PR")
				gitTest(t, created.WorktreeDir, "push", "origin", created.Branch)
				installAbsorbingPullRequestFixture(t, integrationHead, squashSHA, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
			},
			want: "does not contain exact source head",
		},
		{
			name: "PR ref does not match GitHub head metadata",
			mutate: func(t *testing.T, fixture *gitFixture, created CreateResult, squashSHA, integrationHead string) {
				gitTest(t, fixture.remote, "update-ref", "refs/pull/77/head", squashSHA)
				installAbsorbingPullRequestFixture(t, integrationHead, squashSHA, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
			},
			want: "origin advertises refs/pull/77/head",
		},
		{
			name: "PR is not merged",
			mutate: func(t *testing.T, fixture *gitFixture, created CreateResult, squashSHA, integrationHead string) {
				installAbsorbingPullRequestFixture(t, integrationHead, squashSHA, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
				payload, err := json.Marshal(map[string]any{
					"number": 77, "html_url": "https://github.com/acme/app/pull/77", "state": "open",
					"merged_at": nil, "head": map[string]any{"ref": "app-main-merger", "sha": integrationHead},
					"base": map[string]any{"ref": "main", "sha": ""}, "merge_commit_sha": squashSHA,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv("WB_TEST_SINGLE_PULL", string(payload))
			},
			want: "is not merged",
		},
		{
			name: "PR head tree differs from merge tree",
			mutate: func(t *testing.T, fixture *gitFixture, created CreateResult, squashSHA, _ string) {
				gitTest(t, fixture.canonical, "checkout", "integration/abort-absorbed-refusal")
				if err := os.WriteFile(filepath.Join(fixture.canonical, "post-pr.txt"), []byte("not squashed\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				gitTest(t, fixture.canonical, "add", "post-pr.txt")
				gitTest(t, fixture.canonical, "commit", "-m", "post PR head drift")
				updatedIntegrationHead := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
				gitTest(t, fixture.canonical, "push", "origin", "HEAD:refs/heads/integration/abort-absorbed-refusal")
				gitTest(t, fixture.remote, "update-ref", "refs/pull/77/head", updatedIntegrationHead)
				gitTest(t, fixture.canonical, "checkout", "main")
				installAbsorbingPullRequestFixture(t, updatedIntegrationHead, squashSHA, time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC))
			},
			want: "does not equal merge tree",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, created, _, squashSHA, _ := prepareAbsorbedCandidate(t, "abort-absorbed-refusal")
			integrationHead := gitTestOutput(t, fixture.canonical, "rev-parse", "integration/abort-absorbed-refusal")
			test.mutate(t, fixture, created, squashSHA, integrationHead)

			results, err := Abort(context.Background(), AbortOptions{
				ProjectsRoot: fixture.projectsRoot, Task: "abort-absorbed-refusal",
				Disposition: AbortDiscarded, AbsorbedBy: "77", DeleteRemote: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Eligible || !strings.Contains(results[0].Reason, test.want) {
				t.Fatalf("refused strict absorbed abort = %#v, want %q", results, test.want)
			}
			if _, err := os.Stat(created.WorktreeDir); err != nil {
				t.Fatalf("refused proof changed worktree: %v", err)
			}
		})
	}
}

func TestAbortDiscardedRefusesSquashSourceAdvanceAtRemovalBoundary(t *testing.T) {
	fixture, created, _, squashSHA, mergedAt := prepareAbsorbedCandidate(t, "abort-absorbed-source-race")
	integrationHead := gitTestOutput(t, fixture.canonical, "rev-parse", "integration/abort-absorbed-source-race")
	installAbsorbingPullRequestFixture(t, integrationHead, squashSHA, mergedAt)

	_, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "abort-absorbed-source-race",
		Disposition: AbortDiscarded, AbsorbedBy: "77", DeleteRemote: true, Apply: true,
		beforeAbortRemoval: func(worktree string) {
			if err := os.WriteFile(filepath.Join(worktree, "late-source.txt"), []byte("advanced after proof\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitTest(t, worktree, "add", "late-source.txt")
			gitTest(t, worktree, "commit", "-m", "advance source after preflight")
			gitTest(t, worktree, "push", "origin", created.Branch)
		},
	})
	if err == nil || !strings.Contains(err.Error(), "branch head moved") {
		t.Fatalf("source advance abort error = %v", err)
	}
	if _, err := os.Stat(created.WorktreeDir); err != nil {
		t.Fatalf("source advance removed worktree: %v", err)
	}
}

func TestAbortMultiRepositoryRequiresExplicitSelection(t *testing.T) {
	fixture := newGitFixture(t)
	otherCanonical := filepath.Join(fixture.projectsRoot, "acme", "storage")
	if err := os.MkdirAll(filepath.Dir(otherCanonical), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(otherCanonical), "clone", fixture.remote, otherCanonical)
	configureGitUser(t, otherCanonical)

	created, err := Create(context.Background(), []string{"acme/app", "acme/storage"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "explicit-abort-selection", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %#v", created)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "explicit-abort-selection", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true,
	})
	if err == nil {
		t.Fatalf("unselected multi-repository abort succeeded: %#v", results)
	}
	for _, result := range created {
		if _, statErr := os.Stat(result.WorktreeDir); statErr != nil {
			t.Fatalf("unselected abort changed %s: %v", result.Repository, statErr)
		}
	}
	results, err = Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "explicit-abort-selection", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true, All: true,
	})
	if err != nil || len(results) != 2 || !results[0].Applied || !results[1].Applied {
		t.Fatalf("explicit all-member abort = %#v, err=%v", results, err)
	}
}

func TestAbortIgnoresForeignDebrisButRefusesAmbiguousWBPath(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "foreign-debris-abort", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	taskRoot := filepath.Join(fixture.home, "worktrees", "foreign-debris-abort")
	foreign := filepath.Join(taskRoot, "foreign", "debris")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "foreign-debris-abort", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatalf("foreign debris blocked real abort: %v, results=%#v", err, results)
	}
	if len(results) != 1 || !results[0].Applied || !results[0].WorktreeGone {
		t.Fatalf("foreign-debris abort result = %#v", results)
	}
	if _, statErr := os.Stat(foreign); statErr != nil {
		t.Fatalf("foreign debris was not retained: %v", statErr)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("real worktree remains: %v", statErr)
	}

	// A path whose canonical repository exists is WB-shaped corruption, not
	// harmless foreign debris, and must remain a blocking diagnostic.
	fixture = newGitFixture(t)
	otherCanonical := filepath.Join(fixture.projectsRoot, "acme", "storage")
	if err := os.MkdirAll(filepath.Dir(otherCanonical), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(otherCanonical), "clone", fixture.remote, otherCanonical)
	configureGitUser(t, otherCanonical)
	created, err = Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "ambiguous-debris-abort", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ambiguous := filepath.Join(fixture.home, "worktrees", "ambiguous-debris-abort", "acme", "storage")
	if err := os.MkdirAll(ambiguous, 0o755); err != nil {
		t.Fatal(err)
	}
	results, err = Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "ambiguous-debris-abort", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true,
	})
	if err == nil {
		t.Fatalf("ambiguous WB-shaped path did not fail closed: %#v", results)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("failed-close abort changed real worktree: %v", statErr)
	}
}

func TestAbortDiscardedResumesExactBranchAfterWorktreeRemoval(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "discard-resume-after-remove", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected abort crash after worktree removal")
	first, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "discard-resume-after-remove",
		Disposition: AbortDiscarded, DeleteRemote: true, Apply: true,
		afterAbortWorktreeRemoval: func(string) error { return injected },
	})
	if !errors.Is(err, injected) {
		t.Fatalf("abort interruption = %v, want %v", err, injected)
	}
	if len(first) != 1 || !first[0].WorktreeGone || first[0].BranchDeleted || first[0].BacklogID == "" {
		t.Fatalf("interrupted abort = %#v", first)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created[0].Branch); branchErr != nil || !exists {
		t.Fatalf("interrupted abort branch exists=%t err=%v", exists, branchErr)
	}

	resumed, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "discard-resume-after-remove",
		Disposition: AbortDiscarded, DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || !resumed[0].Applied || !resumed[0].WorktreeGone || !resumed[0].BranchDeleted || resumed[0].BacklogID == "" {
		t.Fatalf("resumed abort = %#v", resumed)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created[0].Branch); branchErr != nil || exists {
		t.Fatalf("resumed abort branch exists=%t err=%v", exists, branchErr)
	}
}

// TestAbortDiscardedUnusedWorktreesIsAudited covers the common storage-agent
// failure shape: two untouched worktrees were claimed but never started, so
// they cannot have merged PR evidence and must not become abandoned branches.
func TestAbortDiscardedUnusedWorktreesIsAudited(t *testing.T) {
	fixture := newGitFixture(t)
	otherCanonical := filepath.Join(fixture.projectsRoot, "acme", "storage")
	gitTest(t, fixture.projectsRoot, "clone", fixture.remote, otherCanonical)
	created, err := Create(context.Background(), []string{"acme/app", "acme/storage"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "unused-storage", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %#v", created)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "unused-storage", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true, All: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("abort = %#v", results)
	}
	for _, result := range results {
		if !result.Applied || !result.WorktreeGone || !result.BranchDeleted {
			t.Fatalf("abort result = %#v", result)
		}
	}
	for _, create := range created {
		if _, err := os.Stat(create.WorktreeDir); !os.IsNotExist(err) {
			t.Fatalf("discarded worktree remains: %v", err)
		}
		canonical := fixture.canonical
		if create.Repository == "acme/storage" {
			canonical = otherCanonical
		}
		if exists, err := localBranchExists(context.Background(), canonical, create.Branch); err != nil || exists {
			t.Fatalf("discarded branch exists=%t err=%v", exists, err)
		}
	}
	terminal := filepath.Join(fixture.home, "worklogs", "unused-storage", "runs")
	entries, err := os.ReadDir(terminal)
	if err != nil || len(entries) != 1 {
		t.Fatalf("terminal archive directory = %#v err=%v", entries, err)
	}
	terminals, err := os.ReadDir(filepath.Join(terminal, entries[0].Name(), "terminals"))
	if err != nil || len(terminals) != 2 {
		t.Fatalf("sealed terminal cardinality = %d err=%v, want 2", len(terminals), err)
	}
	for _, terminal := range terminals {
		if !validClaimID(strings.TrimSuffix(terminal.Name(), ".json")) {
			t.Fatalf("invalid terminal claim name: %s", terminal.Name())
		}
	}
}

// TestAbortFilterProcessesUnblockedRepoAndLeavesBlockedRepoIntact keeps the
// coordinated member-filter contract while dirty discard is now protected by
// a private byte capture.
func TestAbortFilterProcessesUnblockedRepoAndLeavesBlockedRepoIntact(t *testing.T) {
	fixture := newGitFixture(t)
	otherCanonical := filepath.Join(fixture.projectsRoot, "acme", "storage")
	gitTest(t, fixture.projectsRoot, "clone", fixture.remote, otherCanonical)
	created, err := Create(context.Background(), []string{"acme/app", "acme/storage"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "filtered-abort", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var storage CreateResult
	for _, create := range created {
		if create.Repository == "acme/storage" {
			storage = create
		}
	}
	if storage.WorktreeDir == "" {
		t.Fatalf("created = %#v, missing acme/storage", created)
	}
	// Simulate a dirty repository whose bytes must be sealed before discard.
	if err := os.WriteFile(filepath.Join(storage.WorktreeDir, "WIP.md"), []byte("dead wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "filtered-abort", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true, Filter: "acme/app",
	})
	if err != nil {
		t.Fatalf("filtered abort failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("filtered abort results = %#v", results)
	}
	var app, blocked AbortResult
	for _, result := range results {
		switch result.Repository {
		case "acme/app":
			app = result
		case "acme/storage":
			blocked = result
		}
	}
	if app.Excluded || !app.Applied || !app.WorktreeGone || !app.BranchDeleted {
		t.Fatalf("filtered-in repository did not complete: %#v", app)
	}
	if !blocked.Excluded || blocked.Applied || blocked.WorktreeGone || blocked.BranchDeleted {
		t.Fatalf("filtered-out repository was touched: %#v", blocked)
	}
	if blocked.Reason != "" {
		t.Fatalf("filtered-out repository unexpectedly reported a block: %#v", blocked)
	}
	if _, err := os.Stat(storage.WorktreeDir); err != nil {
		t.Fatalf("filtered-out worktree was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storage.WorktreeDir, "WIP.md")); err != nil {
		t.Fatalf("filtered-out worktree lost its local change: %v", err)
	}
	if exists, branchErr := localBranchExists(context.Background(), otherCanonical, storage.Branch); branchErr != nil || !exists {
		t.Fatalf("filtered-out branch exists=%t err=%v", exists, branchErr)
	}

	// The remaining dirty repository resolves on a later, unfiltered abort;
	// its exact bytes are represented in the durable private Work Log receipt.
	finished, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "filtered-abort", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatalf("follow-up abort of the remaining repository failed: %v", err)
	}
	if len(finished) != 1 || finished[0].Excluded || !finished[0].Applied || !finished[0].WorktreeGone || !finished[0].BranchDeleted || finished[0].DirtyCapture == nil {
		t.Fatalf("follow-up abort = %#v", finished)
	}
}

// TestAbortDiscardedResolvesSymlinkedProjectsRoot is the regression for a bug
// where preflightAbortRepository and applyDiscardedAbort's
// inspectLifecycleWorktree calls used the caller's raw options.ProjectsRoot
// instead of Abort's own resolved projectsRoot (the one normalizeListOptions
// already produced via absoluteProjectsRoot, exactly as Cleanup's equivalent
// preflight already did). A canonical clone's Git-plumbing-derived owner is
// resolved through any ancestor symlink, so whenever the caller passes an
// unresolved --projects-root that reaches its clones through one -- which the
// CLI does naturally, and which macOS's own tmp directory does via
// /var -> /private/var -- the two sides disagreed and abort refused every
// worktree with "canonical clone owner \"\"". Using an explicit symlink here
// keeps the regression deterministic on any platform, not only macOS.
func TestAbortDiscardedResolvesSymlinkedProjectsRoot(t *testing.T) {
	fixture := newGitFixture(t)
	alias := filepath.Join(filepath.Dir(fixture.projectsRoot), "projects-alias")
	if err := os.Symlink(fixture.projectsRoot, alias); err != nil {
		t.Fatal(err)
	}
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: alias, Operation: "symlinked-abort", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: alias, Task: "symlinked-abort", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || !results[0].WorktreeGone || !results[0].BranchDeleted {
		t.Fatalf("abort through a symlinked projects root = %#v", results)
	}
	if _, err := os.Stat(created[0].WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("discarded worktree remains: %v", err)
	}
}

func TestAbortDiscardedRetiresExactRemoteBranchOnlyWithExplicitAuthorization(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "pushed-discard", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, created[0].WorktreeDir, "push", "-u", "origin", created[0].Branch)

	_, err = Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "pushed-discard",
		Disposition:  AbortDiscarded,
		Apply:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "--remote") {
		t.Fatalf("discard without remote authorization error = %v", err)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("refused discard changed worktree: %v", statErr)
	}
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created[0].Branch); remoteErr != nil || remoteHead == "" {
		t.Fatalf("refused discard changed remote branch: head=%q err=%v", remoteHead, remoteErr)
	}

	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "pushed-discard",
		Disposition:  AbortDiscarded,
		DeleteRemote: true,
		Apply:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || !results[0].RemoteDeleted || !results[0].WorktreeGone || !results[0].BranchDeleted {
		t.Fatalf("discard result = %#v", results)
	}
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created[0].Branch); remoteErr != nil || remoteHead != "" {
		t.Fatalf("discard left remote branch at %q: %v", remoteHead, remoteErr)
	}
}

func TestAbortDiscardedRechecksDirtyStateAtRemovalBoundary(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "raced-discard", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	var writeErr error
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "raced-discard",
		Disposition:  AbortDiscarded,
		DeleteRemote: true,
		Apply:        true,
		beforeAbortRemoval: func(worktree string) {
			writeErr = os.WriteFile(filepath.Join(worktree, "raced.md"), []byte("concurrent writer\n"), 0o644)
		},
	})
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "dirty worktree bytes changed") {
		t.Fatalf("raced discard error = %v, results=%#v", err, results)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("raced discard removed worktree: %v", statErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created[0].Branch); branchErr != nil || !exists {
		t.Fatalf("raced discard removed branch: exists=%t err=%v", exists, branchErr)
	}
	after, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before || after.Lifecycle != "active" {
		t.Fatalf("raced discard changed active projection: before=%#v after=%#v", before, after)
	}
}

func TestAbortDiscardedSealsTrackedAndUntrackedBytes(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "dirty-capture", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tracked := []byte("tracked dirty bytes\n")
	untracked := []byte("untracked dirty bytes\n")
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "README.md"), tracked, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "WIP.md"), untracked, 0o600); err != nil {
		t.Fatal(err)
	}
	dry, err := Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: "dirty-capture", Disposition: AbortDiscarded})
	if err != nil || len(dry) != 1 || dry[0].DirtyCapture == nil || !dry[0].Eligible {
		t.Fatalf("dirty dry-run = %#v, err=%v", dry, err)
	}
	apply, err := Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: "dirty-capture", Disposition: AbortDiscarded, DeleteRemote: true, Apply: true})
	if err != nil || len(apply) != 1 || apply[0].DirtyCapture == nil || *apply[0].DirtyCapture != *dry[0].DirtyCapture {
		t.Fatalf("dirty apply = %#v, err=%v", apply, err)
	}
	var manifest dirtyCaptureManifest
	var manifestPath string
	worklogs := filepath.Join(fixture.home, "worklogs")
	if err := filepath.WalkDir(worklogs, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == "manifest.json" {
			manifestPath = path
			return nil
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if manifestPath == "" {
		t.Fatal("dirty capture manifest was not retained in private Work Log")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil || json.Unmarshal(raw, &manifest) != nil {
		t.Fatalf("dirty capture manifest = %s, err=%v", raw, err)
	}
	if manifest.Receipt != *apply[0].DirtyCapture || len(manifest.Entries) != 2 {
		t.Fatalf("dirty capture receipt = %#v, result=%#v", manifest.Receipt, apply[0].DirtyCapture)
	}
	for _, entry := range manifest.Entries {
		if entry.Kind != "file" || entry.Blob == "" {
			t.Fatalf("dirty capture entry = %#v", entry)
		}
		content, err := os.ReadFile(filepath.Join(filepath.Dir(manifestPath), entry.Blob))
		if err != nil {
			t.Fatal(err)
		}
		want := tracked
		if entry.Path == "WIP.md" {
			want = untracked
		}
		if !bytes.Equal(content, want) {
			t.Fatalf("captured %s = %q, want %q", entry.Path, content, want)
		}
	}
}

func TestAbortDiscardedRefusesOversizeDirtyCapture(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "oversize-dirty-capture", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "oversize.bin"), make([]byte, maxDirtyCaptureFileBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	results, err := Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: "oversize-dirty-capture", Disposition: AbortDiscarded, DeleteRemote: true, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "bounded") {
		t.Fatalf("oversize discard = %#v, err=%v", results, err)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("oversize refusal removed worktree: %v", statErr)
	}
}

func TestAbortNotLandedSealsButRetainsResumableWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "resume-storage", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "resume-storage", Disposition: AbortNotLanded,
		Successor: "codex-resume-2", Apply: true,
		SuccessorIdentity: ClaimExecutionIdentity{Model: "gpt-5.6-terra", CLI: "opencode", Provider: "opencode-go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || results[0].WorktreeGone || results[0].BranchDeleted {
		t.Fatalf("abort = %#v", results)
	}
	if _, err := os.Stat(created[0].WorktreeDir); err != nil {
		t.Fatalf("resumable worktree missing: %v", err)
	}
	after, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Lifecycle != "active" || after.ClaimID == before.ClaimID {
		t.Fatalf("successor projection = %#v, prior = %#v", after, before)
	}
	runDir := filepath.Join(fixture.home, "worklogs", after.EffortID, "runs", after.RunID)
	claims, err := os.ReadDir(filepath.Join(runDir, "claims"))
	if err != nil || len(claims) != 2 {
		t.Fatalf("claim transfer cardinality = %d err=%v, want 2", len(claims), err)
	}
	claimBytes, err := os.ReadFile(filepath.Join(runDir, "claims", after.ClaimID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var successor workLogClaim
	if err := json.Unmarshal(claimBytes, &successor); err != nil {
		t.Fatal(err)
	}
	if successor.Version != 2 || successor.Model != "gpt-5.6-terra" || successor.ModelProvenance != modelProvenanceCallerDeclared ||
		successor.ModelDeclaredBy != "codex-resume-2" || successor.CLI != "opencode" || successor.Provider != "opencode-go" || successor.AgentRuntime != "" {
		t.Fatalf("successor execution identity = %#v", successor)
	}
	terminals, err := os.ReadDir(filepath.Join(runDir, "terminals"))
	if err != nil || len(terminals) != 1 {
		t.Fatalf("terminal transfer cardinality = %d err=%v, want 1", len(terminals), err)
	}
}

func TestAbortHandoffCrashBindsSuccessorExecutionIdentity(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "handoff-crash-binding",
		WorkLog: WorkLogOptions{RunID: "handoff-crash-run", Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(created[0].WorkLogPath)
	if err != nil {
		t.Fatal(err)
	}
	var claim workLogClaim
	if err := json.Unmarshal(claimBytes, &claim); err != nil {
		t.Fatal(err)
	}
	head := gitTestOutput(t, created[0].WorktreeDir, "rev-parse", "HEAD")
	first := ClaimExecutionIdentity{Model: "gpt-5.6-terra", CLI: "opencode", Provider: "opencode-go"}
	drifted := ClaimExecutionIdentity{Model: "gpt-5.6-terra", CLI: "opencode", Provider: "openai-codex"}
	firstID := declaredSuccessorWorkLogClaimID(claim.ClaimID, "next-run", "handoff", first)
	driftedID := declaredSuccessorWorkLogClaimID(claim.ClaimID, "next-run", "handoff", drifted)
	if firstID == driftedID {
		t.Fatal("same model with different commercial routes produced one successor identity")
	}
	runDir, _, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeWorkLogTerminal(fixture.home, runDir, claim, head, "handoff", firstID, "next-run", nil); err != nil {
		_ = runDir.Close()
		t.Fatal(err)
	}
	_ = runDir.Close() // simulate a crash before successor claim publication

	err = transferWorkLogClaim(fixture.home, created[0].WorktreeDir, head, "handoff", "next-run", drifted)
	if err == nil || !strings.Contains(err.Error(), "immutable terminal conflicts") {
		t.Fatalf("identity drift after sealed crash = %v", err)
	}
	afterDrift, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil || afterDrift != projection {
		t.Fatalf("drifted retry changed projection: before=%#v after=%#v err=%v", projection, afterDrift, err)
	}
	claimsDir := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims")
	if _, err := os.Stat(filepath.Join(claimsDir, driftedID+".json")); !os.IsNotExist(err) {
		t.Fatalf("drifted retry published a successor claim: %v", err)
	}
	if err := transferWorkLogClaim(fixture.home, created[0].WorktreeDir, head, "handoff", "next-run", first); err != nil {
		t.Fatalf("same-identity crash retry: %v", err)
	}
	afterRetry, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil || afterRetry.ClaimID != firstID || afterRetry.Lifecycle != "active" {
		t.Fatalf("same-identity retry projection = %#v err=%v", afterRetry, err)
	}
}

func TestAbortResumableRequiresSuccessorAndExplicitModel(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "needs-successor", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: "needs-successor", Disposition: AbortHandoff, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "--successor") {
		t.Fatalf("missing successor error = %v", err)
	}
	_, err = Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: "needs-successor", Disposition: AbortHandoff, Successor: "next-run", Apply: true})
	if err == nil || !strings.Contains(err.Error(), "--model is required") {
		t.Fatalf("missing successor model error = %v", err)
	}
	_, err = Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "needs-successor", Disposition: AbortHandoff,
		Successor: "next-run", Apply: true,
		SuccessorIdentity: ClaimExecutionIdentity{Model: "gpt-5.6-sol", Provider: "xoxb-synthetic-credential"},
	})
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("credential-shaped successor provider error = %v", err)
	}
	after, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("missing model changed claim projection: before=%#v after=%#v", before, after)
	}
	runDir := filepath.Join(fixture.home, "worklogs", before.EffortID, "runs", before.RunID)
	if terminals, readErr := os.ReadDir(filepath.Join(runDir, "terminals")); !os.IsNotExist(readErr) || len(terminals) != 0 {
		t.Fatalf("missing model published terminal records: entries=%#v err=%v", terminals, readErr)
	}
}
