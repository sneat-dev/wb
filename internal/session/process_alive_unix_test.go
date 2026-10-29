//go:build !windows

package session

import (
	"errors"
	"syscall"
	"testing"
)

func TestProcessAliveOnlyAcceptsPositiveKernelAbsence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		pid  int
		err  error
		want bool
	}{
		{name: "invalid pid", pid: 0},
		{name: "current", pid: 42, want: true},
		{name: "absent", pid: 42, err: syscall.ESRCH},
		{name: "wrapped absence", pid: 42, err: errors.Join(errors.New("probe"), syscall.ESRCH)},
		{name: "permission", pid: 42, err: syscall.EPERM, want: true},
		{name: "unknown", pid: 42, err: syscall.EINVAL, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := processAliveWith(tc.pid, func(pid int) error {
				if pid != 42 {
					t.Fatalf("invalid pid reached syscall: %d", pid)
				}
				return tc.err
			})
			if got != tc.want {
				t.Fatalf("alive=%t, want %t", got, tc.want)
			}
		})
	}
}
