//go:build !windows

package procalive

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func TestProbeWithClassifiesSignalAndProcStat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		pid       int
		killErr   error
		statData  string
		statErr   error
		wantErr   error
		wantAlive bool
	}{
		{name: "zero pid", pid: 0, wantErr: syscall.ESRCH},
		{name: "negative pid", pid: -7, wantErr: syscall.ESRCH},
		{name: "esrch", pid: 42, killErr: syscall.ESRCH, wantErr: syscall.ESRCH},
		{name: "wrapped esrch", pid: 42, killErr: errors.Join(errors.New("probe"), syscall.ESRCH), wantErr: syscall.ESRCH},
		{name: "live sleeping", pid: 42, statData: "42 (codex) S 1 42\n", wantAlive: true},
		{name: "zombie", pid: 42, statData: "42 (codex) Z 1 42\n", wantErr: syscall.ESRCH},
		{name: "dead state X", pid: 42, statData: "42 (codex) X 1 42\n", wantErr: syscall.ESRCH},
		{name: "zombie comm with parens", pid: 42, statData: "42 (codex (defunct) v1) Z 1 42\n", wantErr: syscall.ESRCH},
		{name: "unreadable proc falls back to nil", pid: 42, statErr: os.ErrPermission, wantAlive: true},
		{name: "missing proc falls back to nil", pid: 42, statErr: os.ErrNotExist, wantAlive: true},
		{name: "malformed proc falls back to nil", pid: 42, statData: "corrupt", wantAlive: true},
		{name: "eperm with unreadable proc is alive", pid: 42, killErr: syscall.EPERM, statErr: os.ErrPermission, wantErr: syscall.EPERM, wantAlive: true},
		{name: "eperm with live proc is alive", pid: 42, killErr: syscall.EPERM, statData: "42 (other) S 1 42\n", wantErr: syscall.EPERM, wantAlive: true},
		{name: "eperm with zombie proc is dead", pid: 42, killErr: syscall.EPERM, statData: "42 (other) Z 1 42\n", wantErr: syscall.ESRCH},
		{name: "unexpected kill error stays fail-closed alive", pid: 42, killErr: syscall.EINVAL, statErr: os.ErrNotExist, wantErr: syscall.EINVAL, wantAlive: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := probeWith(
				tc.pid,
				func(pid int) error {
					if pid != 42 {
						t.Fatalf("invalid pid reached kill: %d", pid)
					}
					return tc.killErr
				},
				func(pid int) ([]byte, error) {
					if pid != 42 {
						t.Fatalf("unexpected pid reached readProcStat: %d", pid)
					}
					if errors.Is(tc.killErr, syscall.ESRCH) {
						t.Fatal("readProcStat should not be called after ESRCH")
					}
					return []byte(tc.statData), tc.statErr
				},
			)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("probeWith error = %v, want nil", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("probeWith error = %v, want %v", err, tc.wantErr)
			}
			gotAlive := !errors.Is(err, syscall.ESRCH)
			if gotAlive != tc.wantAlive {
				t.Fatalf("alive = %t, want %t", gotAlive, tc.wantAlive)
			}
		})
	}
}

func TestAliveAndProbeCurrentAndInvalidPID(t *testing.T) {
	t.Parallel()
	if !Alive(os.Getpid()) {
		t.Fatal("Alive(self) = false, want true")
	}
	if err := Probe(os.Getpid()); err != nil {
		t.Fatalf("Probe(self) = %v, want nil", err)
	}
	for _, pid := range []int{0, -1} {
		if Alive(pid) {
			t.Fatalf("Alive(%d) = true, want false", pid)
		}
		if err := Probe(pid); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("Probe(%d) = %v, want ESRCH", pid, err)
		}
	}
}

func TestAliveReportsRealZombieProcessAsDead(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("zombie /proc/<pid>/stat detection is Linux-specific")
	}
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child process: %v", err)
	}
	pid := cmd.Process.Pid
	reaped := false
	t.Cleanup(func() {
		if !reaped {
			_ = cmd.Wait()
		}
	})

	deadline := time.Now().Add(5 * time.Second)
	becameZombie := false
	for time.Now().Before(deadline) {
		data, err := defaultReadProcStat(pid)
		if err == nil {
			if state, ok := parseProcStatState(string(data)); ok && state == 'Z' {
				becameZombie = true
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !becameZombie {
		t.Fatalf("child pid %d did not enter zombie state before deadline", pid)
	}

	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("expected kill(%d, 0) to succeed on unreaped zombie, got %v", pid, err)
	}
	if Alive(pid) {
		t.Fatalf("Alive(%d) = true for unreaped zombie, want false", pid)
	}
	if err := Probe(pid); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("Probe(%d) = %v for unreaped zombie, want ESRCH", pid, err)
	}

	reaped = true
	if err := cmd.Wait(); err != nil {
		t.Fatalf("reap zombie child %d: %v", pid, err)
	}
}
