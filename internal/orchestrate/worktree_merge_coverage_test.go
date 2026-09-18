package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestOrchCovConservativeWorktreeMergePRRoute(t *testing.T) {
	t.Parallel()
	decision, err := conservativeWorktreeMergePRRoute(WorktreeMergeRouteAuto, "policy is unavailable")
	if err != nil || decision.Route != WorktreeMergeRoutePullRequest || decision.Requested != WorktreeMergeRouteAuto {
		t.Fatalf("auto decision = %+v, err %v", decision, err)
	}
	if !strings.Contains(decision.Reason, "policy is unavailable") || !strings.Contains(decision.Reason, "conservatively") {
		t.Fatalf("auto reason = %q", decision.Reason)
	}
	decision, err = conservativeWorktreeMergePRRoute(WorktreeMergeRouteDirect, "policy is unavailable")
	if err == nil || !strings.Contains(err.Error(), "direct route is not authoritatively permitted") {
		t.Fatalf("direct error = %v (decision %+v)", err, decision)
	}
	if decision.Route != WorktreeMergeRoutePullRequest {
		t.Fatalf("direct decision = %+v", decision)
	}
}

func TestOrchCovPathInsideWorktreeMergeReports(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	reports := filepath.Join(root, "reports", "worktree-merge")
	if err := os.MkdirAll(reports, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(reports, "r.json")
	writeEngineFile(t, inside, "{}")
	if !pathInsideWorktreeMergeReports(reports, inside) {
		t.Fatalf("%q was not recognised as inside %q", inside, reports)
	}
	for _, outside := range []string{
		reports,
		filepath.Join(root, "reports"),
		filepath.Join(root, "elsewhere", "r.json"),
	} {
		if pathInsideWorktreeMergeReports(reports, outside) {
			t.Fatalf("%q was treated as inside %q", outside, reports)
		}
	}
	// A path that does not exist cannot be resolved, so it is never inside.
	if pathInsideWorktreeMergeReports(reports, filepath.Join(reports, "absent.json")) {
		t.Fatal("a nonexistent report path was accepted")
	}
}

// orchCovMergeReceiptFixture prepares a real candidate and returns the fixture
// plus its persisted receipt and source worktree.
func orchCovMergeReceiptFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt, string) {
	t.Helper()
	fixture := newEngineFixture(t)
	source := createMergeSourceOnBase(t, fixture, "cov-merge-task", "wb/cov/merge", "main", "cov.txt", "cov\n")
	receipt, err := PrepareWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{
		ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main",
		Model: "test-model", AgentRuntime: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, receipt, source.WorktreeDir
}

func TestOrchCovPeekWorktreeMergeReceiptResolvesBothSpellings(t *testing.T) {
	fixture, receipt, _ := orchCovMergeReceiptFixture(t)

	byPath, err := PeekWorktreeMergeReceipt(fixture.githubDir, receipt.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if byPath.ID != receipt.ID || byPath.ReceiptPath != receipt.ReceiptPath {
		t.Fatalf("peek by path = %+v", byPath)
	}
	byWorktree, err := PeekWorktreeMergeReceipt(fixture.githubDir, receipt.Candidate.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	if byWorktree.ReceiptPath != receipt.ReceiptPath {
		t.Fatalf("peek by candidate worktree = %q, want %q", byWorktree.ReceiptPath, receipt.ReceiptPath)
	}
}

func TestOrchCovResolveWorktreeMergeReceiptPathRefusesUnusableInput(t *testing.T) {
	fixture, receipt, _ := orchCovMergeReceiptFixture(t)

	if _, err := resolveWorktreeMergeReceiptPath(fixture.githubDir, "  "); err == nil ||
		!strings.Contains(err.Error(), "candidate worktree or receipt is required") {
		t.Fatalf("empty input error = %v", err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	writeEngineFile(t, outside, "{}")
	if _, err := resolveWorktreeMergeReceiptPath(fixture.githubDir, outside); err == nil ||
		!strings.Contains(err.Error(), "outside the authoritative WB report store") {
		t.Fatalf("outside-the-store error = %v", err)
	}
	if _, err := resolveWorktreeMergeReceiptPath(fixture.githubDir, filepath.Join(t.TempDir(), "no-such-worktree")); err == nil ||
		!strings.Contains(err.Error(), "no worktree merge receipt owns") {
		t.Fatalf("unowned candidate error = %v", err)
	}
	// The reports directory carries sidecars and non-JSON files that are not
	// receipts; a peek must skip them rather than fail on them.
	reportsDir := filepath.Dir(receipt.ReceiptPath)
	if err := os.MkdirAll(filepath.Join(reportsDir, "0000-dir.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(reportsDir, "0001-notes.txt"), "ignored")
	writeEngineFile(t, filepath.Join(reportsDir, "0002-foo.ack.json"), "ignored")
	writeEngineFile(t, filepath.Join(reportsDir, "0003-"+worktreeMergeLandedFailureAcknowledgementSuffix), "ignored")
	writeEngineFile(t, filepath.Join(reportsDir, "0004-garbage.json"), "{not json")
	if _, err := PeekWorktreeMergeReceipt(fixture.githubDir, receipt.ReceiptPath); err != nil {
		t.Fatalf("peek with junk in the reports store: %v", err)
	}
}

func TestOrchCovValidateExactPreparingReceiptAdmitsOnlyAnExactResume(t *testing.T) {
	fixture, prepared, sourceWorktree := orchCovMergeReceiptFixture(t)
	_ = fixture
	head := strings.TrimSpace(runEngineGit(t, sourceWorktree, "rev-parse", "HEAD"))
	sources := prepared.Sources
	lane, operation := "lane-cov", "operation-cov"
	base := WorktreeMergeReceipt{
		Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePreparing,
		Lane: lane, ID: operation, Sources: sources,
		Candidate: WorktreeMergeCandidate{Task: operation, Worktree: sourceWorktree, Branch: "wb/cov/unpublished", SHA: head},
	}
	if err := validateExactPreparingWorktreeMergeReceipt(context.Background(), base, lane, operation, sources); err != nil {
		t.Fatalf("exact preparing receipt refused: %v", err)
	}

	for _, test := range []struct {
		name    string
		mutate  func(*WorktreeMergeReceipt)
		wantIn  string
		prepare func()
	}{
		{
			name:   "receipt is not preparing",
			mutate: func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePrepared },
			wantIn: "only an exact preparing receipt may resume",
		},
		{
			name:   "lane differs",
			mutate: func(r *WorktreeMergeReceipt) { r.Lane = "another-lane" },
			wantIn: "immutable operation identity differs",
		},
		{
			name:   "candidate identity is incomplete",
			mutate: func(r *WorktreeMergeReceipt) { r.Candidate.Task = "another-task" },
			wantIn: "candidate identity is incomplete or differs",
		},
		{
			name:   "candidate head drifted",
			mutate: func(r *WorktreeMergeReceipt) { r.Candidate.SHA = strings.Repeat("a", 40) },
			wantIn: "head drifted",
		},
		{
			name:   "candidate was published",
			mutate: func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "" },
			wantIn: "was published",
			prepare: func() {
				runEngineGit(t, sourceWorktree, "push", "origin", "HEAD:refs/heads/wb/cov/unpublished")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			if test.prepare != nil {
				test.prepare()
			}
			err := validateExactPreparingWorktreeMergeReceipt(context.Background(), candidate, lane, operation, sources)
			if err == nil || !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("error = %v, want %q", err, test.wantIn)
			}
		})
	}

	dirty := base
	writeEngineFile(t, filepath.Join(sourceWorktree, "uncommitted.txt"), "uncommitted\n")
	err := validateExactPreparingWorktreeMergeReceipt(context.Background(), dirty, lane, operation, sources)
	if err == nil || !strings.Contains(err.Error(), "not safely resumable") {
		t.Fatalf("dirty candidate error = %v", err)
	}
}

func TestOrchCovValidatePreparingCandidateProvesTheWholeGraph(t *testing.T) {
	_, prepared, sourceWorktree := orchCovMergeReceiptFixture(t)
	head := strings.TrimSpace(runEngineGit(t, sourceWorktree, "rev-parse", "HEAD"))
	target := prepared.TargetSHA
	if target == "" {
		t.Fatal("prepared receipt carries no target SHA")
	}
	receipt := WorktreeMergeReceipt{
		Phase: WorktreeMergePhasePrepare, Status: WorktreeMergePreparing,
		Candidate: WorktreeMergeCandidate{Worktree: sourceWorktree, SHA: head},
		TargetSHA: target,
		Sources:   []WorktreeMergeSource{{Branch: "wb/cov/merge", SHA: target}},
	}
	if err := validatePreparingWorktreeMergeCandidate(context.Background(), receipt); err != nil {
		t.Fatalf("interrupted candidate refused: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
		wantIn string
	}{
		{
			name:   "no candidate",
			mutate: func(r *WorktreeMergeReceipt) { r.Candidate.SHA = "" },
			wantIn: "no exact interrupted preparing candidate",
		},
		{
			name:   "head drifted",
			mutate: func(r *WorktreeMergeReceipt) { r.Candidate.SHA = strings.Repeat("b", 40) },
			wantIn: "head drifted",
		},
		{
			name:   "candidate does not contain the target",
			mutate: func(r *WorktreeMergeReceipt) { r.TargetSHA = strings.Repeat("c", 40) },
			wantIn: "verify interrupted candidate target",
		},
		{
			name:   "candidate does not contain a source",
			mutate: func(r *WorktreeMergeReceipt) { r.Sources[0].SHA = strings.Repeat("d", 40) },
			wantIn: "verify interrupted candidate source",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := receipt
			candidate.Sources = append([]WorktreeMergeSource(nil), receipt.Sources...)
			test.mutate(&candidate)
			err := validatePreparingWorktreeMergeCandidate(context.Background(), candidate)
			if err == nil || !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("error = %v, want %q", err, test.wantIn)
			}
		})
	}
}

func TestOrchCovPreparedValidationStillValidNeedsAFingerprintablePass(t *testing.T) {
	valid := WorktreeMergeReceipt{
		Status:    WorktreeMergePrepared,
		TargetSHA: strings.Repeat("b", 40),
		Candidate: WorktreeMergeCandidate{SHA: strings.Repeat("a", 40), Task: "task", Worktree: t.TempDir()},
		Sources:   []WorktreeMergeSource{{Task: "task", Worktree: "/worktree", Branch: "wb/cov", SHA: strings.Repeat("c", 40)}},
		Validation: quality.VerificationReport{
			Status: quality.StatusPassed, Revision: strings.Repeat("a", 40), WorkspaceClean: true,
		},
	}
	identity, fingerprintable := worktreeMergeValidationIdentity(valid)
	if !fingerprintable {
		t.Fatal("a receipt with no recorded validators could not be fingerprinted")
	}
	valid.ValidationIdentity = &identity
	if ok, err := preparedValidationStillValid(valid); err != nil || !ok {
		t.Fatalf("exact passed validation = %t, err %v", ok, err)
	}
	drifted := valid
	drifted.Sources = append([]WorktreeMergeSource(nil), valid.Sources...)
	drifted.Sources[0].SHA = strings.Repeat("e", 40)
	if ok, err := preparedValidationStillValid(drifted); err != nil || ok {
		t.Fatalf("source drift = %t, err %v", ok, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt)
	}{
		{name: "wrong status", mutate: func(r *WorktreeMergeReceipt) { r.Status = WorktreeMergePreparing }},
		{name: "no validation status", mutate: func(r *WorktreeMergeReceipt) { r.Validation.Status = "" }},
		{name: "revision differs", mutate: func(r *WorktreeMergeReceipt) { r.Validation.Revision = strings.Repeat("f", 40) }},
		{name: "worktree was dirty", mutate: func(r *WorktreeMergeReceipt) { r.Validation.WorkspaceClean = false }},
		{name: "no recorded identity", mutate: func(r *WorktreeMergeReceipt) { r.ValidationIdentity = nil }},
		{name: "identity differs", mutate: func(r *WorktreeMergeReceipt) {
			other := identity
			other.CandidateSHA = strings.Repeat("0", 40)
			r.ValidationIdentity = &other
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if ok, err := preparedValidationStillValid(candidate); err != nil || ok {
				t.Fatalf("invalid receipt = %t, err %v", ok, err)
			}
		})
	}
}

func TestOrchCovWorktreeMergeCandidateAbsorbedProvesTreeEquality(t *testing.T) {
	t.Parallel()
	dir := orchCovGitRepo(t)
	base := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))

	// An empty commit has the same tree as its parent, which is exactly the
	// shape of a squash landing that changed nothing beyond the target.
	runEngineGit(t, dir, "checkout", "-b", "candidate")
	runEngineGit(t, dir, "-c", "user.name=WB Test", "-c", "user.email=wb@example.test", "commit", "--allow-empty", "-m", "candidate")
	candidate := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
	runEngineGit(t, dir, "checkout", "main")

	prior := WorktreeMergeReceipt{
		PullRequest: "https://example.test/acme/app/pull/41", PublishedCandidateSHA: candidate,
		LandingSHA: base, Candidate: WorktreeMergeCandidate{SHA: candidate},
	}
	absorbed, contained, err := worktreeMergeCandidateAbsorbed(context.Background(), dir, prior, base)
	if err != nil || !absorbed || contained {
		t.Fatalf("absorbed=%t contained=%t err=%v", absorbed, contained, err)
	}

	// A prior receipt with no publication evidence cannot prove absorption.
	incomplete := prior
	incomplete.PullRequest = ""
	if absorbed, contained, err := worktreeMergeCandidateAbsorbed(context.Background(), dir, incomplete, base); err != nil || absorbed || contained {
		t.Fatalf("incomplete prior = %t/%t, err %v", absorbed, contained, err)
	}
	// A landing that is not on the remote target proves nothing.
	stranded := prior
	stranded.LandingSHA = candidate
	if absorbed, contained, err := worktreeMergeCandidateAbsorbed(context.Background(), dir, stranded, base); err != nil || absorbed || contained {
		t.Fatalf("stranded landing = %t/%t, err %v", absorbed, contained, err)
	}
	// The candidate itself being contained is the graph-containment case.
	if absorbed, contained, err := worktreeMergeCandidateAbsorbed(context.Background(), dir, prior, candidate); err != nil || !absorbed || !contained {
		t.Fatalf("contained candidate = %t/%t, err %v", absorbed, contained, err)
	}
	// An unreadable repository is an error, never a silent "not absorbed".
	if _, _, err := worktreeMergeCandidateAbsorbed(context.Background(), t.TempDir(), prior, base); err == nil {
		t.Fatal("an unreadable candidate repository was accepted")
	}
}

func TestOrchCovCanRefreshWorktreeMergeReceiptRequiresAnAdditiveAdvance(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "refresh-task", "wb/cov/refresh", "refresh.txt", "one\n")
	oldSHA := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "second.txt"), "two\n")
	runEngineGit(t, source.WorktreeDir, "add", "second.txt")
	runEngineGit(t, source.WorktreeDir, "commit", "-m", "advance the source")
	newSHA := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))

	prior := WorktreeMergeReceipt{
		Status: WorktreeMergePrepared,
		Candidate: WorktreeMergeCandidate{
			Task: "refresh-task", Worktree: source.WorktreeDir, Branch: "wb/cov/integration", SHA: newSHA,
		},
		Sources: []WorktreeMergeSource{{Task: "refresh-task", Worktree: source.WorktreeDir, Branch: "wb/cov/refresh", SHA: oldSHA}},
	}
	sources := []WorktreeMergeSource{{Task: "refresh-task", Worktree: source.WorktreeDir, Branch: "wb/cov/refresh", SHA: newSHA}}
	refreshable, err := canRefreshWorktreeMergeReceipt(context.Background(), prior, sources)
	if err != nil || !refreshable {
		t.Fatalf("additive advance refreshable=%t err=%v", refreshable, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt, *[]WorktreeMergeSource)
	}{
		{name: "terminal status", mutate: func(r *WorktreeMergeReceipt, _ *[]WorktreeMergeSource) { r.Status = WorktreeMergeComplete }},
		{name: "already landed", mutate: func(r *WorktreeMergeReceipt, _ *[]WorktreeMergeSource) { r.LandingSHA = "landed" }},
		{name: "source set changed", mutate: func(_ *WorktreeMergeReceipt, s *[]WorktreeMergeSource) { *s = nil }},
		{name: "different source task", mutate: func(_ *WorktreeMergeReceipt, s *[]WorktreeMergeSource) { (*s)[0].Task = "other-task" }},
		{name: "no advance", mutate: func(_ *WorktreeMergeReceipt, s *[]WorktreeMergeSource) { (*s)[0].SHA = oldSHA }},

		{name: "no candidate branch", mutate: func(r *WorktreeMergeReceipt, _ *[]WorktreeMergeSource) { r.Candidate.Branch = "" }},
		{name: "published candidate with a different local head", mutate: func(r *WorktreeMergeReceipt, _ *[]WorktreeMergeSource) {
			r.PullRequest = "https://example.test/acme/app/pull/41"
			r.Candidate.SHA = oldSHA
			r.PublishedCandidateSHA = oldSHA
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := prior
			candidate.Sources = append([]WorktreeMergeSource(nil), prior.Sources...)
			candidateSources := append([]WorktreeMergeSource(nil), sources...)
			test.mutate(&candidate, &candidateSources)
			refreshable, err := canRefreshWorktreeMergeReceipt(context.Background(), candidate, candidateSources)
			if err != nil || refreshable {
				t.Fatalf("refreshable=%t err=%v, want a refusal", refreshable, err)
			}
		})
	}

	// A source whose recorded SHA is not a commit at all fails closed with the
	// underlying Git error rather than being read as "no advance".
	broken := append([]WorktreeMergeSource(nil), sources...)
	broken[0].SHA = strings.Repeat("9", 40)
	if refreshable, err := canRefreshWorktreeMergeReceipt(context.Background(), prior, broken); err == nil || refreshable {
		t.Fatalf("unreadable source refreshable=%t err=%v, want a Git error", refreshable, err)
	}

	// A dirty candidate cannot be refreshed in place.
	dirty := prior
	writeEngineFile(t, filepath.Join(source.WorktreeDir, "uncommitted.txt"), "uncommitted\n")
	if refreshable, err := canRefreshWorktreeMergeReceipt(context.Background(), dirty, sources); err != nil || refreshable {
		t.Fatalf("dirty candidate refreshable=%t err=%v", refreshable, err)
	}
	if err := os.Remove(filepath.Join(source.WorktreeDir, "uncommitted.txt")); err != nil {
		t.Fatal(err)
	}

	// A published candidate may be refreshed only while the recorded published
	// SHA is exactly what origin holds.
	runEngineGit(t, source.WorktreeDir, "push", "origin", "HEAD:refs/heads/wb/cov/integration")
	published := prior
	published.PullRequest = "https://example.test/acme/app/pull/41"
	published.PublishedCandidateSHA = newSHA
	if refreshable, err := canRefreshWorktreeMergeReceipt(context.Background(), published, sources); err != nil || !refreshable {
		t.Fatalf("published at the recorded SHA refreshable=%t err=%v", refreshable, err)
	}
	drifted := published
	drifted.PublishedCandidateSHA = oldSHA
	if refreshable, err := canRefreshWorktreeMergeReceipt(context.Background(), drifted, sources); err != nil || refreshable {
		t.Fatalf("published at a different SHA refreshable=%t err=%v", refreshable, err)
	}
}

func TestOrchCovVerifyWorktreeMergeTargetRefusesUnusableInput(t *testing.T) {
	t.Parallel()
	dir := orchCovGitRepo(t)
	head := strings.TrimSpace(runEngineGit(t, dir, "rev-parse", "HEAD"))
	if _, err := verifyWorktreeMergeTarget(context.Background(), "acme/app", dir, "  ", time.Minute, 0, 0, 0); err == nil ||
		!strings.Contains(err.Error(), "target SHA is required") {
		t.Fatalf("empty target error = %v", err)
	}
	if _, err := verifyWorktreeMergeTarget(context.Background(), "acme/app", dir, "no-such-revision", time.Minute, 0, 0, 0); err == nil ||
		!strings.Contains(err.Error(), "archive target no-such-revision") {
		t.Fatalf("unarchivable target error = %v", err)
	}
	// A repository with no origin remote cannot recreate the read-only remote
	// context the target checks expect.
	if _, err := verifyWorktreeMergeTarget(context.Background(), "acme/app", dir, head, time.Minute, 0, 0, 0); err == nil ||
		!strings.Contains(err.Error(), "candidate origin remote for target baseline") {
		t.Fatalf("origin-less target error = %v", err)
	}
}

func TestOrchCovWorktreeMergeValidationIdentityFingerprintsRecordedValidators(t *testing.T) {
	t.Parallel()
	receipt := WorktreeMergeReceipt{
		Candidate: WorktreeMergeCandidate{SHA: "aaa", Worktree: t.TempDir()},
		TargetSHA: "bbb",
		Sources:   []WorktreeMergeSource{{SHA: "ccc"}},
		Validation: quality.VerificationReport{
			Status: quality.StatusPassed,
			Results: []quality.VerificationEntry{
				{Language: "go", Command: "go test ./...", Status: quality.StatusPassed},
				{Language: "shell", Command: "make check", Status: quality.StatusPassed},
				{Language: "go", Command: "", Status: quality.StatusPassed},
			},
		},
	}
	identity, ok := worktreeMergeValidationIdentity(receipt)
	if !ok {
		t.Fatal("a fingerprintable receipt was refused")
	}
	if identity.CandidateSHA != "aaa" || identity.TargetSHA != "bbb" || len(identity.SourceSHAs) != 1 ||
		identity.QualityPolicySHA == "" || identity.WBExecutableSHA == "" {
		t.Fatalf("identity = %+v", identity)
	}
	if len(identity.Validators) != 1 || identity.Validators["go"] == "" {
		t.Fatalf("validators = %+v, want only the resolvable go validator", identity.Validators)
	}
}
