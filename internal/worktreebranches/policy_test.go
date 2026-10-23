package worktreebranches

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBranchInventorySelectionAndCounting(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 30, 23, 0, 0, 0, time.FixedZone("west", -4*3600))
	for _, tc := range []struct {
		options PolicyOptions
		want    bool
	}{
		{PolicyOptions{Only: BranchRetired}, true},
		{PolicyOptions{Branch: "retired/nested/one"}, true},
		{PolicyOptions{Name: "retired/*"}, true},
		{PolicyOptions{Branch: "feature", Name: "other*"}, false},
	} {
		if got := RetiredNamespaceSelected(tc.options); got != tc.want {
			t.Fatalf("retired namespace %v: got %v, want %v", tc.options, got, tc.want)
		}
	}
	for _, tc := range []struct {
		options PolicyOptions
		name    string
		want    bool
	}{
		{PolicyOptions{}, "retired/nested/one", true},
		{PolicyOptions{Branch: "retired/nested/one"}, "retired/nested/one", true},
		{PolicyOptions{Branch: "other"}, "retired/nested/one", false},
		{PolicyOptions{Name: "retired/*"}, "retired/one", true},
		{PolicyOptions{Name: "retired/*"}, "retired/nested/one", false},
		{PolicyOptions{Name: "retired/["}, "retired/one", false},
		{PolicyOptions{Name: "feature/*"}, "retired/one", false},
	} {
		if got := BranchNameSelected(tc.options, tc.name); got != tc.want {
			t.Errorf("name %q under %+v: got %v, want %v", tc.name, tc.options, got, tc.want)
		}
	}
	old := now.Add(-2 * time.Hour)
	for _, tc := range []struct {
		ref  BranchRef
		age  time.Duration
		want bool
	}{
		{BranchRef{Name: "feature"}, 0, false},
		{BranchRef{Name: "retired/one"}, 0, true},
		{BranchRef{Name: "retired/one", CommitterDate: old}, 2 * time.Hour, true},
		{BranchRef{Name: "retired/one", CommitterDate: now.Add(-time.Minute)}, 2 * time.Hour, false},
		{BranchRef{Name: "retired/one", CommitterDate: now.Add(time.Hour)}, time.Hour, false},
		{BranchRef{Name: "retired/one"}, time.Hour, false},
		{BranchRef{Name: "retired/one", CommitterDate: old, UnknownDate: true}, time.Hour, false},
		{BranchRef{Name: "retired/one", CommitterDate: old}, -time.Hour, true},
	} {
		if got := RetiredRefSelected(PolicyOptions{Now: now, OlderThan: tc.age}, tc.ref); got != tc.want {
			t.Errorf("retired ref %+v age %s: got %v, want %v", tc.ref, tc.age, got, tc.want)
		}
	}
	if RetiredRefSelected(PolicyOptions{Branch: "retired/other"}, BranchRef{Name: "retired/one"}) {
		t.Fatal("exact branch selector admitted another retired ref")
	}
	counts := map[string]int{}
	names := map[string]bool{}
	refs := []BranchRef{{Name: "retired/one", CommitterDate: old}, {Name: "retired/one", CommitterDate: old}, {Name: "retired/two", CommitterDate: old}, {Name: "feature"}}
	AccumulateRetiredCounts(PolicyOptions{Now: now, OlderThan: time.Hour}, "org/repo", refs, BranchScopeLocal, counts, names)
	AccumulateRetiredCounts(PolicyOptions{Now: now, OlderThan: time.Hour}, "org/repo", refs[:1], BranchScopeRemote, counts, names)
	if !reflect.DeepEqual(counts, map[string]int{BranchScopeLocal: 3, BranchScopeRemote: 1}) || len(names) != 2 || !names["org/repo|retired/one"] {
		t.Fatalf("retired counts lost scope or distinct names: %v %v", counts, names)
	}
	entry := RetiredBranchEntry("org/repo", PolicyOptions{Base: "main"}, BranchRef{Name: "retired/one", SHA: "1234567890123456", Author: "A", Title: "T", CommitterDate: old}, BranchScopeRemote, "target")
	if entry.Repository != "org/repo" || entry.RefKind != "branch" || entry.ShortSHA != "123456789012" || entry.Base != "main" || entry.TargetSHA != "target" || entry.Author != "A" || entry.Title != "T" || entry.CommitterDate != old || entry.Disposition != BranchRetired || !strings.Contains(entry.Evidence, "quarantine") {
		t.Fatalf("retired entry lost evidence: %+v", entry)
	}
	if !IsRetiredBranch("retired/one") || IsRetiredBranch("retired") || IsRetiredBranch("feature/retired/one") {
		t.Fatal("retired namespace prefix was not exact")
	}
}

func TestDisplayFiltersSortAndTotalsPreserveInputs(t *testing.T) {
	t.Parallel()
	now := time.Now()
	entries := []BranchEntry{
		{Repository: "z", Branch: "b", Scope: BranchScopeRemote, Disposition: BranchContained, CommitterDate: now.Add(-2 * time.Hour)},
		{Repository: "a", Branch: "b", Scope: BranchScopeRemote, Disposition: BranchUnique, CommitterDate: now.Add(-2 * time.Hour)},
		{Repository: "a", Branch: "b", Scope: BranchScopeLocal, Disposition: BranchContained, CommitterDate: now},
		{Repository: "a", Branch: "a", Scope: BranchScopeLocal, Disposition: BranchContained},
	}
	filtered := ApplyListDisplayFilters(entries, PolicyOptions{Only: BranchContained, OlderThan: time.Hour, Now: now})
	if len(filtered) != 2 || filtered[0].Repository != "z" || filtered[1].Branch != "a" || &filtered[0] == &entries[0] {
		t.Fatalf("age/only filtering or copy behavior changed: %+v", filtered)
	}
	if got := ApplyListDisplayFilters(entries, PolicyOptions{OlderThan: -time.Hour, Now: now}); len(got) != len(entries) {
		t.Fatalf("nonpositive age filtered entries: %+v", got)
	}
	if got := ApplyListDisplayFilters(nil, PolicyOptions{}); got == nil || len(got) != 0 {
		t.Fatalf("nil input should produce empty nonnil slice: %#v", got)
	}
	SortBranchEntries(entries)
	if got := []string{entries[0].Branch, entries[1].Scope, entries[2].Scope, entries[3].Repository}; !reflect.DeepEqual(got, []string{"a", BranchScopeLocal, BranchScopeRemote, "z"}) {
		t.Fatalf("three-key sort: %v", got)
	}
	if got := TallyDispositions(entries); !reflect.DeepEqual(got, map[string]int{BranchContained: 3, BranchUnique: 1}) {
		t.Fatalf("disposition totals: %v", got)
	}
	if got := TallyDispositions(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil disposition totals: %v", got)
	}
}

func TestCleanupPlanningGuardsAndReasons(t *testing.T) {
	t.Parallel()
	now := time.Now()
	openHead := &PullRequest{URL: "https://example.test/head"}
	openBase := &PullRequest{URL: "https://example.test/base"}
	for _, disposition := range []string{BranchContained, BranchReceipted, BranchAbsorbed, BranchUnique, BranchProtected, BranchInUse, BranchUnreadable, BranchRetired, BranchSuperseded} {
		for _, atOrigin := range []bool{false, true} {
			want := disposition == BranchContained || disposition == BranchReceipted || (disposition == BranchSuperseded && atOrigin)
			if got := EligibleBranchCleanupDisposition(BranchEntry{Disposition: disposition, SupersededAtOrigin: atOrigin}); got != want {
				t.Errorf("eligibility %s origin %v: got %v", disposition, atOrigin, got)
			}
		}
		if got := PeerEvidenceSafeDisposition(disposition); got != (disposition == BranchContained || disposition == BranchReceipted || disposition == BranchAbsorbed || disposition == BranchUnique) {
			t.Errorf("peer disposition %s: %v", disposition, got)
		}
	}
	if got := SkipReasonForDisposition(BranchEntry{Reason: "tailored", Evidence: "evidence"}); got != "tailored" {
		t.Fatal(got)
	}
	if got := SkipReasonForDisposition(BranchEntry{Evidence: "evidence"}); got != "evidence" {
		t.Fatal(got)
	}
	if got := SkipReasonForDisposition(BranchEntry{Disposition: BranchUnique}); got != "disposition unique is never eligible for --apply" {
		t.Fatal(got)
	}
	if !ScopeIncludesRemote(BranchScopeRemote) || !ScopeIncludesRemote(BranchScopeAll) || ScopeIncludesRemote(BranchScopeLocal) || ScopeIncludesRemote("") {
		t.Fatal("scope guard changed")
	}
	entries := []BranchEntry{
		{Branch: "unsafe", Scope: BranchScopeLocal, Disposition: BranchUnique, Reason: "unique"},
		{Branch: "young", Scope: BranchScopeLocal, Disposition: BranchContained, CommitterDate: now},
		{Branch: "remote unavailable", Scope: BranchScopeRemote, Disposition: BranchContained, OpenPullRequest: openHead},
		{Branch: "local unaffected", Scope: BranchScopeLocal, Disposition: BranchContained, CommitterDate: now.Add(-2 * time.Hour)},
		{Branch: "remote failed", Scope: BranchScopeRemote, Disposition: BranchReceipted, PullRequestQueryFailed: true},
	}
	if RemotePullRequestEvidenceUnavailable(entries, PolicyOptions{Scope: BranchScopeLocal}) || !RemotePullRequestEvidenceUnavailable(entries, PolicyOptions{Scope: BranchScopeAll}) {
		t.Fatal("remote evidence scope not isolated")
	}
	results := PlanBranchCleanup(entries, PolicyOptions{Scope: BranchScopeAll, OlderThan: time.Hour, Now: now})
	if results[0].SkipReason != "unique" || results[1].SkipReason != "branch is younger than --older-than 1h0m0s" || results[2].SkipReason != "remote pull-request evidence unavailable; refusing every remote deletion in this run" || !results[3].Eligible || results[3].Outcome != "planned" || results[4].Eligible {
		t.Fatalf("skip precedence or local isolation: %+v", results)
	}
	if results[2].OpenPullRequest != openHead {
		t.Fatal("cleanup result lost pull request pointer identity")
	}
	for _, tc := range []struct {
		entry BranchEntry
		want  string
	}{
		{BranchEntry{Scope: BranchScopeRemote, Disposition: BranchContained, OpenPullRequest: openHead, OpenBasePullRequest: openBase}, "branch is the head of open pull request https://example.test/head"},
		{BranchEntry{Scope: BranchScopeRemote, Disposition: BranchContained, OpenBasePullRequest: openBase}, "branch is the base of open pull request https://example.test/base"},
		{BranchEntry{Scope: BranchScopeRemote, Disposition: BranchContained}, ""},
		{BranchEntry{Scope: BranchScopeLocal, Disposition: BranchSuperseded, SupersededAtOrigin: true}, ""},
		{BranchEntry{Scope: BranchScopeLocal, Disposition: BranchSuperseded}, "disposition superseded is never eligible for --apply"},
	} {
		result := PlanBranchCleanup([]BranchEntry{tc.entry}, PolicyOptions{Scope: BranchScopeAll})[0]
		if result.SkipReason != tc.want || result.Eligible != (tc.want == "") {
			t.Errorf("plan %+v: %+v, want reason %q", tc.entry, result, tc.want)
		}
	}
	if got := PlanBranchCleanup(nil, PolicyOptions{}); got == nil || len(got) != 0 {
		t.Fatalf("nil plan: %#v", got)
	}
	if RemotePullRequestEvidenceUnavailable([]BranchEntry{{Scope: BranchScopeRemote, Disposition: BranchUnique, PullRequestQueryFailed: true}}, PolicyOptions{Scope: BranchScopeRemote}) {
		t.Fatal("ineligible remote failure poisoned the scope")
	}
	if RemotePullRequestEvidenceUnavailable([]BranchEntry{{Scope: BranchScopeLocal, Disposition: BranchContained, PullRequestQueryFailed: true}}, PolicyOptions{Scope: BranchScopeAll}) {
		t.Fatal("local failure poisoned remote scope")
	}
	if got := TallyCleanupOutcomes([]BranchCleanupResult{{Outcome: "planned"}, {Outcome: "planned"}, {Outcome: "skipped"}}); !reflect.DeepEqual(got, map[string]int{"planned": 2, "skipped": 1}) {
		t.Fatalf("cleanup totals: %v", got)
	}
	if got := TallyCleanupOutcomes(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil cleanup totals: %v", got)
	}
}

func TestReceiptProtectionAndRetirementFormatting(t *testing.T) {
	t.Parallel()
	pr := &PullRequest{Number: 42, URL: "https://example.test/42"}
	base := BranchEntry{Base: "main", PullRequests: []BranchPullRequest{{Number: 42}}}
	fromPR := ReceiptedBranch(base, "1234567890123456", pr, "")
	if fromPR.Disposition != BranchReceipted || fromPR.ReceiptPullRequest != pr || fromPR.LandingSHA != "1234567890123456" || fromPR.Reason != "landed via merged pull request #42; eligible for deletion under --receipts" || !strings.Contains(fromPR.Evidence, "landing 123456789012") || base.Disposition != "" {
		t.Fatalf("merged receipt changed: %+v", fromPR)
	}
	fromPointer := ReceiptedBranch(base, "short", nil, "#12")
	if fromPointer.ReceiptPullRequest != nil || fromPointer.Reason != "content-proven absorbed via --absorbed-by #12; eligible for deletion" || !strings.Contains(fromPointer.Evidence, "resolved to short") {
		t.Fatalf("explicit receipt changed: %+v", fromPointer)
	}
	if ShortSHA("123456789012") != "123456789012" || ShortSHA("1234567890123") != "123456789012" || ShortSHA("") != "" {
		t.Fatal("SHA shortening boundary")
	}
	for _, tc := range []struct {
		branch, base, head, evidence string
		protected                    bool
	}{
		{"main", "main", "main", "is the base branch \"main\"", true},
		{"feature", "main", "feature", "is the canonical clone's current HEAD", true},
		{"master", "main", "other", "matches a configured protected branch name", true},
		{"feature", "main", "other", "matches a configured protected branch name", false},
	} {
		if IsProtectedBranch(tc.branch, tc.base, tc.head) != tc.protected || ProtectedEvidence(tc.branch, tc.base, tc.head) != tc.evidence {
			t.Errorf("protected precedence for %+v", tc)
		}
	}
	now := time.Date(2026, 10, 1, 1, 2, 3, 0, time.FixedZone("east", 2*3600))
	if got := RetiredBranchDestination(now, "feature/nested with spaces/..", "123456789012345"); got != "retired/20260930-feature-nested-with-spaces-123456789012" {
		t.Fatalf("retirement destination: %s", got)
	}
	if got := RetiredBranchDestination(now, "...///", "abc"); got != "retired/20260930-branch-abc" {
		t.Fatalf("empty sanitized segment: %s", got)
	}
	encoded, err := json.Marshal(BranchCleanupResult{BranchEntry: fromPR, Outcome: "planned"})
	if err != nil || !strings.Contains(string(encoded), `"receipt_pull_request"`) || !strings.Contains(string(encoded), `"eligible":false`) || strings.Contains(string(encoded), `"skip_reason"`) {
		t.Fatalf("cleanup JSON shape: %s %v", encoded, err)
	}
}
