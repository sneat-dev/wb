//go:build !windows

package session

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/procalive"
)

//nolint:paralleltest // replaces package-wide procalive.ReadProcStat seam
func TestPruneRemovesZombieOwnedSessionRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")

	// Use the test runner's parent, a real live process, so kill(pid, 0)
	// succeeds and Register records a genuine ProcessStartedAt without starting
	// a process, then simulate it becoming a zombie via the injected
	// /proc/<pid>/stat reader.
	zombiePID := os.Getppid()

	if _, err := Register(dir, Record{PID: os.Getpid(), Runtime: "claude-code"}); err != nil {
		t.Fatalf("register live session: %v", err)
	}
	if _, err := Register(dir, Record{PID: zombiePID, Runtime: "codex"}); err != nil {
		t.Fatalf("register zombie-candidate session: %v", err)
	}

	if !ProcessAlive(zombiePID) {
		t.Fatalf("ProcessAlive(%d) = false before zombie injection, want true", zombiePID)
	}

	prevReadProcStat := procalive.ReadProcStat
	procalive.ReadProcStat = func(pid int) ([]byte, error) {
		if pid == zombiePID {
			return []byte(fmt.Sprintf("%d (codex) Z 1 %d %d 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0\n", pid, pid, pid)), nil
		}
		return prevReadProcStat(pid)
	}
	t.Cleanup(func() { procalive.ReadProcStat = prevReadProcStat })

	if ProcessAlive(zombiePID) {
		t.Fatalf("ProcessAlive(%d) = true for zombie pid, want false", zombiePID)
	}
	if _, ok := Lookup(dir, zombiePID); ok {
		t.Fatalf("Lookup(%d) = true for zombie session, want false", zombiePID)
	}

	views, err := List(dir)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	states := map[int]string{}
	for _, view := range views {
		states[view.PID] = view.State
	}
	if states[zombiePID] != StateGone {
		t.Fatalf("zombie session state = %q, want %q", states[zombiePID], StateGone)
	}
	if states[os.Getpid()] != StateLive {
		t.Fatalf("live session state = %q, want %q", states[os.Getpid()], StateLive)
	}

	removed, err := Prune(dir)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("Prune removed = %d, want 1", removed)
	}

	survivors, err := List(dir)
	if err != nil {
		t.Fatalf("List after Prune: %v", err)
	}
	if len(survivors) != 1 || survivors[0].PID != os.Getpid() {
		t.Fatalf("survivors = %+v, want only live pid %d", survivors, os.Getpid())
	}
}
