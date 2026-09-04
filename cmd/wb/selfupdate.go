package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"time"

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
		UndeterminedVersions: selfUpdateUndeterminedVersions,
		Managers: []selfupdate.Manager{
			selfupdate.Homebrew(selfUpdateHomebrewUpgradeCommand).
				WithExecutableUpgrade("brew", "upgrade", "--cask", "wb"),
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
	cfg := newSelfUpdateConfig()
	command := cobracmd.New(cfg, cobracmd.CommandOptions{
		Short:      "Update the installed wb binary to the latest release",
		Aliases:    []string{"update"},
		JSONFormat: true,
		Errors:     selfUpdateErrors{},
	})

	// Re-sync Agent Skills after every successful self-update invocation
	// that could have changed the on-disk binary (REQ: skills-sync-on-
	// self-update). --check and --dry-run never write anything themselves,
	// so neither runs the sync either.
	originalRunE := command.RunE
	command.RunE = func(cmd *cobra.Command, args []string) error {
		if err := originalRunE(cmd, args); err != nil {
			return err
		}
		if installedVersion, changed := selfUpdateInstalledVersionChanged(cmd, cfg); changed {
			selfUpdateWriteVerifiedVersion(cmd, cfg.CurrentVersion, installedVersion)
			syncSkillsAfterSelfUpdate(cmd, cfg)
		}
		return nil
	}
	return command
}

// selfUpdateShouldSyncSkills reports whether this invocation is the kind
// that can have changed the on-disk binary: neither --check (read-only) nor
// --dry-run (plans without writing) ever does, so neither runs the sync.
func selfUpdateShouldSyncSkills(cmd *cobra.Command, cfg selfupdate.Config) bool {
	if checkOnly, _ := cmd.Flags().GetBool("check"); checkOnly {
		return false
	}
	if dryRun, _ := cmd.Flags().GetBool("dry-run"); dryRun {
		return false
	}
	_, changed := selfUpdateInstalledVersionChanged(cmd, cfg)
	return changed
}

// selfUpdateDetect is the fallback for a wb installation that cannot be found
// on PATH. In the normal case skills must be synchronized with the stable
// launcher (for example /opt/homebrew/bin/wb), not os.Executable's resolved
// Caskroom target, because Homebrew may remove the old target while upgrading.
var selfUpdateDetect = func(cfg selfupdate.Config) (selfupdate.Detection, error) {
	return cfg.DetectSelf()
}

// selfUpdateLookPath is a seam over exec.LookPath. Keeping it separate from
// selfUpdateDetect lets tests model a Homebrew upgrade that removes the old
// Caskroom binary while leaving its stable launcher pointed at the new one.
var selfUpdateLookPath = exec.LookPath

// selfUpdateBinaryVersionChanged verifies a new process through the stable
// launcher before claiming an update by synchronizing skills. In particular,
// an old redirect-only build, an aborted confirmation, and an already-current
// manager run must not make a user infer that the CLI itself changed.
func selfUpdateInstalledVersionChanged(cmd *cobra.Command, cfg selfupdate.Config) (string, bool) {
	binary, ok := selfUpdateSyncBinary(cfg)
	if !ok {
		return "", false
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, binary, "version", "--json").Output() //nolint:gosec // binary is resolved by selfUpdateSyncBinary
	if err != nil {
		return "", false
	}
	var version struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(output, &version); err != nil || version.Version == "" || version.Version == cfg.CurrentVersion {
		return "", false
	}
	return version.Version, true
}

func selfUpdateWriteVerifiedVersion(cmd *cobra.Command, previous, installed string) {
	out := cmd.OutOrStdout()
	if format, _ := cmd.Flags().GetString("format"); format == "json" {
		out = cmd.ErrOrStderr()
	}
	_, _ = fmt.Fprintf(out, "Verified installed wb version: %s (was %s).\n", installed, previous)
}

func selfUpdateSyncBinary(cfg selfupdate.Config) (string, bool) {
	detection, detectErr := selfUpdateDetect(cfg)
	if detectErr != nil || detection.Path == "" {
		return "", false
	}
	// A manual install is an explicit binary location. Do not let an unrelated
	// wb earlier on PATH receive its skills just because this process happened
	// to be launched from a custom location.
	if detection.Method == selfupdate.Manual {
		return detection.Path, true
	}
	if detection.Method != selfupdate.Managed {
		return "", false
	}
	// Homebrew's Caskroom target can disappear during the upgrade. Its stable
	// launcher on PATH is the authority for the new cask.
	binary, err := selfUpdateLookPath(cfg.BinaryName)
	if err == nil && binary != "" {
		return binary, true
	}
	return detection.Path, true
}

// syncSkillsAfterSelfUpdate runs `skills sync` against the on-disk wb binary
// immediately after self-update reports success.
//
// It re-execs the installed binary rather than calling internal/skills in
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
func syncSkillsAfterSelfUpdate(cmd *cobra.Command, cfg selfupdate.Config) {
	binary, ok := selfUpdateSyncBinary(cfg)
	if !ok {
		return
	}
	// cmd.Context() is nil for any *cobra.Command that was never run through
	// Execute/ExecuteContext — every real invocation sets one, but a unit
	// test constructing a bare command, or a future caller of this function
	// outside cobra's own dispatch, must not panic on a nil parent context.
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, binary, "skills", "sync") //nolint:gosec // binary is either the PATH-resolved wb launcher or the resolved installed wb binary itself
	var stdout, stderr bytes.Buffer
	child.Stdout = &stdout
	child.Stderr = &stderr
	if err := child.Run(); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), //nolint:errcheck
			"self-update: skills sync failed (%v); run `wb skills sync` to install/update WB's Agent Skills manually\n%s", err, stderr.String())
		return
	}
	// cobracmd deliberately keeps stdout to one JSON document. A successful
	// nested skills sync is informational, so send it to stderr in JSON mode
	// rather than corrupting the caller's machine-readable update outcome.
	if format, _ := cmd.Flags().GetString("format"); format == "json" {
		_, _ = cmd.ErrOrStderr().Write(stdout.Bytes())
		return
	}
	_, _ = cmd.OutOrStdout().Write(stdout.Bytes())
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
