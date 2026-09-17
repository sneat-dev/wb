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
		Errors: installErrors{},
		HostID: wbCatalogID,
	})
	setDiscoveryTerms(command, "install fleet cli sibling download release brew cask relevant catalog upgrade")
	return command
}

// installErrors maps every cliinstall failure onto wb's own exitFindings
// (1) — wb documents only three exit codes (see the const block in
// main.go) and already treats "ran and found something worth reporting" as
// exitFindings uniformly for self-update's own failures (selfUpdateErrors);
// install's mapper makes the identical no-fourth-code choice rather than
// reserving exitUsage for a runtime refusal that happens well after Cobra's
// own flag parsing already accepted the invocation.
//
// Every message carries an "install: " prefix, never "self-update: "
// (cli-install#req:host-owned-exit-codes: "MUST NOT ... print a
// self-update: message prefix"), so a script grepping stderr for one prefix
// cannot mistake this command's failure for a self-update failure.
type installErrors struct{}

// Failure maps every install failure to exitFindings, explicitly
// enumerating the three FailureKinds cli-install appends after
// KindManagedCommand — KindUnknownTarget, KindNoInstallDir,
// KindDestinationExists — as cli-install#req:host-owned-exit-codes requires
// ("Every host MUST map the three new kinds explicitly ... MUST NOT let
// them fall into a self-update default branch"), plus every shared
// selfupdate.FailureKind self-update's own mapper already classifies
// (TestInstallErrors_SharedKindsMatchSelfUpdateErrors pins that the two
// mappers agree on every one), and *cobracmd.UsageError, the distinguishable
// type for a usage mistake caught inside install's own RunE (an invalid
// --format, or --all combined with names).
//
// err is never nil here in practice: cliinstall/cobracmd v0.21.0's
// mapFailure short-circuits nil before ever calling this method (fixed
// upstream from the v0.20.0 bug this comment used to document — see that
// version's mapFailure doc comment and TestMapFailure_NeverCallsMapperWithNil).
// The switch below still resolves harmlessly for a nil err regardless
// (selfupdate.KindOf(nil) is KindUnexpected), so no explicit guard is
// needed to stay nil-safe.
func (installErrors) Failure(err error) error {
	var usage *cobracmd.UsageError
	if errors.As(err, &usage) {
		// wb reserves exitUsage for an invocation Cobra itself rejects
		// before the root PersistentPreRunE runs (see exitCodeFor's own
		// doc comment); a UsageError surfaces from inside install's own
		// RunE, after that gate already passed, so it joins every other
		// install-time failure at exitFindings rather than claiming
		// exitUsage retroactively.
		return &exitError{code: exitFindings, message: fmt.Sprintf("install: %v", err)}
	}

	switch selfupdate.KindOf(err) {
	case selfupdate.KindUnknownTarget:
		return &exitError{code: exitFindings, message: fmt.Sprintf("install: %v", err)}
	case selfupdate.KindNoInstallDir, selfupdate.KindDestinationExists:
		return &exitError{code: exitFindings, message: fmt.Sprintf("install: %v", err)}
	case selfupdate.KindPermission:
		path := ""
		var failure *selfupdate.Failure
		if errors.As(err, &failure) {
			path = failure.Path
		}
		if path == "" {
			path = "the install destination"
		}
		return &exitError{code: exitFindings, message: fmt.Sprintf(
			"install: permission denied writing %s: %v; re-run with elevated permissions, or install the supported way: %s",
			path, err, selfUpdateHomebrewInstallCommand)}
	default:
		// Every other shared selfupdate.FailureKind (KindAmbiguous,
		// KindReleaseLookup, KindDownload, KindChecksum, KindNonInteractive,
		// KindDowngrade, KindUnknownTag, KindUnsupportedPlatform,
		// KindManagedVersion, KindManagedCommand, KindUnexpected) and any
		// future kind a later library version adds — wb's self-update
		// mapper (selfUpdateErrors) makes the identical no-fourth-code
		// choice for the same kinds.
		return &exitError{code: exitFindings, message: fmt.Sprintf("install: %v", err)}
	}
}
