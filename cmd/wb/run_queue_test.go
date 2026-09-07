package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/runqueue"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestAcquireWithQueueVisibilityEmitsQueuedHeartbeatsThenAdmitted drives
// acquireWithQueueVisibility directly against an injected queue whose only
// slot is already held, with a heartbeat cadence shrunk for the test. It is
// the fast, deterministic counterpart to
// TestRunCommandReportsQueueVisibilityOnStderr below, which proves the same
// behavior end to end through `wb run --`.
func TestAcquireWithQueueVisibilityEmitsQueuedHeartbeatsThenAdmitted(t *testing.T) {
	root := t.TempDir()
	held, _, err := runqueue.Acquire(context.Background(), root, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Registered PIDs are now liveness-checked (a killed process's ticket or
	// holder record must not linger forever), so both self and the holder
	// must carry a real, live PID; this test process's own PID qualifies for
	// the whole test.
	holderPID := os.Getpid()
	holderAnnouncement := held.Announce(runqueue.Participant{PID: holderPID, Summary: "go build"})
	released := make(chan struct{})
	go func() {
		// Longer than queueAdmissionGrace so the wait goes through the
		// queued+heartbeat path rather than resolving as immediate.
		time.Sleep(queueAdmissionGrace + 60*time.Millisecond)
		holderAnnouncement.Cleanup()
		held.Release()
		close(released)
	}()
	t.Cleanup(func() { <-released })

	var out bytes.Buffer
	progress := newRunQueueProgressWithHeartbeat(&out, true, "", 5*time.Millisecond)
	self := runqueue.Participant{PID: os.Getpid(), Summary: "go test", Worktree: "/w/waiter"}

	lease, waited, err := acquireWithQueueVisibility(context.Background(), root, 1, 1, self, progress)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if waited < queueAdmissionGrace {
		t.Fatalf("waited = %s, want at least the admission grace period", waited)
	}

	rendered := out.String()
	wantQueued := fmt.Sprintf("wb run: queued go test (position 1 of 1, waiting on: %d go build)", holderPID)
	if !strings.Contains(rendered, wantQueued) {
		t.Fatalf("output missing the queued line %q: %q", wantQueued, rendered)
	}
	if count := strings.Count(rendered, "wb run: still queued"); count < 2 {
		t.Fatalf("output had %d heartbeats within the wait, want at least 2: %q", count, rendered)
	}
	if !strings.Contains(rendered, "wb run: admitted after") {
		t.Fatalf("output missing the admitted-after-wait line: %q", rendered)
	}
	if strings.Contains(rendered, "admitted (queue empty)") {
		t.Fatalf("a command that had to wait must not also claim an empty queue: %q", rendered)
	}
}

// TestAcquireWithQueueVisibilityAdmitsImmediatelyOnAnEmptyQueue covers the
// common case: a free slot admits inside the grace period and only the
// single "admitted (queue empty)" line prints — no queued line, no
// heartbeats.
func TestAcquireWithQueueVisibilityAdmitsImmediatelyOnAnEmptyQueue(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	progress := newRunQueueProgressWithHeartbeat(&out, true, "", 5*time.Millisecond)
	self := runqueue.Participant{PID: 1, Summary: "go test"}

	lease, waited, err := acquireWithQueueVisibility(context.Background(), root, 1, 1, self, progress)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if waited >= queueAdmissionGrace {
		t.Fatalf("waited = %s, want an immediate admission on an empty queue", waited)
	}
	if got := out.String(); !strings.Contains(got, "wb run: admitted (queue empty)") {
		t.Fatalf("output = %q, want the admitted-immediately line", got)
	}
	if strings.Contains(out.String(), "queued") {
		t.Fatalf("an immediate admission must not print a queued line: %q", out.String())
	}
}

// TestAcquireWithQueueVisibilitySkipsEverythingForUnitsZero covers a
// non-CPU-governed command (git status, and the vast majority of `wb run
// --` invocations): no ticket, no lines, immediate return.
func TestAcquireWithQueueVisibilitySkipsEverythingForUnitsZero(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	progress := newRunQueueProgressWithHeartbeat(&out, true, "", 5*time.Millisecond)
	lease, waited, err := acquireWithQueueVisibility(context.Background(), root, 0, 1, runqueue.Participant{PID: 1, Summary: "git status"}, progress)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if waited != 0 {
		t.Fatalf("waited = %s, want 0 for units <= 0", waited)
	}
	if out.Len() != 0 {
		t.Fatalf("units <= 0 must not print any queue line: %q", out.String())
	}
	if state := runqueue.Peek(root, 1); state.Total != 0 {
		t.Fatalf("units <= 0 must not register a waiting ticket: %+v", state)
	}
}

// buildTinyGoModule writes a trivial buildable module to dir so integration
// tests can drive real `go build`/`go vet` subprocesses through `wb run --`
// without depending on this repository's own build graph.
func buildTinyGoModule(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module tinyfixture\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRunCommandReportsQueueVisibilityOnStderr drives the full `wb run --`
// path: an injected queue whose only slot is already held forces the real
// command to wait, and the test asserts the queued/heartbeat/admitted/done
// receipts land on stderr while stdout — the wrapped command's own output —
// stays untouched. This is the end-to-end counterpart to
// TestAcquireWithQueueVisibilityEmitsQueuedHeartbeatsThenAdmitted.
func TestRunCommandReportsQueueVisibilityOnStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	t.Setenv("WB_ADMISSION_LOAD_FLOOR", "100000")
	root := t.TempDir()
	runQueueHeartbeatOverride = 5 * time.Millisecond
	t.Cleanup(func() { runQueueHeartbeatOverride = 0 })

	module := t.TempDir()
	buildTinyGoModule(t, module)
	t.Chdir(module)

	budget := runqueue.Budget()
	held, _, err := runqueue.Acquire(context.Background(), root, budget, budget)
	if err != nil {
		t.Fatal(err)
	}
	holderAnnouncement := held.Announce(runqueue.Participant{PID: os.Getpid(), Summary: "go build"})
	released := make(chan struct{})
	go func() {
		time.Sleep(queueAdmissionGrace + 60*time.Millisecond)
		holderAnnouncement.Cleanup()
		held.Release()
		close(released)
	}()
	t.Cleanup(func() { <-released })

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--projects-root", root, "--", "go", "build", "./..."}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitOK, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want the wrapped command's own (empty, on success) output only", stdout.String())
	}
	rendered := stderr.String()
	for _, want := range []string{
		"wb run: queued go build",
		"wb run: still queued",
		"wb run: admitted after",
		"wb run: done in",
		"(exit 0)",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("stderr missing %q: %q", want, rendered)
		}
	}
}

// TestRunCommandQuietSuppressesQueueVisibilityLines proves --quiet silences
// every queue receipt while leaving the command's own exit code and streams
// alone.
func TestRunCommandQuietSuppressesQueueVisibilityLines(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	t.Setenv("WB_ADMISSION_LOAD_FLOOR", "100000")
	root := t.TempDir()

	module := t.TempDir()
	buildTinyGoModule(t, module)
	t.Chdir(module)

	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--projects-root", root, "--quiet", "--", "go", "build", "./..."}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d; stderr=%s", code, exitOK, stderr.String())
	}
	if strings.Contains(stderr.String(), "wb run:") {
		t.Fatalf("--quiet did not suppress queue receipts: %q", stderr.String())
	}
}

// TestRunHistoryRecordsQueueWaitAndAdmissionTime confirms the runlog receipt
// carries both the queue wait duration (QueueWaitMS, pre-existing) and the
// new admission timestamp for a governed command.
func TestRunHistoryRecordsQueueWaitAndAdmissionTime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the WB fleet runs on macOS and Linux")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH")
	}
	t.Setenv("WB_ADMISSION_LOAD_FLOOR", "100000")
	root := t.TempDir()

	module, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	buildTinyGoModule(t, module)
	git := exec.Command("git", "init", "-b", "main")
	git.Dir = module
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "run-queue-visibility", EffortKind: worktrees.EffortKindFeature,
		Repository: "acme/app", Worktree: module, Branch: "run-queue-visibility", Base: "main",
		BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
		RunID: "run-1", ClaimID: strings.Repeat("b", 64), Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.WriteManifest(module, manifest); err != nil {
		t.Fatal(err)
	}
	t.Chdir(module)

	budget := runqueue.Budget()
	held, _, err := runqueue.Acquire(context.Background(), root, budget, budget)
	if err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(queueAdmissionGrace + 40*time.Millisecond)
		held.Release()
		close(released)
	}()
	t.Cleanup(func() { <-released })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"run", "--projects-root", root, "--quiet", "--", "go", "build", "./..."}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}

	events, _, err := runlog.ReadCurrent(module)
	if err != nil {
		t.Fatal(err)
	}
	var completed *runlog.Event
	for index := range events {
		if events[index].State == "succeeded" {
			completed = &events[index]
		}
	}
	if completed == nil {
		t.Fatalf("no succeeded event among %d events", len(events))
	}
	if completed.QueueWaitMS < int64(queueAdmissionGrace/time.Millisecond) {
		t.Errorf("QueueWaitMS = %d, want at least the admission grace period", completed.QueueWaitMS)
	}
	if completed.AdmittedAt == nil || completed.AdmittedAt.IsZero() {
		t.Fatalf("AdmittedAt = %v, want a recorded admission time", completed.AdmittedAt)
	}
}

// TestRunQueueListsRunningAndWaitingEntries covers `wb run --queue`: it must
// see an announced holder and a registered waiter without submitting a
// command of its own.
func TestRunQueueListsRunningAndWaitingEntries(t *testing.T) {
	root := t.TempDir()

	lease, _, err := runqueue.Acquire(context.Background(), root, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	// Registered PIDs are liveness-checked (a killed process's record must
	// not linger forever), so both the holder and the waiter need a real,
	// live PID here; this test process's own PID qualifies for the test.
	pid := os.Getpid()
	announcement := lease.Announce(runqueue.Participant{PID: pid, Summary: "go build", Worktree: "/w/one"})
	defer announcement.Cleanup()
	waiter := runqueue.Register(root, runqueue.Participant{PID: pid, Summary: "go test", Worktree: "/w/two"})
	defer waiter.Forget()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"run", "--projects-root", root, "--queue", "--json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}
	var listing runqueue.QueueListing
	if err := json.Unmarshal(stdout.Bytes(), &listing); err != nil {
		t.Fatalf("decode --queue --json output: %v\n%s", err, stdout.String())
	}
	if len(listing.Running) != 1 || listing.Running[0].PID != pid || listing.Running[0].Summary != "go build" {
		t.Fatalf("listing.Running = %+v", listing.Running)
	}
	if len(listing.Waiting) != 1 || listing.Waiting[0].PID != pid || listing.Waiting[0].Summary != "go test" {
		t.Fatalf("listing.Waiting = %+v", listing.Waiting)
	}

	var plainStdout, plainStderr bytes.Buffer
	if code := run([]string{"run", "--projects-root", root, "--queue"}, &plainStdout, &plainStderr); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, plainStderr.String())
	}
	for _, want := range []string{"running (1)", "waiting (1)", fmt.Sprint(pid), "go build", "go test"} {
		if !strings.Contains(plainStdout.String(), want) {
			t.Errorf("--queue text output missing %q: %q", want, plainStdout.String())
		}
	}
}

// TestRunQueueRejectsIncompatibleFlags matches the --history usage-error
// convention: --queue is a standalone report and rejects the WB-mode flags.
func TestRunQueueRejectsIncompatibleFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--queue", "--apply"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "cannot be used with --queue") {
		t.Errorf("stderr does not explain the incompatible flag: %s", stderr.String())
	}
}

// TestRunQuietRequiresCommandMode matches the other command-mode-only flag
// checks (--worker, --idempotency-key, --allow-saturated-host).
func TestRunQuietRequiresCommandMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"run", "--quiet", "recipe-name"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage code %d; stderr=%s", code, exitUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--quiet requires command mode") {
		t.Errorf("stderr does not explain --quiet's command-mode requirement: %s", stderr.String())
	}
}
