package cmdagent

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

// NewReview builds `wb agent review`: the only writer of the acceptance axis.
func NewReview(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var status, note, evaluator string
	var jsonOut bool
	command := &cobra.Command{
		Use:   "review <agent-id>",
		Short: "Record an acceptance decision for a finished execution",
		Long: `Record whether a finished execution is accepted.

Execution status, validation status and review status are independent. A run
whose process exited zero can still be rejected here, and a passing test suite
never accepts work by itself: read the diff, then decide.

The decision is appended to a sidecar, so recording it can never race the worker
owner writing the run record. An execution that has not finished is refused.`,
		Example: `wb agent diff agt-0123456789abcdef0123456789abcdef --patch
wb agent review agt-0123456789abcdef0123456789abcdef --status accepted --note "tests and review pass"
wb agent review agt-0123456789abcdef0123456789abcdef --status needs_changes --note "missing error path"`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			record, err := deps.Review(command.Context(), agentrun.ReviewRequest{
				ProjectsRoot: runtime.Flags().ProjectsRoot, AgentID: args[0],
				Status: status, Evaluator: emptyWord(evaluator, invokingUser()), Note: note,
			})
			if err != nil {
				if agents.IsUnknownAgent(err) {
					return runtime.ExitError(shared.ExitFindings, err.Error())
				}
				return agentRequestError(runtime, err)
			}
			if jsonOut {
				return writeAgentJSON(command.OutOrStdout(), record)
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "%s: %s by %s\n", args[0], record.Status, record.Evaluator)
			return err
		},
	}
	deps.Discovery(command, "review accept reject needs changes verdict approval sign-off")
	command.Flags().StringVar(&status, "status", "", "accepted, rejected or needs_changes (required)")
	command.Flags().StringVar(&note, "note", "", "why this decision was made")
	command.Flags().StringVar(&evaluator, "evaluator", "", "who made the decision (default: the invoking user)")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

// NewPrune builds `wb agent prune`, the retention control.
func NewPrune(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var olderThan time.Duration
	var apply, jsonOut bool
	command := &cobra.Command{
		Use:   "prune",
		Short: "Plan or apply removal of old private execution artefacts",
		Long: `Remove private execution artefacts older than a bound.

Dry-run by default. A run that has not finished, whose task type requires
acceptance while review is still pending, or that was accepted is never removed:
the worktree is the artefact and WB does not decide that work is disposable.`,
		Example: `wb agent prune --older-than 720h
wb agent prune --older-than 720h --apply`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			plan, err := deps.Prune(command.Context(), agentrun.PruneRequest{
				ProjectsRoot: runtime.Flags().ProjectsRoot, OlderThan: olderThan, Apply: apply,
			})
			if err != nil {
				return agentRequestError(runtime, err)
			}
			if jsonOut {
				return writeAgentJSON(command.OutOrStdout(), plan)
			}
			return printPrune(command.OutOrStdout(), plan)
		},
	}
	deps.Discovery(command, "prune retention cleanup remove old runs artifacts disk")
	command.Flags().DurationVar(&olderThan, "older-than", 0, "remove artefacts for runs older than this duration (required)")
	command.Flags().BoolVar(&apply, "apply", false, "delete the planned artefacts; without it the plan is printed only")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

func printPrune(writer io.Writer, plan agentrun.PrunePlan) error {
	if _, err := fmt.Fprintf(writer, "older than: %s\n", (time.Duration(plan.OlderThanMS) * time.Millisecond).String()); err != nil {
		return err
	}
	for _, entry := range plan.Entries {
		if _, err := fmt.Fprintf(writer, "%-8s %s %s\n", entry.Action, entry.AgentID, entry.Reason); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(writer, "removed: %d\n", plan.Removed)
	return err
}

func writeText(writer io.Writer, text string) error {
	_, err := io.WriteString(writer, strings.TrimRight(text, "\n")+"\n")
	return err
}

func invokingUser() string {
	if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
		return user
	}
	return "operator"
}
