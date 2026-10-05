package daemonhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/dashboard"
)

func TestPeerAdminRoutesRequireTheOwnerToken(t *testing.T) {
	t.Parallel()
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

func TestPeerAdminRoutesAreUnreachableFromTheDashboardListener(t *testing.T) {
	t.Parallel()
	mount, _ := peerAdminTestMount(t)
	dashboardHandler := dashboard.NewHandler(dashboard.Options{Mounts: mount.handlers()})
	request := httptest.NewRequest(http.MethodPost, peersRPCPrefix+"invite", strings.NewReader(`{"name":"laptop"}`))
	recorder := httptest.NewRecorder()
	dashboardHandler.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusOK {
		t.Fatalf("peer admin route answered on the TCP dashboard listener: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestPeerAdminInviteWiringReachesTheRealService(t *testing.T) {
	t.Parallel()
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

func TestPeerAdminBlockUnblockDisconnectRoundTrip(t *testing.T) {
	t.Parallel()
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

func TestPeerAdminEnrollReusesTheEnrollmentService(t *testing.T) {
	t.Parallel()
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

func TestPeerAdminEnrollRefusesTheHubsOwnMachineName(t *testing.T) {
	t.Parallel()
	mount, handler := peerAdminTestMount(t)
	response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"enroll", peerEnrollRequest{Name: mount.Machine})
	if response.Code == http.StatusOK {
		t.Fatalf("enroll of the hub's own machine name %q = %d %s, want a refusal", mount.Machine, response.Code, response.Body.String())
	}
}

func TestPeerAdminEnrollRefusesAPeerName(t *testing.T) {
	t.Parallel()
	mount, handler := peerAdminTestMount(t)
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
		t.Fatal(err)
	}
	response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"enroll", peerEnrollRequest{Name: "laptop"})
	if response.Code == http.StatusOK {
		t.Fatalf("enroll of an existing peer name = %d %s, want a refusal", response.Code, response.Body.String())
	}
}

func TestPeerAdminHandlerWithoutAHubAnswersUnavailable(t *testing.T) {
	t.Parallel()
	handler := daemonruntime.AuthenticatedHandler("owner-token", newPeerAdminHTTPHandler(nil))
	response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"invite", peerInviteRequest{Name: "laptop"})
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("invite with no hub = %d, want 503", response.Code)
	}
}
