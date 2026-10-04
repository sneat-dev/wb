package cmdinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbupdate" // AC: install#req:upgrade-command-name, cli-install#req:host-identity-from-catalog
	// cli-install#req:host-identity-from-catalog — a host id absent from the
	// compiled catalog is a programming error the command constructor panics
	// on, mirroring install's own TestInstallCmd_PanicsForUnknownHostID.
	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
)

func TestUpgradeCmd_Registration(t *testing.T) {
	t.Parallel()
	cmd := newUpgradeCmd()
	if cmd.Name() != "upgrade" {
		t.Errorf("Name() = %q, want %q", cmd.Name(), "upgrade")
	}
}

func TestUpgradeCmd_PanicsForUnknownHostID(t *testing.T) {
	t.Parallel()
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected cobracmd.NewUpgrade to panic for an unregistered host id")
			}
		}()
		cobracmd.NewUpgrade(cobracmd.UpgradeCommandOptions{HostID: "nosuchhost-wb-test"})
	}()
}

// AC: install#req:upgrade-exit-code-mapping — upgradeErrors.Failure reuses
// fleetErrors.Failure exactly (embedding, not a hand-rolled duplicate
// switch), so it maps KindUnknownTarget to exitUsage (review S2) and every
// other FailureKind, including the two remaining kinds cli-install appends
// after KindManagedCommand, to wb's exitFindings exactly like install's own
// mapper does — see TestInstallErrors_FailureExitCodes for the per-kind
// table this mirrors — but with its own exact "upgrade: " prefix (review
// S1), never "install:"/"self-update:".
func TestUpgradeErrors_FailureExitCodes(t *testing.T) {
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
			mapped := (newUpgradeErrors(testRuntime())).Failure(&selfupdate.Failure{Kind: testCase.kind, Err: errors.New("boom")})
			var coded *exitError
			if !errors.As(mapped, &coded) {
				t.Fatalf("Failure(%v) did not return an *exitError: %v", testCase.kind, mapped)
			}
			if coded.code != testCase.want {
				t.Errorf("code = %d, want %d", coded.code, testCase.want)
			}
			if !strings.HasPrefix(coded.message, "upgrade: ") {
				t.Errorf("message %q does not carry the exact upgrade: prefix", coded.message)
			}
		})
	}
}

// upgradeErrors.Failure and installErrors.Failure MUST agree on the exit
// CODE for every shared FailureKind, since both embed fleetErrors and
// differ only by prefix/remedy (cli-install#req:host-owned-exit-codes:
// "The upgrade command MUST use the same error mapper"); their MESSAGES are
// deliberately not identical any more — each carries its own command's
// exact prefix (review S1), asserted here.
func TestUpgradeErrors_FailureMatchesInstallErrors(t *testing.T) {
	t.Parallel()
	shared := []selfupdate.FailureKind{
		selfupdate.KindUnknownTarget,
		selfupdate.KindAmbiguous, selfupdate.KindReleaseLookup, selfupdate.KindDownload,
		selfupdate.KindChecksum, selfupdate.KindPermission, selfupdate.KindNonInteractive,
		selfupdate.KindDowngrade, selfupdate.KindUnknownTag, selfupdate.KindUnsupportedPlatform,
		selfupdate.KindManagedCommand, selfupdate.KindUnexpected,
	}
	for _, kind := range shared {
		t.Run(kind.String(), func(t *testing.T) {
			t.Parallel()
			upgradeErr := (newUpgradeErrors(testRuntime())).Failure(&selfupdate.Failure{Kind: kind, Err: errors.New("boom")})
			installErr := (newInstallErrors(testRuntime())).Failure(&selfupdate.Failure{Kind: kind, Err: errors.New("boom")})
			var upgradeCoded, installCoded *exitError
			if !errors.As(upgradeErr, &upgradeCoded) {
				t.Fatalf("upgrade error %v does not resolve to an *exitError", upgradeErr)
			}
			if !errors.As(installErr, &installCoded) {
				t.Fatalf("install error %v does not resolve to an *exitError", installErr)
			}
			if upgradeCoded.code != installCoded.code {
				t.Errorf("upgrade code = %d, install code = %d; want equal for shared kind %v", upgradeCoded.code, installCoded.code, kind)
			}
			if !strings.HasPrefix(upgradeCoded.message, "upgrade: ") {
				t.Errorf("upgrade message %q does not carry the exact upgrade: prefix", upgradeCoded.message)
			}
			if !strings.HasPrefix(installCoded.message, "install: ") {
				t.Errorf("install message %q does not carry the exact install: prefix", installCoded.message)
			}
		})
	}
}

// The permission remedy for an upgrade failure names upgrade's own Homebrew
// cask-UPGRADE command, not install's cask-install command (review S1:
// "The permission remedy for upgrade should be the upgrade command, not
// brew install").
func TestUpgradeErrors_FailurePermissionNamesUpgradeCommand(t *testing.T) {
	t.Parallel()
	mapped := (newUpgradeErrors(testRuntime())).Failure(&selfupdate.Failure{
		Kind: selfupdate.KindPermission,
		Path: "/usr/local/bin/specscore",
		Err:  errors.New("permission denied"),
	})
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(permission) did not return an *exitError: %v", mapped)
	}
	if !strings.Contains(coded.message, wbupdate.HomebrewUpgradeCommand) {
		t.Errorf("message %q does not name upgrade's own cask-upgrade remedy %q", coded.message, wbupdate.HomebrewUpgradeCommand)
	}
	if strings.Contains(coded.message, wbupdate.HomebrewInstallCommand) {
		t.Errorf("message %q names install's own cask-install remedy instead of upgrade's", coded.message)
	}
}

// AC: cli-install#req:upgrade-check — UpgradesAvailable maps a --check
// verdict carrying at least one available/undetermined upgrade onto wb's
// exitFindings, mirroring selfUpdateErrors.UpdateAvailable's own no-fourth-
// code choice (REQ: exit-code-mapping).
func TestUpgradeErrors_UpgradesAvailableMapsToExitFindings(t *testing.T) {
	t.Parallel()
	cases := []cliinstall.UpgradeResult{
		{Target: "specscore", Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable},
		{Target: "wb", Current: "unknown", Latest: "1.1.0", Verdict: selfupdate.Undetermined},
	}
	mapped := (newUpgradeErrors(testRuntime())).UpgradesAvailable(cases)
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("UpgradesAvailable(%+v) did not return an *exitError: %v", cases, mapped)
	}
	if coded.code != exitFindings {
		t.Errorf("code = %d, want exitFindings (%d)", coded.code, exitFindings)
	}
	for _, want := range []string{"specscore", "1.0.0", "1.1.0", "wb", "undetermined"} {
		if !strings.Contains(coded.Error(), want) {
			t.Errorf("message %q missing %q", coded.Error(), want)
		}
	}
}

// AC: cli-install#req:upgrade-check ("a host maps it as its self-update
// maps UpdateAvailable") — upgradeErrors.UpgradesAvailable and
// selfUpdateErrors.UpdateAvailable MUST agree on the exit code for the
// identical underlying verdict, matching install#req:upgrade-exit-code-
// mapping's own equivalence requirement.
func TestUpgradeErrors_UpgradesAvailableMatchesSelfUpdateUpdateAvailable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		results []cliinstall.UpgradeResult
		check   selfupdate.CheckResult
	}{
		{
			name:    "update_available",
			results: []cliinstall.UpgradeResult{{Target: "wb", Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable}},
			check:   selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable},
		},
		{
			name:    "undetermined",
			results: []cliinstall.UpgradeResult{{Target: "wb", Current: "unknown", Latest: "1.1.0", Verdict: selfupdate.Undetermined}},
			check:   selfupdate.CheckResult{Current: "unknown", Latest: "1.1.0", Verdict: selfupdate.Undetermined},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			upgradeErr := (newUpgradeErrors(testRuntime())).UpgradesAvailable(testCase.results)
			selfUpdateErr := (selfUpdateErrors{runtime: testRuntime()}).UpdateAvailable(testCase.check)
			var upgradeCoded, selfUpdateCoded *exitError
			if !errors.As(upgradeErr, &upgradeCoded) {
				t.Fatalf("upgrade error %v does not resolve to an *exitError", upgradeErr)
			}
			if !errors.As(selfUpdateErr, &selfUpdateCoded) {
				t.Fatalf("self-update error %v does not resolve to an *exitError", selfUpdateErr)
			}
			if upgradeCoded.code != selfUpdateCoded.code {
				t.Errorf("upgrade code = %d, self-update code = %d; want equal (cli-install#req:upgrade-check)", upgradeCoded.code, selfUpdateCoded.code)
			}
		})
	}
}

// AC: install#req:upgrade-host-config-and-hook, self-update-equals-upgrade-
// self — afterUpdate is the SAME factory both NewSelfUpdate
// and NewUpgrade configure. Invoking it for two independent
// command instances with the identical update outcome MUST produce
// identical output, proving `wb self-update` and `wb upgrade wb` reach the
// same after-update hook rather than two copies that could drift.
func TestWBAfterUpdate_SelfUpdateAndUpgradeCommandsProduceIdenticalOutput(t *testing.T) {
	t.Parallel()
	var selfUpdateCmd, upgradeCmd *cobra.Command
	selfUpdateCmd = &cobra.Command{Use: "self-update"}
	selfUpdateCmd.Flags().String("format", "text", "")
	upgradeCmd = &cobra.Command{Use: "upgrade"}
	upgradeCmd.Flags().String("format", "text", "")

	var selfOut, selfErr, upgradeOut, upgradeErr bytes.Buffer
	selfUpdateCmd.SetOut(&selfOut)
	selfUpdateCmd.SetErr(&selfErr)
	upgradeCmd.SetOut(&upgradeOut)
	upgradeCmd.SetErr(&upgradeErr)

	update := selfupdate.AfterUpdate{}
	operation := func(_ context.Context, got selfupdate.AfterUpdate, out wbupdate.Output) error {
		if !reflect.DeepEqual(got, update) || out.JSON {
			t.Fatalf("handoff update=%+v JSON=%v", got, out.JSON)
		}
		_, err := fmt.Fprint(out.Out, "recorded update")
		return err
	}
	if err := afterUpdate(&selfUpdateCmd, operation)(context.Background(), update); err != nil {
		t.Fatalf("self-update-shaped hook: %v", err)
	}
	if err := afterUpdate(&upgradeCmd, operation)(context.Background(), update); err != nil {
		t.Fatalf("upgrade-shaped hook: %v", err)
	}
	if selfOut.String() != upgradeOut.String() {
		t.Errorf("self-update hook stdout = %q, upgrade hook stdout = %q; want identical (cli-install#req:self-update-equals-upgrade-self)", selfOut.String(), upgradeOut.String())
	}
	if selfErr.String() != upgradeErr.String() {
		t.Errorf("self-update hook stderr = %q, upgrade hook stderr = %q; want identical (cli-install#req:self-update-equals-upgrade-self)", selfErr.String(), upgradeErr.String())
	}
	if selfOut.Len() == 0 {
		t.Fatal("hook produced no output; test fixture did not exercise the shared path")
	}
}

// AC: review M5 — afterUpdate must never panic if a future refactor
// leaves *cmd nil by the time AfterUpdate fires (today's declare-build-
// assign order makes that unreachable, but the hook stays defensive
// against it): it falls back to a fresh *cobra.Command whose Out/Err
// default to the process's own stdout/stderr rather than dereferencing nil.
func TestWBAfterUpdate_NilCommandPointerFallsBackWithoutPanic(t *testing.T) {
	t.Parallel()
	var nilCmd *cobra.Command
	called := false
	hook := afterUpdate(&nilCmd, func(_ context.Context, _ selfupdate.AfterUpdate, out wbupdate.Output) error {
		called = true
		if out.Out == nil || out.Err == nil || out.JSON {
			t.Fatalf("fallback=%+v", out)
		}
		return nil
	})
	if err := hook(context.Background(), selfupdate.AfterUpdate{}); err != nil {
		t.Fatalf("hook with nil command pointer = %v, want nil", err)
	}
	if !called {
		t.Fatal("operation was not called")
	}
}

func TestAfterUpdateReadsCurrentJSONStreamsAndPreservesCallbackError(t *testing.T) {
	t.Parallel()
	command := &cobra.Command{Use: "self-update"}
	command.Flags().String("format", "text", "")
	var oldOut, oldErr, currentOut, currentErr bytes.Buffer
	command.SetOut(&oldOut)
	command.SetErr(&oldErr)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	update := selfupdate.AfterUpdate{Outcome: selfupdate.Outcome{Action: selfupdate.ActionManagerExecuted, Result: selfupdate.CheckResult{Current: "1.0.0", Latest: "1.1.0"}}, Executable: selfupdate.ExecutableIdentity{Path: "/verified/provider"}}
	sentinel := errors.New("callback failed")
	calls := 0
	callback := afterUpdate(&command, func(gotCtx context.Context, gotUpdate selfupdate.AfterUpdate, out wbupdate.Output) error {
		calls++
		if gotCtx != ctx || !reflect.DeepEqual(gotUpdate, update) {
			t.Fatalf("context/update changed: %v %+v", gotCtx, gotUpdate)
		}
		if !out.JSON || out.Out != &currentOut || out.Err != &currentErr {
			t.Fatalf("adapter did not read current output bindings: %+v", out)
		}
		return sentinel
	})
	command.SetOut(&currentOut)
	command.SetErr(&currentErr)
	if err := command.ParseFlags([]string{"--format=json"}); err != nil {
		t.Fatal(err)
	}
	if err := callback(ctx, update); err != sentinel {
		t.Fatalf("error=%v want original sentinel", err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
}
