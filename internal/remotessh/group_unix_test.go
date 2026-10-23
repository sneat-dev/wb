//go:build !windows

package remotessh

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// noSuchGroup is above every platform's largest process id, so no process
// group has it.
const noSuchGroup = 0x7ffffff0

func TestKillGroupSignalsTheGroupAndReportsOneThatIsGoneAsTheProcessBeingDone(t *testing.T) {
	t.Parallel()
	var signalled []int
	record := func(pid int, signal syscall.Signal) error {
		signalled = append(signalled, pid, int(signal))
		return nil
	}
	// The group is addressed by the negated id of its leader.
	if err := killGroup(record, 4242); err != nil || len(signalled) != 2 || signalled[0] != -4242 || signalled[1] != int(syscall.SIGKILL) {
		t.Fatalf("killGroup = %v, signalled %v", err, signalled)
	}
	if err := killGroup(syscall.Kill, noSuchGroup); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("killGroup of no group = %v, want os.ErrProcessDone", err)
	}
}

// TestInspectPathReadsAFilesModeAndOwner: what this process creates is its own.
func TestInspectPathReadsAFilesModeAndOwner(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o640); err != nil {
		t.Fatal(err)
	}
	if facts, err := inspectPath(path); err != nil || facts.owner != os.Getuid() || facts.mode.Perm() != 0o640 {
		t.Fatalf("inspectPath = %+v, %v", facts, err)
	}
	if _, err := inspectPath(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("a missing file must be reported")
	}
}

// TestAGroupedCommandHasAGroupOfItsOwnThatTheEndOfItsContextKills inspects the
// command without starting it: it is its own group's leader, and what its
// context's end calls is the kill of that group.
func TestAGroupedCommandHasAGroupOfItsOwnThatTheEndOfItsContextKills(t *testing.T) {
	t.Parallel()
	command := prepare(t.Context(), "/nonexistent/bin/ssh", nil, nil, io.Discard, io.Discard, true)
	// A session of its own: its leader is its process group's leader too, and it
	// has no controlling terminal to prompt on.
	if command.SysProcAttr == nil || !command.SysProcAttr.Setsid || command.SysProcAttr.Setpgid || command.Cancel == nil {
		t.Fatalf("the command = %+v", command)
	}
	command.Process = &os.Process{Pid: noSuchGroup}
	if err := command.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel = %v, want the kill of a group that is gone", err)
	}
}
