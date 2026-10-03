package wbupdate

import (
	"slices"
	"testing"

	"github.com/sneat-dev/wb/internal/buildinfo" // TestNewSelfUpdateConfigIdentity pins wb's own release identity: the
	// GitHub repository, binary name, and undetermined-version placeholder that
	// must match collectVersion's own fallback (REQ: wb-release-identity,
	// REQ: wb-version-identity).
	"github.com/strongo/cli-helpers/selfupdate"
)

func TestNewSelfUpdateConfigIdentity(t *testing.T) {
	cfg := Config(buildinfo.Version())
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
	if cfg.CurrentVersion != buildinfo.Snapshot().Version {
		t.Errorf("CurrentVersion = %q, want %q (buildinfo.Snapshot().Version)", cfg.CurrentVersion, buildinfo.Snapshot().Version)
	}
}

// TestNewSelfUpdateConfigHomebrewOnly pins REQ: wb-homebrew-cask: exactly
// one manager, Homebrew, with the non-interactive --yes --cask upgrade command wb's cask (not a
// formula) requires. No Scoop or WinGet — wb publishes no Windows build.
func TestNewSelfUpdateConfigHomebrewOnly(t *testing.T) {
	cfg := Config(buildinfo.Version())

	if len(cfg.Managers) != 1 {
		t.Fatalf("Managers = %d entries, want exactly 1 (Homebrew only)", len(cfg.Managers))
	}
	manager := cfg.Managers[0]
	if manager.Name != "Homebrew" {
		t.Errorf("Managers[0].Name = %q, want %q", manager.Name, "Homebrew")
	}
	if manager.UpgradeCommand != HomebrewUpgradeCommand {
		t.Errorf("Managers[0].UpgradeCommand = %q, want %q", manager.UpgradeCommand, HomebrewUpgradeCommand)
	}
	if manager.UpgradeExecutable != "" || manager.UpgradeArgs != nil {
		t.Errorf("legacy manager command = %q %v, want ordered steps only", manager.UpgradeExecutable, manager.UpgradeArgs)
	}
	if len(manager.UpgradeSteps) != 2 || manager.UpgradeSteps[0].Executable != "brew" ||
		!slices.Equal(manager.UpgradeSteps[0].Args, []string{"update"}) ||
		manager.UpgradeSteps[1].Executable != "brew" ||
		!slices.Equal(manager.UpgradeSteps[1].Args, []string{"upgrade", "--yes", "--cask", "--", "wb"}) {
		t.Errorf("Managers[0].UpgradeSteps = %+v, want brew update then non-interactive brew cask upgrade", manager.UpgradeSteps)
	}
	if !manager.CanExecuteUpgrade() {
		t.Error("Managers[0] is redirect-only; want executable Homebrew upgrade")
	}
}

// TestNewSelfUpdateConfigVersionProbeArgs pins REQ: wb-version-identity: the
// post-swap probe must use wb's machine-readable spelling, not the library's
// "--version" default.
func TestNewSelfUpdateConfigVersionProbeArgs(t *testing.T) {
	cfg := Config(buildinfo.Version())
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
	cfg := Config(buildinfo.Version())
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
	cfg := Config(buildinfo.Version())
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
