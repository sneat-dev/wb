package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeAndCommit(t *testing.T, dir, name, content, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", name)
	gitTest(t, dir, "commit", "-m", message)
	return gitTestOutput(t, dir, "rev-parse", "HEAD")
}

// cherryPickWithDifferentMessage applies sourceSHA's diff onto the current
// HEAD with a different commit message, so the resulting commit shares
// sourceSHA's patch-id (content-only) but never collides with its exact SHA.
// A plain `git cherry-pick` run within the same wall-clock second onto an
// identical parent can reuse the very same commit object — same tree, same
// parent, same author/committer identity and date — which would make the
// branch trivially `contained` instead of exercising `absorbed` at all.
func cherryPickWithDifferentMessage(t *testing.T, dir, sourceSHA, message string) string {
	t.Helper()
	gitTest(t, dir, "cherry-pick", "--no-commit", sourceSHA)
	gitTest(t, dir, "commit", "-m", message)
	return gitTestOutput(t, dir, "rev-parse", "HEAD")
}

// TestBranchListClassifiesEveryEvidenceClass is the AC-1 fixture: a branch
// merged into main (contained), a branch cherry-picked into main so its
// patch-id has a twin upstream (absorbed), a branch with real unpushed work
// (unique), a branch checked out in a linked worktree (in-use), and main
// itself (protected). It also proves the sweep never mutates the fixture.
func TestBranchListClassifiesEveryEvidenceClass(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()

	// contained: merge a feature branch into main and push both.
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/merged")
	writeAndCommit(t, fixture.canonical, "merged.txt", "v1\n", "merged work")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "-m", "merge feature/merged", "feature/merged")

	// absorbed: cherry-pick a branch's commit onto main so its patch-id has a
	// twin upstream, but the branch itself is not an ancestor of main.
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/absorbed")
	absorbedSHA := writeAndCommit(t, fixture.canonical, "absorbed.txt", "v1\n", "absorbed work")
	gitTest(t, fixture.canonical, "checkout", "main")
	cherryPickWithDifferentMessage(t, fixture.canonical, absorbedSHA, "landed: absorbed work")

	// unique: a branch with content git cherry proves is not upstream.
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/unique")
	writeAndCommit(t, fixture.canonical, "unique.txt", "v1\n", "unique work")

	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "push", "origin", "main", "feature/merged", "feature/absorbed", "feature/unique")

	// in-use: create a linked worktree on its own branch.
	worktreeDir := filepath.Join(t.TempDir(), "in-use-worktree")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/in-use", worktreeDir, "main")

	before := gitTestOutput(t, fixture.canonical, "show-ref")

	outcome, err := BranchList(ctx, BranchListOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: "all"})
	if err != nil {
		t.Fatal(err)
	}

	after := gitTestOutput(t, fixture.canonical, "show-ref")
	if before != after {
		t.Fatalf("BranchList mutated refs:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, statErr := os.Stat(worktreeDir); statErr != nil {
		t.Fatalf("BranchList affected the linked worktree: %v", statErr)
	}

	got := map[string]string{}
	for _, entry := range outcome.Entries {
		if entry.Scope != "local" {
			continue
		}
		if entry.Evidence == "" {
			t.Fatalf("entry %s carries no evidence: %#v", entry.Branch, entry)
		}
		got[entry.Branch] = entry.Disposition
	}
	want := map[string]string{
		"main":             BranchProtected,
		"feature/merged":   BranchContained,
		"feature/absorbed": BranchAbsorbed,
		"feature/unique":   BranchUnique,
		"feature/in-use":   BranchInUse,
	}
	for branch, disposition := range want {
		if got[branch] != disposition {
			t.Errorf("branch %s disposition = %q, want %q", branch, got[branch], disposition)
		}
	}
}

// TestBranchCleanupNeverDeletesAbsorbedUnderAnyFlagCombination is the AC-3
// regression: a branch cherry-picked into main and then reverted still emits
// zero unique git-cherry patches, but the target no longer contains the work.
// #req:absorbed-is-report-only requires this branch survive --apply under
// every flag combination the command accepts.
func TestBranchCleanupNeverDeletesAbsorbedUnderAnyFlagCombination(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()

	gitTest(t, fixture.canonical, "checkout", "-b", "feature/landed-then-reverted")
	sourceSHA := writeAndCommit(t, fixture.canonical, "reverted.txt", "v1\n", "work that will be reverted")
	gitTest(t, fixture.canonical, "checkout", "main")
	landedSHA := cherryPickWithDifferentMessage(t, fixture.canonical, sourceSHA, "landed: work that will be reverted")
	gitTest(t, fixture.canonical, "revert", "--no-edit", landedSHA)
	gitTest(t, fixture.canonical, "push", "origin", "main", "feature/landed-then-reverted")

	future := func() time.Time { return time.Now().Add(90 * 24 * time.Hour) }

	for _, scope := range []string{BranchScopeLocal, BranchScopeRemote, BranchScopeAll} {
		for _, olderThan := range []time.Duration{0, time.Hour, 24 * time.Hour} {
			t.Run(scope+"/"+olderThan.String(), func(t *testing.T) {
				outcome, err := BranchCleanup(ctx, BranchCleanupOptions{
					ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: scope,
					Apply: true, OlderThan: olderThan, Now: future,
				})
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, result := range outcome.Results {
					if result.Branch != "feature/landed-then-reverted" {
						continue
					}
					found = true
					if result.Disposition != BranchAbsorbed {
						t.Fatalf("disposition = %q, want %q", result.Disposition, BranchAbsorbed)
					}
					if result.Eligible || result.Applied {
						t.Fatalf("absorbed branch was eligible=%t applied=%t", result.Eligible, result.Applied)
					}
					if result.Reason == "" {
						t.Fatal("absorbed row names no remedy")
					}
				}
				if scope != BranchScopeRemote && !found {
					t.Fatal("absorbed branch missing from local-scoped results")
				}
			})
			if !gitRefExists(fixture.canonical, "refs/heads/feature/landed-then-reverted") {
				t.Fatal("absorbed local branch was deleted")
			}
			if remoteBranchForTest(t, fixture.canonical, "feature/landed-then-reverted") == "" {
				t.Fatal("absorbed remote branch was deleted")
			}
		}
	}
}

// TestBranchCleanupDeletesOnlyContainedAndRefusesInUse is the AC-2 core: a
// contained branch is deleted with --apply, but a branch checked out in a
// linked worktree is never deleted even though its content is contained.
func TestBranchCleanupDeletesOnlyContainedAndRefusesInUse(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()

	gitTest(t, fixture.canonical, "checkout", "-b", "feature/contained")
	writeAndCommit(t, fixture.canonical, "contained.txt", "v1\n", "contained work")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "-m", "merge feature/contained", "feature/contained")
	gitTest(t, fixture.canonical, "push", "origin", "main", "feature/contained")

	// A second contained branch, but checked out in a linked worktree.
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/contained-in-use")
	writeAndCommit(t, fixture.canonical, "inuse.txt", "v1\n", "contained but in use")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "-m", "merge feature/contained-in-use", "feature/contained-in-use")
	gitTest(t, fixture.canonical, "push", "origin", "main", "feature/contained-in-use")
	worktreeDir := filepath.Join(t.TempDir(), "in-use-worktree")
	gitTest(t, fixture.canonical, "worktree", "add", worktreeDir, "feature/contained-in-use")

	// Dry run first: no report directory, nothing deleted.
	dry, err := BranchCleanup(ctx, BranchCleanupOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: "local"})
	if err != nil {
		t.Fatal(err)
	}
	if dry.ReportPath != "" {
		t.Fatal("dry run wrote a report path")
	}

	outcome, err := BranchCleanup(ctx, BranchCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: "local", Apply: true, OlderThan: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.ReportPath == "" {
		t.Fatal("apply did not write a report path")
	}
	if _, statErr := os.Stat(outcome.ReportPath); statErr != nil {
		t.Fatalf("report file missing: %v", statErr)
	}

	if gitRefExists(fixture.canonical, "refs/heads/feature/contained") {
		t.Fatal("contained branch was not deleted")
	}
	if !gitRefExists(fixture.canonical, "refs/heads/feature/contained-in-use") {
		t.Fatal("in-use branch was deleted")
	}
	if _, statErr := os.Stat(worktreeDir); statErr != nil {
		t.Fatalf("apply affected the linked worktree: %v", statErr)
	}

	var appliedInUse, appliedContained bool
	for _, result := range outcome.Results {
		switch result.Branch {
		case "feature/contained":
			appliedContained = result.Applied
		case "feature/contained-in-use":
			appliedInUse = result.Applied
			if result.Disposition != BranchInUse {
				t.Fatalf("in-use branch disposition = %q", result.Disposition)
			}
		}
	}
	if !appliedContained {
		t.Fatal("contained branch was not applied")
	}
	if appliedInUse {
		t.Fatal("in-use branch was applied")
	}
}

// TestBranchCleanupRefusesMovedLocalBranchWithoutAbortingSweep proves the
// compare-and-delete guard: a branch that advances between plan and apply is
// refused with its moved SHA, while an unrelated sibling still deletes.
func TestBranchCleanupRefusesMovedLocalBranchWithoutAbortingSweep(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()

	for _, name := range []string{"feature/moves", "feature/stays"} {
		fileName := strings.ReplaceAll(name, "/", "-") + ".txt"
		gitTest(t, fixture.canonical, "checkout", "main")
		gitTest(t, fixture.canonical, "checkout", "-b", name)
		writeAndCommit(t, fixture.canonical, fileName, "v1\n", "work on "+name)
		gitTest(t, fixture.canonical, "checkout", "main")
		gitTest(t, fixture.canonical, "merge", "--no-ff", "-m", "merge "+name, name)
	}
	gitTest(t, fixture.canonical, "push", "origin", "main", "feature/moves", "feature/stays")

	// Advance feature/moves after the plan would have captured its SHA, by
	// mutating it directly through git before BranchCleanup's own recheck.
	options, err := normalizeBranchCleanupOptions(BranchCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: "local", Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sweep := branchSweepOptions{ProjectsRoot: options.ProjectsRoot, Base: options.Base, Scope: options.Scope, Now: time.Now()}
	entries, _, paths, err := classifyFleetBranchesWithPaths(ctx, sweep)
	if err != nil {
		t.Fatal(err)
	}
	results := planBranchCleanup(entries, sweep)

	// Advance feature/moves now, simulating a race after planning.
	gitTest(t, fixture.canonical, "checkout", "feature/moves")
	writeAndCommit(t, fixture.canonical, "race.txt", "v2\n", "advanced after plan")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "push", "origin", "feature/moves")

	applyBranchCleanup(ctx, results, paths, options, time.Now())

	var movesResult, staysResult *BranchCleanupResult
	for index := range results {
		switch results[index].Branch {
		case "feature/moves":
			movesResult = &results[index]
		case "feature/stays":
			staysResult = &results[index]
		}
	}
	if movesResult == nil || staysResult == nil {
		t.Fatal("expected both candidates in results")
	}
	if movesResult.Applied {
		t.Fatal("moved branch was deleted instead of refused")
	}
	if movesResult.Error == "" {
		t.Fatal("moved branch carries no refusal reason")
	}
	if !staysResult.Applied {
		t.Fatal("sibling candidate was aborted instead of applied")
	}
	if !gitRefExists(fixture.canonical, "refs/heads/feature/moves") {
		t.Fatal("moved branch was deleted")
	}
	if gitRefExists(fixture.canonical, "refs/heads/feature/stays") {
		t.Fatal("sibling branch was not deleted")
	}
}

// TestBranchCleanupUnreadableSkipRowNamesRepositoryAndUnderlyingReason is the
// regression for the founder's `wb branch cleanup --scope all` report: a
// repository whose exact origin target cannot be fetched (for example no
// refs/heads/<base> at all, as with a repository whose default branch is not
// "main") yields a single whole-repository `unreadable` BranchCleanupResult
// with empty Scope and Branch — it is not about one branch. Before the fix,
// skipReasonForDisposition dropped the entry's real Evidence (the exact `git
// fetch` failure `wb branch list` already surfaces for the same entry) and
// substituted a generic "disposition unreadable is never eligible" message
// that names neither the repository nor the underlying cause. The
// Repository field itself was always populated; only the printed skip
// reason discarded it.
func TestBranchCleanupUnreadableSkipRowNamesRepositoryAndUnderlyingReason(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()

	// "does-not-exist" is never pushed, so fetching refs/heads/does-not-exist
	// from origin fails exactly as specscore/winget-pkgs did for "main".
	options, err := normalizeBranchCleanupOptions(BranchCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Base: "does-not-exist", Scope: "all", Apply: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	sweep := branchSweepOptions{ProjectsRoot: options.ProjectsRoot, Base: options.Base, Scope: options.Scope, Now: time.Now()}
	entries, _, _, err := classifyFleetBranchesWithPaths(ctx, sweep)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("whole-repository fetch failure entries = %d, want exactly 1: %#v", len(entries), entries)
	}
	entry := entries[0]
	if entry.Repository == "" {
		t.Fatal("unreadable entry carries no repository")
	}
	if entry.Disposition != BranchUnreadable {
		t.Fatalf("disposition = %q, want %q", entry.Disposition, BranchUnreadable)
	}
	if !strings.Contains(entry.Evidence, "fetch exact origin/does-not-exist target") {
		t.Fatalf("evidence = %q, want it to name the failed fetch", entry.Evidence)
	}

	results := planBranchCleanup(entries, sweep)
	if len(results) != 1 {
		t.Fatalf("cleanup results = %d, want exactly 1", len(results))
	}
	result := results[0]
	if result.Repository == "" {
		t.Fatal("BranchCleanupResult carries no repository")
	}
	if result.Eligible {
		t.Fatal("unreadable repository must never be eligible for --apply")
	}
	// The regression: the printed skip reason must be the real fetch
	// failure, not the generic disposition boilerplate that names neither
	// the repository nor the cause.
	if !strings.Contains(result.SkipReason, "fetch exact origin/does-not-exist target") {
		t.Fatalf("skip reason = %q, want it to surface the underlying fetch failure", result.SkipReason)
	}
	if result.SkipReason == "disposition unreadable is never eligible for --apply" {
		t.Fatal("skip reason regressed to the generic message that drops the repository's real evidence")
	}
}

// TestBranchCleanupAppliesRemoteDeletionWhenNoOpenPullRequestExists is the
// AC-4 positive case: a contained remote branch with no open pull request is
// deleted with force-with-lease against its observed SHA, proving the
// success path end to end rather than only its refusals.
func TestBranchCleanupAppliesRemoteDeletionWhenNoOpenPullRequestExists(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	installMergedPullRequestFixturesWithMerge(t, nil, nil, time.Time{}) // empty gh payload: no PR at all

	gitTest(t, fixture.canonical, "checkout", "-b", "feature/clean-remote")
	writeAndCommit(t, fixture.canonical, "clean.txt", "v1\n", "clean remote work")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "merge", "--no-ff", "-m", "merge feature/clean-remote", "feature/clean-remote")
	gitTest(t, fixture.canonical, "push", "origin", "main", "feature/clean-remote")

	outcome, err := BranchCleanup(ctx, BranchCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: "remote", Apply: true, OlderThan: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	var applied bool
	for _, result := range outcome.Results {
		if result.Branch == "feature/clean-remote" && result.Applied {
			applied = true
		}
	}
	if !applied {
		t.Fatalf("remote deletion did not apply: %#v", outcome.Results)
	}
	if remoteBranchForTest(t, fixture.canonical, "feature/clean-remote") != "" {
		t.Fatal("remote branch still exists after apply")
	}
	if !gitRefExists(fixture.canonical, "refs/heads/feature/clean-remote") {
		t.Fatal("--scope remote unexpectedly deleted the local ref too")
	}
}

// TestBranchCleanupRemoteFailsClosedWithoutPullRequestEvidence is the AC-4
// core: an open PR refuses its branch regardless of containment, and when PR
// evidence cannot be obtained at all, no remote branch is deleted while local
// deletion under --scope all still proceeds.
func TestBranchCleanupRemoteFailsClosedWithoutPullRequestEvidence(t *testing.T) {
	ctx := context.Background()

	setupOpenAndNoPRBranches := func(t *testing.T) (*gitFixture, string) {
		t.Helper()
		fixture := newGitFixture(t)
		gitTest(t, fixture.canonical, "checkout", "-b", "feature/open-pr")
		writeAndCommit(t, fixture.canonical, "openpr.txt", "v1\n", "open pr work")
		gitTest(t, fixture.canonical, "checkout", "main")
		gitTest(t, fixture.canonical, "merge", "--no-ff", "-m", "merge feature/open-pr", "feature/open-pr")
		headSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/feature/open-pr")

		gitTest(t, fixture.canonical, "checkout", "main")
		gitTest(t, fixture.canonical, "checkout", "-b", "feature/no-pr")
		writeAndCommit(t, fixture.canonical, "nopr.txt", "v1\n", "no pr work")
		gitTest(t, fixture.canonical, "checkout", "main")
		gitTest(t, fixture.canonical, "merge", "--no-ff", "-m", "merge feature/no-pr", "feature/no-pr")

		gitTest(t, fixture.canonical, "push", "origin", "main", "feature/open-pr", "feature/no-pr")
		return fixture, headSHA
	}

	t.Run("open PR refuses regardless of containment", func(t *testing.T) {
		fixture, headSHA := setupOpenAndNoPRBranches(t)
		installOpenPullRequestFixture(t, headSHA)
		outcome, err := BranchCleanup(ctx, BranchCleanupOptions{
			ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: "remote", Apply: true, OlderThan: 0,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range outcome.Results {
			if result.Branch == "feature/open-pr" && result.Applied {
				t.Fatal("branch with an open pull request was deleted")
			}
		}
		if remoteBranchForTest(t, fixture.canonical, "feature/open-pr") == "" {
			t.Fatal("remote branch with an open pull request is gone")
		}
	})

	t.Run("missing PR evidence refuses every remote branch but not local", func(t *testing.T) {
		fixture, _ := setupOpenAndNoPRBranches(t)
		installFailingGitHubFixture(t)
		outcome, err := BranchCleanup(ctx, BranchCleanupOptions{
			ProjectsRoot: fixture.projectsRoot, Base: "main", Scope: "all", Apply: true, OlderThan: 0,
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range outcome.Results {
			if result.Scope == "remote" && result.Applied {
				t.Fatalf("remote branch %s was deleted without pull-request evidence", result.Branch)
			}
			if result.Branch == "feature/no-pr" && result.Scope == "local" && !result.Applied {
				t.Fatalf("local deletion was blocked by unrelated remote evidence failure: %#v", result)
			}
		}
		if remoteBranchForTest(t, fixture.canonical, "feature/no-pr") == "" {
			t.Fatal("remote branch disappeared even though evidence was unavailable")
		}
	})
}

// installOpenPullRequestFixture is the same deterministic fake-gh-on-PATH
// mechanism installMergedPullRequestFixture uses, but for an explicitly open
// pull request rather than a merged one.
func installOpenPullRequestFixture(t *testing.T, head string) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := "#!/bin/sh\nset -eu\nif [ \"$1 $2\" != \"api --paginate\" ]; then echo \"unexpected gh command: $*\" >&2; exit 2; fi\nprintf '%s\\n' \"$WB_TEST_OPEN_PULLS\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `[{"number":9,"html_url":"https://example.test/pull/9","state":"open","head":{"ref":"feature/open-pr","sha":"` + head + `"},"base":{"ref":"main","sha":""}}]`
	t.Setenv("WB_TEST_OPEN_PULLS", payload)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
