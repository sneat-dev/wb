package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
)

// AC: install#req:upgrade-command-name, cli-install#req:host-identity-from-catalog
func TestUpgradeCmd_Registration(t *testing.T) {
	cmd := newUpgradeCmd()
	if cmd.Name() != "upgrade" {
		t.Errorf("Name() = %q, want %q", cmd.Name(), "upgrade")
	}
}

// wb upgrade is registered at the root and its help renders without any
// injected environment or network access.
func TestUpgradeCmd_RegisteredAtRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"upgrade", "--help"}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("wb upgrade --help exit = %d, stderr = %s", code, stderr.String())
	}
	for _, want := range []string{"upgrade", "--all", "--check", "--dry-run", "--yes"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help does not mention %q:\n%s", want, stdout.String())
		}
	}
}

// cli-install#req:host-identity-from-catalog — a host id absent from the
// compiled catalog is a programming error the command constructor panics
// on, mirroring install's own TestInstallCmd_PanicsForUnknownHostID.
func TestUpgradeCmd_PanicsForUnknownHostID(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected cobracmd.NewUpgrade to panic for an unregistered host id")
		}
	}()
	cobracmd.NewUpgrade(cobracmd.UpgradeCommandOptions{HostID: "nosuchhost-wb-test"})
}

// AC: install#req:upgrade-exit-code-mapping, cli-install#req:upgrade-targets
// (unknown-target-refused), review S2 — `wb upgrade nosuchcli` MUST fail
// before any confirmation, network request or write, exits exitUsage (the
// invocation was rejected before any work started), and the message
// carries an exact "upgrade: " prefix and names the unknown target, never
// "install:"/"self-update:" (review S1). namedUpgradeCandidates rejects
// every unknown name before probing or looking up anything, so this is
// offline-safe with no injected Env.
func TestUpgradeCmd_UnknownTargetExitsUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"upgrade", "nosuchcli"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage (%d); stderr: %s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "upgrade: ") {
		t.Errorf("stderr does not carry the exact upgrade: prefix: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "nosuchcli") {
		t.Errorf("stderr does not name the unknown target: %q", stderr.String())
	}
	for _, wrongPrefix := range []string{"install:", "self-update:"} {
		if strings.Contains(stderr.String(), wrongPrefix) {
			t.Errorf("stderr carries a %s prefix; upgrade errors MUST NOT: %q", wrongPrefix, stderr.String())
		}
	}
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
			mapped := (newUpgradeErrors()).Failure(&selfupdate.Failure{Kind: testCase.kind, Err: errors.New("boom")})
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
	shared := []selfupdate.FailureKind{
		selfupdate.KindUnknownTarget,
		selfupdate.KindAmbiguous, selfupdate.KindReleaseLookup, selfupdate.KindDownload,
		selfupdate.KindChecksum, selfupdate.KindPermission, selfupdate.KindNonInteractive,
		selfupdate.KindDowngrade, selfupdate.KindUnknownTag, selfupdate.KindUnsupportedPlatform,
		selfupdate.KindManagedCommand, selfupdate.KindUnexpected,
	}
	for _, kind := range shared {
		t.Run(kind.String(), func(t *testing.T) {
			upgradeErr := (newUpgradeErrors()).Failure(&selfupdate.Failure{Kind: kind, Err: errors.New("boom")})
			installErr := (newInstallErrors()).Failure(&selfupdate.Failure{Kind: kind, Err: errors.New("boom")})
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
	mapped := (newUpgradeErrors()).Failure(&selfupdate.Failure{
		Kind: selfupdate.KindPermission,
		Path: "/usr/local/bin/specscore",
		Err:  errors.New("permission denied"),
	})
	var coded *exitError
	if !errors.As(mapped, &coded) {
		t.Fatalf("Failure(permission) did not return an *exitError: %v", mapped)
	}
	if !strings.Contains(coded.message, selfUpdateHomebrewUpgradeCommand) {
		t.Errorf("message %q does not name upgrade's own cask-upgrade remedy %q", coded.message, selfUpdateHomebrewUpgradeCommand)
	}
	if strings.Contains(coded.message, selfUpdateHomebrewInstallCommand) {
		t.Errorf("message %q names install's own cask-install remedy instead of upgrade's", coded.message)
	}
}

// AC: cli-install#req:upgrade-check — UpgradesAvailable maps a --check
// verdict carrying at least one available/undetermined upgrade onto wb's
// exitFindings, mirroring selfUpdateErrors.UpdateAvailable's own no-fourth-
// code choice (REQ: exit-code-mapping).
func TestUpgradeErrors_UpgradesAvailableMapsToExitFindings(t *testing.T) {
	cases := []cliinstall.UpgradeResult{
		{Target: "specscore", Current: "1.0.0", Latest: "1.1.0", Verdict: selfupdate.UpdateAvailable},
		{Target: "wb", Current: "unknown", Latest: "1.1.0", Verdict: selfupdate.Undetermined},
	}
	mapped := (newUpgradeErrors()).UpgradesAvailable(cases)
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
			upgradeErr := (newUpgradeErrors()).UpgradesAvailable(testCase.results)
			selfUpdateErr := (selfUpdateErrors{}).UpdateAvailable(testCase.check)
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

// install#req:upgrade-host-config-and-hook — newSelfUpdateCmd and
// newUpgradeCmd both build their host Config from the IDENTICAL
// newSelfUpdateConfig() constructor (see each function's own source: not a
// second hand-copied literal). This pins that the constructor is a pure,
// deterministic function of the compiled catalog and the running version,
// so two separate call sites can never quietly diverge onto different
// values.
func TestUpgradeCmd_HostConfigConstructorIsDeterministic(t *testing.T) {
	first := newSelfUpdateConfig()
	second := newSelfUpdateConfig()
	if !reflect.DeepEqual(first, second) {
		t.Errorf("newSelfUpdateConfig() is not deterministic:\n%+v\n%+v", first, second)
	}
}

// AC: install#req:upgrade-host-config-and-hook, self-update-equals-upgrade-
// self — wbAfterUpdate is the SAME factory both newSelfUpdateCmdWithConfig
// and newUpgradeCmdWithConfig configure. Invoking it for two independent
// command instances with the identical update outcome MUST produce
// identical output, proving `wb self-update` and `wb upgrade wb` reach the
// same after-update hook rather than two copies that could drift.
func TestWBAfterUpdate_SelfUpdateAndUpgradeCommandsProduceIdenticalOutput(t *testing.T) {
	binary := fakeSelfUpdateBinary(t, `echo "synced: $1 $2"`)
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

	update := successfulSelfUpdate(binary)
	if err := wbAfterUpdate(&selfUpdateCmd)(context.Background(), update); err != nil {
		t.Fatalf("self-update-shaped hook: %v", err)
	}
	if err := wbAfterUpdate(&upgradeCmd)(context.Background(), update); err != nil {
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

// AC: review M5 — wbAfterUpdate must never panic if a future refactor
// leaves *cmd nil by the time AfterUpdate fires (today's declare-build-
// assign order makes that unreachable, but the hook stays defensive
// against it): it falls back to a fresh *cobra.Command whose Out/Err
// default to the process's own stdout/stderr rather than dereferencing nil.
func TestWBAfterUpdate_NilCommandPointerFallsBackWithoutPanic(t *testing.T) {
	binary := fakeSelfUpdateBinary(t, `echo "synced: $1 $2"`)
	var nilCmd *cobra.Command
	hook := wbAfterUpdate(&nilCmd)
	if err := hook(context.Background(), successfulSelfUpdate(binary)); err != nil {
		t.Fatalf("hook with nil command pointer = %v, want nil", err)
	}
}

// AC: install#req:upgrade-host-config-and-hook, cli-install#req:self-update-
// equals-upgrade-self — with the identical selfupdate.Config wb's own
// self-update command would build, `cliinstall.CheckUpgrades`'s host row
// (the library call `wb upgrade wb --check` makes) MUST report the exact
// same current/latest/verdict that `selfupdate.Config.Check` (the library
// call `wb self-update --check` makes) reports, over a hermetic fake
// GitHub releases endpoint — reusing the library's own exported Check/
// CheckUpgrades API as the test seam (no network, no real os.Executable
// dependency: CheckUpgrades' own Current/Latest/Verdict come from calling
// Config.Check regardless of how the host's own binary classifies).
func TestSelfUpdateAndUpgradeSelf_ReachSameCheckVerdict(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"tag_name":"v9.9.9","prerelease":false,"draft":false}]`))
	}))
	defer server.Close()

	cfg := newSelfUpdateConfig()
	cfg.CurrentVersion = "0.1.0"
	cfg.ReleasesAPIURL = server.URL
	cfg.HTTPClient = server.Client()

	ctx := context.Background()

	selfUpdateResult, err := cfg.Check(ctx)
	if err != nil {
		t.Fatalf("self-update side Config.Check: %v", err)
	}
	if selfUpdateResult.Verdict != selfupdate.UpdateAvailable {
		t.Fatalf("fixture sanity: self-update side verdict = %v, want UpdateAvailable", selfUpdateResult.Verdict)
	}

	plan, err := cliinstall.CheckUpgrades(ctx, []string{wbCatalogID}, cliinstall.UpgradeOptions{
		HostID:     wbCatalogID,
		HostConfig: cfg,
		Env:        cliinstall.DefaultInstallEnv(),
	})
	if err != nil {
		t.Fatalf("upgrade side CheckUpgrades: %v", err)
	}
	var hostRow *cliinstall.UpgradeResult
	for i := range plan.Results {
		if plan.Results[i].Host {
			hostRow = &plan.Results[i]
			break
		}
	}
	if hostRow == nil {
		t.Fatalf("CheckUpgrades produced no host row for %q: %+v", wbCatalogID, plan.Results)
	}
	if hostRow.Current != selfUpdateResult.Current || hostRow.Latest != selfUpdateResult.Latest || hostRow.Verdict != selfUpdateResult.Verdict {
		t.Errorf("upgrade wb --check = {current:%s latest:%s verdict:%s}, self-update --check = {current:%s latest:%s verdict:%s}; want identical (cli-install#req:self-update-equals-upgrade-self)",
			hostRow.Current, hostRow.Latest, hostRow.Verdict,
			selfUpdateResult.Current, selfUpdateResult.Latest, selfUpdateResult.Verdict)
	}
}
