package cmdinstall

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/selfupdate" // TestNewSelfUpdateCmdRegistration pins REQ: command-and-alias: the command
	// is named "self-update" and answers to the "update" alias, with --check,
	// --format json (JSONFormat), --version, and --allow-downgrade all present
	// (registered by cobracmd.New, not reimplemented here).
)

func TestNewSelfUpdateCmdRegistration(t *testing.T) {
	t.Parallel()
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

// TestSelfUpdateErrorsFailureMapsToExitFindings pins REQ: exit-code-mapping:
// every operational failure — release lookup, download, checksum,
// non-interactive refusal, unknown tag, refused downgrade, and any other
// non-permission *selfupdate.Failure — is exitFindings (1), never a fourth
// code.
func TestSelfUpdateErrorsFailureMapsToExitFindings(t *testing.T) {
	t.Parallel()
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
			t.Parallel()
			mapped := selfUpdateErrors{runtime: testRuntime()}.Failure(testCase.err)
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
	t.Parallel()
	err := &selfupdate.Failure{
		Kind: selfupdate.KindPermission,
		Path: "/usr/local/bin/wb",
		Err:  fs.ErrPermission,
	}

	mapped := selfUpdateErrors{runtime: testRuntime()}.Failure(err)
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
	t.Parallel()
	err := &selfupdate.Failure{Kind: selfupdate.KindPermission, Err: fs.ErrPermission}
	mapped := selfUpdateErrors{runtime: testRuntime()}.Failure(err)
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
	t.Parallel()
	cases := []selfupdate.CheckResult{
		{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable},
		{Current: "unknown", Latest: "1.1.0", Verdict: selfupdate.Undetermined},
	}
	for _, result := range cases {
		t.Run(result.Verdict.String(), func(t *testing.T) {
			t.Parallel()
			mapped := selfUpdateErrors{runtime: testRuntime()}.UpdateAvailable(result)
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
