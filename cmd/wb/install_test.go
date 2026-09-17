package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
)

// AC: install#req:command-name, cli-install#req:host-identity-from-catalog —
// the command is registered under wb's own catalog id and does not panic
// (cobracmd.New panics when the host id is absent from the compiled
// catalog).
func TestInstallCmd_Registration(t *testing.T) {
	cmd := newInstallCmd()
	if cmd.Name() != "install" {
		t.Errorf("Name() = %q, want %q", cmd.Name(), "install")
	}
}

// wb install is registered at the root and offline: `wb install` with no
// arguments is a pure status probe (cli-install#req:list-offline-read-only)
// against wb's own PATH/host directory, so it must succeed without any
// injected environment or network access.
func TestInstallCmd_RegisteredAtRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"install", "--help"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("wb install --help exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "install") {
		t.Errorf("help does not mention install:\n%s", stdout.String())
	}
}

// cli-install#req:host-identity-from-catalog — a host id absent from the
// compiled catalog is a programming error the command constructor panics
// on. This proves the mechanism cobracmd.New documents, using a bogus id
// rather than "wb" (which always exists).
func TestInstallCmd_PanicsForUnknownHostID(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected cobracmd.New to panic for an unregistered host id")
		}
	}()
	cobracmd.New(cobracmd.CommandOptions{HostID: "nosuchhost-wb-test"})
}

// AC: cli-install#ac:direct-install-writes-only-verified-new-files,
// cli-install#req:host-owned-exit-codes ("KindUnknownTarget to its usage or
// invalid-argument code") — `wb install nosuchcli` MUST fail before any
// confirmation, network request or write, exits exitUsage (review S2: the
// invocation was rejected before any work started, exactly wb's own
// documented meaning for exit 2), and the message carries an exact
// "install: " prefix and names the unknown target, never "self-update:" or
// "upgrade:" (review S1). Plan() rejects every unknown name before probing
// anything, so this is offline-safe with no injected Env.
func TestInstallCmd_UnknownTargetExitsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"install", "nosuchcli"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage (%d); stderr: %s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "install: ") {
		t.Errorf("stderr does not carry the exact install: prefix: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "nosuchcli") {
		t.Errorf("stderr does not name the unknown target: %q", stderr.String())
	}
	for _, wrongPrefix := range []string{"self-update:", "upgrade:"} {
		if strings.Contains(stderr.String(), wrongPrefix) {
			t.Errorf("stderr carries a %s prefix; install errors MUST NOT (cli-install#req:host-owned-exit-codes): %q", wrongPrefix, stderr.String())
		}
	}
}

// AC: cli-install#req:host-owned-exit-codes — fleetErrors.Failure maps
// KindUnknownTarget to exitUsage (review S2) and every other FailureKind,
// including the two remaining kinds cli-install appends after
// KindManagedCommand, to wb's exitFindings; every message carries the exact
// "install: " prefix and never "self-update:"/"upgrade:" (review S1).
func TestInstallErrors_FailureExitCodes(t *testing.T) {
	cases := []struct {
		name string
		kind selfupdate.FailureKind
		want int
	}{
		{"unknown_target", selfupdate.KindUnknownTarget, exitUsage},
		{"no_install_dir", selfupdate.KindNoInstallDir, exitFindings},
		{"destination_exists", selfupdate.KindDestinationExists, exitFindings},
		{"ambiguous", selfupdate.KindAmbiguous, exitFindings},
		{"release_lookup", selfupdate.KindReleaseLookup, exitFindings},
		{"download", selfupdate.KindDownload, exitFindings},
		{"checksum", selfupdate.KindChecksum, exitFindings},
		{"permission", selfupdate.KindPermission, exitFindings},
		{"non_interactive", selfupdate.KindNonInteractive, exitFindings},
		{"downgrade", selfupdate.KindDowngrade, exitFindings},
		{"unknown_tag", selfupdate.KindUnknownTag, exitFindings},
		{"unsupported_platform", selfupdate.KindUnsupportedPlatform, exitFindings},
		{"managed_version", selfupdate.KindManagedVersion, exitFindings},
		{"managed_command", selfupdate.KindManagedCommand, exitFindings},
		{"unexpected", selfupdate.KindUnexpected, exitFindings},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mapped := (newInstallErrors()).Failure(&selfupdate.Failure{Kind: testCase.kind, Err: errors.New("boom")})
			var coded *exitError
			if !errors.As(mapped, &coded) {
				t.Fatalf("Failure(%v) did not return an *exitError: %v", testCase.kind, mapped)
			}
			if coded.code != testCase.want {
				t.Errorf("code = %d, want %d", coded.code, testCase.want)
			}
			if !strings.HasPrefix(coded.message, "install: ") {
				t.Errorf("message %q does not carry the exact install: prefix", coded.message)
			}
		})
	}
}

// *cobracmd.UsageError (an invalid --format, or --all combined with names)
// MUST map to exitUsage (review S2: it is refused before any work started,
// wb's own documented meaning for exit 2) and carry the exact "install: "
// prefix, never "self-update:"/"upgrade:" (review S1).
func TestInstallErrors_FailureUsageError(t *testing.T) {
	mapped := (newInstallErrors()).Failure(&cobracmd.UsageError{Err: errors.New("invalid --format")})
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(usage error) did not return an *exitError: %v", mapped)
	}
	if coded.code != exitUsage {
		t.Errorf("code = %d, want exitUsage (%d)", coded.code, exitUsage)
	}
	if !strings.HasPrefix(coded.message, "install: ") {
		t.Errorf("message %q does not carry the exact install: prefix", coded.message)
	}
}

// A non-*selfupdate.Failure error (selfupdate.KindOf returns KindUnexpected
// for anything that isn't one) still maps to exitFindings rather than
// panicking or losing the underlying message.
func TestInstallErrors_FailureWrapsPlainError(t *testing.T) {
	mapped := (newInstallErrors()).Failure(errors.New("not a *selfupdate.Failure"))
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(plain error) did not return an *exitError: %v", mapped)
	}
	if coded.code != exitFindings {
		t.Errorf("code = %d, want exitFindings (%d)", coded.code, exitFindings)
	}
	if !strings.Contains(coded.message, "not a *selfupdate.Failure") {
		t.Errorf("message %q lost the underlying error text", coded.message)
	}
}

// A permission failure names the executable path and wb's own Homebrew
// install command, exactly like selfUpdateErrors's own permission remedy
// (REQ: permission-remedy-names-brew), with an exact "install: " prefix.
func TestInstallErrors_FailurePermissionNamesPathAndBrew(t *testing.T) {
	mapped := (newInstallErrors()).Failure(&selfupdate.Failure{
		Kind: selfupdate.KindPermission,
		Path: "/usr/local/bin/specscore",
		Err:  errors.New("permission denied"),
	})
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(permission) did not return an *exitError: %v", mapped)
	}
	if !strings.HasPrefix(coded.message, "install: ") {
		t.Errorf("message %q does not carry the exact install: prefix", coded.message)
	}
	for _, want := range []string{"/usr/local/bin/specscore", "elevated permissions", selfUpdateHomebrewInstallCommand} {
		if !strings.Contains(coded.message, want) {
			t.Errorf("message %q missing %q", coded.message, want)
		}
	}
}

func TestInstallErrors_FailurePermissionWithoutPath(t *testing.T) {
	mapped := (newInstallErrors()).Failure(&selfupdate.Failure{Kind: selfupdate.KindPermission, Err: errors.New("permission denied")})
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(permission) did not return an *exitError: %v", mapped)
	}
	if !strings.Contains(coded.message, "the install destination") {
		t.Errorf("message %q does not fall back to a generic destination phrase", coded.message)
	}
}

// TestCatalogWBEntryMatchesGoReleaserConfig is the consumer offline test
// cli-install#req:catalog-identity-single-source requires: an assertion
// that .goreleaser.yml's archive name template, checksum name template,
// platforms, release repository and Homebrew cask coordinates match wb's
// own compiled-in cliinstall catalog entry, so a future drift in either one
// fails wb's own CI instead of silently diverging from a real user's
// install (plan task-7's "GoReleaser-versus-catalog naming test").
func TestCatalogWBEntryMatchesGoReleaserConfig(t *testing.T) {
	entry, ok := cliinstall.ByID(wbCatalogID)
	if !ok {
		t.Fatalf("no catalog entry for %q", wbCatalogID)
	}

	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	contents, err := os.ReadFile(filepath.Join(repoRoot, ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	goreleaser := string(contents)

	if entry.Repository != "sneat-dev/wb" {
		t.Errorf("catalog Repository = %q, want sneat-dev/wb (this repository, GoReleaser's implicit release target: no release.github override in .goreleaser.yml)", entry.Repository)
	}
	if entry.TagPrefix != "" {
		t.Errorf("catalog TagPrefix = %q, want empty: .goreleaser.yml publishes plain vX.Y.Z tags with no prefix", entry.TagPrefix)
	}
	if !strings.Contains(goreleaser, `name_template: "wb_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`) {
		t.Error(".goreleaser.yml archive name_template drifted from wb_<version>_<os>_<arch>, which the catalog entry's nil AssetName (the library's GoReleaser-shaped default) assumes")
	}
	if !strings.Contains(goreleaser, `name_template: "wb_{{ .Version }}_checksums.txt"`) {
		t.Error(".goreleaser.yml checksum name_template drifted from wb_<version>_checksums.txt, which the catalog entry's nil ChecksumsName assumes")
	}
	for _, platform := range []string{"linux", "darwin"} {
		if !strings.Contains(goreleaser, "\n      - "+platform+"\n") {
			t.Errorf(".goreleaser.yml builds.goos is missing %q, present in the catalog's SupportedPlatforms", platform)
		}
	}
	if entry.CaskToken != "sneat-dev/tap/wb" {
		t.Errorf("catalog CaskToken = %q, want sneat-dev/tap/wb (sneat-dev/homebrew-tap's cask named wb)", entry.CaskToken)
	}
	if !strings.Contains(goreleaser, "name: homebrew-tap") || !strings.Contains(goreleaser, "owner: sneat-dev") {
		t.Error(".goreleaser.yml homebrew_casks.repository drifted from sneat-dev/homebrew-tap, which CaskToken sneat-dev/tap/wb assumes")
	}
	wantCaskOS := map[string]bool{"darwin": true, "linux": true}
	if len(entry.CaskOS) != len(wantCaskOS) {
		t.Fatalf("CaskOS = %v, want exactly %v", entry.CaskOS, wantCaskOS)
	}
	for _, caskOS := range entry.CaskOS {
		if !wantCaskOS[caskOS] {
			t.Errorf("unexpected CaskOS entry %q", caskOS)
		}
	}
}

// installErrors and selfUpdateErrors MUST agree on the exit code for every
// kind self-update itself already classifies
// (cli-install#req:host-owned-exit-codes: "a host maps it as its
// self-update maps UpdateAvailable" — extended here to every shared failure
// kind), so `install <name>` and `self-update` give the same code for the
// same underlying failure. Trivially true for wb, which maps every kind to
// the same single exitFindings code either way, but this pins that both
// mappers keep making that same choice as the library grows.
func TestInstallErrors_SharedKindsMatchSelfUpdateErrors(t *testing.T) {
	shared := []selfupdate.FailureKind{
		selfupdate.KindAmbiguous, selfupdate.KindReleaseLookup, selfupdate.KindDownload,
		selfupdate.KindChecksum, selfupdate.KindPermission, selfupdate.KindNonInteractive,
		selfupdate.KindDowngrade, selfupdate.KindUnknownTag, selfupdate.KindUnsupportedPlatform,
		selfupdate.KindManagedCommand, selfupdate.KindUnexpected,
	}
	for _, kind := range shared {
		t.Run(kind.String(), func(t *testing.T) {
			installErr := (newInstallErrors()).Failure(&selfupdate.Failure{Kind: kind, Err: errors.New("boom")})
			selfUpdateErr := (selfUpdateErrors{}).Failure(&selfupdate.Failure{Kind: kind, Err: errors.New("boom")})
			var installCoded, selfUpdateCoded *exitError
			if !errors.As(installErr, &installCoded) {
				t.Fatalf("install error %v does not resolve to an *exitError", installErr)
			}
			if !errors.As(selfUpdateErr, &selfUpdateCoded) {
				t.Fatalf("self-update error %v does not resolve to an *exitError", selfUpdateErr)
			}
			if installCoded.code != selfUpdateCoded.code {
				t.Errorf("install code = %d, self-update code = %d; want equal for shared kind %v",
					installCoded.code, selfUpdateCoded.code, kind)
			}
		})
	}
}
