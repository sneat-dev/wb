package cmdinstall

import (
	"context"
	"errors"
	"fmt"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/wbupdate"
	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/selfupdate"
	"github.com/strongo/cli-helpers/selfupdate/cobracmd"
)

func NewSelfUpdate(runtime shared.Runtime, cfg selfupdate.Config, after AfterUpdate) *cobra.Command {
	var command *cobra.Command
	command = cobracmd.New(cfg, cobracmd.CommandOptions{
		Short:       "Update the installed wb binary to the latest release",
		Aliases:     []string{"update"},
		JSONFormat:  true,
		Errors:      selfUpdateErrors{runtime: runtime},
		AfterUpdate: afterUpdate(&command, after),
	})
	return command
}

// AfterUpdate delegates verified post-update operations without exposing Cobra.
type AfterUpdate func(context.Context, selfupdate.AfterUpdate, wbupdate.Output) error

func afterUpdate(cmd **cobra.Command, after AfterUpdate) selfupdate.AfterUpdateFunc {
	return func(ctx context.Context, update selfupdate.AfterUpdate) error {
		target := *cmd
		if target == nil {
			target = &cobra.Command{}
		}
		format, _ := target.Flags().GetString("format")
		return after(ctx, update, wbupdate.Output{Out: target.OutOrStdout(), Err: target.ErrOrStderr(), JSON: format == "json"})
	}
}

// selfUpdateErrors maps the shared selfupdate library's outcomes onto wb's
// own three documented exit codes (see the const block in main.go). wb does
// not reserve a fourth code for "update available" the way some other
// consumers of this library do: that would grow wb's exit contract past the
// three codes it documents everywhere else, and `wb status`/`wb check`
// already treat "ran and found something to report" as exitFindings rather
// than a code of its own. An operational failure and a reported update
// therefore both land on exitFindings; `--format json` is what makes the two
// distinguishable for a caller that needs to tell them apart
// (REQ: exit-code-mapping).
type selfUpdateErrors struct{ runtime shared.Runtime }

// Failure maps any non-nil error from Config.Update or Config.Check onto
// exitFindings. A permission failure gets a wb-specific remedy naming both
// elevated permissions and wb's own Homebrew install command, since sudo is
// not always the right answer for a wb user (REQ: permission-remedy-names-brew).
func (e selfUpdateErrors) Failure(err error) error {
	var failure *selfupdate.Failure
	if errors.As(err, &failure) && failure.Kind == selfupdate.KindPermission {
		path := failure.Path
		if path == "" {
			path = "the wb executable"
		}
		return e.runtime.ExitError(shared.ExitFindings, fmt.Sprintf(
			"self-update: permission denied writing %s: %v; re-run with elevated permissions, or install the supported way: %s",
			path, failure.Err, wbupdate.HomebrewInstallCommand))
	}
	return e.runtime.ExitError(shared.ExitFindings, fmt.Sprintf("self-update: %v", err))
}

// UpdateAvailable maps a --check verdict that is not up to date onto
// exitFindings, consistent with `wb status` and `wb check`: an available
// update is a finding the same way stale Git state or a policy violation is,
// not a distinct exit code of its own (REQ: exit-code-mapping,
// REQ: check-json-format).
func (e selfUpdateErrors) UpdateAvailable(res selfupdate.CheckResult) error {
	if res.Verdict == selfupdate.Undetermined {
		return e.runtime.ExitError(shared.ExitFindings, fmt.Sprintf(
			"self-update: current wb version is undetermined (%s); latest stable is %s", res.Current, res.Latest))
	}
	return e.runtime.ExitError(shared.ExitFindings, fmt.Sprintf(
		"self-update: update available (%s -> %s)", res.Current, res.Latest))
}
