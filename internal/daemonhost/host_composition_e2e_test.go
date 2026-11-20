//go:build e2e

package daemonhost

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
)

func TestE2EPeerAdminInviteReturnsThePersistedCredential(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	gitContext, cancelGit := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancelGit)
	command := exec.CommandContext(gitContext, "git", "-C", root, "init", "--quiet")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize private persistent store: %v: %s", err, output)
	}
	store, closer, err := hubstore.Open(t.Context(), hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	trust, stats := hub.NewPeerStores(store)
	pepper := bytes.Repeat([]byte{'p'}, 32)
	at := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	admin := &hub.PeerAdminService{Backend: store, Trust: trust, Stats: stats, Pepper: pepper, HubMachineName: "composition-hub", Now: func() time.Time { return at }}
	handler := daemonruntime.AuthenticatedHandler("owner-token", newPeerAdminHTTPHandler(&hubMount{PeerAdmin: admin}))
	response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"invite", peerInviteRequest{Name: "composition-peer"})
	var invite peerInviteResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &invite) != nil || invite.Token == "" || invite.Name != "composition-peer" || invite.Rotated || !invite.CreatedAt.Equal(at) {
		t.Fatalf("invite response status=%d body=%s", response.Code, response.Body.String())
	}
	reopened, reopenedCloser, err := hubstore.Open(t.Context(), hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedCloser.Close() })
	reopenedTrust, reopenedStats := hub.NewPeerStores(reopened)
	peer, found, err := reopenedTrust.GetPeer(t.Context(), invite.PeerID)
	if err != nil || !found || peer.Name != invite.Name || peer.Trust != hub.PeerTrustActive || !peer.CreatedAt.Equal(invite.CreatedAt) {
		t.Fatalf("persisted invite peer=%+v found=%v error=%v", peer, found, err)
	}
	if _, found, err := reopenedStats.GetStats(t.Context(), invite.PeerID); err != nil || !found {
		t.Fatalf("persisted invite statistics found=%v error=%v", found, err)
	}
	digest, err := hub.DigestMachineToken(invite.Token, pepper)
	if err != nil {
		t.Fatal(err)
	}
	_, resolver, _ := hub.NewMachineStores(reopened)
	binding, err := resolver.ResolveMachineCredential(t.Context(), digest)
	if err != nil || binding.MachineID != invite.PeerID || binding.MachineName != invite.Name {
		t.Fatalf("returned token does not resolve to persisted invite: %+v error=%v", binding, err)
	}
}
