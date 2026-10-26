package landinglane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/session"
)

// ReleaseIdle releases an owned lane only after its WB operation has exited.
// Ownership, liveness and removal share Acquire's lock: a concurrent acquire
// either precedes this check or starts after removal with a fresh lane.
// Legacy records inspect the process table conservatively for other WB commands.
func ReleaseIdle(home, repository, target, wbSessionID string) (bool, error) {
	return releaseIdle(home, repository, target, wbSessionID, session.ProcessAlive, legacyOperationActive)
}

func releaseIdle(home, repository, target, wbSessionID string, alive func(int) bool, legacyActive func() (bool, error)) (bool, error) {
	if strings.TrimSpace(wbSessionID) == "" {
		return false, fmt.Errorf("lane release requires a registered WB session; run wb session register first")
	}
	lane := LaneID(strings.TrimSpace(repository), strings.TrimSpace(target))
	file, err := openLockFile(home, lane)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	if err := lockExclusive(file); err != nil {
		return false, fmt.Errorf("lock landing lane %s: %w", lane, err)
	}
	defer func() { _ = unlock(file) }()
	path := recordPath(home, lane)
	record, found, err := readRecord(path)
	if err != nil || !found {
		return false, err
	}
	if record.Owner.WBSessionID != wbSessionID {
		return false, &ConflictError{Record: record}
	}
	pid := record.Owner.OperationPID
	if pid <= 0 {
		active, err := legacyActive()
		if err != nil {
			return false, fmt.Errorf("inspect legacy landing operations: %w", err)
		}
		if active {
			return false, fmt.Errorf("legacy landing lane %s: another WB command is running; stop it before releasing", lane)
		}
	}
	if pid > 0 && alive(pid) {
		return false, fmt.Errorf("landing lane %s is still active (PID %d); stop the owning command before releasing", lane, pid)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("release landing lane %s: %w", lane, err)
	}
	return true, nil
}

// A legacy holder has no operation PID. Refuse every other WB executable,
// including an orphaned operation, rather than treating the live harness as
// the operation or guessing from heartbeat age. Hold the lane lock throughout
// this observation and removal; commands acquiring later create a fresh claim.
func legacyOperationActive() (bool, error) {
	return legacyOperationActiveWithRunner(runner.New())
}

func legacyOperationActiveWithRunner(run runner.Runner) (bool, error) {
	return legacyOperationActiveWith(func() ([]byte, error) {
		result, err := run.RunOpts(context.Background(), "", runner.RunOptions{}, "ps", "-A", "-o", "pid=", "-o", "comm=")
		if err != nil {
			return []byte(result.Stdout), fmt.Errorf("%w: %s", err, strings.TrimSpace(result.Stderr))
		}
		return []byte(result.Stdout), nil
	})
}

func legacyOperationActiveWith(snapshot func() ([]byte, error)) (bool, error) {
	output, err := snapshot()
	if err != nil {
		return false, err
	}
	return otherWBProcess(string(output), os.Getpid())
}

func otherWBProcess(output string, self int) (bool, error) {
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			return false, fmt.Errorf("incomplete process identity")
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			return false, fmt.Errorf("invalid process identity: %w", err)
		}
		name := filepath.Base(strings.Join(fields[1:], " "))
		if pid != self && (name == "wb" || name == "wb.exe" || strings.HasPrefix(name, "wb-") || (len(name) > 2 && strings.HasPrefix(name, "wb") && name[2] >= '0' && name[2] <= '9')) {
			return true, nil
		}
	}
	return false, nil
}
