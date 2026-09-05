package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/selfupdate"
	"github.com/strongo/cli-helpers/selfupdate/cobracmd"
)

// wb binds the shared github.com/strongo/cli-helpers/selfupdate library rather than
// reimplementing any of it: install-method detection, release resolution,
// checksum verification, atomic replacement, and every failure rule are
// that library's Feature, not wb's. This file supplies only what is
// genuinely wb's own — its release identity, the one package manager it
// ships through, and the mapping from the library's outcomes onto wb's own
// exit-code contract. See spec/features/self-update/README.md for the
// Feature spec that draws this boundary, and REQ: library-provided-behavior
// in particular.

// selfUpdateUndeterminedVersions are the Config.CurrentVersion values meaning
// "this build cannot say its own version" (REQ: wb-version-identity). wb has
// two, and neither is the library's default placeholder "dev":
//
//   - "unknown" — collectVersion's own final fallback, when neither a
//     link-time stamp nor module metadata is available.
//   - "(devel)" — what the Go toolchain stamps into build.Main.Version for a
//     binary built from a source tree rather than resolved from a module
//     version, i.e. every `go build ./cmd/wb`. Without it declared, a locally
//     built wb compares "(devel)" against the latest release as if it were a
//     real version, and reports an update available from a version that does
//     not exist.
//
// A Go pseudo-version is deliberately NOT in this set: it is a known version
// that sorts below its eventual release, so "update available" is the right
// answer for it.
var selfUpdateUndeterminedVersions = []string{"unknown", "(devel)"}

// selfUpdateHomebrewUpgradeCommand is the exact command printed for a
// Homebrew-managed install. wb ships as a cask, not a formula, so this must
// carry --cask (REQ: wb-homebrew-cask).
const selfUpdateHomebrewUpgradeCommand = "brew update && brew upgrade --cask wb"

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
		UndeterminedVersions: selfUpdateUndeterminedVersions,
		Managers: []selfupdate.Manager{
			selfupdate.Homebrew(selfUpdateHomebrewUpgradeCommand).
				WithExecutableUpgradeSteps(
					selfupdate.ManagedCommand{Executable: "brew", Args: []string{"update"}},
					selfupdate.ManagedCommand{Executable: "brew", Args: []string{"upgrade", "--cask", "wb"}},
				),
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
	return newSelfUpdateCmdWithConfig(newSelfUpdateConfig())
}

func newSelfUpdateCmdWithConfig(cfg selfupdate.Config) *cobra.Command {
	var command *cobra.Command
	command = cobracmd.New(cfg, cobracmd.CommandOptions{
		Short:      "Update the installed wb binary to the latest release",
		Aliases:    []string{"update"},
		JSONFormat: true,
		Errors:     selfUpdateErrors{},
		AfterUpdate: func(ctx context.Context, update selfupdate.AfterUpdate) error {
			return syncSkillsAfterSelfUpdate(command, ctx, update)
		},
	})
	return command
}

func selfUpdateWriteVerifiedVersion(cmd *cobra.Command, previous, installed string) {
	out := cmd.OutOrStdout()
	if format, _ := cmd.Flags().GetString("format"); format == "json" {
		out = cmd.ErrOrStderr()
	}
	_, _ = fmt.Fprintf(out, "Verified installed wb version: %s (was %s).\n", installed, previous)
}

// syncSkillsAfterSelfUpdate runs `skills sync` against the exact installed
// executable identity resolved by the shared self-update provider.
//
// It re-execs the installed binary rather than calling the shared skillsync adapter in
// this process: when self-update actually swapped the executable, this
// process is still running the OLD build in memory (replacing the file on
// disk does not reload an already-running process), so only a fresh child
// process sees the newly embedded skills. It resolves the configured binary
// name on PATH first: Homebrew's stable launcher survives a cask upgrade,
// whereas os.Executable can still name the deleted old Caskroom target.
//
// A failure here is never fatal to self-update: the update itself already
// succeeded (or there was nothing to do), so a sync that cannot run --
// offline, a permissions issue, no harness present yet -- is reported as a
// warning on stderr rather than turned into a self-update failure.
func syncSkillsAfterSelfUpdate(cmd *cobra.Command, parent context.Context, update selfupdate.AfterUpdate) error {
	if update.Outcome.Action == selfupdate.ActionAlreadyCurrent {
		return nil
	}
	if update.Outcome.PostSwapWarning != nil {
		return fmt.Errorf("skip skills sync because the installed WB version was not verified: %w", update.Outcome.PostSwapWarning)
	}
	installedVersion := update.Outcome.Target
	if installedVersion == "" {
		installedVersion = update.Outcome.Result.Latest
	}
	if installedVersion != "" {
		selfUpdateWriteVerifiedVersion(cmd, update.Outcome.Result.Current, installedVersion)
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, update.Executable.Path, "skills", "sync") //nolint:gosec // path is resolved by the shared self-update provider
	var stdout, stderr bytes.Buffer
	child.Stdout = &stdout
	child.Stderr = &stderr
	if err := child.Run(); err != nil {
		return fmt.Errorf("skills sync failed (%v); run `wb skills sync` to install/update WB's Agent Skills manually: %s", err, stderr.String())
	}
	// cobracmd deliberately keeps stdout to one JSON document. A successful
	// nested skills sync is informational, so send it to stderr in JSON mode
	// rather than corrupting the caller's machine-readable update outcome.
	if format, _ := cmd.Flags().GetString("format"); format == "json" {
		_, _ = cmd.ErrOrStderr().Write(stdout.Bytes())
		return nil
	}
	_, _ = cmd.OutOrStdout().Write(stdout.Bytes())
	return nil
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
