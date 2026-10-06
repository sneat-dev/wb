package cmddeps

import (
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/spf13/cobra"
)

func newPeers(runtime shared.Runtime, operations Dependencies) *cobra.Command {
	options := depsPeersOptions{}
	command := &cobra.Command{
		Use:   "peers <package>[@<version>]",
		Short: "Report a published npm package's peer requirements against one checkout",
		Long: `Answer "can I reuse this package here" with evidence instead of an install.

The question gets asked constantly and answered badly: run the install, read
whatever the package manager prints about peer conflicts, and hope the warning
names the real culprit. That mutates the checkout to find out, and a workspace's
peer warnings do not distinguish "you are two majors behind" from "the publisher
marked this peer optional".

So WB reads the published package's own peerDependencies (and
peerDependenciesMeta), reads what the target checkout actually resolves for each
of them — the version the governing pnpm-lock.yaml or package-lock.json
installs, not the caret range a manifest declares — and prints one row per peer:

    wb deps peers @sneat/core --against ../renewon

Nothing is installed and nothing is written. Each row's verdict is one of:

  satisfied         the target's version is admitted by the peer range
  unsatisfied       the target has it, at a version the range rejects
  missing           the target does not have it at all
  optional_missing  the publisher marked it optional; the target omits it
  unevaluated       WB will not guess this specifier shape, and says so

The last one is deliberate. WB evaluates the specifier subset a Sneat manifest
actually uses — exact pins, carets, tildes, comparison operators,
space-separated conjunctions such as ">=22.0.0 <23.0.0", and || unions of
those. A hyphen range or a workspace:/catalog: protocol is reported unevaluated
with its reason rather than silently counted as compatible.
An unevaluated row is therefore never a pass.

Exit code 1 when any required peer is unsatisfied or missing, 0 otherwise.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			options := options
			report, err := operations.Peers(commandExecutionContext(command), deps.PeerOptions{
				Package: args[0], Against: options.against,
				Timeout: options.timeout, Retry: options.retry,
			})
			if err != nil {
				return err
			}
			if err := writeDependencyReport(command, report, options.format); err != nil {
				return err
			}
			if deps.PeersFailed(report) {
				return runtime.ExitError(shared.ExitFindings, "one or more peer requirements are unsatisfied or missing in the target checkout; see the table above")
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.against, "against", "", "path of the checkout whose installed versions the peers are judged against")
	command.Flags().StringVar(&options.format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().DurationVar(&options.timeout, "timeout", 5*time.Minute, "maximum duration per registry command (0 disables)")
	command.Flags().IntVar(&options.retry, "retry", 0, "additional attempts for a failed registry command")
	return command
}
