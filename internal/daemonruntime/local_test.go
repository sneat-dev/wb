//go:build !windows

package daemonruntime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenDaemonLocalRefusesNonSocketPath(t *testing.T) {
	t.Parallel()
	// A short /tmp-rooted directory, not t.TempDir(): the resolved socket
	// path must stay under the platform's ~104-byte sockaddr_un limit.
	root, err := os.MkdirTemp("/tmp", "wb-nls-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	path := mustDaemonPath(t, daemonLocalAddress, root)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ListenLocal(root)
	if err == nil || !strings.Contains(err.Error(), "refuse to replace non-socket daemon path") {
		t.Fatalf("listenDaemonLocal() error = %v; want a non-socket refusal", err)
	}
}
