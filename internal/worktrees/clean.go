package worktrees

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/repofilter"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreebranches"
)

// Clean is the one fleet verb that audits, and under Apply retires, finished
// work: WB checkouts, branches that no longer have a checkout, and the linked
// worktrees WB's inventory cannot see. See spec/features/cleanup-orchestration.
//
// It composes the existing evidence engines rather than adding one. Checkouts
// are classified by GC's landing classifier and removed through the cleanup
// transaction; branches without a checkout are classified by Branch Hygiene
// with landing receipts on and retired through its compare-and-delete; foreign
// worktrees are reported, never removed. Every selector narrows the dry run and
// the apply through the same predicate, so the plan is the apply.

// Clean scopes.
const (
	CleanScopeLocal  = "local"
	CleanScopeRemote = "remote"
	CleanScopeAll    = "all"
)

// CleanOnlyLanded selects every class that proves the work landed.
const CleanOnlyLanded = "landed"

// CleanClassUnmanaged is the class of a linked worktree WB's inventory does not
// manage. Clean reports it and never removes it.
const CleanClassUnmanaged = "unmanaged"

// CleanOptions selects one pass. Dry run is the default.
type CleanOptions struct {
	ProjectsRoot string
	// Filter is the fleet owner/name substring or glob (see repofilter).
	Filter string
	// Orgs are exact repository owners; any one may match.
	Orgs []string
	// Repository is one exact owner/name.
	Repository string
	// Branch is one exact branch, of a checkout or of a branch ref.
	Branch string
	// Tasks are exact WB task names. Naming a task narrows the pass to
	// checkouts, since a bare branch has no task.
	Tasks []string
	// Scope is local (checkouts and local branches, the default), remote
	// (remote branches only) or all (both; a retired checkout's unchanged
	// origin branch is retired with it).
	Scope string
	// Only narrows the report and the apply to one class (see
	// CleanClassSelected).
	Only         string
	Base         string
	OlderThan    time.Duration
	Apply        bool
	AllowResidue bool
	SupersededBy string
	Workers      int
	SkipSizes    bool
	SkipDetached bool
	TTL          time.Duration
	// SessionFreshness and ResidueDepth are GC's, passed through.
	SessionFreshness time.Duration
	ResidueDepth     int
	Progress         func(ListProgress)
	BranchProgress   io.Writer
	Now              func() time.Time
}

// CleanSelection records what the selectors resolved to.
type CleanSelection struct {
	Filter       string   `json:"filter,omitempty"`
	Orgs         []string `json:"orgs,omitempty"`
	Repository   string   `json:"repository,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	Tasks        []string `json:"tasks,omitempty"`
	Scope        string   `json:"scope"`
	Only         string   `json:"only,omitempty"`
	Repositories []string `json:"repositories"`
}

// CleanOutcome is one whole pass and its receipt.
type CleanOutcome struct {
	SchemaVersion int            `json:"schema_version"`
	Apply         bool           `json:"apply"`
	Selection     CleanSelection `json:"selection"`
	// Worktrees is absent for --scope remote, which touches no checkout.
	Worktrees *GCOutcome `json:"worktrees,omitempty"`
	// Branches are refs with no checkout. A branch a checkout holds is that
	// checkout's row, and a protected branch is never a candidate, so neither
	// is repeated here.
	Branches           []BranchCleanupResult `json:"branches"`
	BranchDiagnostics  []string              `json:"branch_diagnostics,omitempty"`
	BranchReports      []string              `json:"branch_reports,omitempty"`
	Unmanaged          []OrphanWorktree      `json:"unmanaged,omitempty"`
	UnmanagedUnscanned []string              `json:"unmanaged_unscanned,omitempty"`
	Totals             map[string]int        `json:"totals"`
}

// Refused is how many rows this pass kept. It decides the exit code.
func (outcome CleanOutcome) Refused() int { return outcome.Totals["refused"] }

// ErrNoRepositoriesMatched reports a selection that matched no local clone.
// An empty success here is what hid the 0.202.0 glob defect for a whole audit.
var ErrNoRepositoriesMatched = errors.New("0 repositories matched")

// cleanPorts are the engines Clean composes; tests substitute them.
type cleanPorts struct {
	discover func(string) ([]discover.Repo, error)
	gc       func(context.Context, GCOptions) (GCOutcome, error)
	branches func(context.Context, BranchCleanupOptions) (BranchCleanupOutcome, error)
	orphans  func(context.Context, OrphanOptions) (OrphanReport, error)
}

func defaultCleanPorts() cleanPorts {
	return cleanPorts{discover: discover.ScanLocal, gc: GC, branches: BranchCleanup, orphans: Orphans}
}

// Clean runs one pass.
func Clean(ctx context.Context, options CleanOptions) (CleanOutcome, error) {
	return cleanWithPorts(ctx, options, defaultCleanPorts())
}

func cleanWithPorts(ctx context.Context, options CleanOptions, ports cleanPorts) (CleanOutcome, error) {
	options, err := normalizeCleanOptions(options)
	if err != nil {
		return CleanOutcome{}, err
	}
	repositories, err := selectCleanRepositories(ports.discover, options)
	if err != nil {
		return CleanOutcome{}, err
	}
	outcome := CleanOutcome{
		SchemaVersion: 1, Apply: options.Apply, Branches: []BranchCleanupResult{}, Totals: map[string]int{},
		Selection: CleanSelection{
			Filter: options.Filter, Orgs: options.Orgs, Repository: options.Repository, Branch: options.Branch,
			Tasks: options.Tasks, Scope: options.Scope, Only: options.Only, Repositories: repositories,
		},
	}
	selected := cleanRepositorySet{all: !hasRepositorySelector(options), names: map[string]bool{}}
	for _, repository := range repositories {
		selected.names[strings.ToLower(repository)] = true
	}
	if options.Scope != CleanScopeRemote {
		worktreeOutcome, err := ports.gc(ctx, cleanGCOptions(options, selected))
		if err != nil {
			return outcome, err
		}
		outcome.Worktrees = &worktreeOutcome
	}
	if len(options.Tasks) == 0 && options.Only != CleanClassUnmanaged {
		if err := cleanBranches(ctx, ports.branches, options, repositories, &outcome); err != nil {
			return outcome, err
		}
	}
	if options.Scope != CleanScopeRemote && len(options.Tasks) == 0 && options.Branch == "" &&
		(options.Only == "" || options.Only == CleanClassUnmanaged) {
		if err := cleanUnmanaged(ctx, ports.orphans, options, selected, &outcome); err != nil {
			return outcome, err
		}
	}
	summarizeClean(&outcome)
	return outcome, nil
}

func normalizeCleanOptions(options CleanOptions) (CleanOptions, error) {
	options.Filter = strings.TrimSpace(options.Filter)
	if !repofilter.Valid(options.Filter) {
		return options, fmt.Errorf("--filter %q is not a valid glob", options.Filter)
	}
	options.Repository = strings.TrimSpace(options.Repository)
	if options.Repository != "" {
		owner, name, ok := strings.Cut(options.Repository, "/")
		if !ok || !validRepositorySegment(owner) || !validRepositorySegment(name) {
			return options, fmt.Errorf("--repo %q must be one exact owner/name", options.Repository)
		}
	}
	orgs := make([]string, 0, len(options.Orgs))
	for _, org := range options.Orgs {
		org = strings.TrimSpace(org)
		if !validRepositorySegment(org) {
			return options, fmt.Errorf("invalid --org %q", org)
		}
		orgs = append(orgs, org)
	}
	options.Orgs = orgs
	options.Branch = strings.TrimSpace(options.Branch)
	if options.Scope == "" {
		options.Scope = CleanScopeLocal
	}
	switch options.Scope {
	case CleanScopeLocal, CleanScopeRemote, CleanScopeAll:
	default:
		return options, fmt.Errorf("unsupported --scope %q; use local, remote, or all", options.Scope)
	}
	if options.Only != "" && !slices.Contains(CleanClasses(), options.Only) {
		return options, fmt.Errorf("unsupported --only %q; use one of %s", options.Only, strings.Join(CleanClasses(), ", "))
	}
	if len(options.Tasks) > 0 && options.Scope == CleanScopeRemote {
		return options, errors.New("--task selects checkouts, and --scope remote selects none; drop one")
	}
	if options.OlderThan < 0 {
		return options, errors.New("--older-than cannot be negative")
	}
	if strings.TrimSpace(options.SupersededBy) != "" && len(options.Tasks) != 1 &&
		(options.Repository == "" || options.Branch == "") {
		return options, errors.New("--superseded-by names one trusted receipt for one item: supply exactly one --task, or --repo and --branch")
	}
	if options.Workers < 1 {
		options.Workers = DefaultInspectWorkers
	}
	return options, nil
}

// CleanClasses is every value --only accepts.
func CleanClasses() []string {
	return []string{
		CleanOnlyLanded,
		GCClassContained, GCClassLandedClean, GCClassLandedResidue, GCClassDetachedReview,
		GCClassDetachedUnknown, GCClassOpenPR, GCClassDirty, GCClassClaimedLive, GCClassUnpushed, GCClassUnmerged,
		BranchReceipted, BranchSuperseded, BranchAbsorbed, BranchUnique, BranchUnreadable,
		CleanClassUnmanaged,
	}
}

// CleanClassSelected reports whether the --only value selects class. Empty
// selects everything; "landed" selects every class that proves a landing.
func CleanClassSelected(only, class string) bool {
	switch only {
	case "":
		return true
	case CleanOnlyLanded:
		switch class {
		case GCClassContained, GCClassLandedClean, GCClassLandedResidue, GCClassDetachedReview, BranchReceipted, BranchSuperseded:
			return true
		}
		return false
	default:
		return only == class
	}
}

// selectCleanRepositories resolves the selectors against the local clones and
// refuses a selection that matched none.
func selectCleanRepositories(scan func(string) ([]discover.Repo, error), options CleanOptions) ([]string, error) {
	repositories, err := scan(options.ProjectsRoot)
	if err != nil {
		return nil, fmt.Errorf("discover local repositories: %w", err)
	}
	seen := map[string]bool{}
	var slugs []string
	for _, repository := range repositories {
		slug := repository.Slug()
		if seen[slug] || !cleanRepositorySelected(options, slug, repository.Path) {
			continue
		}
		seen[slug] = true
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	if len(slugs) == 0 && hasRepositorySelector(options) {
		return nil, fmt.Errorf("%w %s under %s", ErrNoRepositoriesMatched, describeCleanSelection(options), options.ProjectsRoot)
	}
	return slugs, nil
}

// hasRepositorySelector reports whether the caller narrowed the repositories.
// Without one, a projects root with no clones still has WB's own residue
// (empty task shells, stages) to sweep, so it is not an empty selection.
func hasRepositorySelector(options CleanOptions) bool {
	return options.Filter != "" || len(options.Orgs) > 0 || options.Repository != ""
}

func cleanRepositorySelected(options CleanOptions, slug string, paths ...string) bool {
	if !repofilter.Match(options.Filter, append([]string{slug}, paths...)...) {
		return false
	}
	if options.Repository != "" && !strings.EqualFold(options.Repository, slug) {
		return false
	}
	if len(options.Orgs) == 0 {
		return true
	}
	owner, _, _ := strings.Cut(slug, "/")
	return slices.ContainsFunc(options.Orgs, func(org string) bool { return strings.EqualFold(org, owner) })
}

// cleanRepositorySet is the resolved repository selection: every repository
// when no selector was given (including ones only WB's inventory knows, such
// as a checkout whose canonical clone moved), otherwise exactly the matches.
type cleanRepositorySet struct {
	all   bool
	names map[string]bool
}

func (set cleanRepositorySet) contains(repository string) bool {
	return set.all || set.names[strings.ToLower(repository)]
}

// describeCleanSelection names the selectors; it is only called when at least
// one was given (see hasRepositorySelector).
func describeCleanSelection(options CleanOptions) string {
	var parts []string
	if options.Filter != "" {
		parts = append(parts, "--filter "+options.Filter)
	}
	for _, org := range options.Orgs {
		parts = append(parts, "--org "+org)
	}
	if options.Repository != "" {
		parts = append(parts, "--repo "+options.Repository)
	}
	return strings.Join(parts, " ")
}

// cleanGCOptions maps the selection onto GC. The walk is narrowed by the
// cheapest selector GC's inventory understands, and Select then applies every
// selector exactly, so the plan and the apply see the same rows.
func cleanGCOptions(options CleanOptions, selected cleanRepositorySet) GCOptions {
	walkFilter := options.Filter
	switch {
	case walkFilter != "":
	case options.Repository != "":
		walkFilter = options.Repository
	case len(options.Orgs) == 1:
		walkFilter = options.Orgs[0] + "/*"
	}
	supersededBy := ""
	if len(options.Tasks) == 1 {
		supersededBy = options.SupersededBy
	}
	branch := options.Branch
	return GCOptions{
		ProjectsRoot: options.ProjectsRoot, Tasks: options.Tasks, Filter: walkFilter, Base: options.Base,
		Apply: options.Apply, AllowResidue: options.AllowResidue, SupersededBy: supersededBy,
		SkipDetached: options.SkipDetached, OlderThan: options.OlderThan, TTL: options.TTL,
		SessionFreshness: options.SessionFreshness, ResidueDepth: options.ResidueDepth, Workers: options.Workers,
		SkipSizes: options.SkipSizes, DeleteRemote: options.Scope == CleanScopeAll, Now: options.Now,
		Progress: options.Progress, Only: options.Only,
		Select: func(result ListResult) bool {
			return selected.contains(result.Repository) && (branch == "" || result.Branch == branch)
		},
	}
}

// cleanBranches runs Branch Hygiene once per selected repository, up to
// Workers at a time: each repository's fetch and ref deletions are its own.
func cleanBranches(ctx context.Context, run func(context.Context, BranchCleanupOptions) (BranchCleanupOutcome, error), options CleanOptions, repositories []string, outcome *CleanOutcome) error {
	scope := worktreebranches.BranchScopeLocal
	switch options.Scope {
	case CleanScopeRemote:
		scope = worktreebranches.BranchScopeRemote
	case CleanScopeAll:
		scope = worktreebranches.BranchScopeAll
	}
	only := options.Only
	if !slices.Contains([]string{"", CleanOnlyLanded, GCClassContained, BranchReceipted, BranchSuperseded, BranchAbsorbed, BranchUnique, BranchUnreadable}, only) {
		// A checkout-only class selects no branch.
		return nil
	}
	reportRoot := ""
	if options.Apply {
		home, err := wbhome.Root(options.ProjectsRoot)
		if err != nil {
			return fmt.Errorf("resolve WB home for clean reports: %w", err)
		}
		now := time.Now
		if options.Now != nil {
			now = options.Now
		}
		reportRoot = DefaultBranchCleanupReportDir(home, now())
	}
	supersededBy := ""
	if options.Repository != "" && options.Branch != "" {
		supersededBy = options.SupersededBy
	}
	outcomes := make([]BranchCleanupOutcome, len(repositories))
	errs := make([]error, len(repositories))
	jobs := make(chan int)
	var group sync.WaitGroup
	for range min(options.Workers, len(repositories)) {
		group.Add(1)
		go func() {
			defer group.Done()
			for index := range jobs {
				branchOptions := BranchCleanupOptions{
					ProjectsRoot: options.ProjectsRoot, Base: options.Base, Scope: scope, Apply: options.Apply,
					// Filter narrows the in-use inventory walk to this repository;
					// without it each of N repositories walked the whole fleet.
					OlderThan: options.OlderThan, Filter: repositories[index], Repository: repositories[index], Branch: options.Branch,
					Receipts: true, SupersededBy: supersededBy, Only: only, Now: options.Now, Progress: options.BranchProgress,
				}
				if reportRoot != "" {
					branchOptions.ReportDir = filepath.Join(reportRoot, strings.ReplaceAll(repositories[index], "/", "-"))
				}
				outcomes[index], errs[index] = run(ctx, branchOptions)
			}
		}()
	}
	for index := range repositories {
		jobs <- index
	}
	close(jobs)
	group.Wait()
	for index, branchOutcome := range outcomes {
		if errs[index] != nil {
			outcome.BranchDiagnostics = append(outcome.BranchDiagnostics, fmt.Sprintf("%s: %v", repositories[index], errs[index]))
			continue
		}
		for _, result := range branchOutcome.Results {
			if result.Disposition == BranchInUse || result.Disposition == BranchProtected {
				continue
			}
			outcome.Branches = append(outcome.Branches, result)
		}
		for _, diagnostic := range branchOutcome.Diagnostics {
			outcome.BranchDiagnostics = append(outcome.BranchDiagnostics, repositories[index]+": "+diagnostic)
		}
		if branchOutcome.ReportPath != "" {
			outcome.BranchReports = append(outcome.BranchReports, branchOutcome.ReportPath)
		}
	}
	return nil
}

// cleanUnmanaged reports linked worktrees the inventory did not classify.
// Every checkout WB manages was classified above by the one landing
// classifier; this sweep only names the rest, so two verbs can never again
// disagree about one head.
func cleanUnmanaged(ctx context.Context, run func(context.Context, OrphanOptions) (OrphanReport, error), options CleanOptions, selected cleanRepositorySet, outcome *CleanOutcome) error {
	// Unmanaged rows are only swept beside the checkouts (never for --scope
	// remote), so the inventory is always present here.
	seen := map[string]bool{}
	for _, entry := range outcome.Worktrees.Entries {
		seen[filepath.Clean(entry.WorktreeDir)] = true
	}
	for _, diagnostic := range outcome.Worktrees.Diagnostics {
		seen[filepath.Clean(diagnostic.Path)] = true
	}
	report, err := run(ctx, OrphanOptions{
		ProjectsRoot: options.ProjectsRoot, Base: options.Base,
		Select: selected.contains,
		Skip: func(path string) bool {
			// A checkout the inventory classified or diagnosed is managed.
			return seen[filepath.Clean(path)]
		},
	})
	if err != nil {
		return fmt.Errorf("sweep unmanaged worktrees: %w", err)
	}
	for _, family := range report.Families {
		for _, worktree := range family.Worktrees {
			if worktree.Layout == LayoutCurrent || worktree.Layout == LayoutShared {
				// Under WB's own store: the inventory owns it, even when it
				// reported it as a diagnostic rather than a row.
				continue
			}
			outcome.Unmanaged = append(outcome.Unmanaged, worktree)
		}
	}
	outcome.UnmanagedUnscanned = report.Unscanned
	return nil
}

func summarizeClean(outcome *CleanOutcome) {
	if outcome.Worktrees != nil {
		for key, value := range outcome.Worktrees.Totals {
			outcome.Totals["worktree_"+key] = value
		}
		outcome.Totals["retired"] += outcome.Worktrees.Totals["retired"]
		outcome.Totals["eligible"] += outcome.Worktrees.Totals["eligible"]
		outcome.Totals["refused"] += outcome.Worktrees.Totals["refused"]
	}
	for _, branch := range outcome.Branches {
		outcome.Totals["branch_"+branch.Disposition]++
		switch {
		case branch.Applied:
			outcome.Totals["retired"]++
		case branch.Eligible:
			outcome.Totals["eligible"]++
		default:
			outcome.Totals["refused"]++
		}
	}
	outcome.Totals["unmanaged"] = len(outcome.Unmanaged)
}

// ResolveRetireName maps a name that is not a WB task to the task whose
// checkout has it as its branch. A task named batch5-assembly on branch batch5
// used to answer `wb worktree retire batch5` with "task not found". It returns
// "" when no checkout holds the branch, and an error naming every candidate
// when more than one task does.
func ResolveRetireName(ctx context.Context, projectsRoot, filter, name string) (string, error) {
	return resolveRetireNameWith(ctx, ListWithDiagnostics, projectsRoot, filter, name)
}

func resolveRetireNameWith(ctx context.Context, list func(context.Context, ListOptions) (ListOutcome, error), projectsRoot, filter, name string) (string, error) {
	listed, err := list(ctx, ListOptions{ProjectsRoot: projectsRoot, Filter: filter, IncludeDetached: true})
	if err != nil {
		return "", fmt.Errorf("resolve %q to a WB task: %w", name, err)
	}
	var tasks []string
	for _, result := range listed.Results {
		if result.Branch == name && !slices.Contains(tasks, result.Task) {
			tasks = append(tasks, result.Task)
		}
	}
	sort.Strings(tasks)
	switch len(tasks) {
	case 0:
		return "", nil
	case 1:
		return tasks[0], nil
	default:
		return "", fmt.Errorf("branch %q is checked out by tasks %s; name the task", name, strings.Join(tasks, ", "))
	}
}
