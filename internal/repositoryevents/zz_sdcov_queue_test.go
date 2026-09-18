package repositoryevents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

func TestSdCovNewQueueRejectsUnusableProjectsRoot(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewQueue(blocker); err == nil || !strings.Contains(err.Error(), "create repository event queue") {
		t.Fatalf("NewQueue(file) error = %v", err)
	}
}

func TestSdCovNewQueueSkipsUnrelatedEntriesAndRejectsInvalidRecords(t *testing.T) {
	t.Parallel()
	t.Run("unrelated entries are skipped", func(t *testing.T) {
		t.Parallel()
		projects := t.TempDir()
		jobs := sdCovJobsDir(projects)
		if err := os.MkdirAll(filepath.Join(jobs, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jobs, "notes.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		event := sdCovQueueEvent("event-kept")
		sdCovWriteRecord(t, jobs, "kept.json", sdCovValidJob(event, 4))

		queue, err := NewQueue(projects)
		if err != nil {
			t.Fatal(err)
		}
		if len(queue.jobs) != 1 || queue.jobs[event.ID] == nil {
			t.Fatalf("loaded jobs = %+v", queue.jobs)
		}
		if queue.nextSequence != 5 {
			t.Fatalf("next sequence = %d, want 5", queue.nextSequence)
		}
	})

	t.Run("invalid json is rejected", func(t *testing.T) {
		t.Parallel()
		projects := t.TempDir()
		jobs := sdCovJobsDir(projects)
		if err := os.MkdirAll(jobs, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(jobs, "broken.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewQueue(projects); err == nil || !strings.Contains(err.Error(), "is invalid") {
			t.Fatalf("NewQueue error = %v", err)
		}
	})

	t.Run("invalid record is rejected", func(t *testing.T) {
		t.Parallel()
		projects := t.TempDir()
		jobs := sdCovJobsDir(projects)
		if err := os.MkdirAll(jobs, 0o700); err != nil {
			t.Fatal(err)
		}
		item := sdCovValidJob(sdCovQueueEvent("event-bad-schema"), 1)
		item.Schema = 99
		sdCovWriteRecord(t, jobs, "bad-schema.json", item)
		if _, err := NewQueue(projects); err == nil || !strings.Contains(err.Error(), "is invalid") {
			t.Fatalf("NewQueue error = %v", err)
		}
	})

	t.Run("duplicate sequence is rejected", func(t *testing.T) {
		t.Parallel()
		projects := t.TempDir()
		jobs := sdCovJobsDir(projects)
		if err := os.MkdirAll(jobs, 0o700); err != nil {
			t.Fatal(err)
		}
		sdCovWriteRecord(t, jobs, "first.json", sdCovValidJob(sdCovQueueEvent("event-first"), 7))
		sdCovWriteRecord(t, jobs, "second.json", sdCovValidJob(sdCovQueueEvent("event-second"), 7))
		if _, err := NewQueue(projects); err == nil || !strings.Contains(err.Error(), "duplicates sequence") {
			t.Fatalf("NewQueue error = %v", err)
		}
	})

	t.Run("unreadable entry is rejected", func(t *testing.T) {
		t.Parallel()
		projects := t.TempDir()
		jobs := sdCovJobsDir(projects)
		if err := os.MkdirAll(jobs, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(jobs, "missing-target.json"), filepath.Join(jobs, "dangling.json")); err != nil {
			t.Fatal(err)
		}
		if _, err := NewQueue(projects); err == nil {
			t.Fatal("NewQueue accepted a dangling record symlink")
		}
	})
}

func TestSdCovNewQueueReportsUnlistableDirectory(t *testing.T) {
	t.Parallel()
	sdCovSkipIfPrivileged(t)
	projects := t.TempDir()
	jobs := sdCovJobsDir(projects)
	if err := os.MkdirAll(jobs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(jobs, 0o100); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(jobs, 0o700) })
	if _, err := NewQueue(projects); err == nil {
		t.Fatal("NewQueue ignored an unlistable jobs directory")
	}
}

// TestSdCovNewQueueReportsRecoveryPersistenceFailures drives the two in-line
// persist calls inside NewQueue to failure by pre-creating a directory where
// the atomic rename expects a file. This works regardless of privileges.
func TestSdCovNewQueueReportsRecoveryPersistenceFailures(t *testing.T) {
	t.Parallel()
	t.Run("running record recovery", func(t *testing.T) {
		t.Parallel()
		projects := t.TempDir()
		jobs := sdCovJobsDir(projects)
		if err := os.MkdirAll(jobs, 0o700); err != nil {
			t.Fatal(err)
		}
		event := sdCovQueueEvent("event-running")
		item := sdCovValidJob(event, 1)
		item.State = "running"
		sdCovWriteRecord(t, jobs, "running.json", item)
		if err := os.MkdirAll(filepath.Join(jobs, eventFileName(event.ID)+".json"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := NewQueue(projects); err == nil {
			t.Fatal("NewQueue ignored a failed running-record recovery persist")
		}
	})

	t.Run("supersession reconciliation", func(t *testing.T) {
		t.Parallel()
		projects := t.TempDir()
		jobs := sdCovJobsDir(projects)
		if err := os.MkdirAll(jobs, 0o700); err != nil {
			t.Fatal(err)
		}
		victim := sdCovQueueEvent("event-victim")
		superseder := sdCovQueueEvent("event-superseder")
		sdCovWriteRecord(t, jobs, "victim.json", sdCovValidJob(victim, 1))
		supersederRecord := sdCovValidJob(superseder, 2)
		supersederRecord.Supersedes = []string{victim.ID}
		sdCovWriteRecord(t, jobs, "superseder.json", supersederRecord)
		if err := os.MkdirAll(filepath.Join(jobs, eventFileName(victim.ID)+".json"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := NewQueue(projects); err == nil {
			t.Fatal("NewQueue ignored a failed supersession reconciliation persist")
		}
	})
}

func TestSdCovQueueDefaultAcquireUsesSharedBudget(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	queue, err := NewQueue(projects)
	if err != nil {
		t.Fatal(err)
	}
	release, err := queue.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if release == nil {
		t.Fatal("default acquire returned no release function")
	}
	release()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	blockedRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(blockedRoot, ".wb", "runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedRoot, ".wb", "runtime", "cpu"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	blockedQueue, err := NewQueue(blockedRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blockedQueue.acquire(cancelled); err == nil {
		t.Fatal("default acquire ignored an unusable CPU lease directory")
	}
}

func TestSdCovQueueEnqueueRejectsInvalidEventsAndReportsPersistenceFailures(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue(context.Background(), repositoryevent.Event{}); err == nil {
		t.Fatal("Enqueue accepted an invalid event")
	}

	queue.beforePersist = func(*job) error { return errors.New("forced enqueue persistence failure") }
	rejected := sdCovQueueEvent("event-enqueue-fail")
	if _, err := queue.Enqueue(context.Background(), rejected); err == nil || !strings.Contains(err.Error(), "forced enqueue persistence failure") {
		t.Fatalf("Enqueue persist error = %v", err)
	}
	if queue.jobs[rejected.ID] != nil {
		t.Fatal("rejected event was retained in memory")
	}

	queue.beforePersist = nil
	victim := sdCovQueueEvent("event-victim")
	if _, err := queue.Enqueue(context.Background(), victim); err != nil {
		t.Fatal(err)
	}
	superseder := sdCovQueueEvent("event-superseder")

	queue.beforePersist = func(item *job) error {
		if item.State == "superseded" {
			return errors.New("forced supersede persistence failure")
		}
		return nil
	}
	if _, err := queue.Enqueue(context.Background(), superseder); err == nil || !strings.Contains(err.Error(), "forced supersede persistence failure") {
		t.Fatalf("Enqueue supersede error = %v", err)
	}

	// Re-enqueue the known ID: applySupersedes runs on the existing record and
	// must surface its persistence failure.
	queue.jobs[victim.ID].State = "queued"
	if _, err := queue.Enqueue(context.Background(), superseder); err == nil {
		t.Fatal("Enqueue accepted a duplicate whose supersession could not persist")
	}
}

func TestSdCovQueueClaimNextRestoresQueuedStateOnPersistFailure(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	event := sdCovQueueEvent("event-claim-persist-failure")
	if _, err := queue.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	queue.beforePersist = func(*job) error { return errors.New("forced claim persistence failure") }
	if claimed := queue.claimNext(); claimed != nil {
		t.Fatalf("claimNext returned %+v despite a persistence failure", claimed)
	}
	item := queue.jobs[event.ID]
	if item.State != "queued" || item.Attempts != 0 || !strings.Contains(item.Error, "forced claim persistence failure") {
		t.Fatalf("job after failed claim = %+v", item)
	}
	if len(queue.activeRepos) != 0 {
		t.Fatalf("failed claim reserved aliases: %v", queue.activeRepos)
	}
}

func TestSdCovQueueRunIgnoresNilProcessorAndDefaultsToOneWorker(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue.Run(context.Background(), nil, nil)

	queue.workers = 0
	queue.acquire = func(context.Context) (func(), error) { return func() {}, nil }
	event := sdCovQueueEvent("event-single-worker")
	if _, err := queue.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	processed := make(chan string, 1)
	processor := processorFunc(func(_ context.Context, item repositoryevent.Event, _ ProcessState) (ProcessResult, error) {
		processed <- item.ID
		return ProcessResult{Detail: "pulled"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		queue.Run(ctx, processor, nil)
		close(done)
	}()
	select {
	case id := <-processed:
		if id != event.ID {
			t.Fatalf("processed %q, want %q", id, event.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("single default worker never claimed the queued event")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		queue.mu.Lock()
		state := queue.jobs[event.ID].State
		queue.mu.Unlock()
		if state == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job state = %q", state)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("queue did not stop after cancellation")
	}
}

func TestSdCovQueueWorkerWaitsForRetryThenClaimsNewWork(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue.workers = 1
	queue.retryDelay = 5 * time.Millisecond
	queue.acquire = func(context.Context) (func(), error) { return func() {}, nil }
	processed := make(chan string, 1)
	processor := processorFunc(func(_ context.Context, event repositoryevent.Event, _ ProcessState) (ProcessResult, error) {
		processed <- event.ID
		return ProcessResult{Detail: "pulled"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		queue.Run(ctx, processor, nil)
		close(done)
	}()
	// Let the idle worker cycle through its retry timer at least once before
	// work exists, proving the empty-queue path keeps the worker alive.
	time.Sleep(50 * time.Millisecond)
	event := sdCovQueueEvent("event-after-retry-wait")
	if _, err := queue.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-processed:
		if id != event.ID {
			t.Fatalf("processed %q, want %q", id, event.ID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not claim work enqueued after the retry wait")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not stop after cancellation")
	}
}

func TestSdCovQueueRunClaimedReportsAdmissionFailuresAndSkipsInactiveJobs(t *testing.T) {
	t.Parallel()
	newQueueWithClaimedJob := func(t *testing.T, id string) (*Queue, repositoryevent.Event) {
		t.Helper()
		queue, err := NewQueue(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		event := sdCovQueueEvent(id)
		if _, err := queue.Enqueue(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if claimed := queue.claimNext(); claimed == nil || claimed.Event.ID != id {
			t.Fatalf("claim = %+v", claimed)
		}
		return queue, event
	}

	t.Run("admission failure", func(t *testing.T) {
		t.Parallel()
		queue, event := newQueueWithClaimedJob(t, "event-admission")
		called := false
		queue.acquire = func(context.Context) (func(), error) { return nil, errors.New("no sync admission") }
		queue.runClaimed(context.Background(), processorFunc(func(context.Context, repositoryevent.Event, ProcessState) (ProcessResult, error) {
			called = true
			return ProcessResult{}, nil
		}), event.ID)
		if called {
			t.Fatal("processor ran without admission")
		}
		item := queue.jobs[event.ID]
		if item.State != "queued" || !strings.Contains(item.Error, "no sync admission") {
			t.Fatalf("job after failed admission = %+v", item)
		}
		if queue.activeRepos[normalizeRepository(event.Repository)] {
			t.Fatal("failed admission left the repository active")
		}
	})

	t.Run("unknown and inactive jobs", func(t *testing.T) {
		t.Parallel()
		queue, err := NewQueue(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		queue.acquire = func(context.Context) (func(), error) { return func() {}, nil }
		called := false
		processor := processorFunc(func(context.Context, repositoryevent.Event, ProcessState) (ProcessResult, error) {
			called = true
			return ProcessResult{}, nil
		})
		queue.runClaimed(context.Background(), processor, "event-never-queued")

		event := sdCovQueueEvent("event-already-done")
		if _, err := queue.Enqueue(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if claimed := queue.claimNext(); claimed == nil {
			t.Fatal("claim returned nil")
		}
		queue.finish(event.ID, ProcessResult{Detail: "pulled"}, nil)
		queue.runClaimed(context.Background(), processor, event.ID)
		if called {
			t.Fatal("processor ran for an unknown or completed job")
		}
	})

	t.Run("syncing progress persistence failure", func(t *testing.T) {
		t.Parallel()
		queue, event := newQueueWithClaimedJob(t, "event-syncing-persist")
		queue.beforePersist = func(item *job) error {
			if strings.HasPrefix(item.Progress, "syncing ") {
				return errors.New("forced syncing persistence failure")
			}
			return nil
		}
		called := false
		queue.runClaimed(context.Background(), processorFunc(func(context.Context, repositoryevent.Event, ProcessState) (ProcessResult, error) {
			called = true
			return ProcessResult{}, nil
		}), event.ID)
		if called {
			t.Fatal("processor ran after the syncing state could not be persisted")
		}
		item := queue.jobs[event.ID]
		if item.State != "queued" || !strings.Contains(item.Error, "forced syncing persistence failure") {
			t.Fatalf("job after failed syncing persist = %+v", item)
		}
		if queue.activeRepos[normalizeRepository(event.Repository)] {
			t.Fatal("failed syncing persist left the repository active")
		}
	})
}

func TestSdCovQueueCheckpointCleanupValidatesInputAndJobState(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name             string
		receipt, command string
	}{
		{name: "empty receipt", command: "wb repo transfer cleanup"},
		{name: "empty command", receipt: "receipt.json"},
		{name: "oversized receipt", receipt: strings.Repeat("r", 4097), command: "wb repo transfer cleanup"},
		{name: "oversized command", receipt: "receipt.json", command: strings.Repeat("c", 8193)},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := queue.checkpointCleanup("event-unknown", test.receipt, test.command); err == nil {
				t.Fatal("invalid cleanup checkpoint was accepted")
			}
		})
	}

	if err := queue.checkpointCleanup("event-unknown", "receipt.json", "wb repo transfer cleanup"); err == nil {
		t.Fatal("checkpoint for an unknown job was accepted")
	}

	event := sdCovQueueEvent("event-not-running")
	if _, err := queue.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if claimed := queue.claimNext(); claimed == nil {
		t.Fatal("claim returned nil")
	}
	queue.finish(event.ID, ProcessResult{Detail: "pulled"}, nil)
	if err := queue.checkpointCleanup(event.ID, "receipt.json", "wb repo transfer cleanup"); err == nil {
		t.Fatal("checkpoint for a completed job was accepted")
	}
}

func TestSdCovQueueFinishHandlesUnknownIncompleteAndProgress(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue.finish("event-never-queued", ProcessResult{}, nil)

	incomplete := sdCovQueueEvent("event-incomplete-cleanup")
	if _, err := queue.Enqueue(context.Background(), incomplete); err != nil {
		t.Fatal(err)
	}
	if claimed := queue.claimNext(); claimed == nil {
		t.Fatal("claim returned nil")
	}
	queue.finish(incomplete.ID, ProcessResult{CleanupStateSet: true, CleanupReceipt: "receipt.json"}, nil)
	item := queue.jobs[incomplete.ID]
	if item.State != "queued" || !strings.Contains(item.Error, "incomplete repository transfer cleanup") {
		t.Fatalf("job after incomplete cleanup state = %+v", item)
	}

	completed := sdCovQueueEvent("event-progress")
	if _, err := queue.Enqueue(context.Background(), completed); err != nil {
		t.Fatal(err)
	}
	if claimed := queue.claimNext(); claimed == nil {
		t.Fatal("claim returned nil")
	}
	var messages []string
	queue.progress = func(message string) { messages = append(messages, message) }
	queue.finish(completed.ID, ProcessResult{Detail: "pulled"}, nil)
	if item := queue.jobs[completed.ID]; item.State != "succeeded" || item.Progress != "pulled" {
		t.Fatalf("completed job = %+v", item)
	}
	if len(messages) != 1 || !strings.Contains(messages[0], completed.ID) || !strings.Contains(messages[0], "pulled") {
		t.Fatalf("progress messages = %v", messages)
	}
}

func TestSdCovQueueApplySupersedesSkipsNonQueuedAndReportsPersistFailure(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	victim := sdCovQueueEvent("event-victim")
	queue.jobs[victim.ID] = &job{Event: victim, State: "succeeded"}
	superseder := &job{Event: sdCovQueueEvent("event-superseder"), State: "queued", Supersedes: []string{victim.ID}}

	if err := queue.applySupersedes(superseder); err != nil {
		t.Fatal(err)
	}
	if queue.jobs[victim.ID].State != "succeeded" {
		t.Fatalf("non-queued candidate was superseded: %+v", queue.jobs[victim.ID])
	}

	queue.jobs[victim.ID].State = "queued"
	queue.beforePersist = func(*job) error { return errors.New("forced supersede persistence failure") }
	if err := queue.applySupersedes(superseder); err == nil || !strings.Contains(err.Error(), "forced supersede persistence failure") {
		t.Fatalf("applySupersedes error = %v", err)
	}
}

func TestSdCovQueueReconcileSupersededReportsPersistFailure(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	victim := sdCovQueueEvent("event-victim")
	superseder := sdCovQueueEvent("event-superseder")
	queue.jobs[victim.ID] = &job{Event: victim, State: "queued"}
	queue.jobs[superseder.ID] = &job{Event: superseder, State: "queued", Supersedes: []string{victim.ID}}
	queue.beforePersist = func(*job) error { return errors.New("forced reconcile persistence failure") }
	if err := queue.reconcileSuperseded(); err == nil || !strings.Contains(err.Error(), "forced reconcile persistence failure") {
		t.Fatalf("reconcileSuperseded error = %v", err)
	}
}

func TestSdCovRepositoryAliasesCollapsesCaseEquivalentRename(t *testing.T) {
	t.Parallel()
	event := repositoryevent.Event{
		Version:            repositoryevent.ContractVersion,
		ID:                 "event-case-rename",
		Repository:         "github.com/acme/new",
		PreviousRepository: "github.com/Acme/New",
		Ref:                "refs/heads/main",
		Reason:             repositoryevent.ReasonRepositoryRenamed,
	}
	aliases := repositoryAliases(event)
	if len(aliases) != 1 || aliases[0] != "github.com/acme/new" {
		t.Fatalf("aliases = %v", aliases)
	}
}

func TestSdCovQueueHeartbeatRefreshesRunningJobAndStops(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue.progressEvery = time.Millisecond
	event := sdCovQueueEvent("event-heartbeat")
	if _, err := queue.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if claimed := queue.claimNext(); claimed == nil {
		t.Fatal("claim returned nil")
	}
	messages := make(chan string, 8)
	queue.progress = func(message string) {
		select {
		case messages <- message:
		default:
		}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		queue.heartbeat(context.Background(), event.ID, done)
		close(stopped)
	}()
	select {
	case message := <-messages:
		if !strings.Contains(message, event.ID) {
			t.Fatalf("heartbeat progress = %q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat never reported a running job")
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat did not stop when its done channel closed")
	}
}

func TestSdCovQueueHeartbeatHonorsCancellationAndIgnoresInactiveJobs(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue.progressEvery = time.Millisecond
	messages := make(chan string, 8)
	queue.progress = func(message string) {
		select {
		case messages <- message:
		default:
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stopped := make(chan struct{})
	go func() {
		queue.heartbeat(ctx, "event-unknown", make(chan struct{}))
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat ignored context cancellation")
	}

	event := sdCovQueueEvent("event-heartbeat-inactive")
	if _, err := queue.Enqueue(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if claimed := queue.claimNext(); claimed == nil {
		t.Fatal("claim returned nil")
	}
	queue.finish(event.ID, ProcessResult{Detail: "pulled"}, nil)
	for len(messages) > 0 {
		<-messages // drop the completion message; only heartbeat output matters
	}
	done := make(chan struct{})
	stopped = make(chan struct{})
	go func() {
		queue.heartbeat(context.Background(), event.ID, done)
		close(stopped)
	}()
	time.Sleep(20 * time.Millisecond)
	if len(messages) != 0 {
		t.Fatalf("heartbeat reported an inactive job: %v", <-messages)
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat did not stop")
	}
}

func TestSdCovQueuePersistReportsFilesystemAndRenameFailures(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	event := sdCovQueueEvent("event-persist-target")
	item := sdCovValidJob(event, 1)

	queue.directory = filepath.Join(t.TempDir(), "missing-directory")
	if err := queue.persist(&item); err == nil {
		t.Fatal("persist ignored a missing queue directory")
	}

	directory := t.TempDir()
	queue.directory = directory
	target := filepath.Join(directory, eventFileName(event.ID)+".json")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "occupied"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := queue.persist(&item); err == nil {
		t.Fatal("persist overwrote a directory in place of the record")
	}

	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := queue.persist(&item); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var reloaded job
	if err := json.Unmarshal(raw, &reloaded); err != nil {
		t.Fatal(err)
	}
	if err := validateJobRecord(reloaded); err != nil {
		t.Fatalf("persisted record is invalid: %v", err)
	}
	if reloaded.Event.ID != event.ID {
		t.Fatalf("persisted event id = %q, want %q", reloaded.Event.ID, event.ID)
	}
}

func TestSdCovValidateJobRecordRejectsInvalidFields(t *testing.T) {
	t.Parallel()
	event := sdCovQueueEvent("event-valid")
	base := sdCovValidJob(event, 1)
	if err := validateJobRecord(base); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	renameEvent := repositoryevent.Event{
		Version: repositoryevent.ContractVersion, ID: "event-valid",
		Repository: "github.com/acme/new", PreviousRepository: "github.com/acme/old",
		Ref: "refs/heads/main", Reason: repositoryevent.ReasonRepositoryRenamed,
	}

	tests := []struct {
		name    string
		mutate  func(*job)
		wantErr string
	}{
		{name: "wrong schema", mutate: func(item *job) { item.Schema = 99 }, wantErr: "invalid repository event queue record"},
		{name: "zero sequence", mutate: func(item *job) { item.Sequence = 0 }, wantErr: "invalid repository event queue record"},
		{name: "invalid event", mutate: func(item *job) { item.Event = repositoryevent.Event{} }, wantErr: "invalid repository event queue record"},
		{name: "stale digest", mutate: func(item *job) { item.EventDigest = "stale" }, wantErr: "invalid repository event queue record"},
		{name: "zero queued at", mutate: func(item *job) { item.QueuedAt = time.Time{} }, wantErr: "invalid repository event queue record"},
		{name: "zero updated at", mutate: func(item *job) { item.UpdatedAt = time.Time{} }, wantErr: "invalid repository event queue record"},
		{name: "unknown state", mutate: func(item *job) { item.State = "paused" }, wantErr: "invalid repository event queue state"},
		{
			name: "supersession on a rename",
			mutate: func(item *job) {
				item.Event = renameEvent
				item.EventDigest = eventDigest(renameEvent)
				item.Supersedes = []string{"event-other"}
			},
			wantErr: "invalid repository event supersession",
		},
		{
			name: "too many supersessions",
			mutate: func(item *job) {
				item.Supersedes = make([]string, repositoryevent.MaxLimit+1)
			},
			wantErr: "invalid repository event supersession",
		},
		{name: "invalid supersede id", mutate: func(item *job) { item.Supersedes = []string{"not a valid id"} }, wantErr: "invalid repository event supersession"},
		{name: "self supersession", mutate: func(item *job) { item.Supersedes = []string{item.Event.ID} }, wantErr: "invalid repository event supersession"},
		{name: "receipt without command", mutate: func(item *job) { item.CleanupReceipt = "receipt.json" }, wantErr: "invalid repository transfer cleanup state"},
		{name: "command without receipt", mutate: func(item *job) { item.RecoveryCommand = "wb repo transfer cleanup" }, wantErr: "invalid repository transfer cleanup state"},
		{
			name: "oversized receipt",
			mutate: func(item *job) {
				item.CleanupReceipt = strings.Repeat("r", 4097)
				item.RecoveryCommand = "wb repo transfer cleanup"
			},
			wantErr: "invalid repository transfer cleanup state",
		},
		{
			name: "oversized recovery command",
			mutate: func(item *job) {
				item.CleanupReceipt = "receipt.json"
				item.RecoveryCommand = strings.Repeat("c", 8193)
			},
			wantErr: "invalid repository transfer cleanup state",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			item := base
			test.mutate(&item)
			err := validateJobRecord(item)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("validateJobRecord error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestSdCovQueuePersistReportsUnmarshalableRecord(t *testing.T) {
	t.Parallel()
	queue, err := NewQueue(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	event := sdCovQueueEvent("event-unmarshalable")
	item := sdCovValidJob(event, 1)
	// time.Time.MarshalJSON rejects years outside [0,9999], which is the only
	// way a job record can fail to serialize.
	outOfRange := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	item.Event.OccurredAt = &outOfRange
	queue.directory = t.TempDir()
	if err := queue.persist(&item); err == nil {
		t.Fatal("persist accepted an unserializable record")
	}
	if _, err := os.Stat(filepath.Join(queue.directory, eventFileName(event.ID)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unserializable record left a file behind: %v", err)
	}
}
