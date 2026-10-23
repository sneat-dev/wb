package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
)

//nolint:paralleltest // daemonTestRoot pins process-wide WB_PROJECTS_ROOT; each child owns a private state file and controlled process dependencies.
func TestPeerAdminClientRechecksPrivateCustodyAfterControlledHealth(t *testing.T) {
	for _, fault := range []string{"malformed state", "stopped state", "local client refusal"} {
		t.Run(fault, func(t *testing.T) {
			root := daemonTestRoot(t)
			deps := daemonTestDependencies(t, root)
			controller := newDaemonController(deps, root)
			// The real controller persists ready custody using the established
			// controlled process observations; no daemon process is launched.
			if result, err := controller.Start(context.Background(), daemonruntime.DefaultListen); err != nil || !result.ProcessManagerRunning {
				t.Fatalf("prime private controller: result=%+v error=%v", result, err)
			}
			state, found, err := controller.LoadState()
			if err != nil || !found || state.Status != daemon.StatusReady || state.OwnerToken == "" {
				t.Fatalf("private ready custody: state=%+v found=%v error=%v", state, found, err)
			}
			path := mustDaemonPath(t, daemonruntime.StatePath, root)
			sentinel := errors.New("controlled local client refusal")
			deps.LocalClient = func(gotRoot, token string) (*http.Client, error) {
				if fault != "local client refusal" {
					t.Fatal("local client constructed after custody became invalid")
				}
				if gotRoot != root || token != state.OwnerToken {
					t.Fatalf("local client lost private owner authority: root=%q token matches=%v", gotRoot, token == state.OwnerToken)
				}
				return nil, sentinel
			}
			deps.Health = func(_ context.Context, listen string) error {
				if listen != state.Listen {
					t.Fatalf("health observation address=%q, want %q", listen, state.Listen)
				}
				// Start's already-running path observes readiness before Health.
				// These synchronous private writes prove the later fresh read,
				// without a timing race or a call-count-dependent observation.
				switch fault {
				case "malformed state":
					if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "stopped state":
					stopped := state
					stopped.MarkStopped(deps.Now())
					if err := (daemon.Store{Path: path}).Save(stopped); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}
			client, err := newPeerAdminClient(context.Background(), deps, root)
			if client != nil || err == nil {
				t.Fatalf("invalid custody/client accepted: client=%v error=%v", client, err)
			}
			switch fault {
			case "malformed state":
				var syntax *json.SyntaxError
				if !errors.As(err, &syntax) || !strings.HasPrefix(err.Error(), "decode daemon state:") {
					t.Fatalf("fresh state decode error lost identity or gained Start wrapping: %v", err)
				}
			case "stopped state":
				if err.Error() != "local daemon is not ready" {
					t.Fatalf("fresh stopped custody refusal=%v", err)
				}
			case "local client refusal":
				if err != sentinel {
					t.Fatalf("local client error identity changed: %v", err)
				}
			}
		})
	}
	t.Run("controlled Start refusal", func(t *testing.T) {
		root := daemonTestRoot(t)
		deps := daemonTestDependencies(t, root)
		sentinel := errors.New("controlled process start refused")
		deps.Start = func(string, []string, string) (int, error) { return 0, sentinel }
		client, err := newPeerAdminClient(context.Background(), deps, root)
		if client != nil || !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), "start local daemon:") {
			t.Fatalf("Start refusal lost original identity/context: client=%v error=%v", client, err)
		}
	})
}

//nolint:paralleltest // daemonTestRoot pins process-wide WB_PROJECTS_ROOT; malformed custody is written only inside that private root.
func TestDaemonListenAddressPreservesPrivateStateReadFailure(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	path := mustDaemonPath(t, daemonruntime.StatePath, root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	address, err := daemonListenAddress(deps, root)
	var syntax *json.SyntaxError
	if address != "" || !errors.As(err, &syntax) || !strings.HasPrefix(err.Error(), "decode daemon state:") {
		t.Fatalf("read-only malformed custody error: address=%q error=%v", address, err)
	}
}
