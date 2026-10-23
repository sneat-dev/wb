package worktrees

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/worktreebranches"
)

// A type change on either side must fail compilation of this facade test.
var _ = func(value *PullRequest) *worktreebranches.PullRequest { return value }

func TestBranchPolicyFacadePreservesSelectorsPlansAndJSON(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	sweep := branchSweepOptions{Base: "main", Scope: BranchScopeAll, Only: BranchRetired, Branch: "retired/one", Name: "retired/*", OlderThan: time.Hour, Now: now}
	policy := sweep.branchPolicyOptions()
	if policy.Base != sweep.Base || policy.Scope != sweep.Scope || policy.Only != sweep.Only || policy.Branch != sweep.Branch || policy.Name != sweep.Name || policy.OlderThan != sweep.OlderThan || policy.Now != sweep.Now {
		t.Fatalf("adapter lost selection fields: %+v", policy)
	}
	if !retiredNamespaceSelected(sweep) || !worktreebranches.BranchNameSelected(policy, "retired/one") || worktreebranches.BranchNameSelected(policy, "retired/two") || !isRetiredBranch("retired/one") {
		t.Fatal("facade retired selectors diverged")
	}
	ref := branchRef{Name: "retired/one", SHA: "1234567890123", CommitterDate: now.Add(-2 * time.Hour)}
	if !worktreebranches.RetiredRefSelected(policy, ref) || shortSHA(ref.SHA) != "123456789012" {
		t.Fatal("facade ref selection or SHA shortening diverged")
	}
	repository := discover.Repo{Org: "org", Name: "repo"}
	counts, names := map[string]int{}, map[string]bool{}
	worktreebranches.AccumulateRetiredCounts(policy, repository.Slug(), []branchRef{ref}, BranchScopeLocal, counts, names)
	if counts[BranchScopeLocal] != 1 || !names[repository.Slug()+"|retired/one"] {
		t.Fatalf("facade retired counts: %v %v", counts, names)
	}
	retired := worktreebranches.RetiredBranchEntry(repository.Slug(), policy, ref, BranchScopeLocal, "target")
	if retired.Disposition != BranchRetired || retired.ShortSHA != "123456789012" || retired.Repository != repository.Slug() {
		t.Fatalf("facade retired entry: %+v", retired)
	}
	pr := &PullRequest{Number: 9, URL: "https://example.test/9"}
	entry := worktreebranches.ReceiptedBranch(BranchEntry{Base: "main", PullRequests: []BranchPullRequest{{Number: 9}}}, ref.SHA, pr, "")
	if entry.ReceiptPullRequest != pr || entry.Disposition != BranchReceipted || !strings.Contains(entry.Evidence, "#9") {
		t.Fatalf("facade receipt identity: %+v", entry)
	}
	if !isProtectedBranch("main", "main", "other") || protectedEvidence("main", "main", "other") != `is the base branch "main"` {
		t.Fatal("facade protection diverged")
	}
	if !worktreebranches.ScopeIncludesRemote(BranchScopeAll) || !worktreebranches.PeerEvidenceSafeDisposition(BranchUnique) {
		t.Fatal("facade scope or peer policy diverged")
	}
	if got := worktreebranches.RetiredBranchDestination(now, "feature/one", ref.SHA); got != "retired/20260930-feature-one-123456789012" {
		t.Fatalf("facade retirement destination: %s", got)
	}
	entries := []BranchEntry{{Repository: "z", Branch: "b", Scope: BranchScopeLocal, Disposition: BranchContained}, {Repository: "a", Branch: "a", Scope: BranchScopeRemote, Disposition: BranchReceipted}}
	filtered := applyListDisplayFilters(entries, branchSweepOptions{Only: BranchContained})
	if len(filtered) != 1 || filtered[0].Repository != "z" || &filtered[0] == &entries[0] {
		t.Fatalf("facade filter copy: %+v", filtered)
	}
	sortBranchEntries(entries)
	if entries[0].Repository != "a" || !reflect.DeepEqual(tallyDispositions(entries), map[string]int{BranchContained: 1, BranchReceipted: 1}) {
		t.Fatalf("facade sort or totals: %+v", entries)
	}
	if !worktreebranches.EligibleBranchCleanupDisposition(entry) || worktreebranches.SkipReasonForDisposition(BranchEntry{Evidence: "reason"}) != "reason" {
		t.Fatal("facade eligibility or reason diverged")
	}
	if !worktreebranches.RemotePullRequestEvidenceUnavailable([]BranchEntry{{Scope: BranchScopeRemote, Disposition: BranchContained, PullRequestQueryFailed: true}}, branchSweepOptions{Scope: BranchScopeAll}.branchPolicyOptions()) {
		t.Fatal("facade remote evidence guard diverged")
	}
	plan := worktreebranches.PlanBranchCleanup([]BranchEntry{{Scope: BranchScopeLocal, Disposition: BranchContained}}, branchSweepOptions{Scope: BranchScopeLocal}.branchPolicyOptions())
	if len(plan) != 1 || !plan[0].Eligible || !reflect.DeepEqual(worktreebranches.TallyCleanupOutcomes(plan), map[string]int{"planned": 1}) {
		t.Fatalf("facade cleanup plan: %+v", plan)
	}
	if got := worktreebranches.PlanBranchCleanup(nil, branchSweepOptions{}.branchPolicyOptions()); got == nil {
		t.Fatal("nil facade plan should return empty nonnil slice")
	}
	// Type aliases preserve the exact DTO identity and JSON tags through the facade.
	same, ok := any(pr).(*worktreebranches.PullRequest)
	if !ok || same != pr {
		t.Fatal("PR alias changed pointer identity")
	}
	raw, err := json.Marshal(BranchCleanupResult{BranchEntry: entry, Outcome: "planned"})
	if err != nil || !strings.Contains(string(raw), `"receipt_pull_request"`) || strings.Contains(string(raw), `"skip_reason"`) {
		t.Fatalf("facade JSON shape: %s %v", raw, err)
	}
}
