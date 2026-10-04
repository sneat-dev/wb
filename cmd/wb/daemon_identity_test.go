package main

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
)

// AC: runtime-moves-with-the-home

// AC: runtime-moves-with-the-home (the reported socket path is the bound one)

// AC: status-cannot-be-fooled-by-a-stranger
func TestDaemonStatusReportsAReachableDaemonFromAnotherHome(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonruntime.DefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.WBHome = filepath.Join(string(filepath.Separator), "an", "abandoned", "home")
	state.MarkReady(900, time.Now())
	store := daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 900 }
	deps.Health = func(context.Context, string) error { return nil }

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity != daemonruntime.Identity("foreign_home") {
		t.Fatalf("identity = %q, want %q (%s)", result.Identity, daemonruntime.Identity("foreign_home"), result.IdentityDetail)
	}
	if result.ReadyVerified || result.ReportedState != "unverified" {
		t.Fatalf("foreign daemon reported ready: %#v", result)
	}
	if !result.Reachable || result.ProcessManagerRunning {
		t.Fatalf("reachability and ownership were folded together: %#v", result)
	}
	for _, want := range []string{"an/abandoned/home", result.WBHome} {
		if !strings.Contains(result.IdentityDetail, want) {
			t.Fatalf("identity detail %q does not name %q", result.IdentityDetail, want)
		}
	}
	var text bytes.Buffer
	if err := writeDaemonResult(&text, "text", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state=unverified", "identity=foreign_home", "ready_verified=false", "api_reachable=true"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text status %q does not contain %q", text.String(), want)
		}
	}
	// The record is evidence, not something this home may rewrite.
	stored, found, err := store.Load()
	if err != nil || !found || stored.Status != daemon.StatusReady || stored.WBHome != state.WBHome {
		t.Fatalf("foreign record was modified: %#v, %t, %v", stored, found, err)
	}
}

// AC: status-cannot-be-fooled-by-a-stranger (provenance is its own condition)

// A record written before home identity cannot be shown to belong to this home,
// so it is reported as unverified rather than assumed to match.

// A record that names a different state file describes a daemon this home does
// not read, so it cannot be presented as this home's daemon either.

// AC: a-leftover-daemon-cannot-be-silently-doubled

// AC: a-leftover-daemon-cannot-be-silently-doubled (a held loopback endpoint)
func TestServeDashboardNamesTheEndpointItCouldNotBind(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	address := held.Addr().String()
	store := daemon.Store{Path: mustDaemonPath(t, daemonruntime.StatePath, root)}

	err = serveDashboard(&invocation{}, &cobra.Command{}, deps, address, store, "owner-token", true, false)
	if err == nil {
		t.Fatal("serving on a held endpoint must fail")
	}
	for _, want := range []string{address, "already held"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("bind failure %q does not contain %q", err.Error(), want)
		}
	}
	if _, found, loadErr := store.Load(); loadErr != nil || found {
		t.Fatalf("a failed bind must not record a lifecycle state: found=%t err=%v", found, loadErr)
	}
}

// AC: a-daemon-outlives-its-directory-only-by-stopping

// AC: units-do-not-bake-in-a-resolved-path

func TestReportPinnedLifecycleStateIsNeverSilent(t *testing.T) {
	var out bytes.Buffer
	reportPinnedLifecycleState(&out, "", "/home/a/.wb/runtime/daemon-state.json")
	if out.String() != "" {
		t.Fatalf("an unpinned daemon reported %q", out.String())
	}
	reportPinnedLifecycleState(&out, "/old/daemon-state.json", "/home/a/.wb/runtime/daemon-state.json")
	for _, want := range []string{"/old/daemon-state.json", "/home/a/.wb/runtime/daemon-state.json"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("pinned report %q does not name %q", out.String(), want)
		}
	}
}

// The failure this feature was written after: a daemon answers, this home
// records nothing, and the leftover lives in a state home WB used to write.
// Both facts must be reportable at once.
func TestDaemonStatusNamesAnUnrecordedAnswerAndTheLegacyEndpoint(t *testing.T) {
	root := daemonTestRoot(t)
	// The current home is <root>/.wb now, so the leftover sits in the retired
	// default state home $HOME/.wb.
	legacyDir := daemonLegacyFixture(t)
	deps := daemonTestDependencies(t, root)
	deps.Alive = func(pid int) bool { return pid == 700 }
	legacy := daemon.NewStartingAt(nil, daemonruntime.DefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "legacy-owner", "", "", time.Now())
	legacy.MarkReady(700, time.Now())
	if err := (daemon.Store{Path: daemonLegacyStatePath(legacyDir)}).Save(legacy); err != nil {
		t.Fatal(err)
	}

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Managed || result.Identity != daemonruntime.Identity("absent") || result.ReportedState != "absent" {
		t.Fatalf("status of a home with no daemon = %#v", result)
	}
	if !result.Reachable {
		t.Fatal("something answering on the configured endpoint must be reported")
	}
	if result.LegacyRuntime == nil || result.LegacyRuntime.RuntimeDir != legacyDir {
		t.Fatalf("legacy endpoint = %#v, want %s", result.LegacyRuntime, legacyDir)
	}
	var text bytes.Buffer
	if err := writeDaemonResult(&text, "text", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state=absent", "api_reachable=true", "identity=absent", "legacy_runtime="} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text status %q does not contain %q", text.String(), want)
		}
	}
}
