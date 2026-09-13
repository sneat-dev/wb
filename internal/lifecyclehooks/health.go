package lifecyclehooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

type WorkerHealth struct {
	SchemaVersion int       `json:"schema_version"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	Message       string    `json:"message,omitempty"`
}

type ResumeReport struct {
	Pending       int      `json:"pending"`
	Running       int      `json:"running"`
	WorkerStarted bool     `json:"worker_started"`
	Warnings      []string `json:"warnings,omitempty"`
}

const workerStartGrace = 10 * time.Second

func (dispatcher Dispatcher) workerHealthPath() string {
	return filepath.Join(dispatcher.StateDir, "worker-health.json")
}

func (dispatcher Dispatcher) unseenDir() string {
	return filepath.Join(dispatcher.StateDir, "unseen-failures")
}

func (dispatcher Dispatcher) quarantineDir() string {
	return filepath.Join(dispatcher.StateDir, "quarantine")
}

func (dispatcher Dispatcher) writeWorkerHealth(health WorkerHealth) error {
	health.SchemaVersion = 1
	return writeJSONAtomic(dispatcher.workerHealthPath(), health, 0o600)
}

func (dispatcher Dispatcher) readWorkerHealth() (*WorkerHealth, error) {
	raw, err := os.ReadFile(dispatcher.workerHealthPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var health WorkerHealth
	if err := json.Unmarshal(raw, &health); err != nil || health.SchemaVersion != 1 || health.Status == "" {
		return nil, fmt.Errorf("invalid lifecycle worker health record")
	}
	return &health, nil
}

func (dispatcher Dispatcher) startWorkerIfIdle() (bool, error) {
	if dispatcher.LaunchWorker == nil {
		return false, nil
	}
	if err := dispatcher.ensureState(); err != nil {
		return false, err
	}
	startLock := flock.New(filepath.Join(dispatcher.StateDir, "worker-start.lock"))
	if err := startLock.Lock(); err != nil {
		return false, fmt.Errorf("coordinate lifecycle hook worker start: %w", err)
	}
	defer func() { _ = startLock.Unlock() }()
	worker := flock.New(filepath.Join(dispatcher.StateDir, "worker.lock"))
	idle, err := worker.TryLock()
	if err != nil {
		return false, fmt.Errorf("inspect lifecycle hook worker: %w", err)
	}
	if !idle {
		return false, nil
	}
	if err := worker.Unlock(); err != nil {
		return false, fmt.Errorf("release lifecycle hook worker probe: %w", err)
	}
	health, err := dispatcher.readWorkerHealth()
	if err != nil {
		if _, quarantineErr := dispatcher.quarantineFile(dispatcher.workerHealthPath(), err.Error()); quarantineErr != nil {
			return false, quarantineErr
		}
		health = nil
	}
	now := dispatcher.Now().UTC()
	if health != nil && health.Status == "starting" && now.Sub(health.StartedAt) >= 0 && now.Sub(health.StartedAt) < workerStartGrace {
		return false, nil
	}
	if err := dispatcher.writeWorkerHealth(WorkerHealth{Status: "starting", StartedAt: now}); err != nil {
		return false, err
	}
	request := WorkerRequest{ConfigPath: dispatcher.ConfigPath, StateDir: dispatcher.StateDir, ReceiptPath: dispatcher.ReceiptPath}
	if err := dispatcher.LaunchWorker(request); err != nil {
		_ = dispatcher.writeWorkerHealth(WorkerHealth{Status: "failed", StartedAt: now, FinishedAt: dispatcher.Now().UTC(), Message: boundedMessage("worker launch failed: "+err.Error(), 512)})
		return false, err
	}
	return true, nil
}

func (dispatcher Dispatcher) Resume() (ResumeReport, error) {
	dispatcher = dispatcher.defaults()
	status, err := dispatcher.Status(1)
	if err != nil {
		return ResumeReport{}, err
	}
	report := ResumeReport{Pending: len(status.Pending), Running: len(status.Running)}
	if report.Pending == 0 && report.Running == 0 {
		return report, nil
	}
	report.WorkerStarted, err = dispatcher.startWorkerIfIdle()
	if err != nil {
		report.Warnings = append(report.Warnings, "lifecycle hook worker did not start: "+err.Error())
	}
	return report, err
}

func (dispatcher Dispatcher) recordUnseenFailure(receipt Receipt) error {
	if err := os.MkdirAll(dispatcher.unseenDir(), 0o700); err != nil {
		return err
	}
	if err := validatePrivateDirectory(dispatcher.unseenDir(), "lifecycle hook unseen-failure directory"); err != nil {
		return err
	}
	return writeJSONAtomic(filepath.Join(dispatcher.unseenDir(), receipt.ID+".json"), receipt, 0o600)
}

func (dispatcher Dispatcher) claimUnseenWarnings(limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	if _, err := os.Stat(dispatcher.StateDir); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := dispatcher.ensureState(); err != nil {
		return nil, err
	}
	lock := flock.New(filepath.Join(dispatcher.StateDir, "unseen.lock"))
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	entries, err := os.ReadDir(dispatcher.unseenDir())
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	warnings := make([]string, 0, limit)
	for _, entry := range entries {
		if len(warnings) == limit || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dispatcher.unseenDir(), entry.Name())
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return warnings, readErr
		}
		receipt, err := decodeReceipt(raw)
		if err != nil {
			if _, quarantineErr := dispatcher.quarantineFile(path, "invalid unseen failure: "+err.Error()); quarantineErr != nil {
				return warnings, quarantineErr
			}
			continue
		}
		warnings = append(warnings, fmt.Sprintf("previous lifecycle hook %s for %s failed: %s; inspect with `wb hooks lifecycle status` or retry %s", receipt.Executor, receipt.Repository, receipt.Message, receipt.ID))
		if err := os.Remove(path); err != nil {
			return warnings, err
		}
	}
	return warnings, syncDirectory(dispatcher.unseenDir())
}

func (dispatcher Dispatcher) unseenFailureCount() (int, error) {
	entries, err := os.ReadDir(dispatcher.unseenDir())
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count, nil
}
