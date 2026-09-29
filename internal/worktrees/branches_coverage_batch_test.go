package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
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

	repository := discover.Repo{Org: "acme", Name: "app", Path: root}
	entries := []BranchEntry{}
	names := map[string]bool{}
	count := appendRetiredEntries(ctx, &entries, names, repository, branchSweepOptions{
		Now: now, Name: "retired/*",
	}, []branchRef{
		{Name: "active/one"},
		{Name: "retired/one", UnknownDate: true},
	}, BranchScopeLocal)
	if count != 1 || len(entries) != 1 || !names["acme/app|retired/one"] {
		t.Fatalf("retired append = count %d entries %#v names %#v", count, entries, names)
	}

	decorateBranchCommits(ctx, root, nil)
	decorateBranchCommits(ctx, root, []BranchEntry{{SHA: ""}})
	decorateBranchCommits(ctx, filepath.Join(root, "missing"), []BranchEntry{{SHA: strings.Repeat("a", 40)}})
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

	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if repositories, err := discoverBranchRepositories(fileRoot, ""); err == nil || repositories != nil {
		t.Fatalf("repository discovery through file = %#v/%v", repositories, err)
	}
	if repositories, err := discoverBranchRepositories(root, "missing"); err != nil || len(repositories) != 0 {
		t.Fatalf("filtered repository discovery = %#v/%v", repositories, err)
	}
	if index, diagnostic := branchInUseIndex(ctx, fileRoot, ""); diagnostic == "" || len(index) != 0 {
		t.Fatalf("in-use index through file = %#v/%q", index, diagnostic)
	}
}

func TestBranchesCoverageBatchGitFailureSurfaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing")
	repository := discover.Repo{Org: "acme", Name: "app", Path: missing}
	sweep := branchSweepOptions{Base: "main", Scope: BranchScopeAll, Now: time.Now()}

	entries, diagnostic := inspectRepositoryBranches(ctx, repository, sweep, nil)
	if len(entries) != 1 || entries[0].Disposition != BranchUnreadable || diagnostic == "" {
		t.Fatalf("unreadable repository = %#v/%q", entries, diagnostic)
	}
	entry := BranchEntry{}
	decorateBranchCommit(ctx, missing, &entry)
	entry.SHA = strings.Repeat("a", 40)
	decorateBranchCommit(ctx, missing, &entry)
	if entry.Author != "" || entry.Title != "" {
		t.Fatalf("failed decoration mutated entry = %#v", entry)
	}
	if refs, diagnostic := listRemoteRefs(ctx, missing); refs != nil || diagnostic == "" {
		t.Fatalf("remote refs failure = %#v/%q", refs, diagnostic)
	}
	if refs, diagnostic := listRetiredTags(ctx, missing, false, false); refs != nil || diagnostic == "" {
		t.Fatalf("local retired tags failure = %#v/%q", refs, diagnostic)
	}
	if refs, diagnostic := listRetiredTags(ctx, missing, true, false); refs != nil || diagnostic == "" {
		t.Fatalf("remote retired tags failure = %#v/%q", refs, diagnostic)
	}
	if refs, diagnostic := listRefs(ctx, missing, "refs/heads/", ""); refs != nil || diagnostic == "" {
		t.Fatalf("generic refs failure = %#v/%q", refs, diagnostic)
	}
}

func TestBranchesCoverageBatchSharedCommitDecoration(t *testing.T) {
	t.Parallel()
	const repository = "/fixture/repository"
	head := strings.Repeat("a", 40)
	fake := runnertest.New(t)
	expect := func() {
		fake.ExpectArgv(
			[]string{"git", "-C", repository, "show", "-s", "--format=%H%x1f%an%x1f%s%x1e", head},
			runner.Result{CombinedOutput: head + "\x1fWB Test\x1fseed commit\x1e"}, nil,
		)
	}
	expect()
	entries := []BranchEntry{{SHA: head}, {SHA: head}}
	ctx := withGitRunner(context.Background(), fake)
	decorateBranchCommits(ctx, repository, entries)
	if entries[0].Author == "" || entries[0].Title == "" || entries[1].Author != entries[0].Author || entries[1].Title != entries[0].Title {
		t.Fatalf("batch decoration = %#v", entries)
	}
	expect()
	single := BranchEntry{SHA: head}
	decorateBranchCommit(ctx, repository, &single)
	if single.Author != entries[0].Author || single.Title != entries[0].Title {
		t.Fatalf("single decoration = %#v, batch = %#v", single, entries[0])
	}
}

func TestBranchesCoverageBatchClassificationBoundaries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository := discover.Repo{Org: "acme", Name: "app", Path: filepath.Join(t.TempDir(), "missing")}
	sha := strings.Repeat("a", 40)
	target := strings.Repeat("b", 40)
	sweep := branchSweepOptions{Base: "main"}

	protected := classifyBranch(ctx, repository, sweep, branchRef{Name: "main", SHA: sha}, BranchScopeLocal,
		target, "trunk", nil, nil, nil)
	if protected.Disposition != BranchProtected || !strings.Contains(protected.Evidence, "base branch") {
		t.Fatalf("protected classification = %#v", protected)
	}
	inUse := classifyBranch(ctx, repository, sweep, branchRef{Name: "feature/one", SHA: sha}, BranchScopeLocal,
		target, "main", map[string]string{"acme/app|feature/one": "task-1"}, nil, nil)
	if inUse.Disposition != BranchInUse || inUse.Task != "task-1" || !strings.Contains(inUse.Reason, "task-1") {
		t.Fatalf("claimed classification = %#v", inUse)
	}
	checkedOut := classifyBranch(ctx, repository, sweep, branchRef{Name: "feature/two", SHA: sha}, BranchScopeLocal,
		target, "main", nil, map[string]bool{"feature/two": true}, nil)
	if checkedOut.Disposition != BranchInUse || checkedOut.Task != "" || !strings.Contains(checkedOut.Reason, "working tree") {
		t.Fatalf("checked-out classification = %#v", checkedOut)
	}
	unreadable := classifyBranch(ctx, repository, sweep, branchRef{Name: "feature/three", SHA: sha}, BranchScopeLocal,
		target, "main", nil, nil, nil)
	if unreadable.Disposition != BranchUnreadable {
		t.Fatalf("unreadable classification = %#v", unreadable)
	}

	if got := shortSHA("short"); got != "short" {
		t.Fatalf("short SHA = %q", got)
	}
	if got := shortSHA("123456789012345"); got != "123456789012" {
		t.Fatalf("trimmed SHA = %q", got)
	}
	if got := protectedEvidence("main", "main", "trunk"); !strings.Contains(got, "base") {
		t.Fatalf("base evidence = %q", got)
	}
	if got := protectedEvidence("trunk", "main", "trunk"); !strings.Contains(got, "current HEAD") {
		t.Fatalf("HEAD evidence = %q", got)
	}
	if got := protectedEvidence("develop", "main", "trunk"); !strings.Contains(got, "configured") {
		t.Fatalf("configured evidence = %q", got)
	}

	cache := map[string][]githubPullRequest{sha: {}}
	if receipt, note := classifyLandingReceipt(ctx, repository, branchRef{Name: "feature", SHA: sha}, "main", target, cache); receipt != nil || !strings.Contains(note, "no merged pull request") {
		t.Fatalf("missing receipt = %#v/%q", receipt, note)
	}
	mergedAt := time.Now().UTC()
	cache[sha] = []githubPullRequest{{
		Number: 7, MergedAt: &mergedAt, MergeCommitSHA: "invalid",
		Base: githubRef{Ref: "main"}, Head: githubRef{SHA: sha},
	}}
	if receipt, note := classifyLandingReceipt(ctx, repository, branchRef{Name: "feature", SHA: sha}, "main", target, cache); receipt != nil || !strings.Contains(note, "no valid merge commit") {
		t.Fatalf("invalid receipt = %#v/%q", receipt, note)
	}
	if absorbed, evidence, unique, err := classifyAbsorbedOrUnique(ctx, repository.Path, target, sha); err == nil || absorbed || evidence != "" || unique != 0 {
		t.Fatalf("unreadable absorption = %t/%q/%d/%v", absorbed, evidence, unique, err)
	}

	pullRequest := &PullRequest{Number: 17}
	explicit := receiptedBranch(BranchEntry{Base: "main"}, sha, pullRequest, "17")
	if explicit.Disposition != BranchReceipted || explicit.LandingSHA != sha || explicit.ReceiptPullRequest != pullRequest ||
		!strings.Contains(explicit.Evidence, "--absorbed-by 17") || !strings.Contains(explicit.Reason, "eligible") {
		t.Fatalf("explicit receipt classification = %#v", explicit)
	}
	automatic := receiptedBranch(BranchEntry{Base: "main"}, sha, pullRequest, "")
	if automatic.Disposition != BranchReceipted || !strings.Contains(automatic.Evidence, "pull request #17") ||
		!strings.Contains(automatic.Reason, "--receipts") {
		t.Fatalf("automatic receipt classification = %#v", automatic)
	}

	brokenGit := runnertest.New(t)
	brokenGit.ExpectArgv(
		[]string{"git", "-C", repository.Path, "merge-base", "--is-ancestor", sha, target},
		runner.Result{ExitCode: 1}, errors.New("not an ancestor"),
	)
	brokenGit.ExpectArgv(
		[]string{"git", "-C", repository.Path, "cherry", target, sha},
		runner.Result{}, errors.New("unreadable cherry evidence"),
	)
	unreadableCherry := classifyBranch(withGitRunner(context.Background(), brokenGit), repository, sweep,
		branchRef{Name: "feature/unreadable-cherry", SHA: sha}, BranchScopeLocal, target, "main", nil, nil, nil)
	if unreadableCherry.Disposition != BranchUnreadable || !strings.Contains(unreadableCherry.Evidence, "unreadable cherry evidence") {
		t.Fatalf("unreadable cherry classification = %#v", unreadableCherry)
	}
}
