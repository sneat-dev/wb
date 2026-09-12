package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/spf13/cobra"
)

func newHooksLifecycleCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "lifecycle",
		Short: "Inspect and operate trusted repository-update executors",
	}
	command.AddCommand(
		newHooksLifecycleCheckCmd(),
		newHooksLifecycleStatusCmd(),
		newHooksLifecycleRetryCmd(),
		newHooksLifecycleBackfillCmd(),
		newHooksLifecycleRunPendingCmd(),
	)
	return command
}

func newHooksLifecycleCheckCmd() *cobra.Command {
	var configPath string
	var jsonOut bool
	command := &cobra.Command{
		Use:   "check",
		Short: "Validate lifecycle configuration and executable trust",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			dispatcher := lifecyclehooks.DefaultDispatcher()
			if configPath != "" {
				dispatcher.ConfigPath = configPath
			}
			report, err := dispatcher.Check()
			if err != nil {
				return err
			}
			if jsonOut {
				if err := writeLifecycleJSON(command.OutOrStdout(), report); err != nil {
					return err
				}
			} else {
				if !report.Configured {
					_, _ = fmt.Fprintf(command.OutOrStdout(), "Lifecycle hooks not configured (%s)\n", report.ConfigPath)
				} else {
					for _, executor := range report.Executors {
						_, _ = fmt.Fprintf(command.OutOrStdout(), "%-20s %s  %s\n", executor.Name, executor.Status, executor.Run)
						if executor.Finding != "" {
							_, _ = fmt.Fprintln(command.OutOrStdout(), "  "+executor.Finding)
						}
					}
				}
			}
			if len(report.Findings) != 0 {
				return fmt.Errorf("lifecycle hook check found %d problem(s)", len(report.Findings))
			}
			return nil
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "explicit standard WB config path")
	addJSONFormatFlags(command, &jsonOut)
	return command
}

func newHooksLifecycleStatusCmd() *cobra.Command {
	var limit int
	var jsonOut bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Show queued, running, and recent lifecycle-hook attempts",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			report, err := lifecyclehooks.DefaultDispatcher().Status(limit)
			if err != nil {
				return err
			}
			if jsonOut {
				return writeLifecycleJSON(command.OutOrStdout(), report)
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "Worker   %s\nPending  %d\nRunning  %d\nReceipts %d\n", report.Worker, len(report.Pending), len(report.Running), len(report.Receipts))
			for _, queued := range report.Pending {
				_, _ = fmt.Fprintf(command.OutOrStdout(), "  queued   %-18s %s@%s\n", queued.Executor, queued.Event.Repository, lifecycleShortSHA(queued.Event.NewSHA))
			}
			for _, queued := range report.Running {
				_, _ = fmt.Fprintf(command.OutOrStdout(), "  running  %-18s %s@%s\n", queued.Executor, queued.Event.Repository, lifecycleShortSHA(queued.Event.NewSHA))
			}
			for _, receipt := range report.Receipts {
				_, _ = fmt.Fprintf(command.OutOrStdout(), "%s  %-9s %-18s %s@%s\n", receipt.ID, receipt.Status, receipt.Executor, receipt.Repository, lifecycleShortSHA(receipt.NewSHA))
				if receipt.Status == "failed" {
					_, _ = fmt.Fprintf(command.OutOrStdout(), "  %s\n  stdout: %s\n  stderr: %s\n  retry: wb hooks lifecycle retry %s\n", receipt.Message, receipt.StdoutPath, receipt.StderrPath, receipt.ID)
				}
			}
			return nil
		},
	}
	command.Flags().IntVar(&limit, "limit", 20, "maximum recent receipts to show")
	addJSONFormatFlags(command, &jsonOut)
	return command
}

func newHooksLifecycleRetryCmd() *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "retry <receipt-id>",
		Short: "Requeue one failed lifecycle-hook attempt",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			report, err := lifecyclehooks.DefaultDispatcher().Retry(args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return writeLifecycleJSON(command.OutOrStdout(), report)
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "Queued retry %s\n", args[0])
			for _, warning := range report.Warnings {
				_, _ = fmt.Fprintln(command.ErrOrStderr(), "warning:", warning)
			}
			return nil
		},
	}
	addJSONFormatFlags(command, &jsonOut)
	return command
}

type lifecycleBackfillPlan struct {
	Apply      bool                     `json:"apply"`
	Scanned    int                      `json:"scanned"`
	Skipped    []string                 `json:"skipped,omitempty"`
	Executions []lifecyclehooks.Planned `json:"executions"`
	Enqueue    lifecyclehooks.Report    `json:"enqueue"`
}

func newHooksLifecycleBackfillCmd() *cobra.Command {
	var apply bool
	var jsonOut bool
	command := &cobra.Command{
		Use:   "backfill",
		Short: "Plan or enqueue lifecycle hooks for existing canonical repositories",
		Long: `Inspect existing canonical repositories without changing Git. The default is a
dry-run. With --apply, each matching repository's current HEAD is enqueued as
an explicit lifecycle-backfill event; executor arguments such as --init decide
whether the external tool initializes missing per-repository state.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			dispatcher := lifecyclehooks.DefaultDispatcher()
			plan, err := planLifecycleBackfill(command.Context(), projectsRoot, filterFlag, dispatcher, apply)
			if err != nil {
				return err
			}
			if jsonOut {
				return writeLifecycleJSON(command.OutOrStdout(), plan)
			}
			state := "planned"
			if apply {
				state = "enqueued"
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "Lifecycle backfill  %s %d execution(s) from %d repositories\n", state, len(plan.Executions), plan.Scanned)
			for _, execution := range plan.Executions {
				_, _ = fmt.Fprintf(command.OutOrStdout(), "  %-18s %s@%s\n", execution.Executor, execution.Event.Repository, lifecycleShortSHA(execution.Event.NewSHA))
			}
			for _, skipped := range plan.Skipped {
				_, _ = fmt.Fprintln(command.ErrOrStderr(), "warning:", skipped)
			}
			if !apply && len(plan.Executions) != 0 {
				_, _ = fmt.Fprintln(command.OutOrStdout(), "Re-run with --apply to enqueue this plan.")
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "enqueue the planned lifecycle-hook executions")
	addJSONFormatFlags(command, &jsonOut)
	return command
}

func planLifecycleBackfill(ctx context.Context, root, filter string, dispatcher lifecyclehooks.Dispatcher, apply bool) (lifecycleBackfillPlan, error) {
	repositories, err := discover.ScanLocal(root)
	if err != nil {
		return lifecycleBackfillPlan{}, err
	}
	plan := lifecycleBackfillPlan{Apply: apply, Scanned: len(repositories)}
	var events []lifecyclehooks.Event
	for _, repository := range repositories {
		if filter != "" && !strings.Contains(strings.ToLower(repository.Slug()), strings.ToLower(filter)) {
			continue
		}
		identity, err := lifecyclehooks.RepositoryIdentity(repository.Path)
		if err != nil {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("identify %s: %v", repository.Path, err))
			continue
		}
		head, err := gitops.HeadSHA(repository.Path)
		if err != nil {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("read HEAD for %s: %v", identity, err))
			continue
		}
		events = append(events, lifecyclehooks.Event{Name: lifecyclehooks.EventCheckoutUpdated, Repository: identity, Checkout: repository.Path, NewSHA: head, Cause: "lifecycle-backfill"})
	}
	plan.Executions, _, err = dispatcher.Plan(events)
	if err != nil {
		return plan, err
	}
	sort.Slice(plan.Executions, func(i, j int) bool {
		left, right := plan.Executions[i], plan.Executions[j]
		if left.Event.Repository == right.Event.Repository {
			return left.Executor < right.Executor
		}
		return left.Event.Repository < right.Event.Repository
	})
	if apply && len(plan.Executions) != 0 {
		plan.Enqueue, err = dispatcher.Dispatch(ctx, events)
	}
	return plan, err
}

func newHooksLifecycleRunPendingCmd() *cobra.Command {
	var configPath, stateDir, receiptPath string
	var parallel int
	command := &cobra.Command{
		Use:    "run-pending",
		Short:  "Run durable lifecycle-hook queue entries",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			dispatcher := lifecyclehooks.DefaultDispatcher()
			dispatcher.ConfigPath = configPath
			dispatcher.StateDir = stateDir
			dispatcher.ReceiptPath = receiptPath
			report, err := dispatcher.Drain(command.Context(), parallel)
			for _, warning := range report.Warnings {
				_, _ = fmt.Fprintln(command.ErrOrStderr(), "warning:", warning)
			}
			return err
		},
	}
	command.Flags().StringVar(&configPath, "config", "", "standard WB config path")
	command.Flags().StringVar(&stateDir, "state-dir", "", "private lifecycle-hook state directory")
	command.Flags().StringVar(&receiptPath, "receipt", "", "private lifecycle-hook receipt path")
	command.Flags().IntVar(&parallel, "parallel", 2, "maximum concurrent hook executors")
	_ = command.MarkFlagRequired("config")
	_ = command.MarkFlagRequired("state-dir")
	_ = command.MarkFlagRequired("receipt")
	return command
}

func writeLifecycleJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func lifecycleShortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
