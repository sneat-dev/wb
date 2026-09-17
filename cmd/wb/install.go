package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
)

// wb binds the shared github.com/strongo/cli-helpers/cliinstall library the
// same way selfupdate.go already binds github.com/strongo/cli-helpers/
// selfupdate: the catalog, status probing, destination policy, Homebrew-cask
// and direct-release install methods, verification, confirmation gating,
// dry run and batch reporting all come from that library. This file supplies
// only what is genuinely wb's own — its catalog id and the mapping from the
// library's typed failures onto wb's own three-code exit contract. See
// spec/features/install/README.md for the Feature spec that draws this
// boundary.

// newInstallCmd returns the "install" command, built from
// github.com/strongo/cli-helpers/cliinstall/cobracmd against wb's own
// catalog entry (cli-install#req:host-identity-from-catalog). `wb install`
// lists the fleet CLIs relevant to wb with their live status, and
// `wb install <name>...` installs them the same way wb itself was
// installed. cobracmd.New panics when "wb" is absent from the compiled
// catalog — a programming error TestNewSelfUpdateConfigIdentity and the
// cobracmd package's own tests catch, never a runtime state a user sees.
func newInstallCmd() *cobra.Command {
	command := cobracmd.New(cobracmd.CommandOptions{
		Short:  "List and install fleet CLIs relevant to wb",
		Errors: newInstallErrors(),
		HostID: wbCatalogID,
	})
	setDiscoveryTerms(command, "install fleet cli sibling download release brew cask relevant catalog upgrade")
	return command
}

// fleetErrors maps every cliinstall failure onto wb's own three-code exit
// contract; install.go and upgrade.go each configure one value with their
// own message prefix and permission remedy rather than two independently
// hard-coded copies of the identical switch (review S1: "make the mapper
// take the prefix as a parameter"). Every message carries prefix's own
// verb, never another command's — including never "self-update: "
// (cli-install#req:host-owned-exit-codes: "MUST NOT ... print a
// self-update: message prefix") — so a script grepping stderr for one
// prefix cannot mistake one fleet command's failure for another's.
type fleetErrors struct {
	// prefix names the command whose failure this is ("install" or
	// "upgrade"), used both as the message prefix and as the permission
	// remedy's own verb.
	prefix string
	// remedyCommand is the exact `brew ...` command the permission remedy
	// names: the managed path that would have avoided the permission
	// failure in the first place — install's own cask-install command for
	// install, self-update's cask-upgrade command for upgrade (review S1:
	// "The permission remedy for upgrade should be the upgrade command,
	// not brew install").
	remedyCommand string
}

// Failure maps every install/upgrade failure onto wb's documented exit
// codes, explicitly enumerating the three FailureKinds cli-install appends
// after KindManagedCommand — KindUnknownTarget, KindNoInstallDir,
// KindDestinationExists — as cli-install#req:host-owned-exit-codes requires
// ("Every host MUST map the three new kinds explicitly ... MUST NOT let
// them fall into a self-update default branch"), plus every shared
// selfupdate.FailureKind self-update's own mapper already classifies
// (TestInstallErrors_SharedKindsMatchSelfUpdateErrors pins that the two
// agree on exit code for every one), and *cobracmd.UsageError, the
// distinguishable type for a usage mistake caught inside RunE (an invalid
// --format, or --all combined with names).
//
// KindUnknownTarget and *cobracmd.UsageError map to exitUsage (2), matching
// cli-install#req:host-owned-exit-codes's own MUST ("KindUnknownTarget to
// its usage or invalid-argument code") and wb's own documented "2 — the
// invocation was rejected before any work started": an unknown target, an
// invalid --format, and --all combined with names are all refused before
// any confirmation, network request, or write, exactly the class of mistake
// exitUsage exists for (review S2) — every other failure kind, including
// KindNoInstallDir/KindDestinationExists (the library's own "invalid-state
// or general failure" pairing) and KindPermission, still maps to
// exitFindings (1), wb's no-fourth-code choice for a runtime failure that
// happens after the invocation was accepted.
//
// err is never nil here in practice: cliinstall/cobracmd v0.21.0's
// mapFailure short-circuits nil before ever calling this method. The switch
// below still resolves harmlessly for a nil err regardless
// (selfupdate.KindOf(nil) is KindUnexpected), so no explicit guard is
// needed to stay nil-safe.
func (e fleetErrors) Failure(err error) error {
	var usage *cobracmd.UsageError
	if errors.As(err, &usage) {
		return &exitError{code: exitUsage, message: fmt.Sprintf("%s: %v", e.prefix, err)}
	}

	switch selfupdate.KindOf(err) {
	case selfupdate.KindUnknownTarget:
		return &exitError{code: exitUsage, message: fmt.Sprintf("%s: %v", e.prefix, err)}
	case selfupdate.KindNoInstallDir, selfupdate.KindDestinationExists:
		return &exitError{code: exitFindings, message: fmt.Sprintf("%s: %v", e.prefix, err)}
	case selfupdate.KindPermission:
		path := ""
		var failure *selfupdate.Failure
		if errors.As(err, &failure) {
			path = failure.Path
		}
		if path == "" {
			path = "the " + e.prefix + " destination"
		}
		return &exitError{code: exitFindings, message: fmt.Sprintf(
			"%s: permission denied writing %s: %v; re-run with elevated permissions, or %s the supported way: %s",
			e.prefix, path, err, e.prefix, e.remedyCommand)}
	default:
		// Every other shared selfupdate.FailureKind (KindAmbiguous,
		// KindReleaseLookup, KindDownload, KindChecksum, KindNonInteractive,
		// KindDowngrade, KindUnknownTag, KindUnsupportedPlatform,
		// KindManagedVersion, KindManagedCommand, KindUnexpected) and any
		// future kind a later library version adds — wb's self-update
		// mapper (selfUpdateErrors) makes the identical no-fourth-code
		// choice for the same kinds.
		return &exitError{code: exitFindings, message: fmt.Sprintf("%s: %v", e.prefix, err)}
	}
}

// installErrors is install's own fleetErrors value: "install: " messages,
// and the cask-install command as its permission remedy.
type installErrors struct{ fleetErrors }

func newInstallErrors() installErrors {
	return installErrors{fleetErrors{prefix: "install", remedyCommand: selfUpdateHomebrewInstallCommand}}
}
