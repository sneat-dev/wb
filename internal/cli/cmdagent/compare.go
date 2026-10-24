package cmdagent

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

// parseProfileList splits the comma-separated --profiles value, refusing a
// duplicate so an experiment cannot silently weight one profile twice.
func parseProfileList(runtime shared.Runtime, raw string) ([]string, error) {
	var profiles []string
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		profile := strings.TrimSpace(part)
		if profile == "" {
			continue
		}
		if seen[profile] {
			return nil, runtime.ExitError(shared.ExitUsage, fmt.Sprintf("--profiles names %q twice", profile))
		}
		seen[profile] = true
		profiles = append(profiles, profile)
	}
	if len(profiles) == 0 {
		return nil, runtime.ExitError(shared.ExitUsage, "--profiles requires at least one profile name")
	}
	return profiles, nil
}

// NewCompare builds `wb agent compare`.
func NewCompare(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var profiles, taskType, complexity, effort, task, taskFile, repository, base string
	var repeat, concurrency int
	var timeout time.Duration
	var jsonOut bool
	command := &cobra.Command{
		Use:   "compare",
		Short: "Run one task across several profiles from one pinned base commit",
		Long: `Compare execution profiles on identical input.

Every attempt gets its own worktree and the experiment pins one base commit:
WB creates all the worktrees before starting any worker, records the first
created base SHA, and never starts an attempt whose base differs (it is reported
as base_mismatch instead of being compared unfairly).

Attempts run concurrently up to --concurrency and the command waits for all of
them. A failed attempt never loses another attempt's result. Fallback is
disabled inside a comparison: swapping a profile would invalidate it.

The comparison reports execution status, validation status, review status,
wall time, tokens, cost and the worktree for every attempt. ` + "`wb agent experiment <id>`" + `
re-reads the same durable document later.`,
		Example: `wb agent compare --profiles opencode-flash,dsh-flash --repeat 2 --task-file task.md
wb agent compare --profiles cheap-codex,dsh-flash --task "fix the failing test" --format json`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			text, err := readAgentTask(runtime, deps, command, task, taskFile)
			if err != nil {
				return err
			}
			names, err := parseProfileList(runtime, profiles)
			if err != nil {
				return err
			}
			report, err := deps.Compare(command.Context(), agentrun.CompareRequest{
				ProjectsRoot: runtime.Flags().ProjectsRoot, Stderr: command.ErrOrStderr(),
				Task: text, TaskType: taskType, Complexity: complexity, Effort: effort,
				Profiles: names, Repeat: repeat, Concurrency: concurrency,
				Repository: repository, Base: base,
				Timeout: timeout, TimeoutSet: command.Flags().Changed("timeout"),
			})
			if err != nil {
				return agentRequestError(runtime, err)
			}
			if jsonOut {
				return writeAgentJSON(command.OutOrStdout(), report)
			}
			return printExperiment(command.OutOrStdout(), report)
		},
	}
	deps.Discovery(command, "compare experiment benchmark profiles models parallel repeat variance harness opencode dsh codex")
	command.Flags().StringVar(&profiles, "profiles", "", "comma-separated profile names to compare (required)")
	command.Flags().IntVar(&repeat, "repeat", 1, "run each profile this many times")
	command.Flags().IntVar(&concurrency, "concurrency", 3, "maximum attempts running at once")
	command.Flags().StringVar(&taskType, "task-type", "", "task type whose policy supplies validation and defaults")
	command.Flags().StringVar(&complexity, "task-complexity", "", "work difficulty recorded on the experiment")
	command.Flags().StringVar(&effort, "effort", "", "reasoning effort applied to every profile that supports it")
	command.Flags().StringVar(&task, "task", "", "the bounded task, inline")
	command.Flags().StringVar(&taskFile, "task-file", "", "read the bounded task from this file, or - for stdin")
	command.Flags().StringVar(&repository, "repo", "", "owner/repository to operate on")
	command.Flags().StringVar(&base, "base", "", "base branch every worktree starts from")
	command.Flags().DurationVar(&timeout, "timeout", 0, "bound on each attempt (default: each profile's own bound)")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

// NewExperiment builds `wb agent experiment <id>`.
func NewExperiment(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "experiment <experiment-id>",
		Short: "Re-read one durable comparison from disk",
		Long: `Re-read an experiment and join it with its member executions.

This is how an orchestrator inspects a comparison after a restart, or one that
was interrupted: the experiment document is immutable and every planned attempt
keeps its own run record.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			report, err := deps.Experiment(command.Context(), agentrun.ExperimentRequest{
				ProjectsRoot: runtime.Flags().ProjectsRoot, ExperimentID: args[0],
			})
			if err != nil {
				if agents.IsUnknownExperiment(err) {
					return runtime.ExitError(shared.ExitFindings, err.Error())
				}
				return err
			}
			if jsonOut {
				return writeAgentJSON(command.OutOrStdout(), report)
			}
			return printExperiment(command.OutOrStdout(), report)
		},
	}
	deps.Discovery(command, "experiment comparison results inspect resume read")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

func printExperiment(writer io.Writer, report agents.ExperimentReport) error {
	if _, err := fmt.Fprintf(writer, "experiment %s\nbase: %s\nprofiles: %s x%d\n\n",
		report.ExperimentID, emptyWord(report.BaseSHA, "(no attempt started)"),
		strings.Join(report.Profiles, ", "), report.Repeat); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "%-18s %-4s %-11s %-10s %-12s %-10s %-10s %-8s %s\n",
		"PROFILE", "RUN", "STATUS", "VALIDATION", "REVIEW", "DURATION", "TOKENS", "FILES", "WORKTREE"); err != nil {
		return err
	}
	for _, run := range report.Runs {
		files := 0
		if run.Changes != nil {
			files = run.Changes.FilesChanged
		}
		if _, err := fmt.Fprintf(writer, "%-18s %-4d %-11s %-10s %-12s %-10s %-10s %-8d %s\n",
			run.RequestedProfile, run.RepeatIndex+1, run.Status,
			run.Validation.Status, run.Review.Status,
			(time.Duration(run.DurationMS) * time.Millisecond).Round(time.Millisecond).String(),
			tokenWord(run), files, run.Worktree); err != nil {
			return err
		}
	}
	if len(report.Summaries) > 0 {
		if _, err := fmt.Fprintln(writer, "\nsummary"); err != nil {
			return err
		}
		for _, summary := range report.Summaries {
			if _, err := fmt.Fprintf(writer, "  %-18s attempts=%d completed=%d failed=%d validated=%d accepted=%d mean=%s\n",
				summary.Profile, summary.Attempts, summary.Completed, summary.Failed,
				summary.Validated, summary.Accepted, meanWord(summary.MeanDurationMS)); err != nil {
				return err
			}
		}
	}
	for _, difference := range report.UncontrolledDifferences {
		if _, err := fmt.Fprintln(writer, "note: "+difference); err != nil {
			return err
		}
	}
	return nil
}

func tokenWord(run agents.Result) string {
	if !run.Telemetry.Available || run.Telemetry.Tokens == nil {
		return "n/a"
	}
	total := 0
	if run.Telemetry.Tokens.Input != nil {
		total += *run.Telemetry.Tokens.Input
	}
	if run.Telemetry.Tokens.Output != nil {
		total += *run.Telemetry.Tokens.Output
	}
	return fmt.Sprintf("%d", total)
}

func meanWord(value *int64) string {
	if value == nil {
		return "n/a"
	}
	return (time.Duration(*value) * time.Millisecond).Round(time.Millisecond).String()
}
