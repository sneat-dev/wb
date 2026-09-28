package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/peers"
)

// TestRefuseExistingTokenFileReportsNonNotExistError covers the
// non-ErrNotExist branch of refuseExistingTokenFile (cmd/wb/peers.go): an
// os.Lstat failure that is not "file does not exist" (here, ENOTDIR because
// a path component is a regular file, not a directory) must surface as a
// distinct "check token file" error, never the "already exists" one and
// never a silent nil.
func TestRefuseExistingTokenFileReportsNonNotExistError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	path := filepath.Join(blocker, "token") // blocker is a file, not a dir
	err := refuseExistingTokenFile(path)
	if err == nil {
		t.Fatalf("refuseExistingTokenFile(%q) = nil, want an error", path)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refuseExistingTokenFile(%q) reported ErrNotExist; want the non-ErrNotExist check-failure branch: %v", path, err)
	}
	if !strings.Contains(err.Error(), "check token file") {
		t.Fatalf("refuseExistingTokenFile(%q) = %q, want it to mention 'check token file'", path, err.Error())
	}
}

// TestLoadPeerUpstreamStateReportsUnreadableFile covers the
// non-ErrNotExist read-failure branch of loadPeerUpstreamState
// (cmd/wb/peers.go): an os.ReadFile failure other than "does not exist"
// must be wrapped and returned, not swallowed as an empty state.
func TestLoadPeerUpstreamStateReportsUnreadableFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker file: %v", err)
	}
	path := filepath.Join(blocker, "state.json")
	_, err := loadPeerUpstreamState(path)
	if err == nil {
		t.Fatalf("loadPeerUpstreamState(%q) = nil error, want one", path)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("loadPeerUpstreamState(%q) reported ErrNotExist; want the unreadable-file branch: %v", path, err)
	}
	if !strings.Contains(err.Error(), "read upstream peer state") {
		t.Fatalf("loadPeerUpstreamState(%q) = %q, want it to mention 'read upstream peer state'", path, err.Error())
	}
}

// TestWritePeerDetailRendersConnectedSession covers writePeerDetail's
// (cmd/wb/peers.go) detail.Session != nil branch: a non-nil Session must
// render "connected" instead of the zero-value "none".
func TestWritePeerDetailRendersConnectedSession(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	detail := peers.Detail{Session: &peers.Session{}}
	writePeerDetail(&buf, time.Now(), detail)
	out := buf.String()
	if !strings.Contains(out, "connected") {
		t.Fatalf("writePeerDetail output = %q, want it to report the session as connected", out)
	}
	if strings.Contains(out, "SESSION        none") {
		t.Fatalf("writePeerDetail output = %q, want it not to fall back to 'none'", out)
	}
}
