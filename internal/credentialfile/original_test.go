package credentialfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

var errBoomForCmdWB = errors.New("injected write failure")

func TestWritePrivateCredentialInjectedHonoursAnInjectedCreateFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	inj := &filewrite.Injector{Step: filewrite.StepOpenOrCreate, Err: errBoomForCmdWB}
	if _, err := writePrivateInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writePrivateInjected error = %v", err)
	}
}
func TestWritePrivateCredentialInjectedHonoursAnInjectedWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	inj := &filewrite.Injector{Step: filewrite.StepWrite, Err: errBoomForCmdWB}
	if _, err := writePrivateInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writePrivateInjected error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("credential file not cleaned up after injected write failure: %v", err)
	}
}
func TestWritePrivateCredentialInjectedHonoursAnInjectedSyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	inj := &filewrite.Injector{Step: filewrite.StepSync, Err: errBoomForCmdWB}
	if _, err := writePrivateInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writePrivateInjected error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("credential file not cleaned up after injected sync failure: %v", err)
	}
}
func TestWritePrivateCredentialInjectedHonoursAnInjectedCloseFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	inj := &filewrite.Injector{Step: filewrite.StepClose, Err: errBoomForCmdWB}
	if _, err := writePrivateInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writePrivateInjected error = %v", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("credential file not cleaned up after injected close failure: %v", err)
	}
}
func TestWritePrivateCredentialInjectedHonoursAnInjectedChmodFailureOnAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Err: errBoomForCmdWB}
	if _, err := writePrivateInjected(path, "secret", inj); !errors.Is(err, errBoomForCmdWB) {
		t.Fatalf("writePrivateInjected error = %v", err)
	}
}
func TestWritePrivateCredentialInjectedReportsUnreadableExistingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// The credential path itself is a directory, so its parent
	// (filepath.Dir(path)) already exists and os.MkdirAll succeeds, but the
	// later os.ReadFile(path) fails with "is a directory" rather than
	// os.ErrNotExist.
	path := filepath.Join(dir, "credentials", "hub.token")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("seed directory at credential path: %v", err)
	}
	_, err := writePrivateInjected(path, "token-value", nil)
	if err == nil {
		t.Fatalf("writePrivateInjected(%q) = nil error, want one", path)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("writePrivateInjected(%q) reported ErrNotExist; want the unreadable-existing-file branch: %v", path, err)
	}
	if !strings.Contains(err.Error(), "inspect credential file") {
		t.Fatalf("writePrivateInjected(%q) = %q, want it to mention 'inspect credential file'", path, err.Error())
	}
}
func TestCwDepsReadRemoteEnrollmentTokenRefusals(t *testing.T) {
	if _, err := ReadToken(strings.NewReader("")); err == nil ||
		!strings.Contains(err.Error(), "one non-empty token") {
		t.Fatalf("empty token = %v", err)
	}
	if _, err := ReadToken(strings.NewReader("two tokens\n")); err == nil ||
		!strings.Contains(err.Error(), "one non-empty token") {
		t.Fatalf("two tokens = %v", err)
	}
	if _, err := ReadToken(strings.NewReader(strings.Repeat("x", 16<<10+2))); err == nil ||
		!strings.Contains(err.Error(), "exceeds 16384 bytes") {
		t.Fatalf("oversized token = %v", err)
	}
	token, err := ReadToken(strings.NewReader("  good-token \n"))
	if err != nil || token != "good-token" {
		t.Fatalf("token = %q, %v", token, err)
	}
}
