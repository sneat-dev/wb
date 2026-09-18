package sessionlaunch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

// slCovWrite writes body at path with the exact mode.
func slCovWrite(t *testing.T, path string, mode os.FileMode, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// slCovScript writes one executable /bin/sh script and returns its path.
func slCovScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script")
	slCovWrite(t, path, 0o755, "#!/bin/sh\n"+body)
	return path
}

// slCovExecutable writes one executable regular fixture file.
func slCovExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	slCovWrite(t, path, 0o755, "fixture")
	return path
}

func slCovDigest(body string) sessionmove.Digest { return sessionmove.DigestBytes([]byte(body)) }

// slCovPlan builds a structurally complete launch plan for state-only tests.
func slCovPlan(handoffID string) launchPlan {
	return launchPlan{
		SchemaVersion: launchSchemaVersion, HandoffID: handoffID,
		RequestDigest:          slCovDigest("request"),
		SuccessorWBSessionID:   "wbs-successor",
		PredecessorWBSessionID: "wbs-source",
		Machine:                "hetzner-vm1",
		TmuxName:               "wb-session-wbs-successor",
		Runtime:                RuntimeCodex, Model: "gpt-5",
		StoreRoot: "/tmp/store", WorktreeDir: "/tmp/worktree",
		PinnedCommit: strings.Repeat("b", 40), PinnedBranch: "wb-session/handoff-123",
		HandoverPath:      ".wb/handoffs/handoff-123.md",
		WBExecutable:      "/bin/wb",
		HarnessExecutable: "/bin/codex",
		HarnessArgs:       []string{"-C", "/tmp/worktree", "prompt"},
	}
}

// slCovOpenState creates the minimal on-disk launch state for handoffID below a
// fresh temp root and returns it with the attempts directory present.
func slCovOpenState(t *testing.T) (*launchState, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), sessionmove.DirName)
	if err := os.MkdirAll(filepath.Join(root, "handoff-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	state, err := openLaunchState(root, "handoff-123", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	return state, root
}

// slCovReadyRecord mirrors the exact session record the launcher publishes.
func slCovReadyRecord(plan launchPlan, pid int, started time.Time) session.Record {
	return session.Record{
		PID: pid, WBSessionID: plan.SuccessorWBSessionID, Machine: plan.Machine,
		Runtime: plan.Runtime, Model: plan.Model, TmuxName: plan.TmuxName,
		PredecessorWBSessionID: plan.PredecessorWBSessionID, HandoffID: plan.HandoffID,
		StartedAt: started,
	}
}

// slCovReady builds the exact ready artifact saveReady derives for record.
func slCovReady(plan launchPlan, attempt *launchAttempt, planDigest sessionmove.Digest, record session.Record) launcherReady {
	return launcherReady{SchemaVersion: launchSchemaVersion, HandoffID: plan.HandoffID,
		AttemptID: attempt.id, AttemptIndex: attempt.index, RequestDigest: plan.RequestDigest,
		PlanDigest: planDigest, PID: record.PID, Session: record}
}

// slCovValidAuthority returns a minimal authority that passes Validate.
func slCovValidAuthority() sessionauthority.Launch {
	return sessionauthority.Launch{
		AggregateID: "handoff-123", AggregateDigest: string(slCovDigest("request")), AggregateFile: "request.json",
		SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		TargetMachine: "hetzner-vm1", SourceRuntime: RuntimeCodex, SourceModel: "gpt-5",
		PinnedCommit: strings.Repeat("b", 40), PinnedBranch: "wb-session/handoff-123",
		ContinuationKind:   sessionauthority.ContinuationTracked,
		ContinuationPath:   ".wb/handoffs/handoff-123.md",
		ContinuationDigest: string(slCovDigest("handover\n")),
	}
}

// slCovFakeDeps builds a hermetic dependency set whose tmux is fakeTmux and
// whose processStatus reports ESRCH, with every other seam stubbed to succeed.
func slCovFakeDeps(t *testing.T, tmux *fakeTmux) dependencies {
	t.Helper()
	bin := t.TempDir()
	slCovExecutable(t, bin, "codex")
	slCovExecutable(t, bin, "wb")
	return dependencies{
		tmux:         tmux,
		lookPath:     func(name string) (string, error) { return filepath.Join(bin, name), nil },
		wbExecutable: func() (string, error) { return filepath.Join(bin, "wb"), nil },
		sessionDir:   func(string) (string, error) { return filepath.Join(bin, "sessions"), nil },
		now:          func() time.Time { return time.Date(2026, time.August, 25, 18, 0, 0, 0, time.UTC) },
		pollInterval: time.Millisecond, startTimeout: 50 * time.Millisecond,
		verifyPinned:  func(context.Context, launchPlan) error { return nil },
		processStatus: func(int) error { return syscall.ESRCH },
	}
}
