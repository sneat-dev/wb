//go:build darwin

package session

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

func darwinProcArgsFixture(argc uint32, body string) []byte {
	raw := make([]byte, 4, 4+len(body))
	binary.LittleEndian.PutUint32(raw, argc)
	return append(raw, body...)
}

func darwinKinfoFixture(name string) *unix.KinfoProc {
	process := &unix.KinfoProc{}
	copy(process.Proc.P_comm[:], name)
	return process
}

func TestParseDarwinProcArgs2SeparatesPathArgumentsAndEnvironment(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		raw  []byte
		want []string
		ok   bool
	}{
		{
			name: "path padding and environment",
			raw:  darwinProcArgsFixture(2, "/usr/bin/codex\x00\x00\x00codex\x00app-server\x00TOKEN=private\x00"),
			want: []string{"codex", "app-server"}, ok: true,
		},
		{
			name: "no optional padding",
			raw:  darwinProcArgsFixture(1, "/bin/sleep\x00sleep\x00"),
			want: []string{"sleep"}, ok: true,
		},
		{name: "short header", raw: []byte{1, 0, 0}},
		{name: "zero argc", raw: darwinProcArgsFixture(0, "/bin/sleep\x00sleep\x00")},
		{name: "huge argc", raw: darwinProcArgsFixture(^uint32(0), "/bin/sleep\x00sleep\x00")},
		{name: "missing path terminator", raw: darwinProcArgsFixture(1, "/bin/sleep")},
		{name: "empty path", raw: darwinProcArgsFixture(1, "\x00sleep\x00")},
		{name: "path terminator without argv", raw: darwinProcArgsFixture(1, "/bin/sleep\x00")},
		{name: "path without argv", raw: darwinProcArgsFixture(1, "/bin/sleep\x00\x00")},
		{name: "unterminated first argv", raw: darwinProcArgsFixture(1, "/bin/sleep\x00sleep")},
		{name: "unterminated later argv", raw: darwinProcArgsFixture(2, "/bin/sleep\x00sleep\x0030")},
		{name: "too few terminated strings despite environment suffix", raw: darwinProcArgsFixture(3, "/bin/sleep\x00sleep\x0030\x00ENV=unterminated")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseDarwinProcArgs2(test.raw)
			if ok != test.ok || !slices.Equal(got, test.want) {
				t.Fatalf("parseDarwinProcArgs2() = (%q, %t), want (%q, %t)", got, ok, test.want, test.ok)
			}
		})
	}
}

func TestProcessEvidenceWithDarwinSyscallOutcomes(t *testing.T) {
	t.Parallel()
	validRaw := darwinProcArgsFixture(2, "/bin/sleep\x00\x00sleep\x0030\x00SECRET=ignored\x00")
	for _, test := range []struct {
		name      string
		pid       int
		process   *unix.KinfoProc
		kinfoErr  error
		raw       []byte
		argsErr   error
		wantName  string
		wantArgs  []string
		wantFound bool
	}{
		{name: "invalid pid", pid: -1},
		{name: "kinfo error", pid: 42, kinfoErr: errors.New("kinfo failed")},
		{name: "missing kinfo", pid: 42},
		{name: "empty name", pid: 42, process: darwinKinfoFixture("\x00stale")},
		{name: "procargs error", pid: 42, process: darwinKinfoFixture("sleep"), argsErr: errors.New("procargs failed")},
		{name: "nil procargs", pid: 42, process: darwinKinfoFixture("sleep")},
		{name: "malformed procargs", pid: 42, process: darwinKinfoFixture("sleep"), raw: []byte{1}},
		{
			name: "embedded NUL terminates stale command bytes", pid: 42,
			process: darwinKinfoFixture("sleep\x00stale"), raw: validRaw,
			wantName: "sleep", wantArgs: []string{"sleep", "30"}, wantFound: true,
		},
		{
			name: "full width command name", pid: 42,
			process:  darwinKinfoFixture(string(bytes.Repeat([]byte{'a'}, len(unix.KinfoProc{}.Proc.P_comm)))),
			raw:      validRaw,
			wantName: string(bytes.Repeat([]byte{'a'}, len(unix.KinfoProc{}.Proc.P_comm))),
			wantArgs: []string{"sleep", "30"}, wantFound: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			kinfoCalls, argsCalls := 0, 0
			got, ok := processEvidenceWith(test.pid, darwinProcessEvidenceOps{
				kinfoProc: func(pid int) (*unix.KinfoProc, error) {
					kinfoCalls++
					if pid != test.pid {
						t.Fatalf("kinfo pid = %d, want %d", pid, test.pid)
					}
					return test.process, test.kinfoErr
				},
				procArgs: func(pid int) ([]byte, error) {
					argsCalls++
					if pid != test.pid {
						t.Fatalf("procargs pid = %d, want %d", pid, test.pid)
					}
					return test.raw, test.argsErr
				},
			})
			if ok != test.wantFound || got.Executable != test.wantName || !slices.Equal(got.Args, test.wantArgs) {
				t.Fatalf("processEvidenceWith() = (%#v, %t), want name %q, args %q, found %t", got, ok, test.wantName, test.wantArgs, test.wantFound)
			}
			if test.pid <= 0 && (kinfoCalls != 0 || argsCalls != 0) {
				t.Fatalf("invalid PID reached syscalls: kinfo %d, procargs %d", kinfoCalls, argsCalls)
			}
			if test.kinfoErr != nil || test.process == nil || test.process.Proc.P_comm[0] == 0 {
				if argsCalls != 0 {
					t.Fatalf("unusable process reached procargs: %d calls", argsCalls)
				}
			}
		})
	}
}
