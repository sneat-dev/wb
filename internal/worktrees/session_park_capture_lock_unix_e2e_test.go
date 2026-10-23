//go:build e2e && !windows

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionpark"
	"golang.org/x/sys/unix"
)

//nolint:paralleltest // fixture configures process-wide Git and agent environment
func TestE2EParkedRemoteResumeKeepsJournalCustodyThroughDelivery(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-remote-custody")
	branch := preparePushedParkedWorktree(t, fixture, worktree)
	_, snapshot := captureParkedWorktreeMember(t, fixture, worktree, source, branch)
	bundle := sessionpark.Bundle{
		SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-remote-custody",
		Source: source, Continuation: "private continuation", ParkedAt: time.Now().UTC(),
		Worktrees: []sessionpark.Worktree{snapshot},
	}
	fault := errors.New("delivery refused")
	called := false
	err := WithParkedRemoteResumeCustody(context.Background(), fixture.projectsRoot, bundle, func() error {
		called = true
		lock, openErr := os.OpenFile(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory, localWorkLogLockName), os.O_RDWR, 0)
		if openErr != nil {
			return openErr
		}
		defer func() { _ = lock.Close() }()
		if lockErr := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(lockErr, unix.EWOULDBLOCK) {
			if lockErr == nil {
				_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			}
			t.Fatalf("remote delivery ran without retained Work Log lock: %v", lockErr)
		}
		return fault
	})
	if !called || !errors.Is(err, fault) {
		t.Fatalf("remote delivery = (called=%t, err=%v)", called, err)
	}
	assertParkedCaptureLockAvailable(t, worktree)
}
