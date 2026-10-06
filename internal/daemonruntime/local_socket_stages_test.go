//go:build !windows

package daemonruntime

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalSocketStagesPreserveNativeEndpointAndFailures(t *testing.T) {
	t.Parallel()
	failure := errors.New("private socket filesystem failure")
	for _, stage := range []string{"mkdir", "inspect", "listen", "protect", "remove-stale"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := cwWtDaemonRoot(t)
			path, err := daemonLocalAddress(root)
			if err != nil {
				t.Fatal(err)
			}
			stages := nativeLocalSocketStages()
			switch stage {
			case "mkdir":
				stages.mkdir = func(string, os.FileMode) error { return failure }
			case "inspect":
				stages.lstat = func(string) (os.FileInfo, error) { return nil, failure }
			case "listen":
				stages.listen = func(string, string) (net.Listener, error) { return nil, failure }
			case "protect":
				stages.chmod = func(string, os.FileMode) error { return failure }
			case "remove-stale":
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				listener.(*net.UnixListener).SetUnlinkOnClose(false)
				if err := listener.Close(); err != nil {
					t.Fatal(err)
				}
				stages.remove = func(string) error { return failure }
			}
			listener, err := listenLocalWithStages(root, stages)
			if listener != nil || !errors.Is(err, failure) {
				t.Fatalf("listener/error = %v, %v", listener, err)
			}
			if stage == "protect" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("failed protection left socket: %v", err)
				}
			}
		})
	}
	t.Run("existing native listener is never replaced", func(t *testing.T) {
		t.Parallel()
		root := cwWtDaemonRoot(t)
		listener, err := ListenLocal(root)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = listener.Close() }()
		if second, err := ListenLocal(root); second != nil || err == nil || !strings.Contains(err.Error(), "already accepting") {
			t.Fatalf("listener/error = %v, %v", second, err)
		}
		conn, err := net.DialTimeout("unix", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatalf("original socket lost: %v", err)
		}
		_ = conn.Close()
	})
	t.Run("listener cleanup reports native removal failure", func(t *testing.T) {
		t.Parallel()
		root := cwWtDaemonRoot(t)
		listener, err := ListenLocal(root)
		if err != nil {
			t.Fatal(err)
		}
		path := listener.(*removingListener).path
		listener.(*removingListener).Listener.(*net.UnixListener).SetUnlinkOnClose(false)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "evidence"), []byte("retain"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := listener.Close(); err == nil {
			t.Fatal("nonempty replacement directory removal succeeded")
		}
	})
}
