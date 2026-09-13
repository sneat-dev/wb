package lifecyclehooks

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

type queuedJob struct {
	SchemaVersion  int       `json:"schema_version"`
	Key            string    `json:"key"`
	Executor       string    `json:"executor"`
	Event          Event     `json:"event"`
	CoalescedCount int       `json:"coalesced_count"`
	QueuedAt       time.Time `json:"queued_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func queueKey(executor, checkout string) string {
	return executor + "\x00" + filepath.Clean(checkout)
}

func queueFileName(key string) string {
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:]) + ".json"
}

func (dispatcher Dispatcher) enqueue(item pending) (int, error) {
	if err := dispatcher.ensureState(); err != nil {
		return 0, err
	}
	lock := flock.New(filepath.Join(dispatcher.StateDir, "queue.lock"))
	if err := lock.Lock(); err != nil {
		return 0, fmt.Errorf("lock lifecycle hook queue: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	now := dispatcher.Now().UTC()
	job := queuedJob{
		SchemaVersion: jobSchemaVersion, Key: queueKey(item.name, item.event.Checkout),
		Executor: item.name, Event: item.event, CoalescedCount: item.count,
		QueuedAt: now, UpdatedAt: now,
	}
	path := filepath.Join(dispatcher.pendingDir(), queueFileName(job.Key))
	coalesced := item.count - 1
	if existing, err := readJob(path); err == nil {
		job.QueuedAt = existing.QueuedAt
		job.Event.OldSHA = existing.Event.OldSHA
		job.CoalescedCount += existing.CoalescedCount
		// Every event in this incoming item is folded into the already-pending
		// job. Do not also count the item's internal coalescing a second time.
		coalesced = item.count
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	if err := writeJSONAtomic(path, job, 0o600); err != nil {
		return 0, fmt.Errorf("write lifecycle hook queue item: %w", err)
	}
	return coalesced, nil
}

func (dispatcher Dispatcher) ensureState() error {
	for _, directory := range []string{dispatcher.StateDir, dispatcher.pendingDir(), dispatcher.runningDir(), dispatcher.diagnosticsDir(), dispatcher.unseenDir(), dispatcher.quarantineDir()} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return fmt.Errorf("create lifecycle hook state: %w", err)
		}
		if err := validatePrivateDirectory(directory, "lifecycle hook state directory"); err != nil {
			return fmt.Errorf("protect lifecycle hook state: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return fmt.Errorf("protect lifecycle hook state: %w", err)
		}
	}
	return nil
}

func (dispatcher Dispatcher) pendingDir() string {
	return filepath.Join(dispatcher.StateDir, "pending")
}
func (dispatcher Dispatcher) runningDir() string {
	return filepath.Join(dispatcher.StateDir, "running")
}
func (dispatcher Dispatcher) diagnosticsDir() string {
	return filepath.Join(dispatcher.StateDir, "diagnostics")
}

// Drain executes durable queue entries with bounded concurrency. Only one
// worker owns a state directory; enqueue uses a separate short-lived lock and
// therefore never waits for an external command to finish.
func (dispatcher Dispatcher) Drain(ctx context.Context, parallel int) (report Report, returnErr error) {
	dispatcher = dispatcher.defaults()
	if parallel <= 0 {
		parallel = defaultParallelism
	}
	if parallel > 16 {
		parallel = 16
	}
	if err := dispatcher.ensureState(); err != nil {
		return Report{}, err
	}
	worker := flock.New(filepath.Join(dispatcher.StateDir, "worker.lock"))
	owned, err := worker.TryLock()
	if err != nil {
		return Report{}, fmt.Errorf("lock lifecycle hook worker: %w", err)
	}
	if !owned {
		return Report{}, nil
	}
	started := dispatcher.Now().UTC()
	if err := dispatcher.writeWorkerHealth(WorkerHealth{Status: "running", StartedAt: started}); err != nil {
		_ = worker.Unlock()
		return Report{}, fmt.Errorf("record lifecycle hook worker start: %w", err)
	}
	workerHeld := true
	defer func() {
		if workerHeld {
			_ = worker.Unlock()
		}
		health := WorkerHealth{Status: "idle", StartedAt: started, FinishedAt: dispatcher.Now().UTC()}
		if returnErr != nil {
			health.Status = "failed"
			health.Message = boundedMessage(returnErr.Error(), 512)
		} else if len(report.Warnings) != 0 {
			health.Status = "warning"
			health.Message = boundedMessage(strings.Join(report.Warnings, "; "), 512)
		}
		if err := dispatcher.writeWorkerHealth(health); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("record lifecycle hook worker completion: %w", err)
		}
	}()
	recoveryWarnings, err := dispatcher.recoverRunning()
	report.Warnings = append(report.Warnings, recoveryWarnings...)
	if err != nil {
		return report, err
	}

	for {
		jobs, released, claimWarnings, err := dispatcher.claimBatch(parallel, worker)
		report.Warnings = append(report.Warnings, claimWarnings...)
		if err != nil {
			return report, err
		}
		if released {
			workerHeld = false
			return report, nil
		}
		results := make(chan jobResult, len(jobs))
		var wait sync.WaitGroup
		for _, job := range jobs {
			wait.Add(1)
			go func(job queuedJob) {
				defer wait.Done()
				results <- dispatcher.runJob(ctx, job)
			}(job)
		}
		wait.Wait()
		close(results)
		for result := range results {
			if result.receipt.Status == "succeeded" {
				report.Executed++
			} else {
				report.Warnings = append(report.Warnings, fmt.Sprintf("lifecycle hook %s for %s failed: %s", result.receipt.Executor, result.receipt.Repository, result.receipt.Message))
			}
			if err := appendReceipt(dispatcher.ReceiptPath, result.receipt); err != nil {
				report.Warnings = append(report.Warnings, "record lifecycle hook receipt: "+err.Error())
				continue
			}
			if result.receipt.Status == "failed" {
				if err := dispatcher.recordUnseenFailure(result.receipt); err != nil {
					report.Warnings = append(report.Warnings, "record unseen lifecycle hook failure: "+err.Error())
				}
			}
			if err := dispatcher.complete(result.job); err != nil {
				report.Warnings = append(report.Warnings, "complete lifecycle hook queue item: "+err.Error())
			}
		}
	}
}

func (dispatcher Dispatcher) recoverRunning() ([]string, error) {
	lock := flock.New(filepath.Join(dispatcher.StateDir, "queue.lock"))
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	entries, err := os.ReadDir(dispatcher.runningDir())
	if err != nil {
		return nil, err
	}
	var warnings []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		runningPath := filepath.Join(dispatcher.runningDir(), entry.Name())
		running, err := readJob(runningPath)
		if err != nil {
			quarantined, quarantineErr := dispatcher.quarantineFile(runningPath, err.Error())
			if quarantineErr != nil {
				return warnings, quarantineErr
			}
			warnings = append(warnings, "quarantined invalid running lifecycle-hook item at "+quarantined)
			continue
		}
		pendingPath := filepath.Join(dispatcher.pendingDir(), entry.Name())
		if pending, pendingErr := readJob(pendingPath); pendingErr == nil {
			pending.Event.OldSHA = running.Event.OldSHA
			pending.QueuedAt = running.QueuedAt
			pending.CoalescedCount += running.CoalescedCount
			if err := writeJSONAtomic(pendingPath, pending, 0o600); err != nil {
				return warnings, err
			}
		} else if errors.Is(pendingErr, os.ErrNotExist) {
			if err := os.Rename(runningPath, pendingPath); err != nil {
				return warnings, err
			}
			continue
		} else {
			quarantined, quarantineErr := dispatcher.quarantineFile(pendingPath, pendingErr.Error())
			if quarantineErr != nil {
				return warnings, quarantineErr
			}
			warnings = append(warnings, "quarantined invalid pending lifecycle-hook item at "+quarantined)
			if err := os.Rename(runningPath, pendingPath); err != nil {
				return warnings, err
			}
			continue
		}
		if err := os.Remove(runningPath); err != nil {
			return warnings, err
		}
	}
	return warnings, syncQueueDirectories(dispatcher.pendingDir(), dispatcher.runningDir())
}

func (dispatcher Dispatcher) claimBatch(limit int, worker *flock.Flock) ([]queuedJob, bool, []string, error) {
	queueLock := flock.New(filepath.Join(dispatcher.StateDir, "queue.lock"))
	if err := queueLock.Lock(); err != nil {
		return nil, false, nil, err
	}
	defer func() { _ = queueLock.Unlock() }()
	entries, err := os.ReadDir(dispatcher.pendingDir())
	if err != nil {
		return nil, false, nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	jobs := make([]queuedJob, 0, limit)
	var warnings []string
	for _, entry := range entries {
		if len(jobs) == limit {
			break
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		pendingPath := filepath.Join(dispatcher.pendingDir(), entry.Name())
		job, err := readJob(pendingPath)
		if err != nil {
			quarantined, quarantineErr := dispatcher.quarantineFile(pendingPath, err.Error())
			if quarantineErr != nil {
				return nil, false, warnings, quarantineErr
			}
			warnings = append(warnings, "quarantined invalid pending lifecycle-hook item at "+quarantined)
			continue
		}
		if err := os.Rename(pendingPath, filepath.Join(dispatcher.runningDir(), entry.Name())); err != nil {
			return nil, false, warnings, err
		}
		jobs = append(jobs, job)
	}
	if len(jobs) != 0 {
		if err := syncQueueDirectories(dispatcher.pendingDir(), dispatcher.runningDir()); err != nil {
			return nil, false, warnings, err
		}
		return jobs, false, warnings, nil
	}
	// Release worker ownership while the queue lock still excludes enqueue.
	// A subsequent enqueue will then start a worker that can acquire ownership,
	// closing the otherwise tiny empty-queue shutdown race.
	if err := worker.Unlock(); err != nil {
		return nil, false, warnings, err
	}
	return nil, true, warnings, nil
}

type jobResult struct {
	job     queuedJob
	receipt Receipt
}

func (dispatcher Dispatcher) runJob(parent context.Context, job queuedJob) jobResult {
	started := dispatcher.Now().UTC()
	receipt := Receipt{
		SchemaVersion: receiptSchemaVersion, ID: receiptID(started), Event: job.Event.Name,
		Repository: job.Event.Repository, Checkout: job.Event.Checkout,
		OldSHA: job.Event.OldSHA, NewSHA: job.Event.NewSHA, Cause: job.Event.Cause,
		OperationID: job.Event.OperationID, Executor: job.Executor,
		CoalescedCount: job.CoalescedCount, QueuedAt: job.QueuedAt, StartedAt: started,
	}
	diagnosticDir := filepath.Join(dispatcher.diagnosticsDir(), receipt.ID)
	stdout, stderr, err := openDiagnostics(diagnosticDir)
	if err == nil {
		receipt.StdoutPath = stdout.file.Name()
		receipt.StderrPath = stderr.file.Name()
	}
	if err == nil {
		cfg, found, loadErr := Load(dispatcher.ConfigPath)
		if loadErr != nil {
			err = loadErr
		} else if !found {
			err = errors.New("lifecycle hook configuration no longer exists")
		} else {
			executor, exists := cfg.Executors[job.Executor]
			if !exists {
				err = fmt.Errorf("executor %q is no longer configured", job.Executor)
			} else {
				item := pending{event: job.Event, name: job.Executor, executor: executor, count: job.CoalescedCount}
				var invocation Invocation
				invocation, err = dispatcher.prepare(item)
				if err == nil {
					invocation.Stdout, invocation.Stderr = stdout, stderr
					err = dispatcher.revalidate(invocation)
					if err == nil {
						timeout, _ := executor.timeout()
						ctx, cancel := context.WithTimeout(parent, timeout)
						err = dispatcher.Run(ctx, invocation)
						if errors.Is(ctx.Err(), context.DeadlineExceeded) {
							err = context.DeadlineExceeded
						}
						cancel()
					}
				}
			}
		}
	}
	if stdout != nil {
		receipt.StdoutTruncated = stdout.truncated
		_ = stdout.file.Close()
	}
	if stderr != nil {
		receipt.StderrTruncated = stderr.truncated
		_ = stderr.file.Close()
	}
	finished := dispatcher.Now().UTC()
	receipt.FinishedAt = finished
	receipt.DurationMS = finished.Sub(started).Milliseconds()
	if err != nil {
		receipt.Status = "failed"
		receipt.Failure = failureClass(err)
		receipt.Message = boundedMessage(err.Error(), 512)
	} else {
		receipt.Status = "succeeded"
	}
	return jobResult{job: job, receipt: receipt}
}

type cappedFile struct {
	file      *os.File
	written   int64
	truncated bool
}

func (writer *cappedFile) Write(content []byte) (int, error) {
	original := len(content)
	remaining := int64(maxDiagnosticBytes) - writer.written
	if remaining <= 0 {
		writer.truncated = writer.truncated || original > 0
		return original, nil
	}
	toWrite := content
	if int64(len(toWrite)) > remaining {
		toWrite = toWrite[:remaining]
		writer.truncated = true
	}
	written, err := writer.file.Write(toWrite)
	writer.written += int64(written)
	if err != nil {
		return written, err
	}
	return original, nil
}

func openDiagnostics(directory string) (*cappedFile, *cappedFile, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, nil, err
	}
	stdout, err := os.OpenFile(filepath.Join(directory, "stdout.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, nil, err
	}
	stderr, err := os.OpenFile(filepath.Join(directory, "stderr.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		_ = stdout.Close()
		return nil, nil, err
	}
	return &cappedFile{file: stdout}, &cappedFile{file: stderr}, nil
}

func boundedMessage(message string, limit int) string {
	message = strings.TrimSpace(strings.ReplaceAll(message, "\n", " "))
	if len(message) <= limit {
		return message
	}
	return message[:limit] + "…"
}

func (dispatcher Dispatcher) complete(job queuedJob) error {
	lock := flock.New(filepath.Join(dispatcher.StateDir, "queue.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	path := filepath.Join(dispatcher.runningDir(), queueFileName(job.Key))
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(dispatcher.runningDir())
}

func readJob(path string) (queuedJob, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return queuedJob{}, err
	}
	var job queuedJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return queuedJob{}, fmt.Errorf("decode lifecycle hook queue item %s: %w", path, err)
	}
	if job.SchemaVersion != jobSchemaVersion || job.Key == "" || job.Executor == "" || job.Event.Checkout == "" || job.Key != queueKey(job.Executor, job.Event.Checkout) {
		return queuedJob{}, fmt.Errorf("invalid lifecycle hook queue item %s", path)
	}
	return job, nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func (dispatcher Dispatcher) quarantineFile(path, reason string) (string, error) {
	if err := os.MkdirAll(dispatcher.quarantineDir(), 0o700); err != nil {
		return "", err
	}
	if err := validatePrivateDirectory(dispatcher.quarantineDir(), "lifecycle hook quarantine directory"); err != nil {
		return "", err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)) + "-" + receiptID(dispatcher.Now().UTC()) + ".bad"
	destination := filepath.Join(dispatcher.quarantineDir(), name)
	if err := os.Rename(path, destination); err != nil {
		return "", err
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(destination+".reason.txt", []byte(boundedMessage(reason, 1024)+"\n"), 0o600); err != nil {
		return "", err
	}
	return destination, syncQueueDirectories(filepath.Dir(path), dispatcher.quarantineDir())
}

func appendReceipt(path string, receipt Receipt) error {
	if err := validateReceipt(receipt); err != nil {
		return err
	}
	if err := ensureTrustedParent(path, "lifecycle hook receipt parent"); err != nil {
		return err
	}
	if err := os.MkdirAll(receiptIndexDir(path), 0o700); err != nil {
		return err
	}
	if err := validatePrivateDirectory(receiptIndexDir(path), "lifecycle hook receipt index"); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	expected, existed, err := validateTrustedDataFile(path, "lifecycle hook receipt stream")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if existed && !os.SameFile(expected, opened) {
		return errors.New("lifecycle hook receipt stream changed while opening")
	}
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := writeJSONAtomic(filepath.Join(receiptIndexDir(path), receipt.ID+".json"), receipt, 0o600); err != nil {
		return fmt.Errorf("index lifecycle hook receipt: %w", err)
	}
	return syncDirectory(filepath.Dir(path))
}

func syncQueueDirectories(directories ...string) error {
	for _, directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

func receiptIndexDir(path string) string {
	return path + ".d"
}

func readRecentReceipts(path string, limit int) ([]Receipt, []string, error) {
	var receipts []Receipt
	findings, err := scanReceipts(path, func(receipt Receipt) {
		if len(receipts) == limit {
			copy(receipts, receipts[1:])
			receipts[len(receipts)-1] = receipt
			return
		}
		receipts = append(receipts, receipt)
	})
	return receipts, findings, err
}

func findReceipt(path, id string) (Receipt, bool, error) {
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return Receipt{}, false, fmt.Errorf("invalid lifecycle hook receipt ID %q", id)
	}
	raw, err := os.ReadFile(filepath.Join(receiptIndexDir(path), id+".json"))
	if err == nil {
		var indexed Receipt
		if decodeErr := json.Unmarshal(raw, &indexed); decodeErr == nil && validateReceipt(indexed) == nil && indexed.ID == id {
			return indexed, true, nil
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Receipt{}, false, err
	}
	var selected Receipt
	found := false
	_, err = scanReceipts(path, func(receipt Receipt) {
		if receipt.ID == id {
			selected = receipt
			found = true
		}
	})
	return selected, found, err
}

func scanReceipts(path string, visit func(Receipt)) ([]string, error) {
	if err := ensureTrustedParent(path, "lifecycle hook receipt parent"); err != nil {
		return nil, err
	}
	lock := flock.New(path + ".lock")
	if err := lock.Lock(); err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	expected, exists, err := validateTrustedDataFile(path, "lifecycle hook receipt stream")
	if err != nil || !exists {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(expected, opened) {
		return nil, errors.New("lifecycle hook receipt stream changed while opening")
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var findings []string
	line := 0
	for scanner.Scan() {
		line++
		receipt, err := decodeReceipt(scanner.Bytes())
		if err != nil {
			if len(findings) < 20 {
				findings = append(findings, fmt.Sprintf("ignored invalid lifecycle hook receipt line %d: %v", line, err))
			}
			continue
		}
		visit(receipt)
	}
	return findings, scanner.Err()
}

func decodeReceipt(raw []byte) (Receipt, error) {
	var receipt Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return Receipt{}, err
	}
	if err := validateReceipt(receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func validateReceipt(receipt Receipt) error {
	if receipt.SchemaVersion != receiptSchemaVersion {
		return fmt.Errorf("unsupported receipt schema version %d", receipt.SchemaVersion)
	}
	if receipt.ID == "" || filepath.Base(receipt.ID) != receipt.ID || strings.ContainsAny(receipt.ID, `/\\`) {
		return fmt.Errorf("invalid receipt ID %q", receipt.ID)
	}
	return nil
}
