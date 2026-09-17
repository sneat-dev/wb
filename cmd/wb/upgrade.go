package main

import (
	"fmt"
	"strings"

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

// newUpgradeCmd returns the "upgrade" command, built from
// github.com/strongo/cli-helpers/cliinstall/cobracmd against wb's own
// catalog entry. `wb upgrade` reports or applies upgrades for every
// installed catalog CLI (named ones, or all of them with --all), including
// wb itself: `wb upgrade wb` and `wb self-update` reach the identical
// library call because HostConfig and HostAfterUpdate below are the SAME
// values self-update's own command configures
// (cli-install#req:host-target-is-running-binary,
// cli-install#req:self-update-equals-upgrade-self). cobracmd.NewUpgrade
// panics when "wb" is absent from the compiled catalog, the identical
// programming-error guarantee cobracmd.New already gives install.
func newUpgradeCmd() *cobra.Command {
	return newUpgradeCmdWithConfig(newSelfUpdateConfig())
}

// newUpgradeCmdWithConfig is newUpgradeCmd's testable core: it takes the
// host Config as a parameter, exactly mirroring newSelfUpdateCmdWithConfig,
// so a test can pass a hermetic, release-endpoint-injected Config to both
// constructors and compare their outcomes without touching the network
// (cli-install#req:host-target-is-running-binary; plan task-7's own
// self-update-equals-upgrade-self verification).
func newUpgradeCmdWithConfig(cfg selfupdate.Config) *cobra.Command {
	var command *cobra.Command
	command = cobracmd.NewUpgrade(cobracmd.UpgradeCommandOptions{
		Short:           "Upgrade installed fleet CLIs, including wb itself",
		HostID:          wbCatalogID,
		Errors:          upgradeErrors{},
		HostConfig:      cfg,
		HostAfterUpdate: wbAfterUpdate(&command),
	})
	setDiscoveryTerms(command, "upgrade fleet cli sibling update latest release brew cask relevant catalog check")
	return command
}

// upgradeErrors extends installErrors with the upgrades-available method
// cli-install#req:upgrade-check requires, reusing install's Failure mapping
// exactly instead of duplicating its switch statement
// (cli-install#req:host-owned-exit-codes: "The upgrade command MUST use the
// same error mapper" — mirroring cli-helpers' own README example, `type
// datatugUpgradeErrors struct{ datatugInstallErrors }`). installErrors
// already agrees with selfUpdateErrors on every shared FailureKind's exit
// code (TestInstallErrors_SharedKindsMatchSelfUpdateErrors) and explicitly
// maps the three kinds cli-install appends after KindManagedCommand, so
// upgrade inherits both properties for free rather than restating them a
// third time.
type upgradeErrors struct{ installErrors }

// UpgradesAvailable is called once per REQ: upgrade-check, with every
// target whose verdict is update-available or undetermined, exactly when
// the user ran `wb upgrade --check` (or `--all --check`/`<name> --check`) —
// never for the bare, no-argument report. It maps onto wb's exitFindings
// exactly like selfUpdateErrors.UpdateAvailable already maps self-update's
// own --check verdict (REQ: exit-code-mapping's no-fourth-code choice): wb
// reserves no separate exit code for "an upgrade is available" any more
// than it does for self-update's identical signal.
func (upgradeErrors) UpgradesAvailable(results []cliinstall.UpgradeResult) error {
	parts := make([]string, 0, len(results))
	for _, r := range results {
		if r.Verdict == selfupdate.Undetermined {
			parts = append(parts, fmt.Sprintf("%s (undetermined %s; latest %s)", r.Target, r.Current, r.Latest))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s (%s -> %s)", r.Target, r.Current, r.Latest))
	}
	return &exitError{code: exitFindings, message: fmt.Sprintf("upgrade: update available: %s", strings.Join(parts, ", "))}
}
