package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/peers"
)

// TestPeersInviteReportsAWriteFailureAfterARescuedToken drives the three
// "return err" branches inside runPeersInvite's writeErr-recovery path: the
// warning line, the "copy this now" prompt, and the token itself. Each must
// propagate a failing output writer instead of silently dropping the only
// copy of an already-minted, unrecorded token.
func TestPeersInviteReportsAWriteFailureAfterARescuedToken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only directory permission this test relies on")
	}
	adminServer, mount := newPeerAdminTestServer(t)
	mount.PeerAdmin.MemoryEngine = false
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
		t.Fatal(err)
	}
	deps := testPeersDeps(t, adminServer, nil)

	readOnlyDir := t.TempDir()
	if err := os.Chmod(readOnlyDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyDir, 0o700) })
	tokenFile := filepath.Join(readOnlyDir, "token.txt")

	// Once writeOneTimeToken fails, the recovery path writes exactly three
	// lines, in order: the warning, the prompt, and the token itself.
	for callIndex := 1; callIndex <= 3; callIndex++ {
		callIndex := callIndex
		t.Run(fmt.Sprintf("call-%d", callIndex), func(t *testing.T) {
			out := &failAtCallWriter{failAt: callIndex}
			err := runPeersInvite(context.Background(), deps, t.TempDir(), "laptop", true, tokenFile, false, out)
			if !errors.Is(err, errAtWrite) {
				t.Fatalf("runPeersInvite (fail at call %d) returned %v, want errAtWrite", callIndex, err)
			}
		})
	}
}

// TestPeersInviteReportsAWriteFailureOnTheSuccessHeaderLine drives the
// "return err" branch after runPeersInvite's success-path header line (no
// --token-file, so it is the first and only write attempted before the
// failure).
func TestPeersInviteReportsAWriteFailureOnTheSuccessHeaderLine(t *testing.T) {
	adminServer, mount := newPeerAdminTestServer(t)
	mount.PeerAdmin.MemoryEngine = false
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
		t.Fatal(err)
	}
	deps := testPeersDeps(t, adminServer, nil)

	out := &failAtCallWriter{failAt: 1}
	err := runPeersInvite(context.Background(), deps, t.TempDir(), "laptop", true, "", false, out)
	if !errors.Is(err, errAtWrite) {
		t.Fatalf("runPeersInvite returned %v, want errAtWrite", err)
	}
}

// TestWritePeerDetailShowsConnectedSessionAndDefaultNodeID drives the
// "detail.Session != nil" branch (rendering "connected" instead of "none")
// together with the empty-NodeID substitution.
func TestWritePeerDetailShowsConnectedSessionAndDefaultNodeID(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	detail := peers.Detail{
		Record:  peers.Record{Name: "laptop", Role: "member", Status: "active", ID: "peer-1"},
		Session: &peers.Session{ConnectedAt: now},
	}
	writePeerDetail(&out, now, detail)
	rendered := out.String()
	for _, want := range []string{"SESSION        connected", "NODE ID        -"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("output missing %q: %q", want, rendered)
		}
	}
}

// TestRefuseExistingTokenFileReportsANonNotExistLstatFailure drives the
// "!errors.Is(err, os.ErrNotExist)" branch: an Lstat failure that is not a
// plain "no such file" (here, a path component that is a regular file, not
// a directory) must be reported rather than treated as "the path is free".
func TestRefuseExistingTokenFileReportsANonNotExistLstatFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "token.txt")

	err := refuseExistingTokenFile(path)
	if err == nil {
		t.Fatal("refuseExistingTokenFile returned nil error, want an Lstat-failure report")
	}
	if strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error = %q, want the check-failure message, not the already-exists refusal", err.Error())
	}
	if !strings.Contains(err.Error(), "check token file") {
		t.Fatalf("error = %q, want it to name the check-token-file failure", err.Error())
	}
}
