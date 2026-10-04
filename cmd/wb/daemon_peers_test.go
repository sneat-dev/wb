package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/dashboard"
)

// peerAdminTestMount builds a real *hubMount over a memory-engine store
// (mountHub does not bind any listener, so no port is needed) and wraps its
// owner-token RPC handler exactly as serveDashboard does.
func peerAdminTestMount(t *testing.T) (*hubMount, http.Handler) {
	t.Helper()
	configPath := memoryHubConfig(t)
	mount, err := mountHub(context.Background(), configPath, "127.0.0.1:0", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mountHub = %v, %v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	handler := daemonruntime.AuthenticatedHandler("owner-token", newPeerAdminHTTPHandler(mount))
	return mount, handler
}

func peerAdminRequest(t *testing.T, handler http.Handler, token, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// TestPeerAdminRoutesRequireTheOwnerToken is AC:identity-and-admission's
// "Unauthorised admin: no admin operation succeeds ... without the admin
// [owner] credential" at the RPC layer: every route refuses a missing or
// wrong token before it ever reaches PeerAdminService.
func TestPeerAdminRoutesRequireTheOwnerToken(t *testing.T) {
	_, handler := peerAdminTestMount(t)
	for _, path := range []string{
		peersRPCPrefix + "invite", peersRPCPrefix + "block", peersRPCPrefix + "unblock",
		peersRPCPrefix + "disconnect", peersRPCPrefix + "enroll",
	} {
		if response := peerAdminRequest(t, handler, "", path, map[string]string{}); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s with no token = %d, want 401", path, response.Code)
		}
		if response := peerAdminRequest(t, handler, "wrong-token", path, map[string]string{}); response.Code != http.StatusUnauthorized {
			t.Fatalf("%s with the wrong token = %d, want 401", path, response.Code)
		}
	}
}

// TestPeerAdminRoutesAreUnreachableFromTheDashboardListener is AC:identity-
// and-admission's "Enrollment route" cousin for the whole admin surface:
// none of it is part of the TCP dashboard mux mount.Mounts feeds
// dashboard.NewHandler, only the unix-socket rpcMux serveDashboard builds
// separately.
func TestPeerAdminRoutesAreUnreachableFromTheDashboardListener(t *testing.T) {
	mount, _ := peerAdminTestMount(t)
	dashboardHandler := dashboard.NewHandler(dashboard.Options{Mounts: mount.handlers()})
	request := httptest.NewRequest(http.MethodPost, peersRPCPrefix+"invite", strings.NewReader(`{"name":"laptop"}`))
	recorder := httptest.NewRecorder()
	dashboardHandler.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusOK {
		t.Fatalf("peer admin route answered on the TCP dashboard listener: %d %s", recorder.Code, recorder.Body.String())
	}
}

// TestPeerAdminInviteWiringReachesTheRealService proves the HTTP layer
// forwards to the real PeerAdminService: the memory-engine refusal
// (asserted directly against the service in hub's own tests) surfaces
// through this handler unchanged.
func TestPeerAdminInviteWiringReachesTheRealService(t *testing.T) {
	_, handler := peerAdminTestMount(t)
	response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"invite", peerInviteRequest{Name: "laptop"})
	if response.Code == http.StatusOK {
		t.Fatal("invite over a memory-engine store must be refused")
	}
	var payload map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload["error"], "memory") {
		t.Fatalf("invite refusal = %q, want it to name the memory engine", payload["error"])
	}
}

// TestPeerAdminBlockUnblockDisconnectRoundTrip seeds a peer directly (invite
// itself is refused on the memory engine used for this fixture) and drives
// block, unblock and disconnect through the HTTP layer.
func TestPeerAdminBlockUnblockDisconnectRoundTrip(t *testing.T) {
	mount, handler := peerAdminTestMount(t)
	now := time.Now().UTC()
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), hub.PeerRecord{
		MachineID: "machine_1", Name: "laptop", IdentityID: "local", Trust: hub.PeerTrustActive, CreatedAt: now, TrustChangedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	blockResponse := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"block", peerNameOrIDRequest{Peer: "laptop"})
	if blockResponse.Code != http.StatusOK {
		t.Fatalf("block = %d %s", blockResponse.Code, blockResponse.Body.String())
	}
	var blocked peerTrustResponse
	if err := json.Unmarshal(blockResponse.Body.Bytes(), &blocked); err != nil || blocked.Trust != "blocked" {
		t.Fatalf("block body = %+v, %v", blocked, err)
	}

	unblockResponse := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"unblock", peerNameOrIDRequest{Peer: "machine_1"})
	if unblockResponse.Code != http.StatusOK {
		t.Fatalf("unblock = %d %s", unblockResponse.Code, unblockResponse.Body.String())
	}
	var unblocked peerTrustResponse
	if err := json.Unmarshal(unblockResponse.Body.Bytes(), &unblocked); err != nil || unblocked.Trust != "active" || !unblocked.ResetPending {
		t.Fatalf("unblock body = %+v, %v", unblocked, err)
	}

	disconnectResponse := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"disconnect", peerNameOrIDRequest{Peer: "laptop"})
	if disconnectResponse.Code != http.StatusOK {
		t.Fatalf("disconnect = %d %s", disconnectResponse.Code, disconnectResponse.Body.String())
	}
	var disconnected peerDisconnectResponse
	if err := json.Unmarshal(disconnectResponse.Body.Bytes(), &disconnected); err != nil || disconnected.Disconnected || disconnected.Message == "" {
		t.Fatalf("disconnect body = %+v, %v, want Disconnected=false with a message (Task 2 adds sessions)", disconnected, err)
	}

	unknownResponse := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"block", peerNameOrIDRequest{Peer: "no-such-peer"})
	if unknownResponse.Code != http.StatusNotFound {
		t.Fatalf("block(unknown) = %d, want 404", unknownResponse.Code)
	}
}

// TestPeerAdminEnrollReusesTheEnrollmentService proves the owner RPC's
// "enroll" route (peer-connectivity#req:admin-requires-owner-credential's
// "self-hosted machine enrollment moves to that RPC service") mints a real,
// enrollment-scoped credential.
func TestPeerAdminEnrollReusesTheEnrollmentService(t *testing.T) {
	_, handler := peerAdminTestMount(t)
	response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"enroll", peerEnrollRequest{Name: "second-mac"})
	if response.Code != http.StatusOK {
		t.Fatalf("enroll = %d %s", response.Code, response.Body.String())
	}
	var enrolled peerEnrollResponse
	if err := json.Unmarshal(response.Body.Bytes(), &enrolled); err != nil || enrolled.MachineName != "second-mac" || enrolled.Token == "" {
		t.Fatalf("enroll body = %+v, %v", enrolled, err)
	}
}

// TestPeerAdminEnrollRefusesTheHubsOwnMachineName and
// TestPeerAdminEnrollRefusesAPeerName are S4: the owner RPC's "enroll" route
// mints a plain (non-peer) machine credential, which must never shadow the
// hub's own machine name or an existing peer's name — ensureLocalEnrollment
// is the one caller allowed to enrol the hub's own name, and it never goes
// through this route.
func TestPeerAdminEnrollRefusesTheHubsOwnMachineName(t *testing.T) {
	mount, handler := peerAdminTestMount(t)
	response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"enroll", peerEnrollRequest{Name: mount.Machine})
	if response.Code == http.StatusOK {
		t.Fatalf("enroll of the hub's own machine name %q = %d %s, want a refusal", mount.Machine, response.Code, response.Body.String())
	}
}

func TestPeerAdminEnrollRefusesAPeerName(t *testing.T) {
	mount, handler := peerAdminTestMount(t)
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
		t.Fatal(err)
	}
	response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"enroll", peerEnrollRequest{Name: "laptop"})
	if response.Code == http.StatusOK {
		t.Fatalf("enroll of an existing peer name = %d %s, want a refusal", response.Code, response.Body.String())
	}
}

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
func TestPeerAdminHandlerWithoutAHubAnswersUnavailable(t *testing.T) {
	handler := daemonruntime.AuthenticatedHandler("owner-token", newPeerAdminHTTPHandler(nil))
	response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"invite", peerInviteRequest{Name: "laptop"})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("invite with no hub = %d, want 503", response.Code)
	}
}
