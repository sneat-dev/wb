// Package cmdwait owns the complete wait command registry and presentation.
package cmdwait

import (
	"context"
	"encoding/json"
	"fmt"
	cliprogress "github.com/sneat-dev/wb/internal/cli/progress"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/prsnapshot"
	"github.com/sneat-dev/wb/internal/waitrun"
	"github.com/spf13/cobra"
	"io"
	"strings"
	"time"
)

type Dependencies struct {
	Wait          func(context.Context, waitrun.Request) waitrun.Output
	RegisterWait  func(waitrun.Registration) func()
	Inspect       func(waitrun.ListRequest) (waitrun.ListResult, error)
	ParseSelector func(string) (string, string, error)
	Interactive   func(io.Writer, bool) bool
	Discovery     func(*cobra.Command, string)
}
type Children struct{ Checks, Agent, Operation func() *cobra.Command }

const (
	defaultWaitSlice     = 8 * time.Minute
	defaultWaitInterval  = 60 * time.Second
	waitHourlyReadBudget = 2000
)

func waitBudgetWarning(targets int, interval time.Duration) string {
	if targets <= 0 || interval <= 0 {
		return ""
	}
	perHour := float64(targets*prsnapshot.ReadsPerObservation) * float64(time.Hour) / float64(interval)
	if perHour <= waitHourlyReadBudget {
		return ""
	}
	return fmt.Sprintf("%d target(s) every %s projects about %.0f GitHub read requests an hour against a 5000-read budget for ordinary completed observations; early failures may read less, red details may read more, and conditional responses may be uncharged; widen --interval to spend less",
		targets, interval.Round(time.Second), perHour)
}

func New(runtime shared.Runtime, deps Dependencies, children Children) *cobra.Command {
	command := &cobra.Command{
		Use:   "wait",
		Short: "Wait for something WB can observe, then report what changed",
		Long: `Wait for a condition WB already knows how to observe.

Waiting belongs to WB rather than to an agent's context. Run one of these as a
background command; when it exits, the harness resumes the agent with the
result, so nothing has to remember "check this later".

Every wait is bounded and terminating. Pending is a first-class result: it
exits 1 carrying exact resume arguments for the targets that are still
pending, so the next invocation observes only what is left.

The argument after ` + "`wait`" + ` names a thing, not a domain: a pull request, an
exact commit's checks, an agent run, an operation.

Three of these are the verb-first spelling of commands WB already had. They run
the identical implementation, not a copy, so a receipt produced either way is
the same receipt:

  wb wait checks     is  wb ci wait
  wb wait agent      is  wb agent await
  wb wait operation  is  wb daemon operation wait

The older spellings keep working.

These commands report. They never merge, publish, or change a target. Use
` + "`wb pr land`" + ` to wait for checks and then land a pull request.`,
	}
	command.AddCommand(NewPR(runtime, deps))
	command.AddCommand(children.Checks())
	command.AddCommand(children.Agent())
	command.AddCommand(children.Operation())
	command.AddCommand(NewList(runtime, deps))
	return command
}

func NewPR(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var until string
	var slice, interval time.Duration
	var jsonOut bool
	command := &cobra.Command{
		Use:   "pr <owner/repository#number>...",
		Short: "Wait for one or more pull requests to reach a reportable state",
		Long: `Observe pull requests until every one of them reaches the requested
condition, or the bounded slice ends.

Targets are named the way an agent already holds them — owner/repository#number,
or a pull request URL. WB resolves each target's base branch and exact head
itself; no head SHA has to be discovered first.

Conditions:

  checks-settled  every check on the head has finished, whether it passed or
                  failed, or the pull request is no longer open (default)
  changed         the head commit, open/closed state, draft flag, or the set of
                  check outcomes differs from the first observation
  closed          the pull request is merged or closed

The wait ends when EVERY target satisfies the condition, not the first one. A
first-past-the-post wait would return as soon as the noisiest target moved and
starve the quiet target that actually needed attention.

A transient GitHub read keeps its target pending rather than failing the wait,
so a provider blip resumes instead of reporting a verdict WB did not observe.

This command only reports. It never merges. Landing a pull request when its
checks pass is ` + "`wb pr land <owner/repository#number>`" + `, which waits and
then lands in one call.`,
		Example: `# Wait for one pull request's checks to finish
wb wait pr sneat-dev/wb#581

# Watch several at once and report every one that moved
wb wait pr sneat-dev/wb#581 sneat-co/backstage#491 --until changed

# Run it as a background command and let the harness resume the agent
wb wait pr sneat-dev/wb#581 --slice 8m --json`,
		Args: func(command *cobra.Command, args []string) error {
			if err := cobra.MinimumNArgs(1)(command, args); err != nil {
				return err
			}
			if _, err := parseWaitCondition(until); err != nil {
				return err
			}
			if err := validateWaitBounds(slice, interval); err != nil {
				return err
			}
			_, err := parseWaitTargets(args, deps.ParseSelector)
			return err
		},
		RunE: func(command *cobra.Command, args []string) error {
			condition, err := parseWaitCondition(until)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			targets, err := parseWaitTargets(args, deps.ParseSelector)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			// A delegated wait that nobody can see is the same as no wait: the
			// session goes quiet and looks stopped. Record it before waiting,
			// and clear it however this call ends.
			flags := runtime.Flags()
			release := deps.RegisterWait(waitrun.Registration{ProjectsRoot: flags.ProjectsRoot, Kind: "pr", Targets: targets, Until: string(condition), Slice: slice})
			defer release()
			interactive := deps.Interactive(command.ErrOrStderr(), flags.NonInteractive)
			progress := cliprogress.NewLive(cliprogress.Output(command.ErrOrStderr(), interactive), true)
			progress.Start(fmt.Sprintf("wait pr: %d target(s) until %s", len(targets), condition))
			defer progress.Finish("wait pr: aborted")
			if warning := waitBudgetWarning(len(targets), interval); warning != "" {
				if _, err := fmt.Fprintln(command.ErrOrStderr(), "wait pr: "+warning); err != nil {
					return err
				}
			}
			output := deps.Wait(command.Context(), waitrun.Request{
				Targets:   targets,
				Condition: condition,
				Slice:     slice,
				Interval:  interval,
				Progress: func(update waitrun.Update) {
					progress.Update(fmt.Sprintf("wait pr: observation %d; %d/%d settled; next in %s", update.Observations, update.Settled, update.Total, update.Interval.Round(time.Second)))
				},
			})
			progress.Finish(fmt.Sprintf("wait pr: %s after %d observation(s)", output.Status, output.Observations))
			if output.Status == waitrun.Pending {
				output.ResumeArgs = waitResumeArgs(output, condition, slice, interval, jsonOut)
			}
			if jsonOut {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(output); err != nil {
					return err
				}
			} else if err := printWaitOutput(command.OutOrStdout(), output); err != nil {
				return err
			}
			if output.Status != waitrun.Settled {
				return runtime.ExitError(shared.ExitFindings, "wait pr "+output.Status)
			}
			return nil
		},
	}
	command.Flags().StringVar(&until, "until", string(waitrun.ChecksSettled), "condition to wait for: "+strings.Join(waitConditions(), ", "))
	command.Flags().DurationVar(&slice, "slice", defaultWaitSlice, "maximum observation slice; pending exits 1 with resume arguments")
	command.Flags().DurationVar(&interval, "interval", defaultWaitInterval, "interval between observations")
	shared.AddJSONFormatFlags(command, &jsonOut)
	deps.Discovery(command, "wait poll watch block pull request pr checks green settled changed closed merged review background resume pending harness")
	return command
}

func parseWaitCondition(value string) (waitrun.Condition, error) {
	switch condition := waitrun.Condition(strings.TrimSpace(value)); condition {
	case waitrun.ChecksSettled, waitrun.Changed, waitrun.Closed:
		return condition, nil
	default:
		return "", fmt.Errorf("unsupported --until %q; use %s", value, strings.Join(waitConditions(), ", "))
	}
}

func validateWaitBounds(slice, interval time.Duration) error {
	if slice <= 0 {
		return fmt.Errorf("--slice must be positive")
	}
	if interval <= 0 {
		return fmt.Errorf("--interval must be positive")
	}
	if interval > slice {
		return fmt.Errorf("--interval must not exceed --slice, or WB could never observe twice")
	}
	return nil
}

func parseWaitTargets(args []string, parseSelector func(string) (string, string, error)) ([]waitrun.Reference, error) {
	seen := map[string]bool{}
	targets := make([]waitrun.Reference, 0, len(args))
	for _, argument := range args {
		repository, number, err := parseSelector(argument)
		if err != nil {
			return nil, err
		}
		key := repository + "#" + number
		// A duplicate target is the failure this verb exists to remove: an
		// agent that asks twice should not pay twice.
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, waitrun.Reference{Selector: key, Repository: repository, Number: number})
	}
	return targets, nil
}

func waitConditions() []string {
	return []string{string(waitrun.ChecksSettled), string(waitrun.Changed), string(waitrun.Closed)}
}
