package orchestrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubchecks"
)

func TestLegacyRemovedPRReceiptAndDigestRequireExactFailedPublication(t *testing.T) {
	t.Parallel()
	r, a := historicalObservedAcknowledgement(t)
	r.Route.Route = WorktreeMergeRoutePullRequest
	r.Cleanup = true
	r.PullRequest = "https://github.com/acme/app/pull/108"
	r.PublishedCandidateSHA = r.Candidate.SHA
	r.ID = worktreeMergeOperationID(r.Lane, r.Sources)
	r.Candidate.Task = r.ID
	a.LegacyRemovedPR = r.PullRequest
	a.ObservedSources = nil
	a.ClaimBaseSHA = ""
	a.CandidateWorktree = r.Candidate.Worktree
	a.CandidateBranch = r.Candidate.Branch
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	digest, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	a.ReceiptSHA256 = digest
	if err := validateLegacyRemovedPRAcknowledgement(a, r); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name   string
		mutate func(*WorktreeMergeReceipt, *WorktreeMergeLandedFailureAcknowledgement)
	}{
		{"failed exact checks", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandedFailureAcknowledgement) {
			r.Checks.Status = githubchecks.PullRequestWaitPassed
		}},
		{"published route", func(r *WorktreeMergeReceipt, _ *WorktreeMergeLandedFailureAcknowledgement) {
			r.Route.Route = WorktreeMergeRouteDirect
		}},
		{"audit identity", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) { a.Actor = "" }},
		{"fabricated claim base", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) {
			a.ClaimBaseSHA = r.TargetSHA
		}},
		{"different PR", func(_ *WorktreeMergeReceipt, a *WorktreeMergeLandedFailureAcknowledgement) { a.LegacyRemovedPR += "1" }},
	} {
		//nolint:paralleltest // Rows read the same receipt before the final missing-file proof.
		t.Run(row.name, func(t *testing.T) {
			rr, aa := r, a
			row.mutate(&rr, &aa)
			if err := validateLegacyRemovedPRAcknowledgement(aa, rr); err == nil {
				t.Fatal("invalid immutable evidence accepted")
			}
		})
	}
	if err := os.Remove(r.ReceiptPath); err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyRemovedPRAcknowledgement(a, r); err == nil {
		t.Fatal("missing historical bytes accepted")
	}
}

func TestLegacyRemovedPullRequestIdentityRequiresExactCanonicalRepositoryAndNumber(t *testing.T) {
	t.Parallel()
	number, canonical, err := canonicalLegacyRemovedPullRequestURL("https://github.com/acme/app/pull/108", "acme/app")
	if err != nil || number != "108" || canonical != "https://github.com/acme/app/pull/108" {
		t.Fatalf("canonical identity = %q, %q, %v", number, canonical, err)
	}
	for _, row := range []struct {
		name, selector, repository string
	}{
		{"foreign repository", "https://github.com/other/app/pull/108", "acme/app"},
		{"repository case", "https://github.com/ACME/app/pull/108", "acme/app"},
		{"foreign host", "https://example.test/acme/app/pull/108", "acme/app"},
		{"plain HTTP", "http://github.com/acme/app/pull/108", "acme/app"},
		{"explicit port", "https://github.com:443/acme/app/pull/108", "acme/app"},
		{"credentials", "https://token@github.com/acme/app/pull/108", "acme/app"},
		{"query", "https://github.com/acme/app/pull/108?diff=1", "acme/app"},
		{"empty query", "https://github.com/acme/app/pull/108?", "acme/app"},
		{"fragment", "https://github.com/acme/app/pull/108#files", "acme/app"},
		{"escaped repository", "https://github.com/acme%2Fapp/pull/108", "acme/app"},
		{"escaped number", "https://github.com/acme/app/pull/1%30%38", "acme/app"},
		{"trailing slash", "https://github.com/acme/app/pull/108/", "acme/app"},
		{"browser tab", "https://github.com/acme/app/pull/108/files", "acme/app"},
		{"missing repository", "https://github.com/pull/108", "acme/app"},
		{"missing number", "https://github.com/acme/app/pull/", "acme/app"},
		{"non-numeric number", "https://github.com/acme/app/pull/abc", "acme/app"},
		{"leading zero", "https://github.com/acme/app/pull/0108", "acme/app"},
		{"zero number", "https://github.com/acme/app/pull/0", "acme/app"},
		{"opaque URL", "https:github.com/acme/app/pull/108", "acme/app"},
		{"relative URL", "/acme/app/pull/108", "acme/app"},
		{"empty URL", "", "acme/app"},
		{"malformed URL", "https://github.com/%zz", "acme/app"},
		{"leading whitespace", " https://github.com/acme/app/pull/108", "acme/app"},
		{"trailing whitespace", "https://github.com/acme/app/pull/108 ", "acme/app"},
		{"surrounding whitespace", "\thttps://github.com/acme/app/pull/108\n", "acme/app"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if number, canonical, err := canonicalLegacyRemovedPullRequestURL(row.selector, row.repository); err == nil || number != "" || canonical != "" {
				t.Fatalf("accepted %q: %q %q %v", row.selector, number, canonical, err)
			}
		})
	}
}

func TestLegacyRemovedAcknowledgementReaderAuthenticatesReceiptRepository(t *testing.T) {
	t.Parallel()
	for _, row := range []struct {
		name, pullRequest, wantErr string
	}{
		{"canonical receipt repository", "https://github.com/acme/app/pull/108", ""},
		{"foreign receipt repository", "https://github.com/other/app/pull/108", "names repository"},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			r, a := legacyRemovedAcknowledgementReaderFixture(t, row.pullRequest)
			stored, err := readLandedFailureAcknowledgement(a.AcknowledgementPath, r)
			if row.wantErr == "" {
				if err != nil || stored.ID != a.ID || stored.LegacyRemovedPR != row.pullRequest {
					t.Fatalf("canonical receipt refused: %+v, %v", stored, err)
				}
				return
			}
			// The fixture recomputes the digest from the foreign receipt's exact
			// bytes, so only the shared canonical repository proof can refuse it.
			if err == nil || !strings.Contains(err.Error(), row.wantErr) || stored.ID != "" {
				t.Fatalf("foreign receipt accepted: %+v, %v", stored, err)
			}
		})
	}
}

// legacyRemovedAcknowledgementReaderFixture builds private temporary receipt and
// acknowledgement files whose digest, sidecar URL, and sidecar identity all agree
// with the stored foreign or canonical pull request.
func legacyRemovedAcknowledgementReaderFixture(t *testing.T, pullRequest string) (WorktreeMergeReceipt, WorktreeMergeLandedFailureAcknowledgement) {
	t.Helper()
	r, _ := historicalObservedAcknowledgement(t)
	r.Route.Route = WorktreeMergeRoutePullRequest
	r.Cleanup = true
	r.PullRequest = pullRequest
	r.PublishedCandidateSHA = r.Candidate.SHA
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	digest, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	a := WorktreeMergeLandedFailureAcknowledgement{
		SchemaVersion: 1, Status: "landed_failure_acknowledged", ReceiptSHA256: digest,
		ReceiptPath: r.ReceiptPath, AcknowledgementPath: landedFailureAcknowledgementPath(r.ReceiptPath),
		ReceiptID: r.ID, ReceiptStatus: r.Status, Lane: r.Lane, Repository: r.Repository, Target: r.Target,
		ReceiptTargetSHA: r.TargetSHA, ReceiptLandingSHA: r.LandingSHA, CurrentTargetSHA: "remote-target-sha",
		CandidateSHA: r.Candidate.SHA, CandidateWorktree: r.Candidate.Worktree, CandidateBranch: r.Candidate.Branch,
		Sources: append([]WorktreeMergeSource(nil), r.Sources...), LegacyRemovedPR: r.PullRequest,
		Actor: "operator", Reason: "historical removed pull request proof", RecordedAt: r.CreatedAt,
	}
	a.ID = landedFailureAcknowledgementID(a)
	writeAcknowledgementReadJSON(t, a.AcknowledgementPath, a)
	return r, a
}

func TestLegacyRemovedPullRequestResponseIdentityBindsRepositoryAndNumber(t *testing.T) {
	t.Parallel()
	expected := legacyRemovedPullRequestView{Number: 108, URL: "https://github.com/acme/app/pull/108", State: "MERGED"}
	if err := validateLegacyRemovedPullRequestResponse(expected, "acme/app", "108", "https://github.com/acme/app/pull/108"); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name string
		view legacyRemovedPullRequestView
	}{
		{"missing response number", legacyRemovedPullRequestView{URL: expected.URL}},
		{"foreign response number", legacyRemovedPullRequestView{Number: 109, URL: expected.URL}},
		{"missing response URL", legacyRemovedPullRequestView{Number: 108}},
		{"foreign response repository", legacyRemovedPullRequestView{Number: 108, URL: "https://github.com/other/app/pull/108"}},
		{"foreign response host", legacyRemovedPullRequestView{Number: 108, URL: "https://example.test/acme/app/pull/108"}},
		{"different response URL number", legacyRemovedPullRequestView{Number: 108, URL: "https://github.com/acme/app/pull/109"}},
	} {
		t.Run(row.name, func(t *testing.T) {
			t.Parallel()
			if err := validateLegacyRemovedPullRequestResponse(row.view, "acme/app", "108", "https://github.com/acme/app/pull/108"); err == nil {
				t.Fatal("foreign response identity accepted")
			}
		})
	}
}

// TestContractLegacyRemovedPRAcknowledgementRefusesAmbiguousCanonicalRepository
// proves recovery cannot pick one of two real canonical clones for the same
// coordinate and that the refusal happens before any acknowledgement or receipt
// bytes are written. The existing malformed-repository case is refused by the
// shared pull request validator earlier, so only an actual ambiguity reaches
// this resolver.
func TestContractLegacyRemovedPRAcknowledgementRefusesAmbiguousCanonicalRepository(t *testing.T) {
	t.Parallel()
	r, _ := historicalObservedAcknowledgement(t)
	r.Route.Route = WorktreeMergeRoutePullRequest
	r.Cleanup = true
	r.PullRequest = "https://github.com/acme/app/pull/108"
	r.PublishedCandidateSHA = r.Candidate.SHA
	r.ID = worktreeMergeOperationID(r.Lane, r.Sources)
	r.Candidate.Task = r.ID
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	projectsRoot := t.TempDir()
	remote := filepath.Join(t.TempDir(), "app.git")
	runEngineGit(t, projectsRoot, "init", "--bare", "--initial-branch=main", remote)
	for _, host := range []string{"github.com", "gitlab.example.test"} {
		canonical := filepath.Join(projectsRoot, host, "acme", "app")
		if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
			t.Fatal(err)
		}
		runEngineGit(t, projectsRoot, "clone", "--no-hardlinks", remote, canonical)
	}
	o := WorktreeMergeLandedFailureAcknowledgementOptions{
		ProjectsRoot: projectsRoot, Receipt: r.ReceiptPath, LegacyRemovedPR: true,
		Actor: "reviewer", Reason: "ambiguous canonical repository",
	}
	if _, err := acknowledgeLegacyRemovedPRFailure(t.Context(), o, r, r.ReceiptPath, defaultRunner, readWorktreeMergeReceipt, persistLandedFailureAcknowledgement); err == nil || !strings.Contains(err.Error(), "more than one host") {
		t.Fatalf("ambiguous canonical repository accepted: %v", err)
	}
	if _, err := os.Stat(landedFailureAcknowledgementPath(r.ReceiptPath)); !os.IsNotExist(err) {
		t.Fatalf("ambiguous repository wrote an acknowledgement: %v", err)
	}
	after, err := os.ReadFile(r.ReceiptPath)
	if err != nil || string(before) != string(after) {
		t.Fatalf("ambiguous repository changed the failed receipt: %v", err)
	}
}
