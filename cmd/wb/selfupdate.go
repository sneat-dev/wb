package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/strongo/selfupdate"
	"github.com/strongo/selfupdate/cobracmd"
)

// wb binds the shared github.com/strongo/selfupdate library rather than
// reimplementing any of it: install-method detection, release resolution,
// checksum verification, atomic replacement, and every failure rule are
// that library's Feature, not wb's. This file supplies only what is
// genuinely wb's own — its release identity, the one package manager it
// ships through, and the mapping from the library's outcomes onto wb's own
// exit-code contract. See spec/features/self-update/README.md for the
// Feature spec that draws this boundary, and REQ: library-provided-behavior
// in particular.

// selfUpdateUndeterminedVersion is the Config.CurrentVersion value meaning
// "this build cannot say its own version". wb's version plumbing
// (collectVersion in version.go) falls back to the literal "unknown", not
// the library's default placeholder "dev", so it must be declared
// explicitly (REQ: wb-version-identity). A Go pseudo-version is a known
// version that sorts below its eventual release, not an undetermined one,
// and is deliberately not in this set.
const selfUpdateUndeterminedVersion = "unknown"

// selfUpdateHomebrewUpgradeCommand is the exact command printed for a
// Homebrew-managed install. wb ships as a cask, not a formula, so this must
// carry --cask (REQ: wb-homebrew-cask).
const selfUpdateHomebrewUpgradeCommand = "brew upgrade --cask wb"

// selfUpdateHomebrewInstallCommand is named alongside elevated permissions
// in the permission-failure remedy. It only ever fires on the manual-install
// path — a Homebrew-managed install is redirected long before any write is
// attempted — so a user who hit a permission error is pointed at wb's
// supported install channel instead of being left with only "run as sudo"
// (REQ: permission-remedy-names-brew).
const selfUpdateHomebrewInstallCommand = "brew install --cask sneat-dev/tap/wb"

// newSelfUpdateConfig returns wb's own selfupdate.Config: its release
// identity, the one package manager it ships through, and the platforms
// its GoReleaser build publishes. It is a plain function, not inlined into
// newSelfUpdateCmd, purely so selfupdate_test.go can assert its fields
// directly without constructing a command or touching any I/O
// (REQ: wb-release-identity, REQ: wb-homebrew-cask, REQ: wb-version-identity).
func newSelfUpdateConfig() selfupdate.Config {
	return selfupdate.Config{
		BinaryName:           "wb",
		Repository:           "sneat-dev/wb",
		CurrentVersion:       collectVersion().Version,
		UndeterminedVersions: []string{selfUpdateUndeterminedVersion},
		Managers: []selfupdate.Manager{
			selfupdate.Homebrew(selfUpdateHomebrewUpgradeCommand),
		},
		// Matches .goreleaser.yml's builds.goos/goarch. A host outside this
		// set is refused by the library's own unsupported-platform rule
		// rather than attempting a swap wb publishes no asset for
		// (REQ: wb-release-identity).
		SupportedPlatforms: []selfupdate.Platform{
			{GOOS: "darwin", GOARCH: "amd64"},
			{GOOS: "darwin", GOARCH: "arm64"},
			{GOOS: "linux", GOARCH: "amd64"},
			{GOOS: "linux", GOARCH: "arm64"},
		},
		// wb's machine-readable version spelling, used for the library's
		// post-swap version probe (REQ: wb-version-identity).
		VersionProbeArgs: []string{"version", "--json"},
		// AssetName, ChecksumsName, and DownloadURL are left at the
		// library's GoReleaser-shaped defaults: wb_<version>_<os>_<arch>.tar.gz
		// and wb_<version>_checksums.txt, matching .goreleaser.yml's own
		// archives.name_template and checksum.name_template exactly
		// (REQ: wb-release-identity).
	}
}

// newSelfUpdateCmd returns the "self-update" command (aliased "update"). Its
// canonical name is "self-update", not a bare "update", because in a CLI
// whose other verbs act on other repositories (wb sync, wb deps, wb
// migrate), "update" alone does not say what gets updated
// (REQ: command-and-alias).
//
// Every decision the command makes — install-method detection, the
// Homebrew-managed redirect, checksum-verified atomic replacement, the
// downgrade guard, the confirmation gate — comes from cobracmd.New and the
// selfupdate.Config it is given (REQ: library-provided-behavior). This
// function's only job is to describe wb to that library and to translate
// its outcomes onto wb's own three-code exit contract, via selfUpdateErrors.
func newSelfUpdateCmd() *cobra.Command {
	return cobracmd.New(newSelfUpdateConfig(), cobracmd.CommandOptions{
		Short:      "Update the installed wb binary to the latest release",
		Aliases:    []string{"update"},
		JSONFormat: true,
		Errors:     selfUpdateErrors{},
	})
}

// selfUpdateErrors maps github.com/strongo/selfupdate's outcomes onto wb's
// own three documented exit codes (see the const block in main.go). wb does
// not reserve a fourth code for "update available" the way some other
// consumers of this library do: that would grow wb's exit contract past the
// three codes it documents everywhere else, and `wb status`/`wb check`
// already treat "ran and found something to report" as exitFindings rather
// than a code of its own. An operational failure and a reported update
// therefore both land on exitFindings; `--format json` is what makes the two
// distinguishable for a caller that needs to tell them apart
// (REQ: exit-code-mapping).
type selfUpdateErrors struct{}

// Failure maps any non-nil error from Config.Update or Config.Check onto
// exitFindings. A permission failure gets a wb-specific remedy naming both
// elevated permissions and wb's own Homebrew install command, since sudo is
// not always the right answer for a wb user (REQ: permission-remedy-names-brew).
func (selfUpdateErrors) Failure(err error) error {
	var failure *selfupdate.Failure
	if errors.As(err, &failure) && failure.Kind == selfupdate.KindPermission {
		path := failure.Path
		if path == "" {
			path = "the wb executable"
		}
		return &exitError{code: exitFindings, message: fmt.Sprintf(
			"self-update: permission denied writing %s: %v; re-run with elevated permissions, or install the supported way: %s",
			path, failure.Err, selfUpdateHomebrewInstallCommand)}
	}
	return &exitError{code: exitFindings, message: fmt.Sprintf("self-update: %v", err)}
}

// UpdateAvailable maps a --check verdict that is not up to date onto
// exitFindings, consistent with `wb status` and `wb check`: an available
// update is a finding the same way stale Git state or a policy violation is,
// not a distinct exit code of its own (REQ: exit-code-mapping,
// REQ: check-json-format).
func (selfUpdateErrors) UpdateAvailable(res selfupdate.CheckResult) error {
	if res.Verdict == selfupdate.Undetermined {
		return &exitError{code: exitFindings, message: fmt.Sprintf(
			"self-update: current wb version is undetermined (%s); latest stable is %s", res.Current, res.Latest)}
	}
	return &exitError{code: exitFindings, message: fmt.Sprintf(
		"self-update: update available (%s -> %s)", res.Current, res.Latest)}
}
