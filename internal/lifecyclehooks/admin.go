package lifecyclehooks

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

type Status struct {
	StateDir    string    `json:"state_dir"`
	ReceiptPath string    `json:"receipt_path"`
	Worker      string    `json:"worker"`
	Pending     []Queued  `json:"pending"`
	Running     []Queued  `json:"running"`
	Receipts    []Receipt `json:"receipts"`
}

type Queued struct {
	Executor       string    `json:"executor"`
	Event          Event     `json:"event"`
	CoalescedCount int       `json:"coalesced_count"`
	QueuedAt       time.Time `json:"queued_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func (dispatcher Dispatcher) Status(limit int) (Status, error) {
	dispatcher = dispatcher.defaults()
	if limit <= 0 {
		limit = 20
	}
	status := Status{StateDir: dispatcher.StateDir, ReceiptPath: dispatcher.ReceiptPath, Worker: "idle"}
	if err := dispatcher.ensureState(); err != nil {
		return status, err
	}
	worker := flock.New(filepath.Join(dispatcher.StateDir, "worker.lock"))
	available, err := worker.TryLock()
	if err != nil {
		return status, err
	}
	if available {
		_ = worker.Unlock()
	} else {
		status.Worker = "running"
	}
	if status.Pending, status.Running, err = dispatcher.queueSnapshot(); err != nil {
		return status, err
	}
	if status.Receipts, err = readRecentReceipts(dispatcher.ReceiptPath, limit); err != nil {
		return status, err
	}
	return status, nil
}

func (dispatcher Dispatcher) queueSnapshot() ([]Queued, []Queued, error) {
	lock := flock.New(filepath.Join(dispatcher.StateDir, "queue.lock"))
	if err := lock.Lock(); err != nil {
		return nil, nil, err
	}
	defer func() { _ = lock.Unlock() }()
	pending, err := dispatcher.listQueued(dispatcher.pendingDir())
	if err != nil {
		return nil, nil, err
	}
	running, err := dispatcher.listQueued(dispatcher.runningDir())
	return pending, running, err
}

func (dispatcher Dispatcher) listQueued(directory string) ([]Queued, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	var queued []Queued
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		job, err := readJob(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		queued = append(queued, Queued{Executor: job.Executor, Event: job.Event, CoalescedCount: job.CoalescedCount, QueuedAt: job.QueuedAt, UpdatedAt: job.UpdatedAt})
	}
	sort.Slice(queued, func(i, j int) bool {
		if queued[i].UpdatedAt.Equal(queued[j].UpdatedAt) {
			return queued[i].Executor < queued[j].Executor
		}
		return queued[i].UpdatedAt.Before(queued[j].UpdatedAt)
	})
	return queued, nil
}

func (dispatcher Dispatcher) Retry(receiptID string) (Report, error) {
	dispatcher = dispatcher.defaults()
	selected, found, err := findReceipt(dispatcher.ReceiptPath, receiptID)
	if err != nil {
		return Report{}, err
	}
	if !found {
		return Report{}, fmt.Errorf("lifecycle hook receipt %q was not found", receiptID)
	}
	if selected.Status != "failed" {
		return Report{}, fmt.Errorf("lifecycle hook receipt %q is %s, not failed", receiptID, selected.Status)
	}
	cfg, configured, err := Load(dispatcher.ConfigPath)
	if err != nil {
		return Report{}, err
	}
	if !configured {
		return Report{}, errors.New("lifecycle hook configuration does not exist")
	}
	executor, ok := cfg.Executors[selected.Executor]
	if !ok {
		return Report{}, fmt.Errorf("executor %q is no longer configured", selected.Executor)
	}
	item := pending{event: Event{
		Name: selected.Event, Repository: selected.Repository, Checkout: selected.Checkout,
		OldSHA: selected.OldSHA, NewSHA: selected.NewSHA, Cause: "retry:" + selected.ID,
		OperationID: selected.OperationID,
	}, name: selected.Executor, executor: executor, count: 1}
	coalesced, err := dispatcher.enqueue(item)
	if err != nil {
		return Report{}, err
	}
	report := Report{Enqueued: 1, Coalesced: coalesced}
	if dispatcher.LaunchWorker != nil {
		if err := dispatcher.LaunchWorker(WorkerRequest{ConfigPath: dispatcher.ConfigPath, StateDir: dispatcher.StateDir, ReceiptPath: dispatcher.ReceiptPath}); err != nil {
			report.Warnings = append(report.Warnings, "lifecycle hook is queued but the background worker did not start: "+err.Error())
		}
	}
	return report, nil
}

type CheckReport struct {
	ConfigPath string          `json:"config_path"`
	Configured bool            `json:"configured"`
	Executors  []ExecutorCheck `json:"executors,omitempty"`
	Findings   []string        `json:"findings,omitempty"`
}

type ExecutorCheck struct {
	Name     string `json:"name"`
	Run      string `json:"run"`
	Resolved string `json:"resolved,omitempty"`
	Status   string `json:"status"`
	Finding  string `json:"finding,omitempty"`
}

func (dispatcher Dispatcher) Check() (CheckReport, error) {
	dispatcher = dispatcher.defaults()
	report := CheckReport{ConfigPath: dispatcher.ConfigPath}
	cfg, found, err := Load(dispatcher.ConfigPath)
	if err != nil {
		return report, err
	}
	report.Configured = found
	if !found {
		return report, nil
	}
	names := make([]string, 0, len(cfg.Executors))
	for name := range cfg.Executors {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		executor := cfg.Executors[name]
		check := ExecutorCheck{Name: name, Run: executor.Run, Status: "ready"}
		inspected, err := dispatcher.inspectExecutable(executor.Run)
		if err != nil {
			check.Status = "failed"
			check.Finding = err.Error()
			report.Findings = append(report.Findings, fmt.Sprintf("executor %s: %v", name, err))
		} else {
			check.Resolved = inspected.resolved
		}
		report.Executors = append(report.Executors, check)
	}
	return report, nil
}
