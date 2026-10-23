package cmdworktree

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"time"
)

// NewList constructs the worktree inventory list command.
func NewList(runtime shared.Runtime, list func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error)) *cobra.Command {
	var base, format, absorbedBy, ownerState string
	var github, finalized, notFinalized bool
	var parallel int
	var ttl time.Duration
	command := &cobra.Command{
		Use:   "list [task]",
		Short: "List live WB-managed task worktrees and their lifecycle state",
		Long: `List live WB-managed linked checkout inventory across every
resolver-recognized layout: repository-local <canonical>/.worktrees, the
currently configured shared root, and historic WB-home or projects-root
layouts corroborated by Git's worktree registry and active claims. Changing
worktrees.root never hides or moves an existing checkout; use
'wb worktree relocate' for an explicit physical move.

The default report uses only local Git data. Pass --github to include open and
exact fetched origin-target and pull request evidence used by worktree cleanup.
JSON output is a versioned control-plane envelope containing results,
diagnostics, and WB lifecycle artifacts so automation cannot miss cleanup
backlog that is not represented by a live Git worktree. This replaces the
legacy bare JSON result array; consumers must migrate to the envelope and
check schema_version.
Archived Work Logs and the approved seven-day recent-history view are not yet
joined into this command.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			if finalized && notFinalized {
				return runtime.ExitError(shared.ExitUsage, "--finalized and --not-finalized cannot be combined")
			}
			var finalizedFilter *bool
			switch {
			case finalized:
				value := true
				finalizedFilter = &value
			case notFinalized:
				value := false
				finalizedFilter = &value
			}
			task := ""
			if len(args) == 1 {
				task = args[0]
			}
			flags := runtime.Flags()
			outcome, err := list(command.Context(), worktrees.ListOptions{
				ProjectsRoot: flags.ProjectsRoot,
				Task:         task,
				Base:         base,
				Filter:       flags.Filter,
				OwnerState:   ownerState,
				Finalized:    finalizedFilter,
				AbsorbedBy:   absorbedBy,
				GitHub:       github,
				Workers:      parallel,
				// A detached checkout is what every pull-request review
				// creates. Warning about it and then dropping it from the
				// inventory is why nothing could ever retire one: the measured
				// sweep showed 50 rows for 60 checkouts.
				IncludeDetached: true,
				TTL:             ttl,
			})
			if err != nil {
				return err
			}
			if err := writeInventoryDiagnostics(command.ErrOrStderr(), outcome); err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(outcome)
			}
			return writeInventoryList(command.OutOrStdout(), outcome.Results)
		},
	}
	command.Flags().StringVar(&base, "base", "main", "base branch used to assess local merge state")
	command.Flags().IntVar(&parallel, "parallel", worktrees.DefaultInspectWorkers, "maximum repositories to inspect concurrently")
	command.Flags().BoolVar(&github, "github", false, "include pull request state from GitHub")
	command.Flags().StringVar(&absorbedBy, "absorbed-by", "", "with --github, verify work landed inside this merged pull request number or exact landing commit")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().StringVar(&ownerState, "only", "", "only worktrees with owner PID state: active or orphaned")
	command.Flags().DurationVar(&ttl, "ttl", 7*24*time.Hour, "report a worktree older than this as expired")
	command.Flags().BoolVar(&finalized, "finalized", false, "only worktrees whose claim was sealed by wb worktree log finalize")
	command.Flags().BoolVar(&notFinalized, "not-finalized", false, "only worktrees not yet finalized by wb worktree log finalize")
	return command
}

// NewSummary constructs the worktree inventory summary command.
func NewSummary(runtime shared.Runtime, list func(context.Context, worktrees.ListOptions) (worktrees.ListOutcome, error)) *cobra.Command {
	var base, format string
	var github bool
	command := &cobra.Command{
		Use:   "summary <task>",
		Short: "Brief overview of every worktree, branch, and optional PR for one task",
		Long: `Summarize one WB task/effort across all of its live linked worktrees.

Requires the task name. Reports each repository's worktree path, branch, short
head, clean/dirty/locked state, and whether the head is integrated at the
exact origin target. Pass --github to attach open or merged pull-request
evidence. Unlike 'wb worktree list', this command always scopes to one named
task and formats a brief human overview rather than a flat inventory row.

JSON reuses the same versioned list envelope (results, diagnostics, artifacts).
Prompt bodies are never included; use 'wb worktree info' or 'wb worktree log'
for per-checkout journal detail.`,
		Example: `wb worktree summary improve-login
wb worktree summary improve-login --github --format json`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			flags := runtime.Flags()
			outcome, err := list(command.Context(), worktrees.ListOptions{
				ProjectsRoot: flags.ProjectsRoot,
				Task:         args[0],
				Base:         base,
				Filter:       flags.Filter,
				GitHub:       github,
			})
			if err != nil {
				return err
			}
			if err := writeInventoryDiagnostics(command.ErrOrStderr(), outcome); err != nil {
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(outcome)
			}
			return writeInventorySummary(command.OutOrStdout(), args[0], outcome.Results, github)
		},
	}
	command.Annotations = map[string]string{"wb.dev/discovery-terms": "inspect progress status next action task work worktree branch pull request ready merge"}
	command.Flags().StringVar(&base, "base", "main", "base branch used to assess local merge state")
	command.Flags().BoolVar(&github, "github", false, "include pull request state from GitHub")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	return command
}
func writeInventoryDiagnostics(out io.Writer, outcome worktrees.ListOutcome) error {
	for _, diagnostic := range outcome.Diagnostics {
		if _, err := fmt.Fprintf(out, "warning: task %s candidate %s: %s\n", diagnostic.Task, diagnostic.Path, diagnostic.Message); err != nil {
			return err
		}
	}
	for _, artifact := range outcome.Artifacts {
		if _, err := fmt.Fprintf(out, "info: inventory classified WB internal %s as %s: %s\n", artifact.Kind, artifact.State, artifact.Path); err != nil {
			return err
		}
	}
	return nil
}
