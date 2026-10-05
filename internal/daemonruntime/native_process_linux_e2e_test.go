//go:build e2e && linux

package daemonruntime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/testenv"
)

func privateNativeChild(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "private-native-child")
	script := `#!/bin/sh
if [ "$1" = stop ]; then
  trap 'printf "stopped\n" >> "$2"; exit 0' TERM
  exec 3<> "$3"
  printf 'ready\n' > "$2"
  read value <&3
  exit 0
fi
printf 'stdout:%s:%s\n' "$1" "$2"
printf 'stderr:child\n' >&2
printf 'value:%s\n' "$WB_PRIVATE_NATIVE_VALUE"
printf 'supervisors:%s:%s:%s:%s\n' "$INVOCATION_ID" "$SYSTEMD_EXEC_PID" "$JOURNAL_STREAM" "$XPC_SERVICE_NAME"
`
	if err := testenv.WriteExecutableFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

// The PID belongs to the fixture just started by this test. Wait begins before
// any assertion; cleanup kills and joins it if the caller fails or times out.
func ownedNativeWait(t *testing.T, pid int) func() *os.ProcessState {
	t.Helper()
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var state *os.ProcessState
	var waitErr error
	go func() { state, waitErr = process.Wait(); close(done) }()
	t.Cleanup(func() {
		select {
		case <-done:
			return
		default:
		}
		_ = process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("owned child was not reaped after cleanup kill")
		}
	})
	return func() *os.ProcessState {
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = process.Kill()
			t.Fatal("owned child did not exit within bound")
		}
		if waitErr != nil {
			t.Fatalf("wait for owned child: %v", waitErr)
		}
		return state
	}
}

func TestE2ELinuxPrivateLaunchRefusesNativeFilesystemAndStartFailures(t *testing.T) {
	t.Parallel()
	executable := privateNativeChild(t)
	for _, kind := range []string{"parent file", "log directory", "missing executable", "independent test suffix"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			logPath := filepath.Join(root, "logs", "daemon.log")
			command := executable
			switch kind {
			case "parent file":
				if err := os.WriteFile(filepath.Join(root, "logs"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			case "log directory":
				if err := os.MkdirAll(logPath, 0700); err != nil {
					t.Fatal(err)
				}
			case "missing executable":
				command = filepath.Join(root, "missing-child")
			case "independent test suffix":
				command = filepath.Join(root, "child.test")
			}
			pid, err := defaultNativeOperations().startDaemonProcessForMode(command, nil, logPath, false)
			if pid != 0 || err == nil {
				if pid > 0 {
					ownedNativeWait(t, pid)
				}
				t.Fatalf("pid=%d err=%v", pid, err)
			}
			switch kind {
			case "parent file":
				if !errors.Is(err, syscall.ENOTDIR) {
					t.Fatalf("parent refusal=%v", err)
				}
				if body, e := os.ReadFile(filepath.Join(root, "logs")); e != nil || string(body) != "keep" {
					t.Fatalf("parent blocker=%q err=%v", body, e)
				}
			case "log directory":
				if !errors.Is(err, syscall.EISDIR) {
					t.Fatalf("log refusal=%v", err)
				}
			case "missing executable":
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("start refusal=%v", err)
				}
			}
			if kind == "independent test suffix" {
				if !strings.Contains(err.Error(), "refusing to install") {
					t.Fatalf("suffix refusal=%v", err)
				}
				if _, err := os.Stat(filepath.Dir(logPath)); !os.IsNotExist(err) {
					t.Fatalf("refusal created logs: %v", err)
				}
			}
		})
	}
}

//nolint:paralleltest // Mutates process-wide supervisor environment variables and the inherited child value.
func TestE2ELinuxPrivateLaunchAppendsOutputAndFiltersSupervisorEnvironment(t *testing.T) {
	for _, name := range []string{"INVOCATION_ID", "SYSTEMD_EXEC_PID", "JOURNAL_STREAM", "XPC_SERVICE_NAME"} {
		t.Setenv(name, "parent-supervisor")
	}
	t.Setenv("WB_PRIVATE_NATIVE_VALUE", "private-value")
	executable := privateNativeChild(t)
	root := t.TempDir()
	logPath := filepath.Join(root, "logs", "daemon.log")
	native := defaultNativeOperations()
	pid, err := native.startDaemonProcessForMode(executable, []string{"first", "two words"}, logPath, false)
	if err != nil {
		t.Fatal(err)
	}
	wait := ownedNativeWait(t, pid)
	if state := wait(); !state.Success() {
		t.Fatalf("child state=%v", state)
	}
	if native.processAlive(pid) {
		t.Fatal("reaped child remains alive")
	}
	first, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "stdout:first:two words\nstderr:child\nvalue:private-value\nsupervisors::::\n"
	if string(first) != want {
		t.Fatalf("first log=%q want %q", first, want)
	}
	pid, err = native.startDaemonProcessForMode(executable, []string{"second", "tail"}, logPath, false)
	if err != nil {
		t.Fatal(err)
	}
	wait = ownedNativeWait(t, pid)
	if state := wait(); !state.Success() {
		t.Fatalf("second child state=%v", state)
	}
	body, err := os.ReadFile(logPath)
	if err != nil || string(body) != want+"stdout:second:tail\nstderr:child\nvalue:private-value\nsupervisors::::\n" {
		t.Fatalf("appended log=%q err=%v", body, err)
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(logPath): 0700, logPath: 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("path=%s info=%v err=%v want mode%o", path, info, err, mode)
		}
	}
}

func TestE2ELinuxNativeLivenessAndStopOwnOnlyTheFixtureChild(t *testing.T) {
	t.Parallel()
	native := defaultNativeOperations()
	if !native.processAlive(os.Getpid()) || native.processAlive(-1) {
		t.Fatal("native PID liveness changed")
	}
	if err := native.stopDaemonProcess(0, daemon.SupervisorNone, ""); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	marker := filepath.Join(root, "marker")
	fifo := filepath.Join(root, "input")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	pid, err := native.startDaemonProcessForMode(privateNativeChild(t), []string{"stop", marker, fifo}, filepath.Join(root, "log"), false)
	if err != nil {
		t.Fatal(err)
	}
	wait := ownedNativeWait(t, pid)
	ticker := time.NewTicker(5 * time.Millisecond)
	t.Cleanup(ticker.Stop)
	deadline := time.NewTimer(30 * time.Second)
	t.Cleanup(func() { deadline.Stop() })
	for {
		body, err := os.ReadFile(marker)
		if err == nil && string(body) == "ready\n" {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("owned child did not emit its ready marker")
		}
	}
	if !native.processAlive(pid) {
		t.Fatal("ready owned child is not alive")
	}
	if err := native.stopDaemonProcess(pid, daemon.SupervisorNone, ""); err != nil {
		t.Fatal(err)
	}
	if state := wait(); !state.Success() {
		t.Fatalf("SIGTERM child state=%v", state)
	}
	if body, err := os.ReadFile(marker); err != nil || string(body) != "ready\nstopped\n" {
		t.Fatalf("signal marker=%q err=%v", body, err)
	}
	if native.processAlive(pid) {
		t.Fatal("stopped and reaped child remains alive")
	}
}
