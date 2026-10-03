package cmdfleet

import (
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/cli/statusview"
	"github.com/sneat-dev/wb/internal/fleetinspect"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/spf13/cobra"
)

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	overview := newOverviewOptions(runtime, deps)
	command := &cobra.Command{
		Use:   "fleet",
		Short: "Inspect local fleet inventory, attention, layout, and worktree debt",
		Long: `Inspect the local repository fleet without mutating checkouts.

  wb fleet          overview (stats + attention worklist)
  wb fleet overview same as wb fleet
  wb fleet stats    inventory and attention counts only
  wb fleet status   attention worklist (same shape as historical wb status)
  wb fleet merge-policy audit/apply GitHub merge settings and ruleset conflicts
  wb fleet default-branch audit/apply configured GitHub default branch changes
  wb fleet coverage       inspect CI test coverage summaries across fleet or targets

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
	command.AddCommand(NewStats(runtime, deps))
	command.AddCommand(statusview.NewFleet(runtime, deps.Status))
	command.AddCommand(NewPRs(runtime, deps))
	command.AddCommand(NewMergePolicy(runtime, deps))
	command.AddCommand(NewDefaultBranch(runtime, deps))
	return command
}

type fleetDepthOptions struct {
	remote bool
	hooks  bool
}

type fleetOverviewOptions struct {
	runtime shared.Runtime
	deps    Dependencies
	status  statusview.Options
	all     bool
	details bool
	depth   fleetDepthOptions
}

func newOverviewOptions(runtime shared.Runtime, deps Dependencies) *fleetOverviewOptions {
	return &fleetOverviewOptions{runtime: runtime, deps: deps, status: statusview.Options{Parallel: 4}}
}

func (options *fleetOverviewOptions) bind(command *cobra.Command) {
	statusview.BindFlags(command, &options.status, &options.details, &options.all, true)
	bindFleetDepthFlags(command, &options.depth)
	if flag := command.Flags().Lookup("report-dir"); flag != nil {
		flag.Usage = "write fleet-overview.md and fleet-overview.yaml to this directory"
	}
}

func (options *fleetOverviewOptions) run(cmd *cobra.Command, args []string) error {
	report, err := options.deps.Overview(cmd.Context(), request(options.runtime, options.status, options.depth, options.all))
	if err != nil {
		return err
	}
	if err := writeFleetOverviewOutput(cmd.OutOrStdout(), options.deps, report, options.status.Format, options.status.ReportDir, options.details); err != nil {
		return err
	}
	if repostatus.Failed(report.Status) || report.Stats.Git.Error > 0 {
		return options.runtime.ExitError(shared.ExitFindings, "one or more repositories could not be inspected; see the report above")
	}
	return nil
}

func NewStats(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	options := statusview.Options{Parallel: 4}
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
			report, err := deps.Stats(cmd.Context(), request(runtime, options, depth, false))
			if err != nil {
				return err
			}
			if err := writeFleetStatsOutput(cmd.OutOrStdout(), deps, report, options.Format, options.ReportDir); err != nil {
				return err
			}
			if report.Git.Error > 0 {
				return runtime.ExitError(shared.ExitFindings, "one or more repositories could not be inspected; see git.error in the report")
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.Match, "match", "", "fleet glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&options.Regex, "regex", "", "fleet regular expression matched against org/repo")
	command.Flags().IntVar(&options.Parallel, "parallel", 4, "maximum repositories to inspect concurrently")
	command.Flags().StringVar(&options.Format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&options.ReportDir, "report-dir", "", "write fleet-stats.md and fleet-stats.yaml to this directory")
	bindFleetDepthFlags(command, &depth)
	return command
}

func bindFleetDepthFlags(command *cobra.Command, depth *fleetDepthOptions) {
	command.Flags().BoolVar(&depth.remote, "remote", false, "include sync-drift counts by reconciling with GitHub")
	command.Flags().BoolVar(&depth.hooks, "hooks", false, "include managed-hook finding counts across local clones")
}

func request(runtime shared.Runtime, options statusview.Options, depth fleetDepthOptions, all bool) fleetinspect.Request {
	flags := runtime.Flags()
	return fleetinspect.Request{ProjectsRoot: flags.ProjectsRoot, Filter: flags.Filter, Match: options.Match, Regex: options.Regex, Parallel: options.Parallel, Remote: depth.remote, Hooks: depth.hooks, All: all}
}
