package main

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"
)

// peerAdminTestMount builds a real *hubMount over a memory-engine store
// (mountHub does not bind any listener, so no port is needed) and wraps its
// owner-token RPC handler exactly as serveDashboard does.

// TestPeerAdminRoutesRequireTheOwnerToken is AC:identity-and-admission's
// "Unauthorised admin: no admin operation succeeds ... without the admin
// [owner] credential" at the RPC layer: every route refuses a missing or
// wrong token before it ever reaches PeerAdminService.

// TestPeerAdminRoutesAreUnreachableFromTheDashboardListener is AC:identity-
// and-admission's "Enrollment route" cousin for the whole admin surface:
// none of it is part of the TCP dashboard mux mount.Mounts feeds
// dashboard.NewHandler, only the unix-socket rpcMux serveDashboard builds
// separately.

// TestPeerAdminInviteWiringReachesTheRealService proves the HTTP layer
// forwards to the real PeerAdminService: the memory-engine refusal
// (asserted directly against the service in hub's own tests) surfaces
// through this handler unchanged.

// TestPeerAdminBlockUnblockDisconnectRoundTrip seeds a peer directly (invite
// itself is refused on the memory engine used for this fixture) and drives
// block, unblock and disconnect through the HTTP layer.

// TestPeerAdminEnrollReusesTheEnrollmentService proves the owner RPC's
// "enroll" route (peer-connectivity#req:admin-requires-owner-credential's
// "self-hosted machine enrollment moves to that RPC service") mints a real,
// enrollment-scoped credential.

// TestPeerAdminEnrollRefusesTheHubsOwnMachineName and
// TestPeerAdminEnrollRefusesAPeerName are S4: the owner RPC's "enroll" route
// mints a plain (non-peer) machine credential, which must never shadow the
// hub's own machine name or an existing peer's name — ensureLocalEnrollment
// is the one caller allowed to enrol the hub's own name, and it never goes
// through this route.

// TestNewPeerAdminClientStartsTheLocalDaemonAndAuthenticates and
// TestDaemonListenAddressBranches cover cmd/wb's own daemon-launch seams
// directly — not just through a fake adminClient/listenAddress override, as
// every peers.go CLI test does — using daemonTestDependencies's established,
// safe, entirely in-memory start/stop/health fakes (cmd/wb/daemon_test.go),
// per the common brief's daemon-safety requirement: neither test can reach a
// real subprocess or launchd.
func TestNewPeerAdminClientStartsTheLocalDaemonAndAuthenticates(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	deps.LocalClient = func(string, string) (*http.Client, error) { return &http.Client{}, nil }
	client, err := newPeerAdminClient(context.Background(), deps, root)
	if err != nil {
		t.Fatal(err)
	}
	if client == nil || client.httpClient == nil {
		t.Fatal("newPeerAdminClient returned no usable client")
	}
	t.Run("actual default owner transport", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("the existing Windows local transport is unsupported")
		}
		root, err := os.MkdirTemp("/tmp", "wb-peer-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(root) })
		pinDaemonHome(t, root)
		deps := daemonTestDependencies(t, root)
		deps.LocalClient = nil
		client, err := newPeerAdminClient(context.Background(), deps, root)
		if err != nil {
			t.Fatal(err)
		}
		state, found, err := newDaemonController(deps, root).LoadState()
		if err != nil || !found || state.OwnerToken == "" {
			t.Fatalf("owner record=%+v found=%v error=%v", state, found, err)
		}
		listener, err := daemonruntime.ListenLocal(root)
		if err != nil {
			t.Fatal(err)
		}
		server := &http.Server{Handler: daemonruntime.AuthenticatedHandler(state.OwnerToken, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+state.OwnerToken || r.URL.Path != peersRPCPrefix+"list" {
				t.Errorf("request authority/path=%q %q", r.Header.Get("Authorization"), r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))}
		done := make(chan error, 1)
		go func() { done <- server.Serve(listener) }()
		t.Cleanup(func() { _ = server.Close(); <-done })
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		var response map[string]bool
		if err := client.call(ctx, peersRPCPrefix+"list", struct{}{}, &response); err != nil || !response["ok"] {
			t.Fatalf("owner response=%v error=%v", response, err)
		}
	})

}

func TestDaemonListenAddressBranches(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	if _, err := daemonListenAddress(deps, root); err == nil {
		t.Fatal("expected daemonListenAddress to fail before the daemon has ever started")
	}
	controller := newDaemonController(deps, root)
	if _, err := controller.Start(context.Background(), daemonruntime.DefaultListen); err != nil {
		t.Fatal(err)
	}
	listen, err := daemonListenAddress(deps, root)
	if err != nil || listen == "" {
		t.Fatalf("daemonListenAddress after start = %q, %v", listen, err)
	}
}

// TestPeerAdminHandlerWithoutAHubAnswersUnavailable proves a daemon with no
// hub section still answers every peer admin route, just unavailably,
// rather than panicking on a nil PeerAdmin.
