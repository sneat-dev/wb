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
// cli-install#req:unknown-target-refused — `wb install nosuchcli` MUST fail
// before any confirmation, network request or write, exits exitFindings
// (wb's install mapper reserves no separate code), and the message names
// the unknown target and carries no "self-update:" prefix. Plan() rejects
// every unknown name before probing anything, so this is offline-safe with
// no injected Env.
func TestInstallCmd_UnknownTargetExitsFindings(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"install", "nosuchcli"}, &stdout, &stderr)
	if code != exitFindings {
		t.Fatalf("exit code = %d, want exitFindings (%d); stderr: %s", code, exitFindings, stderr.String())
	}
	if !strings.Contains(stderr.String(), "nosuchcli") {
		t.Errorf("stderr does not name the unknown target: %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "self-update:") {
		t.Errorf("stderr carries a self-update: prefix; install errors MUST NOT (cli-install#req:host-owned-exit-codes): %q", stderr.String())
	}
}

// A nil err IS a real, reachable call on the success/dry-run path — see
// installErrors.Failure's own doc comment for why cliinstall/cobracmd
// v0.20.0 calls opts.Errors.Failure(nil) on every successful run.
func TestInstallErrors_FailureNilIsNil(t *testing.T) {
	if err := (installErrors{}).Failure(nil); err != nil {
		t.Errorf("Failure(nil) = %v, want nil", err)
	}
}

// AC: cli-install#req:host-owned-exit-codes — installErrors.Failure maps
// every FailureKind, including the three cli-install appends after
// KindManagedCommand, to wb's exitFindings, and no message carries a
// "self-update:" prefix.
func TestInstallErrors_FailureExitCodes(t *testing.T) {
	cases := []struct {
		name string
		kind selfupdate.FailureKind
	}{
		{"unknown_target", selfupdate.KindUnknownTarget},
		{"no_install_dir", selfupdate.KindNoInstallDir},
		{"destination_exists", selfupdate.KindDestinationExists},
		{"ambiguous", selfupdate.KindAmbiguous},
		{"release_lookup", selfupdate.KindReleaseLookup},
		{"download", selfupdate.KindDownload},
		{"checksum", selfupdate.KindChecksum},
		{"permission", selfupdate.KindPermission},
		{"non_interactive", selfupdate.KindNonInteractive},
		{"downgrade", selfupdate.KindDowngrade},
		{"unknown_tag", selfupdate.KindUnknownTag},
		{"unsupported_platform", selfupdate.KindUnsupportedPlatform},
		{"managed_version", selfupdate.KindManagedVersion},
		{"managed_command", selfupdate.KindManagedCommand},
		{"unexpected", selfupdate.KindUnexpected},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			mapped := (installErrors{}).Failure(&selfupdate.Failure{Kind: testCase.kind, Err: errors.New("boom")})
			var coded *exitError
			if !errors.As(mapped, &coded) {
				t.Fatalf("Failure(%v) did not return an *exitError: %v", testCase.kind, mapped)
			}
			if coded.code != exitFindings {
				t.Errorf("code = %d, want exitFindings (%d)", coded.code, exitFindings)
			}
			if strings.Contains(coded.message, "self-update:") {
				t.Errorf("message %q carries a self-update: prefix; install errors MUST NOT", coded.message)
			}
		})
	}
}

// *cobracmd.UsageError (an invalid --format, or --all combined with names)
// MUST map to exitFindings too — wb has no separate usage code available
// this late in the command's own RunE (see installErrors.Failure's doc
// comment) — and MUST NOT carry a self-update: prefix.
func TestInstallErrors_FailureUsageError(t *testing.T) {
	mapped := (installErrors{}).Failure(&cobracmd.UsageError{Err: errors.New("invalid --format")})
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(usage error) did not return an *exitError: %v", mapped)
	}
	if coded.code != exitFindings {
		t.Errorf("code = %d, want exitFindings (%d)", coded.code, exitFindings)
	}
	if strings.Contains(coded.message, "self-update:") {
		t.Errorf("message %q carries a self-update: prefix; install errors MUST NOT", coded.message)
	}
}

// A non-*selfupdate.Failure error (selfupdate.KindOf returns KindUnexpected
// for anything that isn't one) still maps to exitFindings rather than
// panicking or losing the underlying message.
func TestInstallErrors_FailureWrapsPlainError(t *testing.T) {
	mapped := (installErrors{}).Failure(errors.New("not a *selfupdate.Failure"))
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
// (REQ: permission-remedy-names-brew), but with an "install: " prefix.
func TestInstallErrors_FailurePermissionNamesPathAndBrew(t *testing.T) {
	mapped := (installErrors{}).Failure(&selfupdate.Failure{
		Kind: selfupdate.KindPermission,
		Path: "/usr/local/bin/specscore",
		Err:  errors.New("permission denied"),
	})
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(permission) did not return an *exitError: %v", mapped)
	}
	for _, want := range []string{"/usr/local/bin/specscore", "elevated permissions", selfUpdateHomebrewInstallCommand} {
		if !strings.Contains(coded.message, want) {
			t.Errorf("message %q missing %q", coded.message, want)
		}
	}
}

func TestInstallErrors_FailurePermissionWithoutPath(t *testing.T) {
	mapped := (installErrors{}).Failure(&selfupdate.Failure{Kind: selfupdate.KindPermission, Err: errors.New("permission denied")})
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
			installErr := (installErrors{}).Failure(&selfupdate.Failure{Kind: kind, Err: errors.New("boom")})
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
