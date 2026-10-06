package cmdhooks

import (
	"encoding/json"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/spf13/cobra"
)

func (family commands) newHooksMetricsCmd() *cobra.Command {
	var (
		configPath  string
		metricsFile string
		repository  string
		days        int
		jsonOut     bool
	)
	cmd := &cobra.Command{
		Use:   "metrics [repository-path]",
		Short: "Chart local commits, push attempts, hook failures, and duration",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			policy, err := family.git.LoadPolicy(shared.ArgumentOrCurrent(args), configPath)
			if err != nil {
				return err
			}
			if metricsFile == "" {
				metricsFile = policy.Metrics.Path
			}
			events, err := family.git.ReadEvents(metricsFile)
			if err != nil {
				return err
			}
			summary := hooks.Summarize(events, days, repository, family.git.Now())
			if jsonOut {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(summary)
			}
			return printHookMetrics(cmd.OutOrStdout(), summary, metricsFile)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "explicit hooks policy")
	cmd.Flags().StringVar(&metricsFile, "file", "", "hook events JSONL file")
	cmd.Flags().StringVar(&repository, "repo", "", "only repositories containing this text")
	cmd.Flags().IntVar(&days, "days", 14, "number of calendar days to chart")
	shared.AddJSONFormatFlags(cmd, &jsonOut)
	return cmd
}

func (family commands) newHooksMeasureCmd() *cobra.Command {
	var (
		configPath  string
		metricsFile string
		repository  string
		days        int
		jsonOut     bool
	)
	cmd := &cobra.Command{
		Use:   "measure [repository-path]",
		Short: "Price the hook profiles and show what deferring to CI on a stream branch saved",
		Long: `Report what each WB-managed hook profile actually costs, from the recorded
events rather than from an estimate.

  commit       formatting and static checks over the files changed in that
               commit; it never runs a test suite
  stream push  a push to a stream/<name> branch, which runs NO local
               verification because CI on the stream pull request is the gate
  other push   every other push, which runs the current full profile unchanged

Each profile carries its measured budget: the slowest run actually observed, not
a guess. The stream saving is the number of stream-branch pushes priced at the
measured average cost of a non-stream push, and the basis is printed beside it
so the arithmetic can be checked.

Anything this window could not price is listed under "not measured", so a zero
saving is never readable as "the stream profile saved nothing".

Use 'wb hooks metrics' for the day-by-day chart; both views are built from the
same recording.`,
		Example: `wb hooks measure .
wb hooks measure . --days 30 --json`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			policy, err := family.git.LoadPolicy(shared.ArgumentOrCurrent(args), configPath)
			if err != nil {
				return err
			}
			if metricsFile == "" {
				metricsFile = policy.Metrics.Path
			}
			events, err := family.git.ReadEvents(metricsFile)
			if err != nil {
				return err
			}
			delta := hooks.Measure(events, days, repository, family.git.Now())
			if jsonOut {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(delta)
			}
			return printHookProfileDelta(cmd.OutOrStdout(), delta, metricsFile)
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "explicit hooks policy")
	cmd.Flags().StringVar(&metricsFile, "file", "", "hook events JSONL file")
	cmd.Flags().StringVar(&repository, "repo", "", "only repositories containing this text")
	cmd.Flags().IntVar(&days, "days", 14, "number of calendar days to price")
	shared.AddJSONFormatFlags(cmd, &jsonOut)
	cmd.Annotations = map[string]string{"wb.dev/discovery-terms": "hooks measure profile delta cost budget stream branch saving commit push duration"}
	return cmd
}
