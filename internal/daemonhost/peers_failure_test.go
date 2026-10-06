package daemonhost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPeerAdminRoutesRejectMalformedJSONBeforeChangingTrust(t *testing.T) {
	t.Parallel()
	mount, handler := peerAdminTestMount(t)
	for _, path := range []string{"invite", "block", "unblock", "disconnect", "enroll"} {
		//nolint:paralleltest // Rows share one mounted trust store; the final peer-list assertion must follow every request.
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, peersRPCPrefix+path, strings.NewReader("{"))
			request.Header.Set("Authorization", "Bearer owner-token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "decode request") {
				t.Fatalf("%s malformed request = %d %s, want JSON decode refusal", path, response.Code, response.Body.String())
			}
		})
	}
	if records, err := mount.PeerAdmin.Trust.ListPeers(context.Background()); err != nil || len(records) != 0 {
		t.Fatalf("malformed requests changed peer trust: records=%v error=%v", records, err)
	}
}

func TestPeerAdminRoutesRejectUnknownPeersWithoutChangingTrust(t *testing.T) {
	t.Parallel()
	mount, handler := peerAdminTestMount(t)
	for _, path := range []string{"unblock", "disconnect"} {
		//nolint:paralleltest // Rows share one mounted trust store; the final peer-list assertion must follow every request.
		t.Run(path, func(t *testing.T) {
			response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+path, peerNameOrIDRequest{Peer: "no-such-peer"})
			if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "peer record not found") {
				t.Fatalf("%s unknown peer = %d %s, want peer-not-found refusal", path, response.Code, response.Body.String())
			}
		})
	}
	if records, err := mount.PeerAdmin.Trust.ListPeers(context.Background()); err != nil || len(records) != 0 {
		t.Fatalf("unknown-peer requests changed trust: records=%v error=%v", records, err)
	}
}
