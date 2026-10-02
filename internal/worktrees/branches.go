package worktrees

import (
	"context"
	"fmt"
	"io"
	"path"
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

type branchRepositoryInspection func(context.Context, discover.Repo, branchSweepOptions, map[string]string) ([]BranchEntry, string)

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
	result, err := branchInventoryService().InventoryRetiredNamespace(ctx, sweep.branchInventorySweep(), generatedAt)
	if err != nil {
		return BranchListOutcome{}, err
	}
	return BranchListOutcome{Host: result.Host, GeneratedAt: result.GeneratedAt, Repository: result.Repository, Org: result.Org, Branch: result.Branch, Base: result.Base, Scope: result.Scope, Entries: result.Entries, Diagnostics: result.Diagnostics, Totals: result.Totals, ElapsedMS: result.ElapsedMS, RetiredRefs: result.RetiredRefs, RetiredBranches: result.RetiredBranches, RetiredTags: result.RetiredTags, RetiredTagNames: result.RetiredTagNames, RetiredRemoteUnavailable: result.RetiredRemoteUnavailable}, nil
}

func branchEvidenceHost() string { return branchInventoryService().BranchEvidenceHost() }

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
	return branchInventoryService().ClassifyFleetBranches(ctx, sweep.branchInventorySweep())
}

// classifyFleetBranchesWithPaths is classifyFleetBranches plus the
// slug-to-local-path lookup cleanup's apply phase needs to reopen each
// candidate's canonical repository for its recheck-before-mutation.
func classifyFleetBranchesWithPaths(ctx context.Context, sweep branchSweepOptions) ([]BranchEntry, []string, map[string]string, error) {
	return branchInventoryService().ClassifyFleetBranchesWithPaths(ctx, sweep.branchInventorySweep())
}

func countRetiredBranches(ctx context.Context, sweep branchSweepOptions) (map[string]int, int, map[string]int, int, bool, []string) {
	return branchInventoryService().CountRetiredBranches(ctx, sweep.branchInventorySweep())
}

// branchInUseKey identifies one repository/branch pair claimed live by a WB
// task, keyed exactly as ListResult reports it.
func branchInUseKey(repository, branch string) string {
	return worktreebranches.BranchInUseKey(repository, branch)
}

// branchInUseIndex builds the fleet-wide set of branches WB itself owns right
// now: checked out in a linked worktree, or named by a live Work Log claim.
// It reuses ListWithDiagnostics — the same live inventory `wb worktree list`
// reports — rather than re-deriving claim state, so the two surfaces can
// never disagree about what WB owns.
func branchInUseIndex(ctx context.Context, projectsRoot, filter string) (map[string]string, string) {
	return branchInventoryService().BranchInUseIndex(ctx, projectsRoot, filter)
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
	return branchInventoryService().CheckedOutLocalBranches(ctx, repositoryPath)
}

// branchRef is one enumerated ref before classification.
type branchRef = worktreebranches.BranchRef

// classifyAttestedReceipt verifies an operator-supplied --absorbed-by pointer
// for one candidate branch, reusing worktree cleanup's exact
// attested-absorption proof (attestedAbsorbedReceipt) rather than a second
// implementation of it. Branch Hygiene's candidates have no worktree — the
// worktree may already be gone, which is exactly the situation this exists
// for (see spec/features/branch-hygiene/README.md) — so the canonical
// repository stands in for both the "worktree" and "repository" arguments the
// shared proof expects; every check it performs runs against the canonical
// clone's object database and touches no working tree.

func shortSHA(sha string) string {
	return worktreebranches.ShortSHA(sha)
}

func isProtectedBranch(branch, base, canonicalHEAD string) bool {
	return worktreebranches.IsProtectedBranch(branch, base, canonicalHEAD)
}
