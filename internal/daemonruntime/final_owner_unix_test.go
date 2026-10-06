//go:build !windows

package daemonruntime

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestForeignNativeDirectoryCannotBecomeRuntimeRoot(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("foreign-owner refusal requires a non-root account")
	}
	root, err := filepath.EvalSymlinks("/etc")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	native, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(native.Uid) == os.Getuid() {
		t.Skip("no foreign-owner directory premise")
	}
	if err := secureDaemonRuntime(root); err == nil || !strings.Contains(err.Error(), "owned") {
		t.Fatalf("foreign-root error=%v", err)
	}
}
