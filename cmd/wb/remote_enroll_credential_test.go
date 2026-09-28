package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWritePrivateCredentialInjectedReportsUnreadableExistingFile
// covers the non-ErrNotExist branch when writePrivateCredentialInjected
// (cmd/wb/remote_enroll.go) inspects an existing credential path: an
// os.ReadFile failure that is not "does not exist" (here, EISDIR because
// the credential path is itself a directory) must surface as "inspect
// credential file", never fall through to writing a fresh credential.
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
	_, err := writePrivateCredentialInjected(path, "token-value", nil)
	if err == nil {
		t.Fatalf("writePrivateCredentialInjected(%q) = nil error, want one", path)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("writePrivateCredentialInjected(%q) reported ErrNotExist; want the unreadable-existing-file branch: %v", path, err)
	}
	if !strings.Contains(err.Error(), "inspect credential file") {
		t.Fatalf("writePrivateCredentialInjected(%q) = %q, want it to mention 'inspect credential file'", path, err.Error())
	}
}
