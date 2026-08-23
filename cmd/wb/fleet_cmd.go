package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/layout"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func newFleetCmd() *cobra.Command {
	overview := newFleetOverviewOptions()
	command := &cobra.Command{
		Use:   "fleet",
		Short: "Inspect local fleet inventory, attention, layout, and worktree debt",
		Long: `Inspect the local repository fleet without mutating checkouts.

  wb fleet          overview (stats + attention worklist)
  wb fleet overview same as wb fleet
  wb fleet stats    inventory and attention counts only
  wb fleet status   attention worklist (same shape as historical wb status)

Default stats stay local: inventory, Git attention, layout placement, and
managed worktrees. Pass --remote for sync-drift counts (contacts GitHub) or
--hooks for managed-hook finding counts. Use wb layout audit for the layout
worklist and wb sync --dry-run for a full sync plan.`,
		Args: cobra.NoArgs,
		RunE: overview.run,
	}
	overview.bind(command)

	overviewCmd := &cobra.Command{
		Use:   "overview",
		Short: "Summarize fleet inventory, Git attention, layout, and managed worktrees",
		Args:  cobra.NoArgs,
		RunE:  overview.run,
	}
	overview.bind(overviewCmd)
	command.AddCommand(overviewCmd)
	command.AddCommand(newFleetStatsCmd())
	command.AddCommand(newFleetStatusCmd())
	return command
}

type fleetDepthOptions struct {
	remote bool
	hooks  bool
}

type fleetOverviewOptions struct {
	status  qualityOptions
	all     bool
	details bool
	depth   fleetDepthOptions
}

func newFleetOverviewOptions() *fleetOverviewOptions {
	return &fleetOverviewOptions{status: qualityOptions{parallel: 4, fleet: true}}
}

func (options *fleetOverviewOptions) bind(command *cobra.Command) {
	bindRepositoryStatusFlags(command, &options.status, &options.details, &options.all, true)
	bindFleetDepthFlags(command, &options.depth)
	if flag := command.Flags().Lookup("report-dir"); flag != nil {
		flag.Usage = "write fleet-overview.md and fleet-overview.yaml to this directory"
	}
}

func (options *fleetOverviewOptions) run(cmd *cobra.Command, args []string) error {
	report, err := collectFleetOverview(projectsRoot, filterFlag, options.status, options.all, options.depth)
	if err != nil {
		return err
	}
	if err := writeFleetOverviewOutput(report, options.status.format, options.status.reportDir, options.details); err != nil {
		return err
	}
	if statusFailed(report.Status) || report.Stats.Git.Error > 0 {
		return &exitError{
			code:    exitFindings,
			message: "one or more repositories could not be inspected; see the report above",
		}
	}
	return nil
}

func newFleetStatsCmd() *cobra.Command {
	options := qualityOptions{parallel: 4, fleet: true}
	var depth fleetDepthOptions
	command := &cobra.Command{
		Use:   "stats",
		Short: "Count local fleet inventory, Git attention, layout, and managed worktrees",
		Long: `Print counts for the local fleet: organizations, repositories, Git
attention, clone layout, and managed WB worktrees.

This is intentionally a rollup, not a worklist. Use wb fleet status for Git
attention detail, wb layout audit for placement findings, and
wb worktree orphans for linked-worktree debt outside the managed hierarchy.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := collectFleetStats(projectsRoot, filterFlag, options, depth)
			if err != nil {
				return err
			}
			if err := writeFleetStatsOutput(report, options.format, options.reportDir); err != nil {
				return err
			}
			if report.Git.Error > 0 {
				return &exitError{
					code:    exitFindings,
					message: "one or more repositories could not be inspected; see git.error in the report",
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.match, "match", "", "fleet glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.regex, "regex", "", "fleet regular expression matched against org/repo")
	command.Flags().IntVar(&options.parallel, "parallel", 4, "maximum repositories to inspect concurrently")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write fleet-stats.md and fleet-stats.yaml to this directory")
	bindFleetDepthFlags(command, &depth)
	return command
}

func bindFleetDepthFlags(command *cobra.Command, depth *fleetDepthOptions) {
	command.Flags().BoolVar(&depth.remote, "remote", false, "include sync-drift counts by reconciling with GitHub")
	command.Flags().BoolVar(&depth.hooks, "hooks", false, "include managed-hook finding counts across local clones")
}

func newFleetStatusCmd() *cobra.Command {
	options := qualityOptions{parallel: 4, fleet: true}
	var details bool
	var all bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Report local Git state for repositories that need attention",
		Long: `Report the fleet attention worklist: modified, untracked, conflicted,
stashed, or unpushed checkouts under --projects-root.

Clean repositories are counted rather than listed unless --all is set. This is
the fleet-shaped form of the historical wb status command.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRepositoryStatus(repositoryStatusRequest{
				path:      ".",
				fleet:     true,
				all:       all,
				details:   details,
				options:   options,
				filter:    filterFlag,
				projects:  projectsRoot,
				titleKind: statusTitleFleet,
			})
		},
	}
	bindRepositoryStatusFlags(command, &options, &details, &all, true)
	if flag := command.Flags().Lookup("report-dir"); flag != nil {
		flag.Usage = "write status.md and status.yaml to this directory"
	}
	return command
}

type fleetStatsReport struct {
	SchemaVersion int                 `yaml:"schema_version" json:"schema_version"`
	Inventory     fleetInventoryStats `yaml:"inventory" json:"inventory"`
	Git           fleetGitStats       `yaml:"git" json:"git"`
	Layout        fleetLayoutStats    `yaml:"layout" json:"layout"`
	Worktrees     fleetWorktreeStats  `yaml:"worktrees" json:"worktrees"`
	Remote        *fleetRemoteStats   `yaml:"remote,omitempty" json:"remote,omitempty"`
	Hooks         *fleetHooksStats    `yaml:"hooks,omitempty" json:"hooks,omitempty"`
}

type fleetInventoryStats struct {
	Organizations int `yaml:"organizations" json:"organizations"`
	Repositories  int `yaml:"repositories" json:"repositories"`
}

type fleetGitStats struct {
	Inspected int `yaml:"inspected" json:"inspected"`
	Clean     int `yaml:"clean" json:"clean"`
	Attention int `yaml:"attention" json:"attention"`
	Error     int `yaml:"error" json:"error"`
}

type fleetLayoutStats struct {
	OK         int `yaml:"ok" json:"ok"`
	TopLevel   int `yaml:"top_level" json:"top_level"`
	Misowned   int `yaml:"misowned" json:"misowned"`
	NoOrigin   int `yaml:"no_origin" json:"no_origin"`
	Unreadable int `yaml:"unreadable" json:"unreadable"`
}

type fleetRemoteStats struct {
	WouldClone   int `yaml:"would_clone" json:"would_clone"`
	WouldPull    int `yaml:"would_pull" json:"would_pull"`
	SkippedDirty int `yaml:"skipped_dirty" json:"skipped_dirty"`
	// Ignored counts repos marked with `wb repo ignore`. Distinct from
	// LocalOnly below, which counts repos wb found on disk but not on GitHub.
	Ignored    int `yaml:"ignored" json:"ignored"`
	LocalOnly  int `yaml:"local_only" json:"local_only"`
	RemoteOnly int `yaml:"remote_only" json:"remote_only"`
	NoOp       int `yaml:"noop" json:"noop"`
	Error      int `yaml:"error" json:"error"`
}

type fleetHooksStats struct {
	Repositories int `yaml:"repositories" json:"repositories"`
	Findings     int `yaml:"findings" json:"findings"`
	Errors       int `yaml:"errors" json:"errors"`
}

type fleetWorktreeStats struct {
	Tasks     int `yaml:"tasks" json:"tasks"`
	Checkouts int `yaml:"checkouts" json:"checkouts"`
	Dirty     int `yaml:"dirty" json:"dirty"`
	Locked    int `yaml:"locked" json:"locked"`
}

type fleetOverviewReport struct {
	SchemaVersion int              `yaml:"schema_version" json:"schema_version"`
	Stats         fleetStatsReport `yaml:"stats" json:"stats"`
	Status        statusIndex      `yaml:"status" json:"status"`
}

func collectFleetOverview(projects, filter string, options qualityOptions, all bool, depth fleetDepthOptions) (fleetOverviewReport, error) {
	stats, fullStatus, err := collectFleetStatsAndStatus(projects, filter, options, depth)
	if err != nil {
		return fleetOverviewReport{}, err
	}
	status := fullStatus
	if !all {
		status = hideCleanRepositories(fullStatus)
	}
	return fleetOverviewReport{
		SchemaVersion: 1,
		Stats:         stats,
		Status:        status,
	}, nil
}

func collectFleetStats(projects, filter string, options qualityOptions, depth fleetDepthOptions) (fleetStatsReport, error) {
	stats, _, err := collectFleetStatsAndStatus(projects, filter, options, depth)
	return stats, err
}

func collectFleetStatsAndStatus(projects, filter string, options qualityOptions, depth fleetDepthOptions) (fleetStatsReport, statusIndex, error) {
	options.fleet = true
	targets, err := qualityTargets(".", projects, filter, options)
	if err != nil {
		return fleetStatsReport{}, statusIndex{}, err
	}
	repositories := runStatusTargets(targets, options.parallel)
	fullStatus := statusIndex{SchemaVersion: 1, Repositories: repositories}
	inventory, err := fleetInventory(projects, filter, options)
	if err != nil {
		return fleetStatsReport{}, statusIndex{}, err
	}
	worktreeStats, err := fleetWorktreeRollup(projects, filter, options)
	if err != nil {
		return fleetStatsReport{}, statusIndex{}, err
	}
	layoutStats, err := fleetLayoutRollup(projects)
	if err != nil {
		return fleetStatsReport{}, statusIndex{}, err
	}
	stats := fleetStatsReport{
		SchemaVersion: 1,
		Inventory:     inventory,
		Git:           summarizeGitStats(repositories),
		Layout:        layoutStats,
		Worktrees:     worktreeStats,
	}
	if depth.remote {
		remoteStats, remoteErr := fleetRemoteRollup(projects, filter, options)
		if remoteErr != nil {
			return fleetStatsReport{}, statusIndex{}, remoteErr
		}
		stats.Remote = &remoteStats
	}
	if depth.hooks {
		hooksStats, hooksErr := fleetHooksRollup(targets, options.parallel)
		if hooksErr != nil {
			return fleetStatsReport{}, statusIndex{}, hooksErr
		}
		stats.Hooks = &hooksStats
	}
	return stats, fullStatus, nil
}

func fleetLayoutRollup(projects string) (fleetLayoutStats, error) {
	summary, err := layout.Counts(context.Background(), projects)
	if err != nil {
		return fleetLayoutStats{}, err
	}
	return fleetLayoutStats{
		OK: summary.OK, TopLevel: summary.TopLevel, Misowned: summary.Misowned,
		NoOrigin: summary.NoOrigin, Unreadable: summary.Unreadable,
	}, nil
}

func fleetRemoteRollup(projects, filter string, options qualityOptions) (fleetRemoteStats, error) {
	repositories, err := fleet(projects, filter, func() []string { return fleetOwners(nil) })
	if err != nil {
		return fleetRemoteStats{}, err
	}
	expression, err := compileFleetRegex(options.regex)
	if err != nil {
		return fleetRemoteStats{}, err
	}
	stats := fleetRemoteStats{}
	for _, repository := range repositories {
		if !matchesQualityTarget(repository.Slug(), filter, options.match, expression) {
			continue
		}
		if repository.Local && !repository.Remote {
			stats.LocalOnly++
		}
		if repository.Remote && !repository.Local && !repository.Archived && !repository.IsFork {
			stats.RemoteOnly++
		}
		result := fleetsync.Sync(repository, projects, true)
		switch result.Status {
		case fleetsync.Cloned:
			stats.WouldClone++
		case fleetsync.Pulled:
			stats.WouldPull++
		case fleetsync.SkippedDirty:
			stats.SkippedDirty++
		case fleetsync.SkippedIgnored:
			stats.Ignored++
		case fleetsync.NoOp:
			stats.NoOp++
		case fleetsync.Failed:
			stats.Error++
		}
	}
	return stats, nil
}

func fleetHooksRollup(targets []qualityTarget, parallel int) (fleetHooksStats, error) {
	stats := fleetHooksStats{Repositories: len(targets)}
	type result struct {
		findings int
		err      bool
	}
	results := make([]result, len(targets))
	runTargets(len(targets), parallel, func(index int) {
		report, err := hooks.Check(targets[index].path, "", hookExecutable(), projectsRoot)
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
	return stats, nil
}

func fleetInventory(projects, filter string, options qualityOptions) (fleetInventoryStats, error) {
	repositories, err := discover.ScanLocal(projects)
	if err != nil {
		return fleetInventoryStats{}, err
	}
	expression, err := compileFleetRegex(options.regex)
	if err != nil {
		return fleetInventoryStats{}, err
	}
	orgs := map[string]struct{}{}
	count := 0
	for _, repository := range repositories {
		if !matchesQualityTarget(repository.Slug(), filter, options.match, expression) {
			continue
		}
		orgs[repository.Org] = struct{}{}
		count++
	}
	return fleetInventoryStats{Organizations: len(orgs), Repositories: count}, nil
}

func fleetWorktreeRollup(projects, filter string, options qualityOptions) (fleetWorktreeStats, error) {
	results, err := worktrees.List(context.Background(), worktrees.ListOptions{
		ProjectsRoot: projects,
		Filter:       filter,
	})
	if err != nil {
		return fleetWorktreeStats{}, err
	}
	expression, err := compileFleetRegex(options.regex)
	if err != nil {
		return fleetWorktreeStats{}, err
	}
	tasks := map[string]struct{}{}
	stats := fleetWorktreeStats{}
	for _, result := range results {
		if !matchesQualityTarget(result.Repository, filter, options.match, expression) {
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

func summarizeGitStats(repositories []repositoryStatusInfo) fleetGitStats {
	stats := fleetGitStats{Inspected: len(repositories)}
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

func writeFleetStatsOutput(report fleetStatsReport, format, reportDir string) error {
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "fleet-stats.md"), []byte(fleetStatsMarkdown(report)), 0o644); err != nil {
			return err
		}
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "fleet-stats.yaml"), raw, 0o644); err != nil {
			return err
		}
	}
	switch format {
	case "markdown":
		_, err := fmt.Print(fleetStatsMarkdown(report))
		return err
	case "yaml":
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func writeFleetOverviewOutput(report fleetOverviewReport, format, reportDir string, details bool) error {
	if reportDir != "" {
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "fleet-overview.md"), []byte(fleetOverviewMarkdown(report, details)), 0o644); err != nil {
			return err
		}
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "fleet-overview.yaml"), raw, 0o644); err != nil {
			return err
		}
	}
	switch format {
	case "markdown":
		_, err := fmt.Print(fleetOverviewMarkdown(report, details))
		return err
	case "yaml":
		raw, err := yaml.Marshal(report)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func fleetStatsMarkdown(report fleetStatsReport) string {
	var out strings.Builder
	out.WriteString("# WB fleet stats\n\n")
	fmt.Fprintf(&out, "- inventory: %s\n", fleetInventorySummary(report.Inventory))
	fmt.Fprintf(&out, "- git: %s\n", fleetGitSummary(report.Git))
	fmt.Fprintf(&out, "- layout: %s\n", fleetLayoutSummary(report.Layout))
	fmt.Fprintf(&out, "- worktrees: %s\n", fleetWorktreeSummary(report.Worktrees))
	if report.Remote != nil {
		fmt.Fprintf(&out, "- remote: %s\n", fleetRemoteSummary(*report.Remote))
	}
	if report.Hooks != nil {
		fmt.Fprintf(&out, "- hooks: %s\n", fleetHooksSummary(*report.Hooks))
	}
	return out.String()
}

func fleetOverviewMarkdown(report fleetOverviewReport, details bool) string {
	var out strings.Builder
	out.WriteString("# WB fleet overview\n\n")
	out.WriteString("## Stats\n\n")
	fmt.Fprintf(&out, "- inventory: %s\n", fleetInventorySummary(report.Stats.Inventory))
	fmt.Fprintf(&out, "- git: %s\n", fleetGitSummary(report.Stats.Git))
	fmt.Fprintf(&out, "- layout: %s\n", fleetLayoutSummary(report.Stats.Layout))
	fmt.Fprintf(&out, "- worktrees: %s\n", fleetWorktreeSummary(report.Stats.Worktrees))
	if report.Stats.Remote != nil {
		fmt.Fprintf(&out, "- remote: %s\n", fleetRemoteSummary(*report.Stats.Remote))
	}
	if report.Stats.Hooks != nil {
		fmt.Fprintf(&out, "- hooks: %s\n", fleetHooksSummary(*report.Stats.Hooks))
	}
	out.WriteString("\n## Attention\n\n")
	attention := strings.TrimPrefix(statusMarkdown(report.Status, details, "# WB fleet status\n\n"), "# WB fleet status\n\n")
	out.WriteString(attention)
	return out.String()
}

func fleetInventorySummary(stats fleetInventoryStats) string {
	return fmt.Sprintf("%d organization%s · %d local repository%s",
		stats.Organizations, pluralSuffix(stats.Organizations),
		stats.Repositories, pluralSuffix(stats.Repositories))
}

func fleetGitSummary(stats fleetGitStats) string {
	return fmt.Sprintf("%d attention · %d clean · %d error (%d inspected)",
		stats.Attention, stats.Clean, stats.Error, stats.Inspected)
}

func fleetLayoutSummary(stats fleetLayoutStats) string {
	return fmt.Sprintf("%d ok · %d top-level · %d misowned · %d no-origin · %d unreadable",
		stats.OK, stats.TopLevel, stats.Misowned, stats.NoOrigin, stats.Unreadable)
}

func fleetRemoteSummary(stats fleetRemoteStats) string {
	return fmt.Sprintf("%d would-clone · %d would-pull · %d skipped-dirty · %d ignored · %d local-only · %d remote-only · %d noop · %d error",
		stats.WouldClone, stats.WouldPull, stats.SkippedDirty, stats.Ignored, stats.LocalOnly, stats.RemoteOnly, stats.NoOp, stats.Error)
}

func fleetHooksSummary(stats fleetHooksStats) string {
	return fmt.Sprintf("%d finding%s across %d repository%s (%d error%s)",
		stats.Findings, pluralSuffix(stats.Findings),
		stats.Repositories, pluralSuffix(stats.Repositories),
		stats.Errors, pluralSuffix(stats.Errors))
}

func fleetWorktreeSummary(stats fleetWorktreeStats) string {
	return fmt.Sprintf("%d task%s · %d checkout%s · %d dirty · %d locked",
		stats.Tasks, pluralSuffix(stats.Tasks),
		stats.Checkouts, pluralSuffix(stats.Checkouts),
		stats.Dirty, stats.Locked)
}

func pluralSuffix(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}
