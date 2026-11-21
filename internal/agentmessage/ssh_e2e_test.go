//go:build e2e

package agentmessage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These adapter contracts require real process exit status and inherited pipe
// lifetime; in-memory runner fakes cannot faithfully reproduce either OS fact.
func TestContractCommandPipeDrainIsBoundedWhenDescendantKeepsPipeOpen(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	t.Cleanup(cancel)
	started := time.Now()
	err := runCommand(ctx, "sh", []string{"-c", "sleep 10 & echo $!"}, strings.NewReader(""), &stdout, &stderr)
	pid, parseErr := strconv.Atoi(strings.TrimSpace(stdout.String()))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	process, findErr := os.FindProcess(pid)
	if findErr != nil {
		t.Fatal(findErr)
	}
	t.Cleanup(func() { _ = process.Kill() })
	if err != nil || time.Since(started) > 5*time.Second {
		t.Fatal(err, time.Since(started))
	}
}

func TestContractCancelledCommandNeverLaunchesMarkerWritingChild(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "child-started")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	err := runCommand(ctx, "sh", []string{"-c", `printf started > "$1"`, "wb-cancel-test", marker}, strings.NewReader(""), &stdout, &stderr)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled command returned %v, want context.Canceled", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled command launched its child: marker stat error %v", err)
	}
}

func TestContractInheritedPipeDelayNeverMasksFailedChildExit(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	t.Cleanup(cancel)
	started := time.Now()
	err := runCommand(ctx, "sh", []string{"-c", "sleep 10 & echo $!; exit 7"}, strings.NewReader(""), &stdout, &stderr)
	pid, parseErr := strconv.Atoi(strings.TrimSpace(stdout.String()))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	process, findErr := os.FindProcess(pid)
	if findErr != nil {
		t.Fatal(findErr)
	}
	t.Cleanup(func() { _ = process.Kill() })
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 7 || time.Since(started) > 5*time.Second {
		t.Fatal(err, time.Since(started))
	}
}

func TestContractSSHProcessExitDiagnostics(t *testing.T) {
	t.Parallel()
	for _, code := range []int{1, 2, 127, 255} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			err := runCommand(context.Background(), "sh", []string{"-c", fmt.Sprintf("exit %d", code)}, strings.NewReader(""), &stdout, &stderr)
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != code {
				t.Fatal("runner lost native exit status", err)
			}
			if diagnostic := unconfirmedDelivery(context.Background(), err); !strings.Contains(diagnostic.Error(), fmt.Sprintf("status %d", code)) {
				t.Fatal(diagnostic)
			}
		})
	}
}

func TestContractMissingSSHExecutableProducesActionablePrivateDiagnostic(t *testing.T) {
	t.Parallel()
	// The absolute missing executable cannot resolve via the test's PATH.
	missing := filepath.Join(t.TempDir(), "private-ssh-executable")
	var stdout, stderr bytes.Buffer
	err := runCommand(context.Background(), missing, nil, strings.NewReader(""), &stdout, &stderr)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing executable error lost: %v", err)
	}
	diagnostic := unconfirmedDelivery(context.Background(), err).Error()
	if !strings.Contains(diagnostic, "local SSH executable is unavailable") || strings.Contains(diagnostic, filepath.Base(missing)) || strings.Contains(diagnostic, filepath.Dir(missing)) {
		t.Fatal("missing executable diagnostic lost its category or leaked its path", diagnostic)
	}
}
