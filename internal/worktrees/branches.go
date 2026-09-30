package worktrees

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/worktreebranches"
)

// Branch Hygiene inventories and safely retires local and remote Git branches
// across the fleet, including the large majority that have no linked worktree
// and no WB Work Log claim, and are therefore invisible to worktree cleanup.
//
// It deliberately shares primitives with Worktree Lifecycle — the same
// fresh-fetch exact target resolution (fetchRemoteTargetHead), the same
// ancestor/tree/patch-id evidence gathering (isAncestor, commitTree, git
// cherry), the same GitHub merged-receipt proof, and the same
// compare-and-delete/force-with-lease ref
// retirement (gitCanonical, runSecureCleanupGitHelper) — while remaining a
// sibling command family with its own evidence taxonomy and its own top-level
// `wb branch` surface. See spec/features/branch-hygiene/README.md.

// Branch disposition is a closed set. A branch carries exactly one.
const BranchContained = worktreebranches.BranchContained
const BranchAbsorbed = worktreebranches.BranchAbsorbed
const BranchReceipted = worktreebranches.BranchReceipted
const BranchSuperseded = worktreebranches.BranchSuperseded
const BranchRetired = worktreebranches.BranchRetired
const BranchUnique = worktreebranches.BranchUnique
const BranchProtected = worktreebranches.BranchProtected
const BranchInUse = worktreebranches.BranchInUse
const BranchUnreadable = worktreebranches.BranchUnreadable

// Branch scope selects which refs a sweep enumerates.
const BranchScopeLocal = worktreebranches.BranchScopeLocal
const BranchScopeRemote = worktreebranches.BranchScopeRemote
const BranchScopeAll = worktreebranches.BranchScopeAll

// BranchListOptions selects the inventory. It is read-only in every
// configuration: it fetches Git refs and optionally reads GitHub PR metadata.
type BranchListOptions struct {
	ProjectsRoot string
	Base         string
	Scope        string // local, remote, or all; default local
	Only         string // one disposition name; empty means every disposition
	OlderThan    time.Duration
	Filter       string
	// Repository and Branch are exact selectors for a single branch. Filter
	// retains its substring semantics for fleet inventory only.
	Repository string
	Org        string
	Branch     string
	Name       string
	// IncludeRetired exposes explicitly quarantined branches. They are kept out
	// of the normal backlog inventory because they require an explicit operator
	// decision, never automatic cleanup.
	IncludeRetired bool
	// WithPRs enriches selected remote rows with hosted pull-request history.
	WithPRs bool
	// Progress receives incremental "[n/N] repository" lines as the sweep
	// works, plus a closing summary. Nil disables progress reporting.
	Progress io.Writer
}

// BranchEntry is one branch and the evidence behind its disposition.
type BranchEntry = worktreebranches.BranchEntry

// BranchListOutcome is the full result of one sweep.
type BranchListOutcome struct {
	Host        string         `json:"host"`
	GeneratedAt time.Time      `json:"generated_at"`
	Repository  string         `json:"repository,omitempty"`
	Org         string         `json:"org,omitempty"`
	Branch      string         `json:"branch,omitempty"`
	Base        string         `json:"base"`
	Scope       string         `json:"scope"`
	Entries     []BranchEntry  `json:"entries"`
	Diagnostics []string       `json:"diagnostics,omitempty"`
	Totals      map[string]int `json:"totals"`
	ElapsedMS   int64          `json:"elapsed_ms"`
	// RetiredRefs is a count of refs, deliberately split by scope. With
	// --scope all a local and remote ref of the same name are two refs.
	RetiredRefs     map[string]int `json:"retired_refs,omitempty"`
	RetiredBranches int            `json:"retired_branches"`
	// RetiredTags is separate because tag-preserving retirement intentionally
	// does not leave a branch namespace behind.
	RetiredTags     map[string]int `json:"retired_tags,omitempty"`
	RetiredTagNames int            `json:"retired_tag_names"`
	// RetiredRemoteUnavailable distinguishes an unknown remote retired count
	// from zero when the narrowly scoped remote refresh fails.
	RetiredRemoteUnavailable bool `json:"retired_remote_unavailable,omitempty"`
}

// BranchList enumerates every branch matching options and reports its
// disposition and evidence. It never creates, moves, deletes, or rewrites any
// ref, index, working tree, worktree registration, report, or journal.
func BranchList(ctx context.Context, options BranchListOptions) (BranchListOutcome, error) {
	normalized, err := normalizeBranchListOptions(options)
	if err != nil {
		return BranchListOutcome{}, err
	}
	return sweepBranches(ctx, normalized)
}

func normalizeBranchListOptions(options BranchListOptions) (BranchListOptions, error) {
	projectsRoot, err := absoluteProjectsRoot(options.ProjectsRoot)
	if err != nil {
		return BranchListOptions{}, err
	}
	options.ProjectsRoot = projectsRoot
	options.Base = strings.TrimSpace(options.Base)
	if options.Base == "" {
		options.Base = "main"
	}
	if err := branchValidationError(context.Background(), "base branch", options.Base); err != nil {
		return BranchListOptions{}, err
	}
	if options.Scope == "" {
		options.Scope = BranchScopeLocal
	}
	switch options.Scope {
	case BranchScopeLocal, BranchScopeRemote, BranchScopeAll:
	default:
		return BranchListOptions{}, fmt.Errorf("unsupported --scope %q; use local, remote, or all", options.Scope)
	}
	if options.Only != "" {
		switch options.Only {
		case BranchContained, BranchAbsorbed, BranchReceipted, BranchSuperseded, BranchUnique, BranchProtected, BranchInUse, BranchUnreadable, BranchRetired:
		default:
			return BranchListOptions{}, fmt.Errorf("unsupported --only %q", options.Only)
		}
	}
	if options.OlderThan < 0 {
		return BranchListOptions{}, fmt.Errorf("--older-than cannot be negative")
	}
	options.Filter = strings.TrimSpace(options.Filter)
	options.Repository = strings.TrimSpace(options.Repository)
	options.Org = strings.TrimSpace(options.Org)
	options.Branch = strings.TrimSpace(options.Branch)
	options.Name = strings.TrimSpace(options.Name)
	if options.Repository != "" {
		parts := strings.Split(options.Repository, "/")
		if len(parts) != 2 || !validRepositorySegment(parts[0]) || !validRepositorySegment(parts[1]) {
			return BranchListOptions{}, fmt.Errorf("invalid --repo %q; use owner/repository", options.Repository)
		}
	}
	if options.Org != "" && !validRepositorySegment(options.Org) {
		return BranchListOptions{}, fmt.Errorf("invalid --org %q", options.Org)
	}
	if options.Branch != "" {
		if err := branchValidationError(context.Background(), "branch", options.Branch); err != nil {
			return BranchListOptions{}, err
		}
	}
	if options.Name != "" {
		if _, err := path.Match(options.Name, ""); err != nil {
			return BranchListOptions{}, fmt.Errorf("invalid --name glob %q: %w", options.Name, err)
		}
	}
	return options, nil
}

// branchSweepOptions is the option surface shared by list and cleanup, so one
// classification engine serves both. now is injectable for deterministic age
// filtering under test.
type branchSweepOptions struct {
	ProjectsRoot string
	Base         string
	Scope        string
	Only         string
	OlderThan    time.Duration
	Filter       string
	Repository   string
	Org          string
	Branch       string
	Name         string
	Progress     io.Writer
	Now          time.Time
	// Receipts enables landing-receipt classification, which costs a GitHub
	// query per non-contained candidate. See
	// #req:receipted-is-opt-in-and-fails-closed.
	Receipts bool
	WithPRs  bool
	Cleanup  bool
	// AbsorbedBy is the optional operator-supplied landing pointer (a merged
	// pull request number or an exact landing commit) that Branch Hygiene
	// verifies with the same attested-absorption proof `wb worktree cleanup
	// --absorbed-by` performs — see attestedAbsorbedReceipt. It selects which
	// receipt to check and never substitutes for one: every candidate is
	// still proved on evidence, so a wrong or dishonest pointer can only fail
	// closed for that candidate. See #req:attested-absorption-requires-exact-entry-point.
	AbsorbedBy     string
	SupersededBy   string
	IncludeRetired bool
}

// Leave one second of scheduling margin below the public ten-second ceiling.
const branchRepositoryHeartbeatInterval = 9 * time.Second

type branchRepositoryInspection func(context.Context, discover.Repo, branchSweepOptions, map[string]string) ([]BranchEntry, string)

type branchRepositoryInspectionResult struct {
	entries    []BranchEntry
	diagnostic string
}

func sweepBranches(ctx context.Context, options BranchListOptions) (BranchListOutcome, error) {
	started := time.Now()
	sweep := branchSweepOptions{
		ProjectsRoot: options.ProjectsRoot, Base: options.Base, Scope: options.Scope,
		Only: options.Only, OlderThan: options.OlderThan, Filter: options.Filter,
		Progress: options.Progress, Now: started,
		Repository: options.Repository, Org: options.Org, Branch: options.Branch, Name: options.Name,
		IncludeRetired: options.IncludeRetired,
		WithPRs:        options.WithPRs,
	}
	if retiredNamespaceSelected(sweep) {
		return inventoryRetiredNamespace(ctx, sweep, started)
	}
	entries, diagnostics, err := classifyFleetBranches(ctx, sweep)
	if err != nil {
		return BranchListOutcome{}, err
	}
	filtered := applyListDisplayFilters(entries, sweep)
	totals := tallyDispositions(filtered)
	retiredRefs, retiredBranches, retiredTags, retiredTagNames, retiredRemoteUnavailable, retiredDiagnostics := countRetiredBranches(ctx, sweep)
	diagnostics = append(diagnostics, retiredDiagnostics...)
	sortBranchEntries(filtered)
	return BranchListOutcome{
		Host: branchEvidenceHost(), GeneratedAt: started, Repository: options.Repository, Org: options.Org, Branch: options.Branch,
		Base: options.Base, Scope: options.Scope, Entries: filtered,
		Diagnostics: diagnostics, Totals: totals, RetiredRefs: retiredRefs, RetiredBranches: retiredBranches, RetiredTags: retiredTags, RetiredTagNames: retiredTagNames, RetiredRemoteUnavailable: retiredRemoteUnavailable, ElapsedMS: time.Since(started).Milliseconds(),
	}, nil
}

// retiredNamespaceSelected identifies selectors whose result can contain only
// retired refs. Those refs need no target-based disposition evidence, so their
// inventory must not fetch origin/<base> merely to count a quarantine.
func retiredNamespaceSelected(sweep branchSweepOptions) bool {
	return worktreebranches.RetiredNamespaceSelected(sweep.branchPolicyOptions())
}

// inventoryRetiredNamespace is the narrow inventory used by --only retired
// and retired/* selectors. Local scope is fully offline. Remote scope refreshes
// only origin's retired namespace, then reports those tracking refs; it never
// fetches origin/<base> or treats a stale tracking snapshot as remote truth.
func inventoryRetiredNamespace(ctx context.Context, sweep branchSweepOptions, generatedAt time.Time) (BranchListOutcome, error) {
	repositories, err := discoverBranchRepositories(sweep.ProjectsRoot, sweep.Filter)
	if err != nil {
		return BranchListOutcome{}, fmt.Errorf("discover repositories below %s: %w", sweep.ProjectsRoot, err)
	}
	entries := make([]BranchEntry, 0)
	retiredRefs := map[string]int{}
	retiredTags := map[string]int{}
	retiredNames := map[string]bool{}
	retiredTagNames := map[string]bool{}
	retiredRemoteUnavailable := false
	diagnostics := []string{fmt.Sprintf("retired namespace inventory skipped fetch of origin/%s", sweep.Base)}
	selected, err := selectBranchRepositories(repositories, sweep.Repository, sweep.Org)
	if err != nil {
		return BranchListOutcome{}, err
	}
	if sweep.Only != "" && sweep.Only != BranchRetired {
		diagnostics = append(diagnostics, fmt.Sprintf("retired namespace cannot match --only %s", sweep.Only))
		return BranchListOutcome{
			Host: branchEvidenceHost(), GeneratedAt: generatedAt, Repository: sweep.Repository, Org: sweep.Org, Branch: sweep.Branch,
			Base: sweep.Base, Scope: sweep.Scope, Entries: entries, Diagnostics: diagnostics,
			Totals: tallyDispositions(entries), RetiredRefs: retiredRefs, RetiredTags: retiredTags, ElapsedMS: time.Since(generatedAt).Milliseconds(),
		}, nil
	}
	for index, repository := range selected {
		reportBranchProgress(sweep.Progress, index+1, len(selected), repository.Slug())
		if sweep.Scope == BranchScopeLocal || sweep.Scope == BranchScopeAll {
			refs, diagnostic := listRefs(ctx, repository.Path, "refs/heads/retired/", "")
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: retired local refs: %s", repository.Slug(), diagnostic))
			} else {
				retiredRefs[BranchScopeLocal] += appendRetiredEntries(ctx, &entries, retiredNames, repository, sweep, refs, BranchScopeLocal)
			}
			tags, diagnostic := listRetiredTags(ctx, repository.Path, false, true)
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: retired local tags: %s", repository.Slug(), diagnostic))
			} else {
				retiredTags[BranchScopeLocal] += appendRetiredTagEntries(ctx, &entries, retiredTagNames, repository, sweep, tags, BranchScopeLocal)
			}
		}
		if sweep.Scope == BranchScopeRemote || sweep.Scope == BranchScopeAll {
			refs, diagnostic := listRetiredRemoteRefs(ctx, repository.Path)
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: retired remote refs: %s", repository.Slug(), diagnostic))
				retiredRemoteUnavailable = true
			} else {
				retiredRefs[BranchScopeRemote] += appendRetiredEntries(ctx, &entries, retiredNames, repository, sweep, refs, BranchScopeRemote)
			}
			tags, tagDiagnostic := listRetiredTags(ctx, repository.Path, true, true)
			if tagDiagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: retired remote tags: %s", repository.Slug(), tagDiagnostic))
				retiredRemoteUnavailable = true
			} else {
				retiredTags[BranchScopeRemote] += appendRetiredTagEntries(ctx, &entries, retiredTagNames, repository, sweep, tags, BranchScopeRemote)
			}
		}
	}
	sortBranchEntries(entries)
	reportBranchSummary(sweep.Progress, tallyDispositions(entries), time.Since(generatedAt))
	return BranchListOutcome{
		Host: branchEvidenceHost(), GeneratedAt: generatedAt, Repository: sweep.Repository, Org: sweep.Org, Branch: sweep.Branch,
		Base: sweep.Base, Scope: sweep.Scope, Entries: entries, Diagnostics: diagnostics,
		Totals: tallyDispositions(entries), RetiredRefs: retiredRefs, RetiredBranches: len(retiredNames), RetiredTags: retiredTags, RetiredTagNames: len(retiredTagNames), RetiredRemoteUnavailable: retiredRemoteUnavailable, ElapsedMS: time.Since(generatedAt).Milliseconds(),
	}, nil
}

func appendRetiredEntries(ctx context.Context, entries *[]BranchEntry, names map[string]bool, repository discover.Repo, sweep branchSweepOptions, refs []branchRef, scope string) int {
	return appendRetiredRefEntries(ctx, entries, names, repository, sweep, refs, scope, "branch")
}

func appendRetiredTagEntries(ctx context.Context, entries *[]BranchEntry, names map[string]bool, repository discover.Repo, sweep branchSweepOptions, refs []branchRef, scope string) int {
	return appendRetiredRefEntries(ctx, entries, names, repository, sweep, refs, scope, "tag")
}

func appendRetiredRefEntries(ctx context.Context, entries *[]BranchEntry, names map[string]bool, repository discover.Repo, sweep branchSweepOptions, refs []branchRef, scope, refKind string) int {
	start, count := len(*entries), 0
	for _, ref := range refs {
		if !retiredRefSelected(sweep, ref) {
			continue
		}
		entry := retiredBranchEntry(repository, sweep, ref, scope, "")
		entry.RefKind = refKind
		*entries = append(*entries, entry)
		names[repository.Slug()+"|"+ref.Name] = true
		count++
	}
	decorateBranchCommits(ctx, repository.Path, (*entries)[start:])
	return count
}

// decorateBranchCommits reads metadata for one repository's selected refs in
// one Git process. Retired list output has the same author/title fields as the
// normal disposition path without turning a fleet count into one process per
// ref.
func decorateBranchCommits(ctx context.Context, repositoryPath string, entries []BranchEntry) {
	if len(entries) == 0 {
		return
	}
	args := []string{"show", "-s", "--format=%H%x1f%an%x1f%s%x1e"}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if entry.SHA != "" && !seen[entry.SHA] {
			args = append(args, entry.SHA)
			seen[entry.SHA] = true
		}
	}
	if len(args) == 3 {
		return
	}
	output, err := git(ctx, repositoryPath, args...)
	if err != nil {
		return
	}
	metadata := make(map[string][2]string, len(entries))
	for _, record := range strings.Split(output, "\x1e") {
		fields := strings.SplitN(record, "\x1f", 3)
		if len(fields) == 3 {
			metadata[strings.TrimSpace(fields[0])] = [2]string{strings.TrimSpace(fields[1]), strings.TrimSpace(fields[2])}
		}
	}
	for index := range entries {
		if detail, ok := metadata[entries[index].SHA]; ok {
			entries[index].Author, entries[index].Title = detail[0], detail[1]
		}
	}
}

func branchEvidenceHost() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "unknown"
	}
	return strings.TrimSpace(host)
}

func applyListDisplayFilters(entries []BranchEntry, sweep branchSweepOptions) []BranchEntry {
	return worktreebranches.ApplyListDisplayFilters(entries, sweep.branchPolicyOptions())
}

func sortBranchEntries(entries []BranchEntry) {
	worktreebranches.SortBranchEntries(entries)
}

func tallyDispositions(entries []BranchEntry) map[string]int {
	return worktreebranches.TallyDispositions(entries)
}

// classifyFleetBranches is the shared enumeration engine for both list and
// cleanup: it discovers every canonical repository below ProjectsRoot,
// resolves the freshly fetched exact target once per repository, enumerates
// local and/or remote branches, and classifies each into the closed evidence
// taxonomy. A repository whose target cannot be fetched yields the
// unreadable disposition for its branches without blocking the rest of the
// sweep.
func classifyFleetBranches(ctx context.Context, sweep branchSweepOptions) ([]BranchEntry, []string, error) {
	entries, diagnostics, _, err := classifyFleetBranchesWithPaths(ctx, sweep)
	return entries, diagnostics, err
}

// classifyFleetBranchesWithPaths is classifyFleetBranches plus the
// slug-to-local-path lookup cleanup's apply phase needs to reopen each
// candidate's canonical repository for its recheck-before-mutation.
func classifyFleetBranchesWithPaths(ctx context.Context, sweep branchSweepOptions) ([]BranchEntry, []string, map[string]string, error) {
	repositories, err := discoverBranchRepositories(sweep.ProjectsRoot, sweep.Filter)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("discover repositories below %s: %w", sweep.ProjectsRoot, err)
	}
	repositories, err = selectBranchRepositories(repositories, sweep.Repository, sweep.Org)
	if err != nil {
		return nil, nil, nil, err
	}
	paths := make(map[string]string, len(repositories))
	for _, repository := range repositories {
		paths[repository.Slug()] = repository.Path
	}
	inUse, diagnostic := branchInUseIndex(ctx, sweep.ProjectsRoot, sweep.Filter)
	var diagnostics []string
	if diagnostic != "" {
		diagnostics = append(diagnostics, diagnostic)
	}
	start := time.Now()
	var entries []BranchEntry
	total := len(repositories)
	for index, repository := range repositories {
		reportBranchProgress(sweep.Progress, index+1, total, repository.Slug())
		repositoryEntries, diagnostic := inspectRepositoryBranchesWithHeartbeat(
			ctx, repository, sweep, inUse, index+1, total,
			branchRepositoryHeartbeatInterval, inspectRepositoryBranches,
		)
		entries = append(entries, repositoryEntries...)
		if diagnostic != "" {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	reportBranchSummary(sweep.Progress, tallyDispositions(entries), time.Since(start))
	return entries, diagnostics, paths, nil
}

func countRetiredBranches(ctx context.Context, sweep branchSweepOptions) (map[string]int, int, map[string]int, int, bool, []string) {
	repositories, err := discoverBranchRepositories(sweep.ProjectsRoot, sweep.Filter)
	if err != nil {
		return nil, 0, nil, 0, false, []string{fmt.Sprintf("count retired branches: discover repositories: %v", err)}
	}
	repositories, err = selectBranchRepositories(repositories, sweep.Repository, sweep.Org)
	if err != nil {
		return nil, 0, nil, 0, false, []string{fmt.Sprintf("count retired branches: %v", err)}
	}
	names := map[string]bool{}
	counts := map[string]int{}
	tagNames := map[string]bool{}
	tagCounts := map[string]int{}
	retiredRemoteUnavailable := false
	var diagnostics []string
	for _, repository := range repositories {
		if sweep.Scope == BranchScopeLocal || sweep.Scope == BranchScopeAll {
			refs, diagnostic := listLocalRefs(ctx, repository.Path)
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: count retired local refs: %s", repository.Slug(), diagnostic))
			}
			accumulateRetiredCounts(sweep, repository, refs, BranchScopeLocal, counts, names)
			tags, diagnostic := listRetiredTags(ctx, repository.Path, false, true)
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: count retired local tags: %s", repository.Slug(), diagnostic))
			}
			accumulateRetiredCounts(sweep, repository, tags, BranchScopeLocal, tagCounts, tagNames)
		}
		if sweep.Scope == BranchScopeRemote || sweep.Scope == BranchScopeAll {
			// Inventory already fetched remote refs for classification. Count the
			// resulting tracking refs directly so a visible retired total never
			// causes a second network fetch or hides its failure.
			refs, diagnostic := listRefs(ctx, repository.Path, "refs/remotes/origin/", "origin/")
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: count retired remote refs: %s", repository.Slug(), diagnostic))
				retiredRemoteUnavailable = true
			}
			accumulateRetiredCounts(sweep, repository, refs, BranchScopeRemote, counts, names)
			tags, diagnostic := listRetiredTags(ctx, repository.Path, true, sweep.OlderThan > 0)
			if diagnostic != "" {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: count retired remote tags: %s", repository.Slug(), diagnostic))
				retiredRemoteUnavailable = true
			}
			accumulateRetiredCounts(sweep, repository, tags, BranchScopeRemote, tagCounts, tagNames)
		}
	}
	return counts, len(names), tagCounts, len(tagNames), retiredRemoteUnavailable, diagnostics
}

func accumulateRetiredCounts(sweep branchSweepOptions, repository discover.Repo, refs []branchRef, scope string, counts map[string]int, names map[string]bool) {
	worktreebranches.AccumulateRetiredCounts(sweep.branchPolicyOptions(), repository.Slug(), refs, scope, counts, names)
}

func retiredRefSelected(sweep branchSweepOptions, ref branchRef) bool {
	return worktreebranches.RetiredRefSelected(sweep.branchPolicyOptions(), ref)
}

func branchNameSelected(sweep branchSweepOptions, name string) bool {
	return worktreebranches.BranchNameSelected(sweep.branchPolicyOptions(), name)
}

func inspectRepositoryBranchesWithHeartbeat(
	ctx context.Context,
	repository discover.Repo,
	sweep branchSweepOptions,
	inUse map[string]string,
	index, total int,
	interval time.Duration,
	inspect branchRepositoryInspection,
) ([]BranchEntry, string) {
	if sweep.Progress == nil || interval <= 0 {
		return inspect(ctx, repository, sweep, inUse)
	}
	result := make(chan branchRepositoryInspectionResult, 1)
	started := time.Now()
	go func() {
		entries, diagnostic := inspect(ctx, repository, sweep, inUse)
		result <- branchRepositoryInspectionResult{entries: entries, diagnostic: diagnostic}
	}()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case inspected := <-result:
			return inspected.entries, inspected.diagnostic
		case <-ticker.C:
			_, _ = fmt.Fprintf(sweep.Progress, "[%d/%d] still scanning %s (%s)\n",
				index, total, repository.Slug(), time.Since(started).Round(time.Second))
		}
	}
}

func discoverBranchRepositories(projectsRoot, filter string) ([]discover.Repo, error) {
	repositories, err := discover.ScanLocal(projectsRoot)
	if err != nil {
		return nil, err
	}
	if filter == "" {
		return repositories, nil
	}
	filtered := make([]discover.Repo, 0, len(repositories))
	for _, repository := range repositories {
		if strings.Contains(repository.Slug(), filter) {
			filtered = append(filtered, repository)
		}
	}
	return filtered, nil
}

func selectBranchRepositories(repositories []discover.Repo, repositorySlug, org string) ([]discover.Repo, error) {
	selected := make([]discover.Repo, 0, len(repositories))
	for _, repository := range repositories {
		owner, _, _ := strings.Cut(repository.Slug(), "/")
		if (repositorySlug == "" || repository.Slug() == repositorySlug) && (org == "" || owner == org) {
			selected = append(selected, repository)
		}
	}
	if repositorySlug != "" && len(selected) == 0 {
		return nil, fmt.Errorf("selected repository %q was not discovered", repositorySlug)
	}
	return selected, nil
}

// branchInUseKey identifies one repository/branch pair claimed live by a WB
// task, keyed exactly as ListResult reports it.
func branchInUseKey(repository, branch string) string { return repository + "|" + branch }

// branchInUseIndex builds the fleet-wide set of branches WB itself owns right
// now: checked out in a linked worktree, or named by a live Work Log claim.
// It reuses ListWithDiagnostics — the same live inventory `wb worktree list`
// reports — rather than re-deriving claim state, so the two surfaces can
// never disagree about what WB owns.
func branchInUseIndex(ctx context.Context, projectsRoot, filter string) (map[string]string, string) {
	outcome, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: projectsRoot, Filter: filter})
	if err != nil {
		return map[string]string{}, fmt.Sprintf("read WB worktree inventory: %v", err)
	}
	index := make(map[string]string, len(outcome.Results))
	for _, result := range outcome.Results {
		index[branchInUseKey(result.Repository, result.Branch)] = result.Task
	}
	return index, ""
}

func reportBranchProgress(out io.Writer, index, total int, repository string) {
	if out == nil {
		return
	}
	_, _ = fmt.Fprintf(out, "[%d/%d] scanning %s\n", index, total, repository)
}

func reportBranchSummary(out io.Writer, totals map[string]int, elapsed time.Duration) {
	if out == nil {
		return
	}
	names := make([]string, 0, len(totals))
	for name := range totals {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", name, totals[name]))
	}
	_, _ = fmt.Fprintf(out, "done in %s: %s\n", elapsed.Round(time.Millisecond), strings.Join(parts, " "))
}

// inspectRepositoryBranches classifies every branch in one repository. A
// fetch failure for the exact target yields unreadable for the whole
// repository rather than aborting the sweep.
func inspectRepositoryBranches(ctx context.Context, repository discover.Repo, sweep branchSweepOptions, inUse map[string]string) ([]BranchEntry, string) {
	slug := repository.Slug()
	targetSHA, err := fetchRemoteTargetHead(ctx, repository.Path, sweep.Base)
	if err != nil {
		return []BranchEntry{{
			Repository: slug, Base: sweep.Base, Disposition: BranchUnreadable,
			Evidence: fmt.Sprintf("fetch exact origin/%s target: %v", sweep.Base, err),
		}}, fmt.Sprintf("%s: fetch exact origin/%s target: %v", slug, sweep.Base, err)
	}
	canonicalHEAD, _ := git(ctx, repository.Path, "rev-parse", "--abbrev-ref", "HEAD")
	canonicalHEAD = strings.TrimSpace(canonicalHEAD)

	var entries []BranchEntry
	var diagnostics []string
	if sweep.Scope == BranchScopeLocal || sweep.Scope == BranchScopeAll {
		local, diagnostic := listLocalRefs(ctx, repository.Path)
		if diagnostic != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %s", slug, diagnostic))
		}
		checkedOut, diagnostic := checkedOutLocalBranches(ctx, repository.Path)
		if diagnostic != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %s", slug, diagnostic))
		}
		pullRequestCache := map[string][]githubPullRequest{}
		for _, ref := range local {
			if !branchNameSelected(sweep, ref.Name) {
				continue
			}
			if isRetiredBranch(ref.Name) && !sweep.IncludeRetired && sweep.Only != BranchRetired && sweep.Branch != ref.Name && sweep.Name == "" {
				continue
			}
			if isRetiredBranch(ref.Name) {
				entries = append(entries, retiredBranchEntry(repository, sweep, ref, BranchScopeLocal, targetSHA))
			} else {
				entries = append(entries, classifyBranch(ctx, repository, sweep, ref, BranchScopeLocal, targetSHA, canonicalHEAD, inUse, checkedOut, pullRequestCache))
			}
			decorateBranchCommit(ctx, repository.Path, &entries[len(entries)-1])
		}
	}
	if sweep.Scope == BranchScopeRemote || sweep.Scope == BranchScopeAll {
		remote, diagnostic := listRemoteRefs(ctx, repository.Path)
		if diagnostic != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %s", slug, diagnostic))
		}
		// Remote retirement deletes the same named ref. It therefore must see
		// local worktrees and live claims too; a remote-only plan may never
		// bypass an in-use guard merely because it enumerated origin refs.
		checkedOut, checkedOutDiagnostic := checkedOutLocalBranches(ctx, repository.Path)
		if checkedOutDiagnostic != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %s", slug, checkedOutDiagnostic))
		}
		pullRequestCache := map[string][]githubPullRequest{}
		branchPullRequestCache := map[string]branchPullRequestEvidence{}
		for _, ref := range remote {
			if !branchNameSelected(sweep, ref.Name) {
				continue
			}
			if isRetiredBranch(ref.Name) && !sweep.IncludeRetired && sweep.Only != BranchRetired && sweep.Branch != ref.Name && sweep.Name == "" {
				continue
			}
			if isRetiredBranch(ref.Name) {
				entries = append(entries, retiredBranchEntry(repository, sweep, ref, BranchScopeRemote, targetSHA))
			} else {
				entries = append(entries, classifyBranch(ctx, repository, sweep, ref, BranchScopeRemote, targetSHA, canonicalHEAD, inUse, checkedOut, pullRequestCache))
				entry := &entries[len(entries)-1]
				if (sweep.WithPRs && entry.Disposition != BranchProtected && entry.Disposition != BranchUnreadable) ||
					(sweep.Cleanup && eligibleBranchCleanupDisposition(*entry)) {
					decorateRemoteBranchPullRequests(ctx, repository, ref, entry, branchPullRequestCache, sweep.WithPRs)
				}
			}
			decorateBranchCommit(ctx, repository.Path, &entries[len(entries)-1])
		}
	}
	return entries, strings.Join(diagnostics, "; ")
}

func decorateBranchCommit(ctx context.Context, repositoryPath string, entry *BranchEntry) {
	entries := []BranchEntry{*entry}
	decorateBranchCommits(ctx, repositoryPath, entries)
	*entry = entries[0]
}

func retiredBranchEntry(repository discover.Repo, sweep branchSweepOptions, ref branchRef, scope, targetSHA string) BranchEntry {
	return worktreebranches.RetiredBranchEntry(repository.Slug(), sweep.branchPolicyOptions(), ref, scope, targetSHA)
}

func isRetiredBranch(branch string) bool {
	return worktreebranches.IsRetiredBranch(branch)
}

// checkedOutLocalBranches lists every branch checked out in any linked
// worktree of this repository, WB-managed or not. #req:evidence-class-
// taxonomy's in-use disposition covers "checked out in any linked worktree,"
// not only worktrees WB itself created, so this is independent of
// branchInUseIndex (which additionally names the owning WB task when there is
// one).
func checkedOutLocalBranches(ctx context.Context, repositoryPath string) (map[string]bool, string) {
	output, err := git(ctx, repositoryPath, "worktree", "list", "--porcelain")
	if err != nil {
		return map[string]bool{}, fmt.Sprintf("enumerate linked worktrees: %v", err)
	}
	checkedOut := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		if branch, ok := strings.CutPrefix(line, "branch refs/heads/"); ok {
			checkedOut[branch] = true
		}
	}
	return checkedOut, ""
}

// branchRef is one enumerated ref before classification.
type branchRef = worktreebranches.BranchRef

func listLocalRefs(ctx context.Context, repositoryPath string) ([]branchRef, string) {
	return listRefs(ctx, repositoryPath, "refs/heads/", "")
}

func listRemoteRefs(ctx context.Context, repositoryPath string) ([]branchRef, string) {
	return fetchRemoteRefs(ctx, repositoryPath, "+refs/heads/*:refs/remotes/origin/*", "refs/remotes/origin/", "fetch --prune origin")
}

func listRetiredRemoteRefs(ctx context.Context, repositoryPath string) ([]branchRef, string) {
	return fetchRemoteRefs(ctx, repositoryPath, "+refs/heads/retired/*:refs/remotes/origin/retired/*", "refs/remotes/origin/retired/", "fetch --prune origin retired namespace")
}

func fetchRemoteRefs(ctx context.Context, repositoryPath, refspec, refPrefix, failureLabel string) ([]branchRef, string) {
	if _, err := git(ctx, repositoryPath, "fetch", "--prune", "origin", refspec); err != nil {
		return nil, fmt.Sprintf("%s: %v", failureLabel, err)
	}
	return listRefs(ctx, repositoryPath, refPrefix, "origin/")
}

// listRetiredTags keeps remote tag inspection separate from local tags. It
// uses ls-remote for remote scope so branch inventory never writes fetched
// tags into the caller's local tag namespace.
func listRetiredTags(ctx context.Context, repositoryPath string, remote, metadata bool) ([]branchRef, string) {
	if !remote {
		return listRefs(ctx, repositoryPath, "refs/tags/retired/", "")
	}
	output, err := git(ctx, repositoryPath, "ls-remote", "--tags", "--refs", "origin", "refs/tags/retired/*")
	if err != nil {
		return nil, fmt.Sprintf("ls-remote retired tags: %v", err)
	}
	refs := []branchRef{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.HasSuffix(fields[1], "^{}") || !strings.HasPrefix(fields[1], "refs/tags/retired/") || !isGitObjectID(fields[0]) {
			continue
		}
		refs = append(refs, branchRef{Name: strings.TrimPrefix(fields[1], "refs/tags/"), SHA: fields[0], UnknownDate: true})
	}
	if !metadata || len(refs) == 0 {
		return refs, ""
	}
	// Metadata needs objects, but branch inventory is read-only with respect to
	// the caller's clone: use one temporary bare repository and a bounded
	// wildcard refspec rather than changing FETCH_HEAD, tags, or object storage.
	origin, err := git(ctx, repositoryPath, "remote", "get-url", "origin")
	if err != nil {
		return nil, fmt.Sprintf("resolve origin for retired tags: %v", err)
	}
	temporary, err := os.MkdirTemp("", "wb-retired-tag-metadata-")
	if err != nil {
		return nil, fmt.Sprintf("create retired tag metadata repository: %v", err)
	}
	defer func() { _ = os.RemoveAll(temporary) }()
	if _, err := git(ctx, temporary, "init", "--bare"); err != nil {
		return nil, fmt.Sprintf("initialize retired tag metadata repository: %v", err)
	}
	if _, err := git(ctx, temporary, "fetch", "--no-tags", strings.TrimSpace(origin), "+refs/tags/retired/*:refs/tags/retired/*"); err != nil {
		return nil, fmt.Sprintf("fetch retired tag metadata: %v", err)
	}
	for i := range refs {
		observed, err := git(ctx, temporary, "rev-parse", "refs/tags/"+refs[i].Name)
		if err != nil || observed != refs[i].SHA {
			return nil, fmt.Sprintf("retired tag %s changed during metadata fetch", refs[i].Name)
		}
		const separator = "\x1f"
		commit, err := git(ctx, temporary, "show", "-s", "--format=%cI%x1f%an%x1f%s", "refs/tags/"+refs[i].Name+"^{commit}")
		if err != nil {
			return nil, fmt.Sprintf("read retired tag commit metadata: %v", err)
		}
		parts := strings.SplitN(commit, separator, 3)
		if len(parts) != 3 {
			return nil, "invalid retired tag commit metadata"
		}
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(parts[0]))
		if err != nil {
			return nil, fmt.Sprintf("parse retired tag commit date: %v", err)
		}
		refs[i].CommitterDate, refs[i].UnknownDate = parsed, false
		refs[i].Author, refs[i].Title = strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
	}
	return refs, ""
}

func listRefs(ctx context.Context, repositoryPath, refPrefix, namePrefix string) ([]branchRef, string) {
	const separator = "\x1f"
	format := strings.Join([]string{"%(refname:short)", "%(objectname)", "%(committerdate:iso-strict)"}, separator)
	output, err := git(ctx, repositoryPath, "for-each-ref", "--format="+format, refPrefix)
	if err != nil {
		return nil, fmt.Sprintf("enumerate %s: %v", refPrefix, err)
	}
	var refs []branchRef
	for _, line := range strings.Split(output, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, separator)
		if len(fields) != 3 {
			continue
		}
		shortName := strings.TrimPrefix(fields[0], namePrefix)
		if namePrefix != "" && shortName == fields[0] {
			continue // did not carry the expected remote prefix; skip rather than misreport
		}
		if shortName == "HEAD" {
			continue // origin/HEAD is a symbolic pointer, not a branch
		}
		committerDate, _ := time.Parse(time.RFC3339, fields[2])
		refs = append(refs, branchRef{Name: shortName, SHA: fields[1], CommitterDate: committerDate})
	}
	return refs, ""
}

func classifyBranch(
	ctx context.Context,
	repository discover.Repo,
	sweep branchSweepOptions,
	ref branchRef,
	scope string,
	targetSHA, canonicalHEAD string,
	inUse map[string]string,
	checkedOut map[string]bool,
	pullRequestCache map[string][]githubPullRequest,
) BranchEntry {
	entry := BranchEntry{
		Repository: repository.Slug(), Branch: ref.Name, Scope: scope,
		SHA: ref.SHA, ShortSHA: shortSHA(ref.SHA), CommitterDate: ref.CommitterDate,
		Base: sweep.Base, TargetSHA: targetSHA,
	}

	// protected, in-use, and unreadable are evaluated before contained, so a
	// protected or claimed branch is never reported as deletable.
	if isProtectedBranch(ref.Name, sweep.Base, canonicalHEAD) {
		entry.Disposition = BranchProtected
		entry.Evidence = protectedEvidence(ref.Name, sweep.Base, canonicalHEAD)
		return entry
	}
	task, claimed := inUse[branchInUseKey(repository.Slug(), ref.Name)]
	if claimed || checkedOut[ref.Name] {
		entry.Disposition = BranchInUse
		entry.Task = task
		switch {
		case claimed:
			entry.Evidence = fmt.Sprintf("checked out or claimed by WB task %s", task)
			entry.Reason = fmt.Sprintf("owned by wb worktree task %s; use `wb worktree cleanup %s` or `wb worktree abort %s`, never wb branch cleanup", task, task, task)
		default:
			entry.Evidence = "checked out in a linked worktree"
			entry.Reason = "checked out in a linked worktree; wb branch cleanup never touches a working tree"
		}
		return entry
	}
	if sweep.SupersededBy != "" {
		receipt, rejection := supersessionReceiptForEntry(ctx, sweep.SupersededBy, ListResult{
			Repository: repository.Slug(), Branch: ref.Name, HeadSHA: ref.SHA,
			Base: sweep.Base, RemoteTargetSHA: targetSHA, CanonicalDir: repository.Path,
		})
		if rejection == "" {
			entry.Disposition = BranchSuperseded
			entry.SupersededAtOrigin = true
			entry.SupersessionReceipt, entry.SupersessionReviewer, entry.SupersessionReceiptID = sweep.SupersededBy, receipt.Approval.Actor, receipt.Approval.ReceiptID
			digest, digestErr := supersessionFileSHA256(sweep.SupersededBy)
			if digestErr != nil {
				entry.Disposition, entry.Evidence = BranchUnreadable, fmt.Sprintf("digest supersession receipt: %v", digestErr)
				return entry
			}
			entry.SupersessionSHA256 = digest
			entry.Evidence = "trusted reviewer receipt binds the exact source, target, replacements, and complete residual inventory"
			return entry
		}
		entry.SupersessionRejection = rejection
	}

	contained, err := isAncestor(ctx, repository.Path, ref.SHA, targetSHA)
	if err != nil {
		entry.Disposition = BranchUnreadable
		entry.Evidence = fmt.Sprintf("merge-base --is-ancestor: %v", err)
		return entry
	}
	if contained {
		entry.Disposition = BranchContained
		entry.Evidence = fmt.Sprintf("merge-base --is-ancestor %s %s", entry.ShortSHA, shortSHA(targetSHA))
		return entry
	}

	absorbed, absorbedEvidence, uniqueCount, err := classifyAbsorbedOrUnique(ctx, repository.Path, targetSHA, ref.SHA)
	if err != nil {
		entry.Disposition = BranchUnreadable
		entry.Evidence = fmt.Sprintf("git cherry: %v", err)
		return entry
	}

	// An operator-supplied --absorbed-by pointer is checked before both patch
	// evidence and --receipts auto-discovery: like a discovered receipt, a
	// branch that proves out this way must never be classified absorbed in
	// the first place (#req:absorbed-is-report-only governs only what
	// actually becomes absorbed, not what a stronger independent proof
	// reclassifies before that assignment happens). It reuses worktree
	// cleanup's exact attested-absorption proof (see attestedAbsorbedReceipt)
	// rather than a second implementation: the named commit must be exactly
	// where the work entered the target, and merging the branch into it, and
	// into the fetched target, must add nothing to either. A pointer that
	// fails this proof for the current branch is an ordinary negative for
	// THAT branch, never a hard error — a fleet sweep with an unfiltered
	// --absorbed-by fails closed candidate by candidate, never aborts.
	absorbedByNote := ""
	if sweep.AbsorbedBy != "" {
		receipt, rejection, err := classifyAttestedReceipt(ctx, repository, ref, sweep.Base, targetSHA, sweep.AbsorbedBy)
		if err != nil {
			entry.Disposition = BranchUnreadable
			entry.Evidence = fmt.Sprintf("--absorbed-by verification failed: %v", err)
			return entry
		}
		if receipt != nil {
			return receiptedBranch(entry, receipt.LandingSHA, receipt.PullRequest, sweep.AbsorbedBy)
		}
		entry.AbsorbedByRejection = rejection
		absorbedByNote = "; --absorbed-by: " + rejection
	}

	// A proved landing receipt outranks patch evidence in both directions: a
	// multi-commit squash landing presents as unique (no individual patch-id
	// survives squashing) even though every byte is in the target, and
	// patch-id equality alone must never make a branch deletable. Any receipt
	// failure names itself and leaves the patch-evidence disposition standing.
	// See #req:receipted-requires-a-proved-landing.
	receiptNote := ""
	if sweep.Receipts {
		receipt, note := classifyLandingReceipt(ctx, repository, ref, sweep.Base, targetSHA, pullRequestCache)
		if receipt != nil {
			return receiptedBranch(entry, receipt.MergeSHA, receipt, "")
		}
		receiptNote = "; receipt: " + note
	}
	if absorbed {
		entry.Disposition = BranchAbsorbed
		entry.Evidence = absorbedEvidence + receiptNote + absorbedByNote
		entry.Reason = "absorbed by patch-id or tree equality only; never eligible for --apply. " +
			"If this branch belongs to a WB task, run `wb worktree cleanup <task> --absorbed-by <pr-or-commit>`; " +
			"if it has no worktree, run `wb branch cleanup --absorbed-by <pr-or-commit>`; " +
			"otherwise this requires an explicit human decision"
		// The text renderer prefers Reason over Evidence, so a named failing
		// receipt check must ride along or the operator never sees it.
		entry.Reason += receiptNote + absorbedByNote
		return entry
	}
	entry.Disposition = BranchUnique
	entry.Evidence = fmt.Sprintf("git cherry reports %d unique patch(es) not upstream", uniqueCount) + receiptNote + absorbedByNote
	return entry
}

func receiptedBranch(entry BranchEntry, landingSHA string, pullRequest *PullRequest, absorbedBy string) BranchEntry {
	return worktreebranches.ReceiptedBranch(entry, landingSHA, pullRequest, absorbedBy)
}

// classifyAttestedReceipt verifies an operator-supplied --absorbed-by pointer
// for one candidate branch, reusing worktree cleanup's exact
// attested-absorption proof (attestedAbsorbedReceipt) rather than a second
// implementation of it. Branch Hygiene's candidates have no worktree — the
// worktree may already be gone, which is exactly the situation this exists
// for (see spec/features/branch-hygiene/README.md) — so the canonical
// repository stands in for both the "worktree" and "repository" arguments the
// shared proof expects; every check it performs runs against the canonical
// clone's object database and touches no working tree.
func classifyAttestedReceipt(
	ctx context.Context,
	repository discover.Repo,
	ref branchRef,
	base, targetSHA, absorbedBy string,
) (*absorbedReceipt, string, error) {
	return attestedAbsorbedReceipt(
		ctx, repository.Path, repository.Path, repository.Slug(), ref.SHA, base, targetSHA, absorbedBy,
	)
}

func shortSHA(sha string) string {
	return worktreebranches.ShortSHA(sha)
}

func isProtectedBranch(branch, base, canonicalHEAD string) bool {
	return worktreebranches.IsProtectedBranch(branch, base, canonicalHEAD)
}

func protectedEvidence(branch, base, canonicalHEAD string) string {
	return worktreebranches.ProtectedEvidence(branch, base, canonicalHEAD)
}

// classifyLandingReceipt tries to prove, on evidence, that a non-ancestor
// branch's work is in the target: GitHub's commit-to-pull-request index must
// name a merged pull request into the exact base whose merge commit is
// contained in the fetched target, and the local three-way proof must show the
// branch adds nothing to the landing commit or the target. Each failing check
// names itself, and every failure leaves the branch in its patch-evidence
// disposition — a branch never becomes eligible because a check could not be
// run. See #req:receipted-requires-a-proved-landing and
// #req:receipted-is-opt-in-and-fails-closed.
func classifyLandingReceipt(
	ctx context.Context,
	repository discover.Repo,
	ref branchRef,
	base, targetSHA string,
	cache map[string][]githubPullRequest,
) (*PullRequest, string) {
	pullRequests, ok := cache[ref.SHA]
	if !ok {
		fetched, err := githubPullRequests(ctx, repository.Path, repository.Slug(), ref.SHA)
		if err != nil {
			return nil, fmt.Sprintf("pull-request query failed: %v", err)
		}
		pullRequests = fetched
		if cache != nil {
			cache[ref.SHA] = pullRequests
		}
	}
	pullRequest := absorbingPullRequest(pullRequests, base)
	if pullRequest == nil {
		return nil, fmt.Sprintf("no merged pull request into %s names this head", base)
	}
	if !isGitObjectID(pullRequest.MergeSHA) {
		return nil, fmt.Sprintf("merged pull request #%d carries no valid merge commit", pullRequest.Number)
	}
	landed, err := isAncestor(ctx, repository.Path, pullRequest.MergeSHA, targetSHA)
	if err != nil {
		return nil, fmt.Sprintf("landing containment check failed: %v", err)
	}
	if !landed {
		return nil, fmt.Sprintf("landing commit %s of pull request #%d is not contained in the fetched target",
			shortSHA(pullRequest.MergeSHA), pullRequest.Number)
	}
	// The two proof halves fail for different reasons and deserve different
	// evidence: a branch that never fully entered its landing commit was
	// amended while landing, while one that landed in full and fails only
	// against the target has been overtaken — by later edits or a revert,
	// which tree arithmetic cannot tell apart. Both stay ineligible, but the
	// second case names the landing commit, where the content remains
	// recoverable forever, so the remaining human decision is an informed one.
	inLanding, err := contentContained(ctx, repository.Path, ref.SHA, pullRequest.MergeSHA)
	if err != nil {
		return nil, fmt.Sprintf("three-way proof failed to run: %v", err)
	}
	if !inLanding {
		return nil, fmt.Sprintf(
			"landing commit %s of pull request #%d does not carry this branch's work in full; it may have been amended while landing",
			shortSHA(pullRequest.MergeSHA), pullRequest.Number)
	}
	inTarget, err := contentContained(ctx, repository.Path, ref.SHA, targetSHA)
	if err != nil {
		return nil, fmt.Sprintf("three-way proof failed to run: %v", err)
	}
	if !inTarget {
		return nil, fmt.Sprintf(
			"landed in full via pull request #%d, but the target has since diverged from that work — later edits and a revert are indistinguishable here; the content remains recoverable at landing commit %s",
			pullRequest.Number, shortSHA(pullRequest.MergeSHA))
	}
	return pullRequest, ""
}

// classifyAbsorbedOrUnique implements the absorbed/unique split. absorbed is
// true when git cherry reports zero unique patches, or when the branch tree
// is identical to the target tree; both are patch-id/content evidence only,
// never a landing receipt. See #req:absorbed-is-report-only.
func classifyAbsorbedOrUnique(ctx context.Context, repositoryPath, targetSHA, branchSHA string) (absorbed bool, evidence string, uniqueCount int, err error) {
	output, err := git(ctx, repositoryPath, "cherry", targetSHA, branchSHA)
	if err != nil {
		return false, "", 0, err
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "+") {
			uniqueCount++
		}
	}
	if uniqueCount == 0 {
		return true, fmt.Sprintf("git cherry %s %s reports 0 unique patches", shortSHA(targetSHA), shortSHA(branchSHA)), 0, nil
	}
	branchTree, err := commitTree(ctx, repositoryPath, branchSHA)
	if err != nil {
		return false, "", uniqueCount, nil //nolint:nilerr // tree comparison is best-effort supplementary evidence
	}
	targetTree, err := commitTree(ctx, repositoryPath, targetSHA)
	if err != nil {
		return false, "", uniqueCount, nil //nolint:nilerr // see above
	}
	if branchTree == targetTree {
		return true, fmt.Sprintf("tree %s identical to target tree %s", shortSHA(branchTree), shortSHA(targetTree)), 0, nil
	}
	return false, "", uniqueCount, nil
}
