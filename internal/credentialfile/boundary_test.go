package credentialfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failedReader struct{ err error }

func (r failedReader) Read([]byte) (int, error) { return 0, r.err }

func TestTokenReadErrorPreservesIdentity(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("read refused")
	token, err := ReadToken(failedReader{sentinel})
	if token != "" || !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "read machine credential from stdin") {
		t.Fatalf("token=%q error=%v", token, err)
	}
}
func TestPrivateCredentialCreationReuseAndConflict(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "token")
	created, err := WritePrivate(path, "secret")
	if !created || err != nil {
		t.Fatalf("create=%v,%v", created, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "secret\n" {
		t.Fatalf("bytes=%q,%v", raw, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	created, err = WritePrivate(path, "secret")
	if created || err != nil {
		t.Fatalf("reuse=%v,%v", created, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%v,%v", info, err)
	}
	created, err = WritePrivate(path, "different")
	if created || err == nil || !strings.Contains(err.Error(), "different contents") {
		t.Fatalf("conflict=%v,%v", created, err)
	}
	raw, err = os.ReadFile(path)
	if err != nil || string(raw) != "secret\n" {
		t.Fatalf("conflict changed bytes=%q,%v", raw, err)
	}
}
func TestCredentialDirectoryFailureDoesNotCreateCredential(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := WritePrivate(filepath.Join(parent, "token"), "secret")
	if created || err == nil || !strings.Contains(err.Error(), "create credential directory") {
		t.Fatalf("result=%v,%v", created, err)
	}
	raw, err := os.ReadFile(parent)
	if err != nil || string(raw) != "retain" {
		t.Fatalf("parent=%q,%v", raw, err)
	}
}

var _ io.Reader = failedReader{}
