package daemonruntime

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/daemon"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStatusSeparatesIdentityNativeRecordAndTransportEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"absent-refused", "current-ready", "bridge-success", "bridge-refused", "bridge-default", "provenance-refused"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			root := cwWtDaemonRoot(t)
			deps := daemonTestDependencies(t, root)
			deps.Alive = func(pid int) bool { return pid == 900 }
			controller := NewController(deps, root)
			provenance, err := controller.Provenance()
			if err != nil {
				t.Fatal(err)
			}
			state := daemonTestState(t, root, DefaultListen, provenance, "owner", deps.Now())
			state.MarkReady(900, deps.Now())
			failure := errors.New("native observer refused")
			if mode != "absent-refused" {
				if err := controller.store.Save(state); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "absent-refused":
				controller.deps.Health = func(context.Context, string) error { return failure }
			case "provenance-refused":
				controller.deps.Executable = func() (string, error) { return "", failure }
			case "bridge-success", "bridge-refused", "bridge-default":
				controller.deps.Health = func(context.Context, string) error { return os.ErrPermission }
				if mode != "bridge-default" {
					controller.deps.BridgeHealth = func(context.Context, string, string) error {
						if mode == "bridge-refused" {
							return failure
						}
						return nil
					}
				}
			}
			result, err := controller.Status(context.Background())
			if mode == "provenance-refused" {
				if !errors.Is(err, failure) {
					t.Fatalf("error=%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "absent-refused":
				if result.Managed || result.Reachable || result.ReachabilityError != failure.Error() {
					t.Fatalf("status=%+v", result)
				}
			case "current-ready":
				if !result.ReadyVerified || result.Identity != identityCurrent {
					t.Fatalf("status=%+v", result)
				}
			case "bridge-success":
				if !result.Reachable || result.ReachabilityTransport != "file_bridge" || result.DirectTransportReachable {
					t.Fatalf("status=%+v", result)
				}
			default:
				if result.Reachable || !strings.Contains(result.ReachabilityError, "protected file bridge") {
					t.Fatalf("status=%+v", result)
				}
			}
		})
	}
}

func TestHubStatusProjectsActualConfigurationAndCompleteLiveRedelivery(t *testing.T) {
	t.Parallel()
	root := cwWtDaemonRoot(t)
	deps := daemonTestDependencies(t, root)
	path := hubTestConfig(t, fmt.Sprintf("hub:\n  store:\n    engine: memory\n    path: %s\n  github:\n    app:\n      app_id: 1\n      private_key_file: %s\n      webhook_secret_file: %s\n      public_url: https://private.example.test\n", root, filepath.Join(root, "app.pem"), filepath.Join(root, "secret")))
	deps.HubConfigPath = func() string { return path }
	at := time.Date(2026, 10, 4, 1, 2, 3, 0, time.UTC)
	failureAt := at.Add(-time.Minute)
	live := &HubRedeliverySweep{LastSweepAt: &at, Redelivered: 2, Abandoned: 3, Uncounted: 4, LastFailureAt: &failureAt, LastFailureClass: "private-refusal"}
	deps.HubHealth = func(context.Context, string) (HubStatus, error) { return HubStatus{WebhookRedelivery: live}, nil }
	status := NewController(deps, root).hubStatus(context.Background(), DefaultListen)
	if !status.Webhook || status.WebhookPublicURL != "https://private.example.test" || !reflect.DeepEqual(status.WebhookRedelivery, live) {
		t.Fatalf("status=%+v", status)
	}
}

//nolint:paralleltest // This native fixture or its helper changes process-wide HOME, PATH or supervisor environment; testing restores it.
func TestStatusReportsGenuineRetiredHomeWithoutMutatingIt(t *testing.T) {
	root := daemonTestRoot(t)
	legacyDir := daemonLegacyFixture(t)
	deps := daemonTestDependencies(t, root)
	deps.Alive = func(pid int) bool { return pid == 4321 }
	legacy := daemon.NewStartingAt(nil, DefaultListen, daemon.Provenance{Executable: "old", SHA256: "old"}, "legacy-owner", "", "", deps.Now())
	legacy.MarkReady(4321, deps.Now())
	if err := (daemon.Store{Path: daemonLegacyStatePath(legacyDir)}).Save(legacy); err != nil {
		t.Fatal(err)
	}
	before := daemonTestDirectorySnapshot(t, legacyDir)
	result, err := NewController(deps, root).Status(context.Background())
	if err != nil || result.LegacyRuntime == nil || !result.LegacyRuntime.StateLive || result.LegacyRuntime.StatePID != 4321 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if after := daemonTestDirectorySnapshot(t, legacyDir); before != after {
		t.Fatalf("retired home mutated before=%s after=%s", before, after)
	}
}
