package worktrees

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/worktreebranches"
)

func TestBranchesCoverageBatchNormalizeOptions(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	valid, err := normalizeBranchListOptions(BranchListOptions{
		ProjectsRoot: root, Base: " ", Filter: " needle ", Repository: " acme/app ",
		Org: " acme ", Branch: " feature/one ", Name: " feature/* ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if valid.Base != "main" || valid.Scope != BranchScopeLocal || valid.Filter != "needle" ||
		valid.Repository != "acme/app" || valid.Org != "acme" || valid.Branch != "feature/one" || valid.Name != "feature/*" {
		t.Fatalf("normalized options = %#v", valid)
	}
	for name, mutate := range map[string]func(*BranchListOptions){
		"base":       func(options *BranchListOptions) { options.Base = "bad..branch" },
		"scope":      func(options *BranchListOptions) { options.Scope = "sideways" },
		"only":       func(options *BranchListOptions) { options.Only = "mystery" },
		"older than": func(options *BranchListOptions) { options.OlderThan = -time.Second },
		"repository": func(options *BranchListOptions) { options.Repository = "unqualified" },
		"org":        func(options *BranchListOptions) { options.Org = "." },
		"branch":     func(options *BranchListOptions) { options.Branch = "bad..branch" },
		"name":       func(options *BranchListOptions) { options.Name = "[" },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			options := BranchListOptions{ProjectsRoot: root, Base: "main", Scope: BranchScopeLocal}
			mutate(&options)
			if _, err := normalizeBranchListOptions(options); err == nil {
				t.Fatalf("invalid %s option was accepted", name)
			}
		})
	}
}

func TestBranchesCoverageBatchPureInventory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	now := time.Unix(1_000, 0).UTC()

	outcome, err := sweepBranches(ctx, BranchListOptions{
		ProjectsRoot: root, Base: "main", Scope: BranchScopeLocal,
	})
	if err != nil || len(outcome.Entries) != 0 {
		t.Fatalf("empty sweep = %#v/%v", outcome, err)
	}
	if _, err := inventoryRetiredNamespace(ctx, branchSweepOptions{
		ProjectsRoot: root, Base: "main", Scope: BranchScopeLocal, Repository: "acme/missing",
	}, now); err == nil {
		t.Fatal("missing selected retired repository was accepted")
	}
	listed, err := BranchList(ctx, BranchListOptions{ProjectsRoot: root})
	if err != nil || len(listed.Entries) != 0 || listed.Base != "main" || listed.Scope != BranchScopeLocal {
		t.Fatalf("public empty branch list = %#v/%v", listed, err)
	}

	repository := discover.Repo{Org: "acme", Name: "app", Path: root}
	entries := []BranchEntry{}
	names := map[string]bool{}
	count := branchInventoryService().AppendRetiredEntries(ctx, &entries, names, branchInventoryRepository(repository), (branchSweepOptions{
		Now: now, Name: "retired/*",
	}).branchInventorySweep(), []branchRef{
		{Name: "active/one"},
		{Name: "retired/one", UnknownDate: true},
	}, BranchScopeLocal)
	if count != 1 || len(entries) != 1 || !names["acme/app|retired/one"] {
		t.Fatalf("retired append = count %d entries %#v names %#v", count, entries, names)
	}

	if strings.TrimSpace(branchEvidenceHost()) == "" {
		t.Fatal("branch evidence host is empty")
	}

	filtered := applyListDisplayFilters([]BranchEntry{
		{Branch: "keep", Disposition: BranchContained, CommitterDate: now.Add(-2 * time.Hour)},
		{Branch: "wrong-kind", Disposition: BranchUnique, CommitterDate: now.Add(-2 * time.Hour)},
		{Branch: "young", Disposition: BranchContained, CommitterDate: now.Add(-time.Minute)},
		{Branch: "unknown-date", Disposition: BranchContained},
	}, branchSweepOptions{Only: BranchContained, OlderThan: time.Hour, Now: now})
	if len(filtered) != 2 || filtered[0].Branch != "keep" || filtered[1].Branch != "unknown-date" {
		t.Fatalf("display filters = %#v", filtered)
	}

	unsorted := []BranchEntry{
		{Repository: "z/repo", Branch: "a", Scope: BranchScopeRemote},
		{Repository: "a/repo", Branch: "b", Scope: BranchScopeLocal},
		{Repository: "a/repo", Branch: "a", Scope: BranchScopeRemote},
		{Repository: "a/repo", Branch: "a", Scope: BranchScopeLocal},
	}
	sortBranchEntries(unsorted)
	if got := []string{
		unsorted[0].Repository + "/" + unsorted[0].Branch + "/" + unsorted[0].Scope,
		unsorted[1].Repository + "/" + unsorted[1].Branch + "/" + unsorted[1].Scope,
		unsorted[2].Repository + "/" + unsorted[2].Branch + "/" + unsorted[2].Scope,
		unsorted[3].Repository + "/" + unsorted[3].Branch + "/" + unsorted[3].Scope,
	}; strings.Join(got, ",") != "a/repo/a/local,a/repo/a/remote,a/repo/b/local,z/repo/a/remote" {
		t.Fatalf("sorted entries = %#v", got)
	}

	classified, diagnostics, paths, err := classifyFleetBranchesWithPaths(ctx, branchSweepOptions{
		ProjectsRoot: root, Base: "main", Scope: BranchScopeLocal,
	})
	if err != nil || len(classified) != 0 || len(diagnostics) != 0 || len(paths) != 0 {
		t.Fatalf("empty fleet classification = %#v/%#v/%#v/%v", classified, diagnostics, paths, err)
	}
	if _, _, _, err := classifyFleetBranchesWithPaths(ctx, branchSweepOptions{
		ProjectsRoot: root, Base: "main", Scope: BranchScopeLocal, Repository: "acme/missing",
	}); err == nil {
		t.Fatal("missing selected fleet repository was accepted")
	}

	counts, branchNames, tagCounts, tagNames, unavailable, diagnostics := countRetiredBranches(ctx, branchSweepOptions{
		ProjectsRoot: root, Scope: BranchScopeAll,
	})
	if len(counts) != 0 || branchNames != 0 || len(tagCounts) != 0 || tagNames != 0 || unavailable || len(diagnostics) != 0 {
		t.Fatalf("empty retired counts = %#v/%d/%#v/%d/%t/%#v", counts, branchNames, tagCounts, tagNames, unavailable, diagnostics)
	}
	_, _, _, _, _, diagnostics = countRetiredBranches(ctx, branchSweepOptions{
		ProjectsRoot: root, Scope: BranchScopeLocal, Repository: "acme/missing",
	})
	if len(diagnostics) != 1 || !strings.Contains(diagnostics[0], "was not discovered") {
		t.Fatalf("missing retired-count repository diagnostics = %#v", diagnostics)
	}

	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if repositories, err := branchInventoryService().DiscoverBranchRepositories(fileRoot, ""); err == nil || repositories != nil {
		t.Fatalf("repository discovery through file = %#v/%v", repositories, err)
	}
	if repositories, err := branchInventoryService().DiscoverBranchRepositories(root, "missing"); err != nil || len(repositories) != 0 {
		t.Fatalf("filtered repository discovery = %#v/%v", repositories, err)
	}
	if index, diagnostic := branchInUseIndex(ctx, fileRoot, ""); diagnostic == "" || len(index) != 0 {
		t.Fatalf("in-use index through file = %#v/%q", index, diagnostic)
	}
}

func TestBranchesCoverageBatchRefactoredInventoryHelpers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Unix(10_000, 0).UTC()
	repositories := []discover.Repo{
		{Org: "acme", Name: "one", Path: "/repos/acme/one"},
		{Org: "acme", Name: "two", Path: "/repos/acme/two"},
		{Org: "other", Name: "three", Path: "/repos/other/three"},
	}
	sweep := branchSweepOptions{Now: now, OlderThan: time.Hour, Name: "retired/*", Base: "main"}
	refs := []branchRef{
		{Name: "retired/old", CommitterDate: now.Add(-2 * time.Hour)},
		{Name: "retired/new", CommitterDate: now.Add(-time.Minute)},
		{Name: "active/old", CommitterDate: now.Add(-2 * time.Hour)},
	}
	counts, names := map[string]int{}, map[string]bool{}
	worktreebranches.AccumulateRetiredCounts(sweep.branchPolicyOptions(), repositories[0].Slug(), refs, BranchScopeLocal, counts, names)
	if counts[BranchScopeLocal] != 1 || !names["acme/one|retired/old"] || len(names) != 1 {
		t.Fatalf("retired counts = %#v/%#v", counts, names)
	}

	entries := []BranchEntry{}
	tagNames := map[string]bool{}
	if count := branchInventoryService().AppendRetiredTagEntries(ctx, &entries, tagNames, branchInventoryRepository(repositories[0]), sweep.branchInventorySweep(),
		[]branchRef{{Name: "retired/old", CommitterDate: now.Add(-2 * time.Hour)}}, BranchScopeRemote); count != 1 {
		t.Fatalf("appended retired tags = %d", count)
	}
	if len(entries) != 1 || entries[0].RefKind != "tag" || entries[0].Scope != BranchScopeRemote || !tagNames["acme/one|retired/old"] {
		t.Fatalf("retired tag entries = %#v/%#v", entries, tagNames)
	}

	if !retiredNamespaceSelected(branchSweepOptions{Only: BranchRetired}) ||
		!retiredNamespaceSelected(branchSweepOptions{Branch: "retired/one"}) ||
		!retiredNamespaceSelected(branchSweepOptions{Name: "retired/*"}) ||
		retiredNamespaceSelected(branchSweepOptions{Branch: "feature/one"}) {
		t.Fatal("retired namespace selector classification is inconsistent")
	}
	if got := tallyDispositions([]BranchEntry{{Disposition: BranchRetired}, {Disposition: BranchRetired}, {Disposition: BranchUnique}}); got[BranchRetired] != 2 || got[BranchUnique] != 1 {
		t.Fatalf("disposition totals = %#v", got)
	}

	var progress bytes.Buffer
	worktreebranches.ReportBranchProgress(nil, 1, 1, "ignored")
	worktreebranches.ReportBranchProgress(&progress, 2, 3, "acme/one")
	worktreebranches.ReportBranchSummary(&progress, map[string]int{BranchUnique: 2, BranchContained: 1}, 1500*time.Millisecond)
	if output := progress.String(); !strings.Contains(output, "[2/3] scanning acme/one") ||
		!strings.Contains(output, "contained=1 unique=2") {
		t.Fatalf("progress output = %q", output)
	}

}

func TestBranchesCoverageBatchSHAShorteningAndReceiptedBranchEvidence(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)

	if got := shortSHA("short"); got != "short" {
		t.Fatalf("short SHA = %q", got)
	}
	if got := shortSHA("123456789012345"); got != "123456789012" {
		t.Fatalf("trimmed SHA = %q", got)
	}

	pullRequest := &PullRequest{Number: 17}
	explicit := worktreebranches.ReceiptedBranch(BranchEntry{Base: "main"}, sha, pullRequest, "17")
	if explicit.Disposition != BranchReceipted || explicit.LandingSHA != sha || explicit.ReceiptPullRequest != pullRequest ||
		!strings.Contains(explicit.Evidence, "--absorbed-by 17") || !strings.Contains(explicit.Reason, "eligible") {
		t.Fatalf("explicit receipt classification = %#v", explicit)
	}
	automatic := worktreebranches.ReceiptedBranch(BranchEntry{Base: "main"}, sha, pullRequest, "")
	if automatic.Disposition != BranchReceipted || !strings.Contains(automatic.Evidence, "pull request #17") ||
		!strings.Contains(automatic.Reason, "--receipts") {
		t.Fatalf("automatic receipt classification = %#v", automatic)
	}
}
