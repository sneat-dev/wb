package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
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

func TestBranchListExactRepositoryAndBranchSelectorsDoNotUseFilterSemantics(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/exact")
	writeAndCommit(t, fixture.canonical, "exact.txt", "v1\n", "exact")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "push", "origin", "feature/exact")

	outcome, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote,
		Repository: "acme/app", Branch: "feature/exact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Entries) != 1 || outcome.Entries[0].Repository != "acme/app" || outcome.Entries[0].Branch != "feature/exact" {
		t.Fatalf("exact selection = %#v", outcome.Entries)
	}
	if _, err := BranchList(context.Background(), BranchListOptions{ProjectsRoot: fixture.projectsRoot, Repository: "widget"}); err == nil {
		t.Fatal("unqualified --repo was accepted")
	}
}

func TestArchiveReviewedBranchRestoresExactHeadOutsideSourceClone(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/recovery")
	head := writeAndCommit(t, fixture.canonical, "recovery.txt", "v1\n", "recovery")
	receipt := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(receipt, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := supersessionFileSHA256(receipt)
	if err != nil {
		t.Fatal(err)
	}
	dir, err := archiveReviewedBranch(context.Background(), t.TempDir(), fixture.canonical, BranchCleanupResult{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature/recovery", SHA: head, TargetSHA: head, SupersessionReceipt: receipt, SupersessionSHA256: digest}})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsLocal(dir) {
		t.Fatalf("archive path must be absolute: %s", dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "source.bundle")); err != nil {
		t.Fatalf("bundle missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
	manifestRaw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest reviewedBranchRecoveryManifest
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.RestoredHead != head || manifest.VerifiedAt.IsZero() {
		t.Fatalf("manifest does not record independent restore verification: %#v", manifest)
	}
}

func TestArchiveReviewedBranchRefusesUnsafeRecoveryLocations(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/recovery-location")
	head := writeAndCommit(t, fixture.canonical, "recovery-location.txt", "v1\n", "recovery")
	receipt := filepath.Join(t.TempDir(), "receipt.json")
	if err := os.WriteFile(receipt, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := supersessionFileSHA256(receipt)
	if err != nil {
		t.Fatal(err)
	}
	result := BranchCleanupResult{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature/recovery-location", SHA: head, TargetSHA: head, SupersessionReceipt: receipt, SupersessionSHA256: digest}}
	if _, err := archiveReviewedBranch(context.Background(), fixture.canonical, fixture.canonical, result); err == nil {
		t.Fatal("source clone recovery path was accepted")
	}

	gitTest(t, fixture.canonical, "checkout", "main")
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, fixture.canonical, "worktree", "add", linked, "feature/recovery-location")
	if _, err := archiveReviewedBranch(context.Background(), filepath.Join(linked, "reports"), fixture.canonical, result); err == nil {
		t.Fatal("linked source worktree recovery path was accepted")
	}

	outside := t.TempDir()
	link := filepath.Join(t.TempDir(), "report-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveReviewedBranch(context.Background(), link, fixture.canonical, result); err == nil {
		t.Fatal("symlinked recovery path was accepted")
	}
}

func TestPeerEvidenceRequiresFreshExactSafeLocalHostEvidence(t *testing.T) {
	now := time.Now().UTC()
	sha := "0123456789012345678901234567890123456789"
	base := BranchCleanupOptions{Scope: BranchScopeRemote, Repository: "acme/app", Branch: "feature/x", RequireHosts: []string{branchEvidenceHost()}}
	result := []BranchCleanupResult{{BranchEntry: BranchEntry{Repository: "acme/app", Branch: "feature/x", Scope: BranchScopeRemote, SHA: sha}}}
	write := func(t *testing.T, outcome BranchListOutcome) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "peer.json")
		data, err := json.Marshal(outcome)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := BranchListOutcome{Host: branchEvidenceHost(), GeneratedAt: now, Repository: "acme/app", Branch: "feature/x", Entries: []BranchEntry{{Repository: "acme/app", Branch: "feature/x", Scope: BranchScopeRemote, SHA: sha, Disposition: BranchContained}}}
	base.PeerEvidence = []string{write(t, good)}
	if err := validatePeerEvidence(base, result, now); err != nil {
		t.Fatal(err)
	}
	stale := good
	stale.GeneratedAt = now.Add(-6 * time.Minute)
	base.PeerEvidence = []string{write(t, stale)}
	if err := validatePeerEvidence(base, result, now); err == nil {
		t.Fatal("stale evidence accepted")
	}
	inUse := good
	inUse.Entries[0].Disposition = BranchInUse
	base.PeerEvidence = []string{write(t, inUse)}
	if err := validatePeerEvidence(base, result, now); err == nil {
		t.Fatal("in-use evidence accepted")
	}
	moved := good
	moved.Entries[0].SHA = "1111111111111111111111111111111111111111"
	base.PeerEvidence = []string{write(t, moved)}
	if err := validatePeerEvidence(base, result, now); err == nil {
		t.Fatal("mismatched evidence accepted")
	}
	unknown := good
	unknown.Entries[0].Disposition = ""
	base.PeerEvidence = []string{write(t, unknown)}
	if err := validatePeerEvidence(base, result, now); err == nil {
		t.Fatal("unknown peer disposition accepted")
	}
	wrongTarget := good
	wrongTarget.Entries[0].TargetSHA = "1111111111111111111111111111111111111111"
	result[0].TargetSHA = sha
	base.PeerEvidence = []string{write(t, wrongTarget)}
	if err := validatePeerEvidence(base, result, now); err == nil {
		t.Fatal("peer evidence with mismatched target accepted")
	}
	wrongBase := good
	wrongBase.Base = "master"
	base.PeerEvidence = []string{write(t, wrongBase)}
	if err := validatePeerEvidence(base, result, now); err == nil {
		t.Fatal("peer evidence with mismatched base accepted")
	}
}

func TestReviewedCleanupOptionsAndPlanFailClosed(t *testing.T) {
	for _, scope := range []string{BranchScopeRemote, BranchScopeAll} {
		if _, err := normalizeBranchCleanupOptions(BranchCleanupOptions{ProjectsRoot: t.TempDir(), Scope: scope, Repository: "acme/app", Branch: "feature/x", SupersededBy: "receipt.json"}); err == nil {
			t.Fatalf("%s reviewed cleanup accepted without peer evidence", scope)
		}
	}
	entries := []BranchEntry{
		{Repository: "acme/app", Branch: "feature/x", Scope: BranchScopeRemote, Disposition: "", SupersededAtOrigin: true},
		{Repository: "acme/app", Branch: "feature/y", Scope: BranchScopeRemote, Disposition: BranchSuperseded, SupersededAtOrigin: true},
	}
	planned := planBranchCleanup(entries, branchSweepOptions{})
	if planned[0].Eligible {
		t.Fatal("empty disposition was eligible for reviewed cleanup")
	}
	if !planned[1].Eligible {
		t.Fatal("explicit superseded disposition was not eligible")
	}
}

func TestReviewedRemoteForkGuardFailsClosed(t *testing.T) {
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	if err := testenv.WriteExecutableFile(script, []byte("#!/bin/sh\nset -eu\nprintf '%s\\n' \"$WB_TEST_REPOSITORY_METADATA\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("WB_TEST_REPOSITORY_METADATA", `{"fork":true}`)
	if err := reviewedRemoteForkGuard(context.Background(), t.TempDir(), "acme/app"); err == nil {
		t.Fatal("fork origin was accepted for reviewed remote retirement")
	}
	t.Setenv("WB_TEST_REPOSITORY_METADATA", `{}`)
	if err := reviewedRemoteForkGuard(context.Background(), t.TempDir(), "acme/app"); err == nil {
		t.Fatal("missing fork metadata was accepted")
	}
	t.Setenv("WB_TEST_REPOSITORY_METADATA", `{"fork":false}`)
	if err := reviewedRemoteForkGuard(context.Background(), t.TempDir(), "acme/app"); err != nil {
		t.Fatalf("non-fork origin rejected: %v", err)
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

func TestRetiredBranchDestinationFlattensTheSourceName(t *testing.T) {
	got := retiredBranchDestination(time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC), "feature/old/name", "0123456789abcdef")
	if got != "retired/20260923-feature-old-name-0123456789ab" {
		t.Fatalf("destination = %q", got)
	}
}

func TestQuarantineManifestRequiresExactIdentityAndReason(t *testing.T) {
	_, err := validateQuarantineRequests([]BranchQuarantineRequest{{Repository: "acme/app", Ref: "feature/old", Reason: "obsolete"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateQuarantineRequests([]BranchQuarantineRequest{{Repository: "acme/app", Ref: "feature/old", SHA: "bad", Reason: "obsolete"}}); err == nil {
		t.Fatal("invalid manifest SHA was accepted")
	}
	if _, err := validateQuarantineRequests([]BranchQuarantineRequest{{Repository: "acme/app", Ref: "retired/old", SHA: "0123456789012345678901234567890123456789", Reason: "obsolete"}}); err == nil {
		t.Fatal("already retired source was accepted")
	}
}

func TestAtomicLocalBranchQuarantinePreservesExactCommitAndRefusesCollision(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/old")
	head := writeAndCommit(t, fixture.canonical, "old.txt", "old\n", "old branch")
	gitTest(t, fixture.canonical, "checkout", "main")
	destination := "retired/20260923-feature-old-" + shortSHA(head)
	if err := atomicLocalBranchRename(context.Background(), fixture.canonical, "feature/old", destination, head); err != nil {
		t.Fatal(err)
	}
	if gitRefExists(fixture.canonical, "refs/heads/feature/old") {
		t.Fatal("source remains after quarantine")
	}
	got := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+destination)
	if got != head {
		t.Fatalf("retired head = %s, want %s", got, head)
	}
	if err := atomicLocalBranchRename(context.Background(), fixture.canonical, "main", destination, gitTestOutput(t, fixture.canonical, "rev-parse", "main")); err == nil {
		t.Fatal("destination collision was accepted")
	}
}

func TestBranchQuarantinePlansAndAppliesWithDurableReport(t *testing.T) {
	fixture := newGitFixture(t)
	bin := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitTest(t, fixture.canonical, "checkout", "-b", "feature/quarantine")
	head := writeAndCommit(t, fixture.canonical, "quarantine.txt", "v1\n", "old branch")
	gitTest(t, fixture.canonical, "checkout", "main")
	options := BranchQuarantineOptions{ProjectsRoot: fixture.projectsRoot, Repository: "acme/app", Branch: "feature/quarantine", SHA: head, Reason: "obsolete", Now: func() time.Time { return time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC) }}
	plan, err := BranchQuarantine(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Results) != 1 || plan.Results[0].Outcome != "planned" || plan.ReportPath != "" {
		t.Fatalf("plan = %#v", plan)
	}
	options.Apply, options.ReportDir = true, filepath.Join(t.TempDir(), "receipt")
	applied, err := BranchQuarantine(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Results[0].Outcome != "quarantined" || applied.ReportPath == "" {
		t.Fatalf("apply = %#v", applied)
	}
	if _, err := os.Stat(applied.ReportPath); err != nil {
		t.Fatalf("durable report: %v", err)
	}
	if gitRefExists(fixture.canonical, "refs/heads/feature/quarantine") {
		t.Fatal("source was not quarantined")
	}
}

func TestReserveQuarantineReportDirCreatesFreshDefaultParentAndRefusesReuse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh", "reports", "branch-quarantine", "run")
	if err := reserveQuarantineReportDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("reserved dir = %v, %v", info, err)
	}
	if err := reserveQuarantineReportDir(dir); err == nil {
		t.Fatal("existing report directory was reused")
	}
}

func TestRetiredCountHonoursExactBranchAndAgeSelectors(t *testing.T) {
	now := time.Now()
	sweep := branchSweepOptions{Branch: "retired/one", OlderThan: time.Hour, Now: now}
	// The count helper's selector predicate is shared for both scopes; this
	// table protects the no-double-count presentation contract independently of
	// repository discovery.
	if !retiredRefSelected(sweep, branchRef{Name: "retired/one", CommitterDate: now.Add(-2 * time.Hour)}) {
		t.Fatal("matching retired ref was excluded")
	}
	if retiredRefSelected(sweep, branchRef{Name: "retired/two", CommitterDate: now.Add(-2 * time.Hour)}) {
		t.Fatal("exact branch selector was ignored")
	}
	if retiredRefSelected(sweep, branchRef{Name: "retired/one", CommitterDate: now.Add(-time.Minute)}) {
		t.Fatal("age selector was ignored")
	}
}

func TestRetiredNamespaceFastPathHonoursOnlySelector(t *testing.T) {
	if !retiredNamespaceSelected(branchSweepOptions{Name: "retired/*", Only: BranchUnique}) {
		t.Fatal("retired name with --only unique did not select the no-base-fetch inventory")
	}
	if !retiredNamespaceSelected(branchSweepOptions{Branch: "retired/example", Only: BranchContained}) {
		t.Fatal("exact retired ref with a non-retired disposition did not select the no-base-fetch inventory")
	}
	if !retiredNamespaceSelected(branchSweepOptions{Name: "retired/*", Only: BranchRetired}) {
		t.Fatal("--only retired did not select the retired inventory")
	}
}

func TestRetiredNamespaceIncompatibleOnlyIsOfflineAndEmpty(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "retired/incompatible-only")
	writeAndCommit(t, fixture.canonical, "retired.txt", "local\n", "retire local branch")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	outcome, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeLocal, Name: "retired/*", Only: BranchUnique,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Entries) != 0 || outcome.RetiredBranches != 0 || outcome.RetiredRefs[BranchScopeLocal] != 0 {
		t.Fatalf("incompatible retired selector = %#v", outcome)
	}
	joined := strings.Join(outcome.Diagnostics, "\n")
	if !strings.Contains(joined, "skipped fetch of origin/main") || !strings.Contains(joined, "cannot match --only unique") {
		t.Fatalf("incompatible retired selector diagnostics = %#v", outcome.Diagnostics)
	}
}

func TestRetiredNamespaceInventoryIsOfflineForLocalRetiredGlob(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "retired/local-only")
	writeAndCommit(t, fixture.canonical, "retired.txt", "local\n", "retire local branch")
	gitTest(t, fixture.canonical, "checkout", "main")
	// An unavailable origin proves this selector never asks for origin/main.
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	var progress strings.Builder
	outcome, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeLocal, Name: "retired/*", Progress: &progress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Entries) != 1 || outcome.Entries[0].Branch != "retired/local-only" || outcome.Entries[0].Disposition != BranchRetired {
		t.Fatalf("retired local inventory = %#v", outcome.Entries)
	}
	if outcome.Entries[0].TargetSHA != "" {
		t.Fatalf("retired offline inventory unexpectedly resolved a base target: %#v", outcome.Entries[0])
	}
	if outcome.Entries[0].Author == "" || outcome.Entries[0].Title != "retire local branch" {
		t.Fatalf("retired list metadata = %#v", outcome.Entries[0])
	}
	if outcome.RetiredRefs[BranchScopeLocal] != 1 || outcome.RetiredBranches != 1 {
		t.Fatalf("retired counts = refs=%#v names=%d", outcome.RetiredRefs, outcome.RetiredBranches)
	}
	if !strings.Contains(strings.Join(outcome.Diagnostics, "\n"), "skipped fetch of origin/main") {
		t.Fatalf("offline diagnostic missing: %#v", outcome.Diagnostics)
	}
	if !strings.Contains(progress.String(), "[1/1] scanning acme/app") {
		t.Fatalf("retired inventory did not report per-repository progress: %q", progress.String())
	}
}

func TestRetiredNamespaceListsAndCountsTagsSeparately(t *testing.T) {
	fixture := newGitFixture(t)
	sha := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "tag", "retired/tag-only", sha)
	gitTest(t, fixture.canonical, "push", "origin", "refs/tags/retired/tag-only")
	// No origin/main read is needed for a retired-only tag selector.
	gitTest(t, fixture.remote, "update-ref", "-d", "refs/heads/main")
	for _, scope := range []string{BranchScopeLocal, BranchScopeRemote, BranchScopeAll} {
		outcome, err := BranchList(context.Background(), BranchListOptions{ProjectsRoot: fixture.projectsRoot, Scope: scope, Name: "retired/*"})
		if err != nil {
			t.Fatal(err)
		}
		if outcome.RetiredBranches != 0 || outcome.RetiredTagNames != 1 || len(outcome.Entries) != map[string]int{BranchScopeLocal: 1, BranchScopeRemote: 1, BranchScopeAll: 2}[scope] {
			t.Fatalf("scope %s outcome = %#v", scope, outcome)
		}
		for _, entry := range outcome.Entries {
			if entry.RefKind != "tag" || entry.Branch != "retired/tag-only" || entry.Disposition != BranchRetired {
				t.Fatalf("tag entry = %#v", entry)
			}
		}
	}
}

func TestRemoteRetiredTagFetchesMetadataAndHonoursAge(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "tag-source")
	sha := writeAndCommit(t, fixture.canonical, "remote-tag.txt", "remote only\n", "remote retired tag source")
	gitTest(t, fixture.canonical, "tag", "retired/remote-metadata", sha)
	gitTest(t, fixture.canonical, "push", "origin", "refs/tags/retired/remote-metadata")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "branch", "-D", "tag-source")
	gitTest(t, fixture.canonical, "tag", "-d", "retired/remote-metadata")
	gitTest(t, fixture.canonical, "gc", "--prune=now")
	fetchHeadPath := filepath.Join(fixture.canonical, ".git", "FETCH_HEAD")
	beforeFetchHead, beforeFetchHeadErr := os.ReadFile(fetchHeadPath)
	refs, diagnostic := listRetiredTags(context.Background(), fixture.canonical, true, true)
	if diagnostic != "" || len(refs) != 1 || refs[0].CommitterDate.IsZero() || refs[0].Title != "remote retired tag source" {
		t.Fatalf("temporary remote tag metadata = %#v, diagnostic=%q", refs, diagnostic)
	}
	if gitRefExists(fixture.canonical, "refs/tags/retired/remote-metadata") {
		t.Fatal("remote metadata fetch created a local tag ref")
	}
	afterFetchHead, afterFetchHeadErr := os.ReadFile(fetchHeadPath)
	if (beforeFetchHeadErr == nil) != (afterFetchHeadErr == nil) || (beforeFetchHeadErr == nil && string(beforeFetchHead) != string(afterFetchHead)) {
		t.Fatalf("remote metadata fetch changed caller FETCH_HEAD: before=%q/%v after=%q/%v", beforeFetchHead, beforeFetchHeadErr, afterFetchHead, afterFetchHeadErr)
	}

	outcome, err := BranchList(context.Background(), BranchListOptions{ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote, Name: "retired/*"})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Entries) != 1 || outcome.Entries[0].CommitterDate.IsZero() || outcome.Entries[0].Title != "remote retired tag source" {
		t.Fatalf("remote tag metadata = %#v", outcome)
	}
	aged, err := BranchList(context.Background(), BranchListOptions{ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote, Name: "retired/*", OlderThan: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(aged.Entries) != 0 || aged.RetiredTagNames != 0 {
		t.Fatalf("young remote tag bypassed age filter: %#v", aged)
	}
}

func TestRetiredNamespaceRemoteFailureIsDiagnosticNotSyntheticBranch(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	outcome, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote, Name: "retired/*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Entries) != 0 || !outcome.RetiredRemoteUnavailable {
		t.Fatalf("remote failure result = %#v", outcome)
	}
	if !strings.Contains(strings.Join(outcome.Diagnostics, "\n"), "retired remote refs") {
		t.Fatalf("remote failure diagnostic missing: %#v", outcome.Diagnostics)
	}
}

func TestOrdinaryRemoteInventoryMarksUnavailableRetiredTags(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))

	outcome, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.RetiredRemoteUnavailable {
		t.Fatalf("remote tag failure was rendered as a known count: %#v", outcome)
	}
	if !strings.Contains(strings.Join(outcome.Diagnostics, "\n"), "count retired remote tags") {
		t.Fatalf("remote tag diagnostic missing: %#v", outcome.Diagnostics)
	}
}

func TestRetiredNamespaceRemoteInventoryFetchesOnlyRetiredRefs(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "retired/remote-only")
	writeAndCommit(t, fixture.canonical, "retired-remote.txt", "remote\n", "retire remote branch")
	gitTest(t, fixture.canonical, "push", "origin", "retired/remote-only")
	gitTest(t, fixture.canonical, "checkout", "main")
	// A generic branch sweep needs origin/main. Removing it from the fixture
	// proves the retired selector refreshes only its own remote namespace.
	gitTest(t, fixture.remote, "update-ref", "-d", "refs/heads/main")

	outcome, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote, Only: BranchRetired,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Entries) != 1 || outcome.Entries[0].Scope != BranchScopeRemote || outcome.Entries[0].Branch != "retired/remote-only" || outcome.Entries[0].Disposition != BranchRetired {
		t.Fatalf("retired remote inventory = %#v", outcome.Entries)
	}
	if outcome.RetiredRefs[BranchScopeRemote] != 1 || outcome.RetiredBranches != 1 {
		t.Fatalf("retired counts = refs=%#v names=%d", outcome.RetiredRefs, outcome.RetiredBranches)
	}
}

func TestRetiredNamespaceRemoteInventoryRefreshesAndPrunesTrackingRefs(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "retired/remote-refresh")
	first := writeAndCommit(t, fixture.canonical, "retired-refresh.txt", "one\n", "first retired remote")
	gitTest(t, fixture.canonical, "push", "origin", "retired/remote-refresh")
	second := writeAndCommit(t, fixture.canonical, "retired-refresh.txt", "two\n", "second retired remote")
	gitTest(t, fixture.canonical, "push", "origin", "HEAD:retired/remote-refresh")
	gitTest(t, fixture.canonical, "checkout", "main")
	gitTest(t, fixture.canonical, "update-ref", "refs/remotes/origin/retired/remote-refresh", first)

	updated, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote, Name: "retired/*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Entries) != 1 || updated.Entries[0].SHA != second {
		t.Fatalf("remote retired refresh = %#v, want %s", updated.Entries, second)
	}
	gitTest(t, fixture.remote, "update-ref", "-d", "refs/heads/retired/remote-refresh")

	pruned, err := BranchList(context.Background(), BranchListOptions{
		ProjectsRoot: fixture.projectsRoot, Scope: BranchScopeRemote, Name: "retired/*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pruned.Entries) != 0 || pruned.RetiredRefs[BranchScopeRemote] != 0 || gitRefExists(fixture.canonical, "refs/remotes/origin/retired/remote-refresh") {
		t.Fatalf("remote retired prune = outcome=%#v tracking=%t", pruned, gitRefExists(fixture.canonical, "refs/remotes/origin/retired/remote-refresh"))
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
	if err := testenv.WriteExecutableFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `[{"number":9,"html_url":"https://example.test/pull/9","state":"open","head":{"ref":"feature/open-pr","sha":"` + head + `","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}]`
	t.Setenv("WB_TEST_OPEN_PULLS", payload)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
