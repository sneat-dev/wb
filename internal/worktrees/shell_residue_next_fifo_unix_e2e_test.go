//go:build e2e && (darwin || linux)

package worktrees

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

//nolint:paralleltest // Parent is parallel; the isolated child bounds a native FIFO regression that would otherwise block the cohort.
func TestE2EShellResidueNextFinalIdentityRefusesUnopenedFIFO(t *testing.T) {
	const marker = "WB_SHELL_RESIDUE_NEXT_FIFO_CHILD"
	if os.Getenv(marker) == "1" {
		handle := newShellResidueNextHandle(t)
		before, err := handle.worktree.Stat()
		if err != nil {
			t.Fatal(err)
		}
		retained := handle.worktreePath + ".retained"
		var replacement os.FileInfo
		observed := false
		err = removeWorktreeResidueObserved(handle, func(phase residueRemovalPhase, held *cleanupWorktreeHandle) {
			if phase != residueBeforeRootRemoval {
				return
			}
			observed = true
			if err := os.Rename(held.worktreePath, retained); err != nil {
				t.Fatal(err)
			}
			// There is deliberately no FIFO writer: any O_RDONLY absence probe
			// would block here. Fstatat metadata admission does not open the FIFO.
			if err := unix.Mkfifo(held.worktreePath, 0600); err != nil {
				t.Fatal(err)
			}
			replacement, err = os.Lstat(held.worktreePath)
			if err != nil {
				t.Fatal(err)
			}
			if replacement.Mode()&os.ModeNamedPipe == 0 {
				t.Fatal("native replacement is not a FIFO")
			}
		})
		if !observed || err == nil || !strings.Contains(err.Error(), "remove residual worktree") || !strings.Contains(err.Error(), "directory identity changed before retirement") {
			t.Fatalf("FIFO admission=%v observed=%v", err, observed)
		}
		original, err := os.Stat(retained)
		if err != nil || !os.SameFile(before, original) {
			t.Fatalf("owned original lost: %v %v", original, err)
		}
		current, err := os.Lstat(handle.worktreePath)
		if err != nil || !os.SameFile(replacement, current) || current.Mode()&os.ModeNamedPipe == 0 {
			t.Fatalf("unowned FIFO lost: %v %v", current, err)
		}
		if entries, err := os.ReadDir(retained); err != nil || len(entries) != 0 {
			t.Fatalf("owned retained directory altered: %v %v", entries, err)
		}
		return
	}
	t.Parallel()
	ctx := context.Background()
	if deadline, ok := t.Deadline(); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline)
		t.Cleanup(cancel)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2EShellResidueNextFinalIdentityRefusesUnopenedFIFO$")
	command.Env = append(os.Environ(), marker+"=1")
	if directory := wtLifeCovCoverDir(); testing.CoverMode() != "" && directory != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+directory)
		command.Env = append(command.Env, "GOCOVERDIR="+directory)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("bounded native FIFO child=%v deadline=%v\n%s", err, ctx.Err(), output)
	}
}
