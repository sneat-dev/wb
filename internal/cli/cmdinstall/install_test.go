package cmdinstall

import (
	"errors"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbupdate" // AC: install#req:command-name, cli-install#req:host-identity-from-catalog —
	// the command is registered under wb's own catalog id and does not panic
	// (cobracmd.New panics when the host id is absent from the compiled
	// catalog).
	// cli-install#req:host-identity-from-catalog — a host id absent from the
	// compiled catalog is a programming error the command constructor panics
	// on. This proves the mechanism cobracmd.New documents, using a bogus id
	// rather than "wb" (which always exists).
	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
)

func TestInstallCmd_Registration(t *testing.T) {
	t.Parallel()
	cmd := newInstallCmd()
	if cmd.Name() != "install" {
		t.Errorf("Name() = %q, want %q", cmd.Name(), "install")
	}
}

func TestInstallCmd_PanicsForUnknownHostID(t *testing.T) {
	t.Parallel()
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected cobracmd.New to panic for an unregistered host id")
			}
		}()
		cobracmd.New(cobracmd.CommandOptions{HostID: "nosuchhost-wb-test"})
	}()
}

// AC: cli-install#req:host-owned-exit-codes — fleetErrors.Failure maps
// KindUnknownTarget to exitUsage (review S2) and every other FailureKind,
// including the two remaining kinds cli-install appends after
// KindManagedCommand, to wb's exitFindings; every message carries the exact
// "install: " prefix and never "self-update:"/"upgrade:" (review S1).
func TestInstallErrors_FailureExitCodes(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			mapped := (newInstallErrors(testRuntime())).Failure(&selfupdate.Failure{Kind: testCase.kind, Err: errors.New("boom")})
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
	t.Parallel()
	mapped := (newInstallErrors(testRuntime())).Failure(&cobracmd.UsageError{Err: errors.New("invalid --format")})
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
	t.Parallel()
	mapped := (newInstallErrors(testRuntime())).Failure(errors.New("not a *selfupdate.Failure"))
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
	t.Parallel()
	mapped := (newInstallErrors(testRuntime())).Failure(&selfupdate.Failure{
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
	for _, want := range []string{"/usr/local/bin/specscore", "elevated permissions", wbupdate.HomebrewInstallCommand} {
		if !strings.Contains(coded.message, want) {
			t.Errorf("message %q missing %q", coded.message, want)
		}
	}
}

func TestInstallErrors_FailurePermissionWithoutPath(t *testing.T) {
	t.Parallel()
	mapped := (newInstallErrors(testRuntime())).Failure(&selfupdate.Failure{Kind: selfupdate.KindPermission, Err: errors.New("permission denied")})
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(permission) did not return an *exitError: %v", mapped)
	}
	if !strings.Contains(coded.message, "the install destination") {
		t.Errorf("message %q does not fall back to a generic destination phrase", coded.message)
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
	t.Parallel()
	shared := []selfupdate.FailureKind{
		selfupdate.KindAmbiguous, selfupdate.KindReleaseLookup, selfupdate.KindDownload,
		selfupdate.KindChecksum, selfupdate.KindPermission, selfupdate.KindNonInteractive,
		selfupdate.KindDowngrade, selfupdate.KindUnknownTag, selfupdate.KindUnsupportedPlatform,
		selfupdate.KindManagedCommand, selfupdate.KindUnexpected,
	}
	for _, kind := range shared {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			installErr := (newInstallErrors(testRuntime())).Failure(&selfupdate.Failure{Kind: kind, Err: errors.New("boom")})
			selfUpdateErr := (selfUpdateErrors{runtime: testRuntime()}).Failure(&selfupdate.Failure{Kind: kind, Err: errors.New("boom")})
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
