package worktrees

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
)

// Branch Hygiene inventories and safely retires local and remote Git branches
// across the fleet, including the large majority that have no linked worktree
// and no WB Work Log claim, and are therefore invisible to worktree cleanup.
//
// It deliberately shares primitives with Worktree Lifecycle — the same
// fresh-fetch exact target resolution (fetchRemoteTargetHead), the same
// ancestor/tree/patch-id evidence gathering (isAncestor, commitTree, git
// cherry), the same GitHub pull-request evidence (githubPullRequests,
// matchingPullRequests), and the same compare-and-delete/force-with-lease ref
// retirement (gitCanonical, runSecureCleanupGitHelper) — while remaining a
// sibling command family with its own evidence taxonomy and its own top-level
// `wb branch` surface. See spec/features/branch-hygiene/README.md.

// Branch disposition is a closed set. A branch carries exactly one.
const (
	BranchContained  = "contained"  // ancestor of the fetched exact target; always eligible for deletion
	BranchAbsorbed   = "absorbed"   // patch-id/tree equal to the target, but not an ancestor; report-only, forever
	BranchReceipted  = "receipted"  // a proved landing receipt shows the work is in the target; eligible only under --receipts
	BranchUnique     = "unique"     // has content git cherry proves is not upstream
	BranchProtected  = "protected"  // base, canonical HEAD, or a protected name
	BranchInUse      = "in-use"     // checked out in a linked worktree, or named by a WB Work Log claim
	BranchUnreadable = "unreadable" // required evidence could not be obtained
)

// Branch scope selects which refs a sweep enumerates.
const (
	BranchScopeLocal  = "local"
	BranchScopeRemote = "remote"
	BranchScopeAll    = "all"
)

// protectedBranchNames are hard-coded per branch-hygiene's Open Questions
// pending a configurable follow-up.
var protectedBranchNames = map[string]bool{
	"main":   true,
	"master": true,
}

// BranchListOptions selects the inventory. It is read-only in every
// configuration: its only permitted remote interaction is fetching.
type BranchListOptions struct {
	ProjectsRoot string
	Base         string
	Scope        string // local, remote, or all; default local
	Only         string // one disposition name; empty means every disposition
	OlderThan    time.Duration
	Filter       string
	// Progress receives incremental "[n/N] repository" lines as the sweep
	// works, plus a closing summary. Nil disables progress reporting.
	Progress io.Writer
}

// BranchEntry is one branch and the evidence behind its disposition.
type BranchEntry struct {
	Repository      string       `json:"repository"`
	Branch          string       `json:"branch"`
	Scope           string       `json:"scope"` // local or remote
	SHA             string       `json:"sha"`
	ShortSHA        string       `json:"short_sha"`
	CommitterDate   time.Time    `json:"committer_date,omitempty"`
	Base            string       `json:"base"`
	TargetSHA       string       `json:"target_sha,omitempty"`
	Disposition     string       `json:"disposition"`
	Evidence        string       `json:"evidence"`
	Reason          string       `json:"reason,omitempty"`
	Task            string       `json:"task,omitempty"`
	OpenPullRequest *PullRequest `json:"open_pull_request,omitempty"`
	// LandingSHA and ReceiptPullRequest carry a receipted branch's proved
	// landing so apply can re-verify the receipt — not ancestry, which a
	// receipted branch fails by construction — against the freshly fetched
	// target. See #req:receipted-requires-a-proved-landing.
	LandingSHA         string       `json:"landing_sha,omitempty"`
	ReceiptPullRequest *PullRequest `json:"receipt_pull_request,omitempty"`
	// PullRequestQueryFailed distinguishes "no open pull request" from "WB
	// could not ask GitHub." Cleanup's remote apply fails the whole scope
	// closed on the latter; list surfaces it but never refuses on it.
	PullRequestQueryFailed bool `json:"pull_request_query_failed,omitempty"`
}

// BranchListOutcome is the full result of one sweep.
type BranchListOutcome struct {
	Base        string         `json:"base"`
	Scope       string         `json:"scope"`
	Entries     []BranchEntry  `json:"entries"`
	Diagnostics []string       `json:"diagnostics,omitempty"`
	Totals      map[string]int `json:"totals"`
	ElapsedMS   int64          `json:"elapsed_ms"`
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
	if !validBranch(context.Background(), options.Base) {
		return BranchListOptions{}, fmt.Errorf("invalid base branch %q", options.Base)
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
		case BranchContained, BranchAbsorbed, BranchReceipted, BranchUnique, BranchProtected, BranchInUse, BranchUnreadable:
		default:
			return BranchListOptions{}, fmt.Errorf("unsupported --only %q", options.Only)
		}
	}
	if options.OlderThan < 0 {
		return BranchListOptions{}, fmt.Errorf("--older-than cannot be negative")
	}
	options.Filter = strings.TrimSpace(options.Filter)
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
	Progress     io.Writer
	Now          time.Time
	// Receipts enables landing-receipt classification, which costs a GitHub
	// query per non-contained candidate. See
	// #req:receipted-is-opt-in-and-fails-closed.
	Receipts bool
}

func sweepBranches(ctx context.Context, options BranchListOptions) (BranchListOutcome, error) {
	started := time.Now()
	sweep := branchSweepOptions{
		ProjectsRoot: options.ProjectsRoot, Base: options.Base, Scope: options.Scope,
		Only: options.Only, OlderThan: options.OlderThan, Filter: options.Filter,
		Progress: options.Progress, Now: started,
	}
	entries, diagnostics, err := classifyFleetBranches(ctx, sweep)
	if err != nil {
		return BranchListOutcome{}, err
	}
	totals := tallyDispositions(entries)
	filtered := applyListDisplayFilters(entries, sweep)
	sortBranchEntries(filtered)
	return BranchListOutcome{
		Base: options.Base, Scope: options.Scope, Entries: filtered,
		Diagnostics: diagnostics, Totals: totals, ElapsedMS: time.Since(started).Milliseconds(),
	}, nil
}

func applyListDisplayFilters(entries []BranchEntry, sweep branchSweepOptions) []BranchEntry {
	filtered := make([]BranchEntry, 0, len(entries))
	for _, entry := range entries {
		if sweep.Only != "" && entry.Disposition != sweep.Only {
			continue
		}
		if sweep.OlderThan > 0 && !entry.CommitterDate.IsZero() && sweep.Now.Sub(entry.CommitterDate) < sweep.OlderThan {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func sortBranchEntries(entries []BranchEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Repository != entries[j].Repository {
			return entries[i].Repository < entries[j].Repository
		}
		if entries[i].Branch != entries[j].Branch {
			return entries[i].Branch < entries[j].Branch
		}
		return entries[i].Scope < entries[j].Scope
	})
}

func tallyDispositions(entries []BranchEntry) map[string]int {
	totals := map[string]int{}
	for _, entry := range entries {
		totals[entry.Disposition]++
	}
	return totals
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
		repositoryEntries, diagnostic := inspectRepositoryBranches(ctx, repository, sweep, inUse)
		entries = append(entries, repositoryEntries...)
		if diagnostic != "" {
			diagnostics = append(diagnostics, diagnostic)
		}
	}
	reportBranchSummary(sweep.Progress, tallyDispositions(entries), time.Since(start))
	return entries, diagnostics, paths, nil
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
			entries = append(entries, classifyBranch(ctx, repository, sweep, ref, BranchScopeLocal, targetSHA, canonicalHEAD, inUse, checkedOut, pullRequestCache))
		}
	}
	if sweep.Scope == BranchScopeRemote || sweep.Scope == BranchScopeAll {
		remote, diagnostic := listRemoteRefs(ctx, repository.Path)
		if diagnostic != "" {
			diagnostics = append(diagnostics, fmt.Sprintf("%s: %s", slug, diagnostic))
		}
		pullRequestCache := map[string][]githubPullRequest{}
		for _, ref := range remote {
			entries = append(entries, classifyBranch(ctx, repository, sweep, ref, BranchScopeRemote, targetSHA, canonicalHEAD, inUse, nil, pullRequestCache))
		}
	}
	return entries, strings.Join(diagnostics, "; ")
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
type branchRef struct {
	Name          string
	SHA           string
	CommitterDate time.Time
}

func listLocalRefs(ctx context.Context, repositoryPath string) ([]branchRef, string) {
	return listRefs(ctx, repositoryPath, "refs/heads/", "")
}

func listRemoteRefs(ctx context.Context, repositoryPath string) ([]branchRef, string) {
	if _, err := git(ctx, repositoryPath, "fetch", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return nil, fmt.Sprintf("fetch --prune origin: %v", err)
	}
	return listRefs(ctx, repositoryPath, "refs/remotes/origin/", "origin/")
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
	if scope == BranchScopeLocal {
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
		if scope == BranchScopeRemote {
			classifyRemotePullRequestGate(ctx, repository, ref, targetSHA, &entry, pullRequestCache)
		}
		return entry
	}

	absorbed, absorbedEvidence, uniqueCount, err := classifyAbsorbedOrUnique(ctx, repository.Path, targetSHA, ref.SHA)
	if err != nil {
		entry.Disposition = BranchUnreadable
		entry.Evidence = fmt.Sprintf("git cherry: %v", err)
		return entry
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
			entry.Disposition = BranchReceipted
			entry.LandingSHA = receipt.MergeSHA
			entry.ReceiptPullRequest = receipt
			entry.Evidence = fmt.Sprintf(
				"merged pull request #%d into %s; landing %s is in the fetched target and the three-way proof holds",
				receipt.Number, sweep.Base, shortSHA(receipt.MergeSHA))
			entry.Reason = fmt.Sprintf(
				"landed via merged pull request #%d; eligible for deletion under --receipts", receipt.Number)
			if scope == BranchScopeRemote {
				classifyRemotePullRequestGate(ctx, repository, ref, targetSHA, &entry, pullRequestCache)
			}
			return entry
		}
		receiptNote = "; receipt: " + note
	}
	if absorbed {
		entry.Disposition = BranchAbsorbed
		entry.Evidence = absorbedEvidence + receiptNote
		entry.Reason = "absorbed by patch-id or tree equality only; never eligible for --apply. " +
			"If this branch belongs to a WB task, run `wb worktree cleanup <task> --absorbed-by <pr-or-commit>`; " +
			"otherwise this requires an explicit human decision"
		// The text renderer prefers Reason over Evidence, so a named failing
		// receipt check must ride along or the operator never sees it.
		entry.Reason += receiptNote
		return entry
	}
	entry.Disposition = BranchUnique
	entry.Evidence = fmt.Sprintf("git cherry reports %d unique patch(es) not upstream", uniqueCount) + receiptNote
	if scope == BranchScopeRemote {
		classifyRemotePullRequestGate(ctx, repository, ref, targetSHA, &entry, pullRequestCache)
	}
	return entry
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func isProtectedBranch(branch, base, canonicalHEAD string) bool {
	if branch == base || branch == canonicalHEAD {
		return true
	}
	return protectedBranchNames[branch]
}

func protectedEvidence(branch, base, canonicalHEAD string) string {
	switch branch {
	case base:
		return fmt.Sprintf("is the base branch %q", base)
	case canonicalHEAD:
		return "is the canonical clone's current HEAD"
	default:
		return "matches a configured protected branch name"
	}
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

// classifyRemotePullRequestGate attaches open pull-request evidence to a
// remote branch entry when one exists. Listing never refuses on missing PR
// evidence — that fail-closed rule belongs to cleanup's apply path — but the
// evidence is surfaced here so a dry run already shows what would block it.
func classifyRemotePullRequestGate(ctx context.Context, repository discover.Repo, ref branchRef, targetSHA string, entry *BranchEntry, cache map[string][]githubPullRequest) {
	pullRequests, ok := cache[ref.SHA]
	if !ok {
		fetched, err := githubPullRequests(ctx, repository.Path, repository.Slug(), ref.SHA)
		if err != nil {
			entry.PullRequestQueryFailed = true
			return // list reports the missing evidence; apply's remote gate refuses on it
		}
		pullRequests = fetched
		if cache != nil {
			cache[ref.SHA] = pullRequests
		}
	}
	open, _ := matchingPullRequests(pullRequests, entry.Base, ref.SHA)
	entry.OpenPullRequest = open
}
