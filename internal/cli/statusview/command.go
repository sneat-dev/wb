// Package statusview presents reusable local repository status commands.
package statusview

import (
	"io"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/spf13/cobra"
)

// Dependencies provides the concrete operations and terminal policy for one command instance.
type Dependencies struct {
	Collect      func(reposelection.Request, repostatus.Observer) (repostatus.Index, error)
	WriteReports func(repostatus.Index, string, bool, string) error
	Interactive  func(io.Writer, bool) bool
}

type request struct {
	runtime             shared.Runtime
	deps                Dependencies
	path                string
	fleet, all, details bool
	options             Options
	filter, projects    string
	titleKind           statusTitleKind
	progress, output    io.Writer
}

func runRequest(request request) error {
	var progress *statusProgress
	report, err := request.deps.Collect(reposelection.Request{
		Path: request.path, ProjectsRoot: request.projects, Filter: request.filter,
		Fleet: request.fleet, Match: request.options.Match, Regex: request.options.Regex,
		Parallel: request.options.Parallel,
	}, repostatus.Observer{
		Start: func(total int) {
			progress = newStatusProgress(request.progress, request.deps.Interactive(request.progress, request.runtime.Flags().NonInteractive))
			progress.start(total)
		},
		Complete: func(target reposelection.Target, row repostatus.Row) { progress.complete(target, row) },
	})
	progress.finish()
	if err != nil {
		return err
	}
	if request.fleet && !request.all {
		report = repostatus.HideClean(report)
	}
	title := statusMarkdownTitle(request.titleKind, request.fleet)
	if request.options.ReportDir != "" {
		if err := request.deps.WriteReports(report, request.options.ReportDir, request.details, title); err != nil {
			return err
		}
	}
	if err := writeOutput(request.output, report, request.options.Format, request.details, title); err != nil {
		return err
	}
	if repostatus.Failed(report) {
		return request.runtime.ExitError(shared.ExitFindings, "one or more repositories could not be inspected; see the `error` field of each `error` row above")
	}
	return nil
}

// NewHistorical keeps the historical entry point. Prefer the explicit nouns:
// wb fleet status / wb fleet stats / wb fleet, and wb repo status.
func NewHistorical(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	options := Options{Parallel: 4}
	var details bool
	var all bool
	command := &cobra.Command{
		Use:   "status [repository-path]",
		Short: "Report local Git state (fleet worklist, or one repository when a path is given)",
		Long: `Report local Git attention for the fleet or one repository.

Prefer the explicit commands:
  wb fleet status   fleet attention worklist
  wb fleet stats    inventory and attention counts
  wb fleet          overview (stats + attention)
  wb repo status    one repository

Without a path this command matches wb fleet status. With a path it matches
wb repo status. It reads local Git state only and never fetches or contacts
GitHub. Fleet scans show a live completion counter on stderr when attached to
a terminal; --non-interactive disables it.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			fleet := true
			if len(args) == 1 {
				path = args[0]
				fleet = false
			}
			return runRequest(request{
				runtime:   runtime,
				path:      path,
				fleet:     fleet,
				all:       all,
				details:   details,
				options:   options,
				filter:    runtime.Flags().Filter,
				projects:  runtime.Flags().ProjectsRoot,
				titleKind: statusTitleAuto,
				progress:  cmd.ErrOrStderr(),
				output:    cmd.OutOrStdout(),
				deps:      deps,
			})
		},
	}
	BindFlags(command, &options, &details, &all, true)
	return command
}

func NewRepository(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	options := Options{Parallel: 4}
	var details bool
	command := &cobra.Command{
		Use:   "status [repository-path]",
		Short: "Report local Git state for one repository",
		Long: `Report local Git state for one repository checkout.

Defaults to the current directory when no path is given. Unlike wb fleet
status, this always answers for the named checkout — clean or not — and does
not scan the projects-root fleet.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			return runRequest(request{
				runtime:   runtime,
				path:      path,
				fleet:     false,
				all:       true,
				details:   details,
				options:   options,
				filter:    "",
				projects:  runtime.Flags().ProjectsRoot,
				titleKind: statusTitleRepo,
				progress:  cmd.ErrOrStderr(),
				output:    cmd.OutOrStdout(),
				deps:      deps,
			})
		},
	}
	BindFlags(command, &options, &details, nil, false)
	return command
}
func NewFleet(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	options := Options{Parallel: 4}
	var details bool
	var all bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Report local Git state for repositories that need attention",
		Long: `Report the fleet attention worklist: modified, untracked, conflicted,
stashed, or unpushed checkouts under --projects-root.

Clean repositories are counted rather than listed unless --all is set. This is
the fleet-shaped form of the historical wb status command. A live completion
counter is shown on stderr when attached to a terminal; --non-interactive
disables it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRequest(request{
				runtime:   runtime,
				path:      ".",
				fleet:     true,
				all:       all,
				details:   details,
				options:   options,
				filter:    runtime.Flags().Filter,
				projects:  runtime.Flags().ProjectsRoot,
				titleKind: statusTitleFleet,
				progress:  cmd.ErrOrStderr(),
				output:    cmd.OutOrStdout(),
				deps:      deps,
			})
		},
	}
	BindFlags(command, &options, &details, &all, true)
	if flag := command.Flags().Lookup("report-dir"); flag != nil {
		flag.Usage = "write status.md and status.yaml to this directory"
	}
	return command
}

type statusTitleKind int

const (
	statusTitleAuto statusTitleKind = iota
	statusTitleFleet
	statusTitleRepo
)

func statusMarkdownTitle(kind statusTitleKind, fleet bool) string {
	switch kind {
	case statusTitleFleet:
		return "# WB fleet status\n\n"
	case statusTitleRepo:
		return "# WB repository status\n\n"
	default:
		if fleet {
			return "# WB local repository status\n\n"
		}
		return "# WB repository status\n\n"
	}
}
