//go:build e2e && !darwin && !windows

package daemonruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/testenv"
)

//nolint:paralleltest // Mutates process-wide PATH and the private launchctl argv environment variable.
func TestE2ENativeLaunchctlBindingUsesOnlyThePrivateExecutable(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$WB_PRIVATE_LAUNCHCTL_ARGV\"\nprintf 'private-launchctl-output\\n'\n"
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "launchctl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("WB_PRIVATE_LAUNCHCTL_ARGV", log)
	native := defaultNativeOperations()
	bounds := defaultNativeCommandBounds()
	bounds.Systemctl = 30 * time.Second
	native.commandBounds = func() nativeCommandBounds { return bounds }
	output, err := native.runLaunchctl("print", "private-target")
	if err != nil || string(output) != "private-launchctl-output\n" {
		t.Fatalf("default launchctl=%q,%v", output, err)
	}
	if present, label := native.daemonSupervisorPresent(daemon.SupervisorLaunchd, "private-label"); !present || label != "private-label" {
		t.Fatalf("presence=%v label=%q", present, label)
	}
	want := fmt.Sprintf("print private-target\nprint gui/%d/private-label\n", os.Getuid())
	if body, err := os.ReadFile(log); err != nil || string(body) != want {
		t.Fatalf("argv=%q err=%v want %q", body, err, want)
	}
	if strings.Contains(want, "dev.sneat.wb.daemon") {
		t.Fatal("fixture targeted a WB service")
	}
}
