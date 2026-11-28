//go:build e2e && !windows

package worktrees

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/procalive"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktreecollab"
)

//nolint:paralleltest // replaces package-wide procalive.ReadProcStat and session resolver seams
func TestE2ERetireAcceptsInvokingSessionWhenRecordedOwnerIsZombie(t *testing.T) {
	worktree := newJournalWorktree(t)

	// Use the test runner's parent, a real live process owned by this user, as
	// the recorded owner so kill(pid, 0) succeeds without starting a process,
	// then simulate it turning into a zombie via the injected
	// /proc/<pid>/stat reader.
	ownerPID := os.Getppid()

	if _, err := recordOwner(worktree, "retire-zombie", "codex/owner", "test-model", ownerPID); err != nil {
		t.Fatalf("recordOwner: %v", err)
	}

	home := t.TempDir()
	sessionDir := filepath.Join(home, session.DirName)
	ownerSession, err := session.Register(sessionDir, session.Record{
		PID:             ownerPID,
		Runtime:         "codex",
		Model:           "test-model",
		WBSessionID:     "wbs-zombie-owner",
		NativeHarnessID: "codex-owner",
	})
	if err != nil {
		t.Fatalf("register owner session: %v", err)
	}

	SetSessionResolver(func() (AgentIdentity, bool) {
		return AgentIdentity{
			Runtime:     "claude-code",
			AgentID:     "invoker",
			Model:       "test-model",
			PID:         os.Getpid(),
			WBSessionID: "wbs-invoking-session",
			Registered:  true,
		}, true
	})
	t.Cleanup(func() { SetSessionResolver(nil) })

	ports := retireCustodyPorts{
		resolve: func(context.Context, string, string) (worktreecollab.Checkout, error) {
			return custodyTestCheckout(), nil
		},
		load: func(string, worktreecollab.Checkout) (worktreecollab.State, bool, error) {
			return worktreecollab.State{}, false, nil
		},
		identity:    CurrentIdentity,
		liveSession: corroborateRetireCustodySession,
	}

	beforeOwners, err := ownerViews(worktree)
	if err != nil {
		t.Fatalf("ownerViews before zombie injection: %v", err)
	}
	if len(beforeOwners) != 1 || beforeOwners[0].PIDStatus != "active" {
		t.Fatalf("owners before zombie injection = %#v, want single active owner", beforeOwners)
	}
	if _, err := retireCheckCustodyWith(context.Background(), "/projects", home, ListResult{WorktreeDir: worktree, Owners: beforeOwners}, ports); err == nil || !strings.Contains(err.Error(), "competing active claim") {
		t.Fatalf("retireCheckCustodyWith before zombie injection = %v, want competing active claim refusal", err)
	}
	if processIsDead(ownerPID) {
		t.Fatalf("processIsDead(%d) = true before zombie injection, want false", ownerPID)
	}

	prevReadProcStat := procalive.ReadProcStat
	procalive.ReadProcStat = func(pid int) ([]byte, error) {
		if pid == ownerPID {
			return []byte(fmt.Sprintf("%d (codex) Z 1 %d %d 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0\n", pid, pid, pid)), nil
		}
		return prevReadProcStat(pid)
	}
	t.Cleanup(func() { procalive.ReadProcStat = prevReadProcStat })

	afterOwners, err := ownerViews(worktree)
	if err != nil {
		t.Fatalf("ownerViews after zombie injection: %v", err)
	}
	if len(afterOwners) != 1 || afterOwners[0].PIDStatus != "orphaned" {
		t.Fatalf("owners after zombie injection = %#v, want single orphaned owner", afterOwners)
	}
	if !processIsDead(ownerPID) {
		t.Fatalf("processIsDead(%d) = false for zombie owner, want true", ownerPID)
	}

	fence, err := retireCheckCustodyWith(context.Background(), "/projects", home, ListResult{WorktreeDir: worktree, Owners: afterOwners}, ports)
	if err != nil {
		t.Fatalf("retireCheckCustodyWith for zombie owner = %v, want acceptance", err)
	}
	if fence.mode != "legacy" {
		t.Fatalf("retire custody fence = %+v, want legacy mode", fence)
	}

	if err := corroborateRetireCustodySession(home, ownerSession.WBSessionID); err == nil || !strings.Contains(err.Error(), "not live") {
		t.Fatalf("corroborateRetireCustodySession(zombie) = %v, want not live error", err)
	}
}
