package cmdinstall

import (
	"fmt"
	"strings"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/wbupdate"

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
)

// wb binds the shared github.com/strongo/cli-helpers/cliinstall upgrade
// adapter the same way install.go and selfupdate.go already bind that
// library's install and self-update adapters: release resolution, per-
// target policy (managed redirect/executable, manual replace, ahead-of-
// latest, ambiguous refusal), batch confirmation, dry run, and reporting all
// come from that library. This file supplies only what is genuinely wb's
// own — the catalog id, the SAME self-update Config and after-update hook
// `wb self-update` configures, and the mapping from the library's typed
// outcomes onto wb's own three-code exit contract. See
// spec/features/install/README.md's Upgrading section for the Feature spec
// that draws this boundary.

// NewUpgrade binds the upstream upgrade adapter to WB catalog, error, and post-update policy.
func NewUpgrade(runtime shared.Runtime, cfg selfupdate.Config, after AfterUpdate) *cobra.Command {
	var command *cobra.Command
	command = cobracmd.NewUpgrade(cobracmd.UpgradeCommandOptions{
		Short:           "Upgrade installed fleet CLIs, including wb itself",
		HostID:          wbupdate.CatalogID,
		Errors:          newUpgradeErrors(runtime),
		HostConfig:      cfg,
		HostAfterUpdate: afterUpdate(&command, after),
	})
	return command
}

// upgradeErrors extends fleetErrors with the upgrades-available method
// cli-install#req:upgrade-check requires, reusing the identical Failure
// mapping install.go configures — same exit-code classification for every
// kind — but with its own "upgrade: " prefix and its own permission remedy
// (review S1), rather than duplicating fleetErrors' switch statement a
// third time (cli-install#req:host-owned-exit-codes: "The upgrade command
// MUST use the same error mapper" — mirroring cli-helpers' own README
// example, `type datatugUpgradeErrors struct{ datatugInstallErrors }`).
type upgradeErrors struct{ fleetErrors }

// newUpgradeErrors returns upgrade's own fleetErrors value: "upgrade: "
// messages, and the Homebrew cask-UPGRADE command (not install's) as its
// permission remedy, since re-running `brew install` is the wrong advice
// for a copy that is already installed and merely failed to write during
// an upgrade (review S1).
func newUpgradeErrors(runtime shared.Runtime) upgradeErrors {
	return upgradeErrors{fleetErrors{runtime: runtime, prefix: "upgrade", remedyCommand: wbupdate.HomebrewUpgradeCommand}}
}

// UpgradesAvailable is called once per REQ: upgrade-check, with every
// target whose verdict is update-available or undetermined, exactly when
// the user ran `wb upgrade --check` (or `--all --check`/`<name> --check`) —
// never for the bare, no-argument report. It maps onto wb's exitFindings
// exactly like selfUpdateErrors.UpdateAvailable already maps self-update's
// own --check verdict (REQ: exit-code-mapping's no-fourth-code choice): wb
// reserves no separate exit code for "an upgrade is available" any more
// than it does for self-update's identical signal.
func (e upgradeErrors) UpgradesAvailable(results []cliinstall.UpgradeResult) error {
	parts := make([]string, 0, len(results))
	for _, r := range results {
		if r.Verdict == selfupdate.Undetermined {
			parts = append(parts, fmt.Sprintf("%s (undetermined %s; latest %s)", r.Target, r.Current, r.Latest))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s -> %s)", r.Target, r.Current, r.Latest))
	}
	return e.runtime.ExitError(shared.ExitFindings, fmt.Sprintf("upgrade: update available: %s", strings.Join(parts, ", ")))
}
