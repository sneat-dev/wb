// Package fleetinspect collects concrete fleet rollups using existing domain authorities.
package fleetinspect

import (
	"context"
	"fmt"
	"regexp"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/layout"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type Scope struct{ ProjectsRoot string }
type Options struct {
	Fleet, AllowEmpty bool
	Parallel          int
	Match, Regex      string
}
type DepthOptions struct{ Remote, Hooks bool }
type Request struct {
	ProjectsRoot, Filter, Match, Regex string
	Parallel                           int
	AllowEmpty, Remote, Hooks, All     bool
}
type Dependencies struct {
	Select     func(reposelection.Request) ([]reposelection.Target, error)
	Inspect    func([]reposelection.Target, int, func(reposelection.Target, repostatus.Row)) []repostatus.Row
	Scan       func(string) ([]discover.Repo, error)
	Layout     func(context.Context, string) (layout.Summary, error)
	Worktrees  func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error)
	Remote     func(string, string, func() []string) ([]discover.Repo, error)
	Owners     func([]string) []string
	Sync       func(context.Context, discover.Repo, string, bool, bool) fleetsync.Result
	Hooks      func(string, string, string, string) (hooks.CheckReport, error)
	Executable func() string
}
type Service struct{ deps Dependencies }

func New(deps Dependencies) *Service { return &Service{deps: deps} }
func (service *Service) Stats(_ context.Context, r Request) (StatsReport, error) {
	return service.collectFleetStats(Scope{r.ProjectsRoot}, r.ProjectsRoot, r.Filter, Options{Fleet: true, Parallel: r.Parallel, Match: r.Match, Regex: r.Regex, AllowEmpty: r.AllowEmpty}, DepthOptions{r.Remote, r.Hooks})
}
func (service *Service) Overview(_ context.Context, r Request) (OverviewReport, error) {
	return service.collectFleetOverview(Scope{r.ProjectsRoot}, r.ProjectsRoot, r.Filter, Options{Fleet: true, Parallel: r.Parallel, Match: r.Match, Regex: r.Regex, AllowEmpty: r.AllowEmpty}, r.All, DepthOptions{r.Remote, r.Hooks})
}

type StatsReport struct {
	SchemaVersion int            `yaml:"schema_version" json:"schema_version"`
	Inventory     InventoryStats `yaml:"inventory" json:"inventory"`
	Git           GitStats       `yaml:"git" json:"git"`
	Layout        LayoutStats    `yaml:"layout" json:"layout"`
	Worktrees     WorktreeStats  `yaml:"worktrees" json:"worktrees"`
	Remote        *RemoteStats   `yaml:"remote,omitempty" json:"remote,omitempty"`
	Hooks         *HooksStats    `yaml:"hooks,omitempty" json:"hooks,omitempty"`
}

type InventoryStats struct {
	Organizations int `yaml:"organizations" json:"organizations"`
	Repositories  int `yaml:"repositories" json:"repositories"`
}

type GitStats struct {
	Inspected int `yaml:"inspected" json:"inspected"`
	Clean     int `yaml:"clean" json:"clean"`
	Attention int `yaml:"attention" json:"attention"`
	Error     int `yaml:"error" json:"error"`
}

type LayoutStats struct {
	OK         int `yaml:"ok" json:"ok"`
	TopLevel   int `yaml:"top_level" json:"top_level"`
	Misowned   int `yaml:"misowned" json:"misowned"`
	NoOrigin   int `yaml:"no_origin" json:"no_origin"`
	Unreadable int `yaml:"unreadable" json:"unreadable"`
}

type RemoteStats struct {
	WouldClone   int `yaml:"would_clone" json:"would_clone"`
	WouldPull    int `yaml:"would_pull" json:"would_pull"`
	SkippedDirty int `yaml:"skipped_dirty" json:"skipped_dirty"`
	// Ignored counts repos marked with `wb repo ignore`. Distinct from
	// LocalOnly below, which counts repos wb found on disk but not on GitHub.
	Ignored int `yaml:"ignored" json:"ignored"`
	// EmptyRemote counts repos whose origin publishes no branches yet, so
	// there is nothing to pull until someone pushes.
	EmptyRemote int `yaml:"empty_remote" json:"empty_remote"`
	// ArchivedUnlandable counts archived repos whose clone holds commits that
	// exist on no remote. The remote is read-only, so they can never be
	// pushed: the state needs a decision and will not clear on its own.
	ArchivedUnlandable int `yaml:"archived_unlandable" json:"archived_unlandable"`
	LocalOnly          int `yaml:"local_only" json:"local_only"`
	RemoteOnly         int `yaml:"remote_only" json:"remote_only"`
	NoOp               int `yaml:"noop" json:"noop"`
	Error              int `yaml:"error" json:"error"`
}

type HooksStats struct {
	Repositories int `yaml:"repositories" json:"repositories"`
	Findings     int `yaml:"findings" json:"findings"`
	Errors       int `yaml:"errors" json:"errors"`
}

type WorktreeStats struct {
	Tasks     int `yaml:"tasks" json:"tasks"`
	Checkouts int `yaml:"checkouts" json:"checkouts"`
	Dirty     int `yaml:"dirty" json:"dirty"`
	Locked    int `yaml:"locked" json:"locked"`
}

type OverviewReport struct {
	SchemaVersion int              `yaml:"schema_version" json:"schema_version"`
	Stats         StatsReport      `yaml:"stats" json:"stats"`
	Status        repostatus.Index `yaml:"status" json:"status"`
}

func (service *Service) collectFleetOverview(inv Scope, projects, filter string, options Options, all bool, depth DepthOptions) (OverviewReport, error) {
	stats, fullStatus, err := service.collectFleetStatsAndStatus(inv, projects, filter, options, depth)
	if err != nil {
		return OverviewReport{}, err
	}
	status := fullStatus
	if !all {
		status = repostatus.HideClean(fullStatus)
	}
	return OverviewReport{
		SchemaVersion: 1,
		Stats:         stats,
		Status:        status,
	}, nil
}

func (service *Service) collectFleetStats(inv Scope, projects, filter string, options Options, depth DepthOptions) (StatsReport, error) {
	stats, _, err := service.collectFleetStatsAndStatus(inv, projects, filter, options, depth)
	return stats, err
}

func (service *Service) collectFleetStatsAndStatus(inv Scope, projects, filter string, options Options, depth DepthOptions) (StatsReport, repostatus.Index, error) {
	options.Fleet = true
	targets, err := service.deps.Select(reposelection.Request{Path: ".", ProjectsRoot: projects, Filter: filter, Fleet: true, Parallel: options.Parallel, Match: options.Match, Regex: options.Regex, AllowEmpty: options.AllowEmpty})
	if err != nil {
		return StatsReport{}, repostatus.Index{}, err
	}
	repositories := service.deps.Inspect(targets, options.Parallel, nil)
	fullStatus := repostatus.Index{SchemaVersion: 1, Repositories: repositories}
	inventory, err := service.fleetInventory(projects, filter, options)
	if err != nil {
		return StatsReport{}, repostatus.Index{}, err
	}
	worktreeStats, err := service.fleetWorktreeRollup(projects, filter, options)
	if err != nil {
		return StatsReport{}, repostatus.Index{}, err
	}
	layoutStats, err := service.fleetLayoutRollup(projects)
	if err != nil {
		return StatsReport{}, repostatus.Index{}, err
	}
	stats := StatsReport{
		SchemaVersion: 1,
		Inventory:     inventory,
		Git:           summarizeGitStats(repositories),
		Layout:        layoutStats,
		Worktrees:     worktreeStats,
	}
	if depth.Remote {
		remoteStats, remoteErr := service.fleetRemoteRollup(projects, filter, options)
		if remoteErr != nil {
			return StatsReport{}, repostatus.Index{}, remoteErr
		}
		stats.Remote = &remoteStats
	}
	if depth.Hooks {
		hooksStats := service.fleetHooksRollup(inv, targets, options.Parallel)
		stats.Hooks = &hooksStats
	}
	return stats, fullStatus, nil
}

func (service *Service) fleetLayoutRollup(projects string) (LayoutStats, error) {
	summary, err := service.deps.Layout(context.Background(), projects)
	if err != nil {
		return LayoutStats{}, err
	}
	return LayoutStats{
		OK: summary.OK, TopLevel: summary.TopLevel, Misowned: summary.Misowned,
		NoOrigin: summary.NoOrigin, Unreadable: summary.Unreadable,
	}, nil
}

func (service *Service) fleetRemoteRollup(projects, filter string, options Options) (RemoteStats, error) {
	repositories, err := service.deps.Remote(projects, filter, func() []string { return service.deps.Owners(nil) })
	if err != nil {
		return RemoteStats{}, err
	}
	expression, err := compileFleetRegex(options.Regex)
	if err != nil {
		return RemoteStats{}, err
	}
	stats := RemoteStats{}
	for _, repository := range repositories {
		if !reposelection.Match(repository.Slug(), filter, options.Match, expression) {
			continue
		}
		if repository.Local && !repository.Remote {
			stats.LocalOnly++
		}
		if repository.Remote && !repository.Local && !repository.Archived && !repository.IsFork {
			stats.RemoteOnly++
		}
		// Always dry-run here, so pruneArchived=true only classifies what
		// wb sync --prune-archived would do; nothing is ever deleted by a
		// fleet status/overview pass.
		result := service.deps.Sync(context.Background(), repository, projects, true, true)
		switch result.Status {
		case fleetsync.Cloned:
			stats.WouldClone++
		case fleetsync.Pulled:
			stats.WouldPull++
		case fleetsync.SkippedDirty:
			stats.SkippedDirty++
		case fleetsync.SkippedIgnored:
			stats.Ignored++
		case fleetsync.EmptyRemote:
			stats.EmptyRemote++
		case fleetsync.ArchivedUnlandable:
			stats.ArchivedUnlandable++
		case fleetsync.NoOp:
			stats.NoOp++
		case fleetsync.Failed:
			stats.Error++
		}
	}
	return stats, nil
}

func (service *Service) fleetHooksRollup(inv Scope, targets []reposelection.Target, parallel int) HooksStats {
	stats := HooksStats{Repositories: len(targets)}
	type result struct {
		findings int
		err      bool
	}
	results := make([]result, len(targets))
	reposelection.ForEach(len(targets), parallel, func(index int) {
		report, err := service.deps.Hooks(targets[index].Path, "", service.deps.Executable(), inv.ProjectsRoot)
		if err != nil {
			results[index].err = true
			return
		}
		results[index].findings = len(report.Findings)
	})
	for _, item := range results {
		stats.Findings += item.findings
		if item.err {
			stats.Errors++
		}
	}
	return stats
}

func (service *Service) fleetInventory(projects, filter string, options Options) (InventoryStats, error) {
	repositories, err := service.deps.Scan(projects)
	if err != nil {
		return InventoryStats{}, err
	}
	expression, err := compileFleetRegex(options.Regex)
	if err != nil {
		return InventoryStats{}, err
	}
	orgs := map[string]struct{}{}
	count := 0
	for _, repository := range repositories {
		if !reposelection.Match(repository.Slug(), filter, options.Match, expression) {
			continue
		}
		orgs[repository.Org] = struct{}{}
		count++
	}
	return InventoryStats{Organizations: len(orgs), Repositories: count}, nil
}

func (service *Service) fleetWorktreeRollup(projects, filter string, options Options) (WorktreeStats, error) {
	results, err := service.deps.Worktrees(context.Background(), worktrees.ListOptions{
		ProjectsRoot: projects,
		Filter:       filter,
	})
	if err != nil {
		return WorktreeStats{}, err
	}
	expression, err := compileFleetRegex(options.Regex)
	if err != nil {
		return WorktreeStats{}, err
	}
	tasks := map[string]struct{}{}
	stats := WorktreeStats{}
	for _, result := range results {
		if !reposelection.Match(result.Repository, filter, options.Match, expression) {
			continue
		}
		tasks[result.Task] = struct{}{}
		stats.Checkouts++
		if !result.Clean {
			stats.Dirty++
		}
		if result.Locked {
			stats.Locked++
		}
	}
	stats.Tasks = len(tasks)
	return stats, nil
}

func compileFleetRegex(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	compiled, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid --regex: %w", err)
	}
	return compiled, nil
}

func summarizeGitStats(repositories []repostatus.Row) GitStats {
	stats := GitStats{Inspected: len(repositories)}
	for _, repository := range repositories {
		switch repository.Status {
		case "clean":
			stats.Clean++
		case "attention":
			stats.Attention++
		case "error":
			stats.Error++
		}
	}
	return stats
}
