package peersrun

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

var errBoomForCmdWB = errors.New("injected write failure")

func TestSavePeerUpstreamStateInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "peer-upstream.json")
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomForCmdWB}
	if err := savePeerUpstreamStateInjected(path, peerUpstreamState{Blocked: true}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("savePeerUpstreamStateInjected error = %v", err)
	}
}
func TestSavePeerUpstreamStateInjectedHonoursAnInjectedChmodFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "peer-upstream.json")
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Err: errBoomForCmdWB}
	if err := savePeerUpstreamStateInjected(path, peerUpstreamState{Blocked: true}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("savePeerUpstreamStateInjected error = %v", err)
	}
}
func TestSavePeerUpstreamStateInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "peer-upstream.json")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomForCmdWB}
	if err := savePeerUpstreamStateInjected(path, peerUpstreamState{Blocked: true}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("savePeerUpstreamStateInjected error = %v", err)
	}
}
func TestSavePeerUpstreamStateInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "peer-upstream.json")
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomForCmdWB}
	if err := savePeerUpstreamStateInjected(path, peerUpstreamState{Blocked: true}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("savePeerUpstreamStateInjected error = %v", err)
	}
}
func TestSavePeerUpstreamStateInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "peer-upstream.json")
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomForCmdWB}
	if err := savePeerUpstreamStateInjected(path, peerUpstreamState{Blocked: true}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("savePeerUpstreamStateInjected error = %v", err)
	}
	assertNoLeftoverPeerUpstreamTempFile(t, dir)
}
func TestSavePeerUpstreamStateInjectedHonoursAnInjectedRenameFailure(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "peer-upstream.json")
	inj := &filewrite.Injector{Step: filewrite.StepRename, Err: errBoomForCmdWB}
	if err := savePeerUpstreamStateInjected(path, peerUpstreamState{Blocked: true}, inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("savePeerUpstreamStateInjected error = %v", err)
	}
	assertNoLeftoverPeerUpstreamTempFile(t, dir)
}
func TestWriteOneTimeTokenInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomForCmdWB}
	if err := writeOneTimeTokenInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writeOneTimeTokenInjected error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("token file not cleaned up after injected write failure: %v", err)
	}
}
func TestWriteOneTimeTokenInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "token")
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomForCmdWB}
	if err := writeOneTimeTokenInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writeOneTimeTokenInjected error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("token file not cleaned up after injected close failure: %v", err)
	}
}
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
func assertNoLeftoverPeerUpstreamTempFile(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".peer-upstream-*.json.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover peer upstream temp file(s) after failure: %v", matches)
	}
}
