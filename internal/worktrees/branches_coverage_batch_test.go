package worktrees

import (
	"bytes"
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
	selected, err := selectBranchRepositories(repositories, "", "acme")
	if err != nil || len(selected) != 2 || selected[0].Slug() != "acme/one" || selected[1].Slug() != "acme/two" {
		t.Fatalf("organization selection = %#v/%v", selected, err)
	}
	selected, err = selectBranchRepositories(repositories, "other/three", "other")
	if err != nil || len(selected) != 1 || selected[0].Slug() != "other/three" {
		t.Fatalf("repository selection = %#v/%v", selected, err)
	}
	if selected, err = selectBranchRepositories(repositories, "missing/repository", ""); err == nil || selected != nil {
		t.Fatalf("missing repository selection = %#v/%v", selected, err)
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

	directCalls := 0
	directEntries, directDiagnostic := inspectRepositoryBranchesWithHeartbeat(ctx, repositories[0], branchSweepOptions{}, nil, 1, 1, 0,
		func(context.Context, discover.Repo, branchSweepOptions, map[string]string) ([]BranchEntry, string) {
			directCalls++
			return []BranchEntry{{Branch: "direct"}}, "direct diagnostic"
		})
	if directCalls != 1 || len(directEntries) != 1 || directDiagnostic != "direct diagnostic" {
		t.Fatalf("direct inspection = %d/%#v/%q", directCalls, directEntries, directDiagnostic)
	}

	release := make(chan struct{})
	var heartbeat bytes.Buffer
	timer := time.AfterFunc(5*time.Millisecond, func() { close(release) })
	t.Cleanup(func() { timer.Stop() })
	heartbeatEntries, heartbeatDiagnostic := inspectRepositoryBranchesWithHeartbeat(ctx, repositories[0],
		branchSweepOptions{Progress: &heartbeat}, nil, 1, 2, time.Millisecond,
		func(context.Context, discover.Repo, branchSweepOptions, map[string]string) ([]BranchEntry, string) {
			<-release
			return []BranchEntry{{Branch: "heartbeat"}}, ""
		})
	if len(heartbeatEntries) != 1 || heartbeatDiagnostic != "" || !strings.Contains(heartbeat.String(), "still scanning acme/one") {
		t.Fatalf("heartbeat inspection = %#v/%q/%q", heartbeatEntries, heartbeatDiagnostic, heartbeat.String())
	}
}

func TestBranchesCoverageBatchRefParsingWithFakeGit(t *testing.T) {
	t.Parallel()
	const repository = "/fixture/repository"
	const separator = "\x1f"
	const format = "--format=%(refname:short)" + separator + "%(objectname)" + separator + "%(committerdate:iso-strict)"
	sha := strings.Repeat("a", 40)
	date := time.Unix(20_000, 0).UTC().Format(time.RFC3339)
	ctx := context.Background()

	remote := runnertest.New(t)
	remote.ExpectArgv([]string{"git", "-C", repository, "fetch", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*"}, runner.Result{}, nil)
	remote.ExpectArgv([]string{"git", "-C", repository, "for-each-ref", format, "refs/remotes/origin/"}, runner.Result{CombinedOutput: strings.Join([]string{
		"origin/HEAD" + separator + sha + separator + date,
		"origin/feature/one" + separator + sha + separator + date,
		"feature/wrong-prefix" + separator + sha + separator + date,
		"malformed",
		"",
	}, "\n")}, nil)
	refs, diagnostic := listRemoteRefs(withGitRunner(ctx, remote), repository)
	if diagnostic != "" || len(refs) != 1 || refs[0].Name != "feature/one" || refs[0].SHA != sha || refs[0].CommitterDate.IsZero() {
		t.Fatalf("remote refs = %#v/%q", refs, diagnostic)
	}

	retired := runnertest.New(t)
	retired.ExpectArgv([]string{"git", "-C", repository, "fetch", "--prune", "origin", "+refs/heads/retired/*:refs/remotes/origin/retired/*"}, runner.Result{}, nil)
	retired.ExpectArgv([]string{"git", "-C", repository, "for-each-ref", format, "refs/remotes/origin/retired/"}, runner.Result{
		CombinedOutput: "origin/retired/one" + separator + sha + separator + date,
	}, nil)
	refs, diagnostic = listRetiredRemoteRefs(withGitRunner(ctx, retired), repository)
	if diagnostic != "" || len(refs) != 1 || refs[0].Name != "retired/one" {
		t.Fatalf("retired remote refs = %#v/%q", refs, diagnostic)
	}

	local := runnertest.New(t)
	local.ExpectArgv([]string{"git", "-C", repository, "for-each-ref", format, "refs/heads/"}, runner.Result{
		CombinedOutput: "feature/local" + separator + sha + separator + "not-a-date",
	}, nil)
	refs, diagnostic = listLocalRefs(withGitRunner(ctx, local), repository)
	if diagnostic != "" || len(refs) != 1 || refs[0].Name != "feature/local" || !refs[0].CommitterDate.IsZero() {
		t.Fatalf("local refs = %#v/%q", refs, diagnostic)
	}

	worktrees := runnertest.New(t)
	worktrees.ExpectArgv([]string{"git", "-C", repository, "worktree", "list", "--porcelain"}, runner.Result{
		CombinedOutput: "worktree /one\nbranch refs/heads/feature/one\n\nworktree /detached\ndetached\n",
	}, nil)
	checkedOut, diagnostic := checkedOutLocalBranches(withGitRunner(ctx, worktrees), repository)
	if diagnostic != "" || !checkedOut["feature/one"] || len(checkedOut) != 1 {
		t.Fatalf("checked out branches = %#v/%q", checkedOut, diagnostic)
	}

	remoteTags := runnertest.New(t)
	remoteTags.ExpectArgv([]string{"git", "-C", repository, "ls-remote", "--tags", "--refs", "origin", "refs/tags/retired/*"}, runner.Result{
		CombinedOutput: strings.Join([]string{
			sha + " refs/tags/retired/one",
			"invalid refs/tags/retired/bad",
			sha + " refs/tags/active/wrong",
			sha + " refs/tags/retired/peeled^{}",
		}, "\n"),
	}, nil)
	refs, diagnostic = listRetiredTags(withGitRunner(ctx, remoteTags), repository, true, false)
	if diagnostic != "" || len(refs) != 1 || refs[0].Name != "retired/one" || !refs[0].UnknownDate {
		t.Fatalf("remote retired tags = %#v/%q", refs, diagnostic)
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
