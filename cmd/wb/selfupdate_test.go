package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/selfupdate"
)

// TestNewSelfUpdateConfigIdentity pins wb's own release identity: the
// GitHub repository, binary name, and undetermined-version placeholder that
// must match collectVersion's own fallback (REQ: wb-release-identity,
// REQ: wb-version-identity).
func TestNewSelfUpdateConfigIdentity(t *testing.T) {
	cfg := newSelfUpdateConfig()

	if cfg.BinaryName != "wb" {
		t.Errorf("BinaryName = %q, want %q", cfg.BinaryName, "wb")
	}
	if cfg.Repository != "sneat-dev/wb" {
		t.Errorf("Repository = %q, want %q", cfg.Repository, "sneat-dev/wb")
	}
	// Both spellings of "this build cannot say its version" must be declared,
	// or the library compares the placeholder as if it were a real version.
	// "(devel)" is not hypothetical: it is what the Go toolchain stamps into
	// build.Main.Version for every `go build ./cmd/wb`, and an undeclared
	// "(devel)" made a locally built wb report an update available FROM a
	// version that does not exist.
	for _, want := range []string{"unknown", "(devel)"} {
		if !slices.Contains(cfg.UndeterminedVersions, want) {
			t.Errorf("UndeterminedVersions = %v, missing %q", cfg.UndeterminedVersions, want)
		}
	}
	// A Go pseudo-version is a KNOWN version that sorts below its release, so
	// it must not be swept into the undetermined set.
	if slices.Contains(cfg.UndeterminedVersions, "v0.23.3-0.20260809071100-889b6d621f76") {
		t.Error("a Go pseudo-version must not be treated as undetermined")
	}
	// wb's placeholders must match what collectVersion actually produces, or a
	// genuinely unstamped build reports itself "undetermined" to a human while
	// comparing as a real, sortable version to the library.
	if cfg.CurrentVersion != collectVersion().Version {
		t.Errorf("CurrentVersion = %q, want %q (collectVersion().Version)", cfg.CurrentVersion, collectVersion().Version)
	}
}

// TestNewSelfUpdateConfigHomebrewOnly pins REQ: wb-homebrew-cask: exactly
// one manager, Homebrew, with the --cask upgrade command wb's cask (not a
// formula) requires. No Scoop or WinGet — wb publishes no Windows build.
func TestNewSelfUpdateConfigHomebrewOnly(t *testing.T) {
	cfg := newSelfUpdateConfig()

	if len(cfg.Managers) != 1 {
		t.Fatalf("Managers = %d entries, want exactly 1 (Homebrew only)", len(cfg.Managers))
	}
	manager := cfg.Managers[0]
	if manager.Name != "Homebrew" {
		t.Errorf("Managers[0].Name = %q, want %q", manager.Name, "Homebrew")
	}
	if manager.UpgradeCommand != "brew upgrade --cask wb" {
		t.Errorf("Managers[0].UpgradeCommand = %q, want %q", manager.UpgradeCommand, "brew upgrade --cask wb")
	}
	if manager.UpgradeExecutable != "brew" {
		t.Errorf("Managers[0].UpgradeExecutable = %q, want brew", manager.UpgradeExecutable)
	}
	if want := []string{"upgrade", "--cask", "wb"}; !slices.Equal(manager.UpgradeArgs, want) {
		t.Errorf("Managers[0].UpgradeArgs = %v, want %v", manager.UpgradeArgs, want)
	}
	if !manager.CanExecuteUpgrade() {
		t.Error("Managers[0] is redirect-only; want executable Homebrew upgrade")
	}
}

// TestNewSelfUpdateConfigVersionProbeArgs pins REQ: wb-version-identity: the
// post-swap probe must use wb's machine-readable spelling, not the library's
// "--version" default.
func TestNewSelfUpdateConfigVersionProbeArgs(t *testing.T) {
	cfg := newSelfUpdateConfig()
	want := []string{"version", "--json"}
	if len(cfg.VersionProbeArgs) != len(want) {
		t.Fatalf("VersionProbeArgs = %v, want %v", cfg.VersionProbeArgs, want)
	}
	for i, arg := range want {
		if cfg.VersionProbeArgs[i] != arg {
			t.Errorf("VersionProbeArgs[%d] = %q, want %q", i, cfg.VersionProbeArgs[i], arg)
		}
	}
}

// TestNewSelfUpdateConfigSupportedPlatforms pins REQ: wb-release-identity:
// exactly the darwin/linux x amd64/arm64 matrix .goreleaser.yml publishes —
// no more, no less, so an unpublished platform is refused by the library
// rather than attempting a swap wb has no asset for.
func TestNewSelfUpdateConfigSupportedPlatforms(t *testing.T) {
	cfg := newSelfUpdateConfig()
	want := map[selfupdate.Platform]bool{
		{GOOS: "darwin", GOARCH: "amd64"}: true,
		{GOOS: "darwin", GOARCH: "arm64"}: true,
		{GOOS: "linux", GOARCH: "amd64"}:  true,
		{GOOS: "linux", GOARCH: "arm64"}:  true,
	}
	if len(cfg.SupportedPlatforms) != len(want) {
		t.Fatalf("SupportedPlatforms = %v, want exactly %d entries", cfg.SupportedPlatforms, len(want))
	}
	for _, p := range cfg.SupportedPlatforms {
		if !want[p] {
			t.Errorf("unexpected platform %+v in SupportedPlatforms", p)
		}
		delete(want, p)
	}
	if len(want) != 0 {
		t.Errorf("SupportedPlatforms is missing %v", want)
	}
}

// TestNewSelfUpdateConfigDefaultAssetNaming pins the AC that wb's asset
// naming must match .goreleaser.yml (wb_<version>_<os>_<arch>.tar.gz and
// wb_<version>_checksums.txt) exactly by NOT overriding AssetName,
// ChecksumsName, or DownloadURL — the library's own defaults already
// produce those names (verified against .goreleaser.yml's name_template
// fields), so an override here would be a silent divergence from the
// GoReleaser config that publishes the real assets.
func TestNewSelfUpdateConfigDefaultAssetNaming(t *testing.T) {
	cfg := newSelfUpdateConfig()
	if cfg.AssetName != nil {
		t.Error("AssetName is overridden; wb's naming must match the library's GoReleaser-shaped default")
	}
	if cfg.ChecksumsName != nil {
		t.Error("ChecksumsName is overridden; wb's naming must match the library's GoReleaser-shaped default")
	}
	if cfg.DownloadURL != nil {
		t.Error("DownloadURL is overridden; wb's naming must match the library's GoReleaser-shaped default")
	}
}

// TestNewSelfUpdateCmdRegistration pins REQ: command-and-alias: the command
// is named "self-update" and answers to the "update" alias, with --check,
// --format json (JSONFormat), --version, and --allow-downgrade all present
// (registered by cobracmd.New, not reimplemented here).
func TestNewSelfUpdateCmdRegistration(t *testing.T) {
	cmd := newSelfUpdateCmd()

	if cmd.Use != "self-update" {
		t.Errorf("Use = %q, want %q", cmd.Use, "self-update")
	}
	if len(cmd.Aliases) != 1 || cmd.Aliases[0] != "update" {
		t.Errorf("Aliases = %v, want [update]", cmd.Aliases)
	}
	for _, flag := range []string{"check", "yes", "version", "allow-downgrade", "dry-run", "format"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("flag %q is not registered", flag)
		}
	}
}

// TestRootCmdRegistersSelfUpdate pins that the command is actually wired
// into the root command tree, and that both its canonical name and its
// alias resolve there — the alias resolution a caller of `wb update` relies
// on happens through cobra.Command.Find, not through a second registration.
func TestRootCmdRegistersSelfUpdate(t *testing.T) {
	root := newRootCmd()

	found, _, err := root.Find([]string{"self-update"})
	if err != nil {
		t.Fatalf("wb self-update: %v", err)
	}
	if found.Name() != "self-update" {
		t.Errorf("wb self-update resolved to %q", found.Name())
	}

	foundAlias, _, err := root.Find([]string{"update"})
	if err != nil {
		t.Fatalf("wb update: %v", err)
	}
	if foundAlias.Name() != "self-update" {
		t.Errorf("wb update resolved to %q, want the self-update command", foundAlias.Name())
	}
}

// TestSelfUpdateVersionFlagNotSwallowedByRoot proves the interaction the
// brief calls out: hasVersionFlag stops at the first non-flag token (the
// subcommand name), so self-update's own --version — which pins a release
// tag, not a request for wb's build identity — is never mistaken for the
// root-level `wb --version` handled before cobra even runs
// (REQ: version-flag). main_test.go's TestHasVersionFlagRecognisesOnlyRootLevelRequests
// covers the same claim from hasVersionFlag's own table; this test proves it
// from the self-update command's actual registered --version flag instead of
// a hard-coded string, so the two can't silently drift apart.
func TestSelfUpdateVersionFlagNotSwallowedByRoot(t *testing.T) {
	cmd := newSelfUpdateCmd()
	if cmd.Flags().Lookup("version") == nil {
		t.Fatal("self-update does not register its own --version flag")
	}

	for _, name := range append([]string{cmd.Name()}, cmd.Aliases...) {
		args := []string{name, "--version", "v0.24.0"}
		if hasVersionFlag(args) {
			t.Errorf("hasVersionFlag(%v) = true; self-update's own --version was taken as the root's", args)
		}
	}
}

// TestSelfUpdateErrorsFailureMapsToExitFindings pins REQ: exit-code-mapping:
// every operational failure — release lookup, download, checksum,
// non-interactive refusal, unknown tag, refused downgrade, and any other
// non-permission *selfupdate.Failure — is exitFindings (1), never a fourth
// code.
func TestSelfUpdateErrorsFailureMapsToExitFindings(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"release lookup", &selfupdate.Failure{Kind: selfupdate.KindReleaseLookup, Err: errors.New("network unreachable")}},
		{"download", &selfupdate.Failure{Kind: selfupdate.KindDownload, Err: errors.New("404")}},
		{"checksum", &selfupdate.Failure{Kind: selfupdate.KindChecksum, Err: errors.New("mismatch")}},
		{"non-interactive", &selfupdate.Failure{Kind: selfupdate.KindNonInteractive, Err: errors.New("no tty")}},
		{"downgrade", &selfupdate.Failure{Kind: selfupdate.KindDowngrade, Err: errors.New("refusing")}},
		{"unknown tag", &selfupdate.Failure{Kind: selfupdate.KindUnknownTag, Err: errors.New("no such release")}},
		{"unsupported platform", &selfupdate.Failure{Kind: selfupdate.KindUnsupportedPlatform, Err: errors.New("no asset")}},
		{"ambiguous", &selfupdate.Failure{Kind: selfupdate.KindAmbiguous, Path: "/opt/wb", Err: errors.New("ambiguous")}},
		{"unexpected", &selfupdate.Failure{Kind: selfupdate.KindUnexpected, Err: errors.New("boom")}},
		{"plain error", errors.New("some other failure")},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mapped := selfUpdateErrors{}.Failure(testCase.err)
			var coded *exitError
			if !errors.As(mapped, &coded) {
				t.Fatalf("Failure(%v) did not return an *exitError: %v", testCase.err, mapped)
			}
			if coded.code != exitFindings {
				t.Errorf("code = %d, want exitFindings (%d)", coded.code, exitFindings)
			}
		})
	}
}

// TestSelfUpdateErrorsFailurePermissionNamesPathAndBrew pins
// REQ: permission-remedy-names-brew: a permission failure's message must
// name the executable path, mention elevated permissions, and give wb's own
// Homebrew install command as the alternative — the remedy that is
// specifically wb's own, not the library's.
func TestSelfUpdateErrorsFailurePermissionNamesPathAndBrew(t *testing.T) {
	err := &selfupdate.Failure{
		Kind: selfupdate.KindPermission,
		Path: "/usr/local/bin/wb",
		Err:  fs.ErrPermission,
	}

	mapped := selfUpdateErrors{}.Failure(err)
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(%v) did not return an *exitError: %v", err, mapped)
	}
	if coded.code != exitFindings {
		t.Errorf("code = %d, want exitFindings (%d)", coded.code, exitFindings)
	}

	message := coded.Error()
	for _, want := range []string{"/usr/local/bin/wb", "elevated permissions", "brew install --cask sneat-dev/tap/wb"} {
		if !strings.Contains(message, want) {
			t.Errorf("permission-failure message %q does not contain %q", message, want)
		}
	}
}

// TestSelfUpdateErrorsFailurePermissionWithoutPath covers the defensive
// fallback when a *selfupdate.Failure of KindPermission somehow carries no
// Path (the library always sets it today, but the mapper must not print an
// empty path if that ever changes).
func TestSelfUpdateErrorsFailurePermissionWithoutPath(t *testing.T) {
	err := &selfupdate.Failure{Kind: selfupdate.KindPermission, Err: fs.ErrPermission}
	mapped := selfUpdateErrors{}.Failure(err)
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(%v) did not return an *exitError: %v", err, mapped)
	}
	if !strings.Contains(coded.Error(), "the wb executable") {
		t.Errorf("message = %q, want a fallback naming \"the wb executable\"", coded.Error())
	}
}

// TestSelfUpdateErrorsUpdateAvailableMapsToExitFindings pins the second half
// of REQ: exit-code-mapping: an available update under --check is a finding,
// exitFindings (1), exactly like `wb status` and `wb check` report findings
// — not a distinct exit code.
func TestSelfUpdateErrorsUpdateAvailableMapsToExitFindings(t *testing.T) {
	cases := []selfupdate.CheckResult{
		{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable},
		{Current: "unknown", Latest: "1.1.0", Verdict: selfupdate.Undetermined},
	}
	for _, result := range cases {
		t.Run(result.Verdict.String(), func(t *testing.T) {
			mapped := selfUpdateErrors{}.UpdateAvailable(result)
			var coded *exitError
			if !errors.As(mapped, &coded) {
				t.Fatalf("UpdateAvailable(%+v) did not return an *exitError: %v", result, mapped)
			}
			if coded.code != exitFindings {
				t.Errorf("code = %d, want exitFindings (%d)", coded.code, exitFindings)
			}
			if !strings.Contains(coded.Error(), result.Current) || !strings.Contains(coded.Error(), result.Latest) {
				t.Errorf("message %q does not name both current (%q) and latest (%q)", coded.Error(), result.Current, result.Latest)
			}
		})
	}
}

// TestSelfUpdateShouldSyncSkillsSkipsCheckAndDryRun pins REQ:
// skills-sync-on-self-update's own boundary: neither a read-only --check nor
// a --dry-run plan can have changed the on-disk binary, so neither may
// trigger the post-update skills sync.
func TestSelfUpdateShouldSyncSkillsSkipsCheckAndDryRun(t *testing.T) {
	tests := []struct {
		name     string
		check    bool
		dryRun   bool
		wantSync bool
	}{
		{"plain update", false, false, true},
		{"check only", true, false, false},
		{"dry run", false, true, false},
		{"check and dry run", true, true, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			binary := fakeSelfUpdateBinary(t, `if [ "$1" = "version" ]; then echo '{"version":"new"}'; fi`)
			withSelfUpdateDetect(t, func(selfupdate.Config) (selfupdate.Detection, error) {
				return selfupdate.Detection{Method: selfupdate.Manual, Path: binary}, nil
			})
			cmd := &cobra.Command{Use: "self-update"}
			cmd.Flags().Bool("check", false, "")
			cmd.Flags().Bool("dry-run", false, "")
			if err := cmd.Flags().Set("check", boolFlagValue(test.check)); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Flags().Set("dry-run", boolFlagValue(test.dryRun)); err != nil {
				t.Fatal(err)
			}
			if got := selfUpdateShouldSyncSkills(cmd, selfupdate.Config{CurrentVersion: "old"}); got != test.wantSync {
				t.Errorf("selfUpdateShouldSyncSkills() = %v, want %v", got, test.wantSync)
			}
		})
	}
}

func boolFlagValue(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

// fakeSelfUpdateBinary writes an executable POSIX shell script standing in
// for a detected installed wb binary and returns its path. It ignores its
// arguments (syncSkillsAfterSelfUpdate always calls it with "skills sync"),
// so the fixture only needs to control stdout/stderr/exit code.
func fakeSelfUpdateBinary(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-wb")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// withSelfUpdateDetect overrides selfUpdateDetect for the duration of one
// test, exactly like cobracmd's own detectFunc seam, and restores it
// afterward so later tests are unaffected.
func withSelfUpdateDetect(t *testing.T, detect func(selfupdate.Config) (selfupdate.Detection, error)) {
	t.Helper()
	original := selfUpdateDetect
	selfUpdateDetect = detect
	t.Cleanup(func() { selfUpdateDetect = original })
}

func withSelfUpdateLookPath(t *testing.T, lookPath func(string) (string, error)) {
	t.Helper()
	original := selfUpdateLookPath
	selfUpdateLookPath = lookPath
	t.Cleanup(func() { selfUpdateLookPath = original })
}

// TestSyncSkillsAfterSelfUpdateForwardsDetectedBinaryStdout proves the
// process re-exec actually happens against the path selfUpdateDetect names,
// not this test process itself, and that its stdout reaches the caller.
func TestSyncSkillsAfterSelfUpdateForwardsStableLauncherStdout(t *testing.T) {
	binary := fakeSelfUpdateBinary(t, `echo "synced: $1 $2"`)
	withSelfUpdateDetect(t, func(selfupdate.Config) (selfupdate.Detection, error) {
		return selfupdate.Detection{Method: selfupdate.Managed, Path: "old-caskroom-wb"}, nil
	})
	withSelfUpdateLookPath(t, func(string) (string, error) {
		return binary, nil
	})

	cmd := &cobra.Command{Use: "self-update"}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	syncSkillsAfterSelfUpdate(cmd, selfupdate.Config{})

	if got := stdout.String(); got != "synced: skills sync\n" {
		t.Errorf("stdout = %q, want the detected binary's own output forwarded", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty on success", stderr.String())
	}
}

// TestSyncSkillsAfterHomebrewUpdateUsesStableLauncher covers the real cask
// transition: os.Executable can still name a Caskroom binary that Homebrew
// removed, but `wb` on PATH resolves through Homebrew's stable symlink to the
// new cask. The sync must run the new build's embedded skills, not fail trying
// the old target.
func TestSyncSkillsAfterHomebrewUpdateUsesStableLauncher(t *testing.T) {
	oldCaskroomBinary := filepath.Join(t.TempDir(), "removed-old-wb")
	newLauncher := fakeSelfUpdateBinary(t, `echo "new cask: $1 $2"`)
	withSelfUpdateDetect(t, func(selfupdate.Config) (selfupdate.Detection, error) {
		return selfupdate.Detection{Path: oldCaskroomBinary}, nil
	})
	withSelfUpdateLookPath(t, func(name string) (string, error) {
		if name != "wb" {
			t.Fatalf("LookPath name = %q, want wb", name)
		}
		return newLauncher, nil
	})

	cmd := &cobra.Command{Use: "self-update"}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	syncSkillsAfterSelfUpdate(cmd, newSelfUpdateConfig())

	if got := stdout.String(); got != "new cask: skills sync\n" {
		t.Errorf("stdout = %q, want skills from the stable post-upgrade launcher", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

func TestSelfUpdateShouldSyncSkillsProbesNewHomebrewLauncherVersion(t *testing.T) {
	oldCaskroomBinary := filepath.Join(t.TempDir(), "removed-old-wb")
	newLauncher := fakeSelfUpdateBinary(t, `
if [ "$1" = "version" ] && [ "$2" = "--json" ]; then
  echo '{"version":"0.91.0"}'
  exit 0
fi
exit 42`)
	withSelfUpdateDetect(t, func(selfupdate.Config) (selfupdate.Detection, error) {
		return selfupdate.Detection{Method: selfupdate.Managed, Path: oldCaskroomBinary}, nil
	})
	withSelfUpdateLookPath(t, func(string) (string, error) { return newLauncher, nil })
	cmd := &cobra.Command{Use: "self-update"}
	cmd.Flags().Bool("check", false, "")
	cmd.Flags().Bool("dry-run", false, "")

	if !selfUpdateShouldSyncSkills(cmd, selfupdate.Config{BinaryName: "wb", CurrentVersion: "0.80.0"}) {
		t.Error("selfUpdateShouldSyncSkills() = false, want true after the new Homebrew launcher proves a changed version")
	}
}

func TestSyncSkillsAfterSelfUpdateKeepsManualBinaryOverCompetingPATHWB(t *testing.T) {
	manualBinary := fakeSelfUpdateBinary(t, `echo "manual: $1 $2"`)
	otherPATHBinary := fakeSelfUpdateBinary(t, `echo "wrong PATH binary"`)
	withSelfUpdateDetect(t, func(selfupdate.Config) (selfupdate.Detection, error) {
		return selfupdate.Detection{Method: selfupdate.Manual, Path: manualBinary}, nil
	})
	withSelfUpdateLookPath(t, func(string) (string, error) { return otherPATHBinary, nil })
	cmd := &cobra.Command{Use: "self-update"}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	syncSkillsAfterSelfUpdate(cmd, newSelfUpdateConfig())

	if got := stdout.String(); got != "manual: skills sync\n" {
		t.Errorf("stdout = %q, want the manually invoked binary's own skills", got)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestSyncSkillsAfterSelfUpdateReportsFailureWithoutFailingSelfUpdate proves
// a failed sync degrades to a warning: syncSkillsAfterSelfUpdate returns
// nothing for its caller to fail on, so a sync problem never turns a
// successful self-update into a reported failure.
func TestSyncSkillsAfterSelfUpdateReportsFailureWithoutFailingSelfUpdate(t *testing.T) {
	binary := fakeSelfUpdateBinary(t, `echo "boom" 1>&2; exit 1`)
	withSelfUpdateDetect(t, func(selfupdate.Config) (selfupdate.Detection, error) {
		return selfupdate.Detection{Method: selfupdate.Managed, Path: "old-caskroom-wb"}, nil
	})
	withSelfUpdateLookPath(t, func(string) (string, error) {
		return binary, nil
	})

	cmd := &cobra.Command{Use: "self-update"}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	syncSkillsAfterSelfUpdate(cmd, selfupdate.Config{})

	if !strings.Contains(stderr.String(), "self-update: skills sync failed") {
		t.Errorf("stderr = %q, want a skills-sync failure warning", stderr.String())
	}
	if !strings.Contains(stderr.String(), "wb skills sync") {
		t.Errorf("stderr = %q, want the manual fallback command named", stderr.String())
	}
	if !strings.Contains(stderr.String(), "boom") {
		t.Errorf("stderr = %q, want the child's own stderr surfaced", stderr.String())
	}
}

// TestSyncSkillsAfterSelfUpdateNoOpWhenDetectionFails covers both ways
// selfUpdateDetect can fail to name a binary: an error, and a zero-value
// Detection with an empty Path. Neither may write anything or panic.
func TestSyncSkillsAfterSelfUpdateNoOpWhenDetectionFails(t *testing.T) {
	tests := []struct {
		name   string
		detect func(selfupdate.Config) (selfupdate.Detection, error)
	}{
		{"detection error", func(selfupdate.Config) (selfupdate.Detection, error) {
			return selfupdate.Detection{}, errors.New("cannot resolve executable")
		}},
		{"empty path", func(selfupdate.Config) (selfupdate.Detection, error) {
			return selfupdate.Detection{}, nil
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withSelfUpdateLookPath(t, func(string) (string, error) { return "", os.ErrNotExist })
			withSelfUpdateDetect(t, test.detect)
			cmd := &cobra.Command{Use: "self-update"}
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			syncSkillsAfterSelfUpdate(cmd, selfupdate.Config{})

			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Errorf("stdout=%q stderr=%q, want both empty when detection cannot name a binary", stdout.String(), stderr.String())
			}
		})
	}
}

func TestSyncSkillsAfterSelfUpdateKeepsJSONStdoutSingleDocument(t *testing.T) {
	binary := fakeSelfUpdateBinary(t, `echo "skills-sync-output"`)
	withSelfUpdateDetect(t, func(selfupdate.Config) (selfupdate.Detection, error) {
		return selfupdate.Detection{Method: selfupdate.Managed, Path: "old-caskroom-wb"}, nil
	})
	withSelfUpdateLookPath(t, func(string) (string, error) { return binary, nil })
	cmd := &cobra.Command{Use: "self-update"}
	cmd.Flags().String("format", "text", "")
	if err := cmd.Flags().Set("format", "json"); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	syncSkillsAfterSelfUpdate(cmd, newSelfUpdateConfig())

	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want no nested sync output in JSON mode", stdout.String())
	}
	if got := stderr.String(); got != "skills-sync-output\n" {
		t.Errorf("stderr = %q, want nested sync output", got)
	}
}
