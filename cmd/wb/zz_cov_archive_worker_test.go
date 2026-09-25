package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/archiveprune"
	"github.com/sneat-dev/wb/internal/daemon"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/runqueue"
)

func TestCwCovArchiveCleanCommandInProcess(t *testing.T) {
	root := t.TempDir()
	remotesRoot := t.TempDir()
	clean := initArchivableClone(t, root, remotesRoot, "acme", "clean-repo")
	dirty := initArchivableClone(t, root, remotesRoot, "acme", "dirty-repo")
	if err := os.WriteFile(filepath.Join(dirty, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	installArchivedFakeGh(t)
	t.Setenv("WB_HOME", t.TempDir())

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newArchiveCleanCmd(&invocation{projectsRoot: root}) })
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("dry-run exit = %d\n%s", code, stdout)
	}
	for _, want := range []string{
		"would delete acme/clean-repo",
		"skipped      acme/dirty-repo",
		"untracked file untracked.txt (4 bytes)",
		"1 eligible, 1 skipped; dry-run only, pass --apply to delete",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("dry-run report missing %q:\n%s", want, stdout)
		}
	}
	if _, err := os.Stat(clean); err != nil {
		t.Fatal("dry-run deleted the deletable clone")
	}

	// JSON and YAML carry the same outcome machine-readably.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newArchiveCleanCmd(&invocation{projectsRoot: root}) }, "--format", "json")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("json exit = %d", code)
	}
	var outcome archiveprune.Outcome
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("json outcome: %v\n%s", err, stdout)
	}
	if len(outcome.Results) != 2 || outcome.Apply {
		t.Fatalf("outcome = %+v", outcome)
	}
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newArchiveCleanCmd(&invocation{projectsRoot: root}) }, "--format", "yaml")
	if code := exitCodeOf(t, err); code != exitOK || !strings.Contains(stdout, "results:") {
		t.Fatalf("yaml exit = %d\n%s", code, stdout)
	}

	// An unknown format is refused before anything is inspected.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newArchiveCleanCmd(&invocation{projectsRoot: root}) }, "--format", "toml"); err == nil ||
		!strings.Contains(err.Error(), `unsupported format "toml"`) {
		t.Fatalf("unknown format error = %v", err)
	}

	// --apply deletes the eligible clone and preserves the refused one.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newArchiveCleanCmd(&invocation{projectsRoot: root}) }, "--apply")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("apply exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "deleted      acme/clean-repo") || !strings.Contains(stdout, "1 deleted, 1 skipped") {
		t.Errorf("apply report = %s", stdout)
	}
	if _, err := os.Stat(clean); !os.IsNotExist(err) {
		t.Fatalf("--apply did not remove the eligible clone: %v", err)
	}
	if _, err := os.Stat(dirty); err != nil {
		t.Fatalf("--apply removed the refused clone: %v", err)
	}
}

func TestCwCovArchiveCleanFailedAndPrintArchiveClean(t *testing.T) {
	outcome := archiveprune.Outcome{
		Apply: true,
		Results: []archiveprune.Result{
			{Repository: "acme/deleted", Applied: true, Reason: "archived and clean"},
			{Repository: "acme/would", Eligible: true, Reason: "archived and clean"},
			{Repository: "acme/skipped", Reason: "unpushed commits"},
			{Repository: "acme/broken", Eligible: true, Error: "permission denied"},
			{Repository: "acme/untracked", Reason: "contains untracked files", Untracked: []archiveprune.UntrackedEntry{
				{Kind: "file", Path: "notes.txt", Size: 12},
			}, ReceiptPath: "/tmp/receipt.json"},
		},
	}
	command := newArchiveCleanCmd(&invocation{})
	var out bytes.Buffer
	command.SetOut(&out)
	printArchiveClean(command, outcome)
	text := out.String()
	for _, want := range []string{
		"deleted      acme/deleted", "failed       acme/broken",
		"would delete acme/would", "skipped      acme/skipped",
		"untracked file notes.txt (12 bytes)", "receipt /tmp/receipt.json",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("archive report missing %q:\n%s", want, text)
		}
	}
	if !archiveCleanFailed(outcome) {
		t.Error("archiveCleanFailed missed a deletion error")
	}
	if archiveCleanFailed(archiveprune.Outcome{Results: []archiveprune.Result{{Eligible: true}}}) {
		t.Error("a planned dry-run result must not count as a failure")
	}

	// An empty sweep says so rather than printing an empty list.
	out.Reset()
	printArchiveClean(command, archiveprune.Outcome{})
	if !strings.Contains(out.String(), "no local clones matched") {
		t.Errorf("empty archive report = %q", out.String())
	}
}

func TestCwCovCanonicalWorkerRootsAndPermissions(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "repo")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalWorkerRoots(nil); err == nil || !strings.Contains(err.Error(), "at least one --root") {
		t.Fatalf("empty roots error = %v", err)
	}
	if _, err := canonicalWorkerRoots([]string{"relative/path"}); err == nil || !strings.Contains(err.Error(), "must be absolute") {
		t.Fatalf("relative root error = %v", err)
	}
	if _, err := canonicalWorkerRoots([]string{filepath.Join(root, "absent")}); err == nil {
		t.Fatal("a missing root must be refused")
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalWorkerRoots([]string{file}); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("file root error = %v", err)
	}
	// The same root named twice is canonicalized to one entry.
	permitted, err := canonicalWorkerRoots([]string{root, root + string(filepath.Separator)})
	if err != nil {
		t.Fatal(err)
	}
	if len(permitted) != 1 {
		t.Fatalf("permitted = %v, want one deduplicated root", permitted)
	}
	if ok, err := workerPermitsDirectory(permitted, nested); err != nil || !ok {
		t.Fatalf("nested directory = (%t, %v), want permitted", ok, err)
	}
	if ok, err := workerPermitsDirectory(permitted, "relative"); err == nil || ok {
		t.Fatalf("relative cwd = (%t, %v), want a refusal", ok, err)
	}
	if ok, err := workerPermitsDirectory(permitted, filepath.Join(root, "absent")); err == nil || ok {
		t.Fatalf("missing cwd = (%t, %v), want a refusal", ok, err)
	}
	if ok, err := workerPermitsDirectory(permitted, t.TempDir()); err != nil || ok {
		t.Fatalf("outside cwd = (%t, %v), want a refusal", ok, err)
	}
}

func TestCwCovMergeWorkerEnvironmentReplacesEveryOccurrence(t *testing.T) {
	environment := mergeWorkerEnvironment(
		[]string{"PATH=/bin", "GOMAXPROCS=99", "GOMAXPROCS=98", "KEEP=1"},
		map[string]string{"GOMAXPROCS": "4", "WB_CPU_UNITS": "4"},
	)
	joined := strings.Join(environment, "\n")
	if strings.Contains(joined, "GOMAXPROCS=99") || strings.Contains(joined, "GOMAXPROCS=98") {
		t.Fatalf("stale values survived: %v", environment)
	}
	if strings.Count(joined, "GOMAXPROCS=4") != 1 {
		t.Fatalf("GOMAXPROCS was not set exactly once: %v", environment)
	}
	for _, want := range []string{"KEEP=1", "PATH=/bin", "WB_CPU_UNITS=4"} {
		if !strings.Contains(joined, want) {
			t.Errorf("environment lost %q: %v", want, environment)
		}
	}
}

func TestCwCovWorkerTailBufferKeepsTheTailBounded(t *testing.T) {
	var buffer workerTailBuffer
	chunk := bytes.Repeat([]byte("a"), 40<<10)
	if n, err := buffer.Write(chunk); err != nil || n != len(chunk) {
		t.Fatalf("Write = (%d, %v)", n, err)
	}
	marker := []byte("TAIL-MARKER")
	chunk2 := append(bytes.Repeat([]byte("b"), 40<<10), marker...)
	if _, err := buffer.Write(chunk2); err != nil {
		t.Fatal(err)
	}
	if buffer.Len() > 64<<10 {
		t.Fatalf("buffer length = %d, want it bounded at 64KiB", buffer.Len())
	}
	if !bytes.HasSuffix(buffer.Bytes(), marker) {
		t.Fatalf("the retained window must be the tail, ending in the newest bytes")
	}
}

func TestCwCovWorkerConnectCommandValidation(t *testing.T) {
	root := t.TempDir()
	build := func() *cobra.Command { return newWorkerConnectCmd(&invocation{}, defaultDaemonDependencies()) }
	cases := map[string][]string{
		"missing id":      {"--root", root},
		"relative root":   {"--id", "cw-worker", "--root", "relative"},
		"missing root":    {"--id", "cw-worker", "--root", filepath.Join(root, "absent")},
		"no roots at all": {"--id", "cw-worker"},
		"unknown format":  {"--id", "cw-worker", "--root", root, "--format", "toml"},
	}
	for name, args := range cases {
		_, _, err := cwCovExec(t, root, build, args...)
		if err == nil {
			t.Errorf("%s: worker connect accepted an invalid invocation", name)
			continue
		}
		if code := exitCodeOf(t, err); code != exitUsage {
			t.Errorf("%s: exit = %d, want usage", name, code)
		}
	}
}

func TestCwCovWorkerCmdSurface(t *testing.T) {
	command := newWorkerCmd(&invocation{}, defaultDaemonDependencies())
	sub, _, err := command.Find([]string{"connect"})
	if err != nil || sub == command {
		t.Fatalf("worker connect subcommand is missing: %v", err)
	}
	connect := newWorkerConnectCmd(&invocation{}, defaultDaemonDependencies())
	for _, name := range []string{"id", "root", "cpu-capacity", "format", "json"} {
		if connect.Flags().Lookup(name) == nil {
			t.Errorf("worker connect is missing --%s", name)
		}
	}
}

func TestCwCovExecuteWorkerAssignmentRunsAndReports(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := daemonTestService(t, root, "test-build", "cw-worker-run", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	path, handler := daemonv1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := daemonv1connect.NewDaemonServiceClient(server.Client(), server.URL)

	ctx := context.Background()
	operation, err := client.SubmitOperation(ctx, connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: work, Argv: []string{"go", "version"}, TargetWorkerId: "cw-worker",
	}))
	if err != nil {
		t.Fatal(err)
	}
	registered, err := client.RegisterWorker(ctx, connect.NewRequest(&daemonv1.RegisterWorkerRequest{
		WorkerId: "cw-worker", Build: "test-build", ProtocolVersion: daemon.ProtocolVersion,
		Os: runtime.GOOS, Arch: runtime.GOARCH, CpuCapacity: 1, PermittedRoots: []string{root},
	}))
	if err != nil {
		t.Fatal(err)
	}
	registration := registered.Msg.Registration
	leased, err := client.LeaseOperation(ctx, connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}

	// An assignment whose generations do not match this registration is
	// refused rather than executed.
	mismatched := &daemonv1.WorkerAssignment{
		WorkerId: "someone-else", WorkerGeneration: registration.WorkerGeneration,
		SchedulerGeneration: registration.SchedulerGeneration, OperationId: "op", LeaseId: "lease",
		WorkingDirectory: work, Argv: []string{"go", "version"},
	}
	command := newWorkerConnectCmd(&invocation{projectsRoot: root}, defaultDaemonDependencies())
	command.SetContext(ctx)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	if err := executeWorkerAssignment(&invocation{projectsRoot: root}, command, client, registration, []string{root}, mismatched); err == nil ||
		!strings.Contains(err.Error(), "different worker") {
		t.Fatalf("mismatched generation error = %v", err)
	}
	// An assignment with no command is refused.
	empty := &daemonv1.WorkerAssignment{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration,
		SchedulerGeneration: registration.SchedulerGeneration, OperationId: "op", LeaseId: "lease",
		WorkingDirectory: work,
	}
	if err := executeWorkerAssignment(&invocation{projectsRoot: root}, command, client, registration, []string{root}, empty); err == nil ||
		!strings.Contains(err.Error(), "without a command") {
		t.Fatalf("empty argv error = %v", err)
	}

	assignment := leased.Msg.Assignment
	err = executeWorkerAssignment(&invocation{projectsRoot: root}, command, client, registration, []string{root}, assignment)
	if err != nil {
		t.Fatalf("executeWorkerAssignment: %v", err)
	}
	completed, err := client.GetOperation(ctx, connect.NewRequest(&daemonv1.GetOperationRequest{
		OperationId: operation.Msg.OperationId,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Msg.State != daemonv1.OperationState_OPERATION_STATE_SUCCEEDED {
		t.Fatalf("operation state = %v (error %q)", completed.Msg.State, completed.Msg.Error)
	}
	if !strings.Contains(string(completed.Msg.StdoutTail), "go version") {
		t.Errorf("stdout tail = %q, want the child's output", completed.Msg.StdoutTail)
	}
}

// An operation submitted with an explicit CpuUnits (the trusted raw-execution
// fallback) must be admitted through the explicit budget-sum pool rather than
// argv-based reclassification (PR #628, M4).
func TestCwCovExecuteWorkerAssignmentAdmitsExplicitCpuUnits(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := daemonTestService(t, root, "test-build", "cw-worker-explicit", func() error { return errors.New("raw disabled") })
	if err != nil {
		t.Fatal(err)
	}
	path, handler := daemonv1connect.NewDaemonServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := daemonv1connect.NewDaemonServiceClient(server.Client(), server.URL)

	ctx := context.Background()
	operation, err := client.SubmitOperation(ctx, connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: work, Argv: []string{"go", "version"}, CpuUnits: 1, TargetWorkerId: "cw-worker-explicit",
	}))
	if err != nil {
		t.Fatal(err)
	}
	registered, err := client.RegisterWorker(ctx, connect.NewRequest(&daemonv1.RegisterWorkerRequest{
		WorkerId: "cw-worker-explicit", Build: "test-build", ProtocolVersion: daemon.ProtocolVersion,
		Os: runtime.GOOS, Arch: runtime.GOARCH, CpuCapacity: 1, PermittedRoots: []string{root},
	}))
	if err != nil {
		t.Fatal(err)
	}
	registration := registered.Msg.Registration
	leased, err := client.LeaseOperation(ctx, connect.NewRequest(&daemonv1.LeaseOperationRequest{
		WorkerId: registration.WorkerId, WorkerGeneration: registration.WorkerGeneration, WaitMilliseconds: 1,
	}))
	if err != nil {
		t.Fatal(err)
	}
	assignment := leased.Msg.Assignment
	if assignment.CpuUnits == 0 {
		t.Fatal("assignment lost the explicit CpuUnits; test fixture no longer proves the explicit path")
	}
	command := newWorkerConnectCmd(&invocation{projectsRoot: root}, defaultDaemonDependencies())
	command.SetContext(ctx)
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	if err := executeWorkerAssignment(&invocation{projectsRoot: root}, command, client, registration, []string{root}, assignment); err != nil {
		t.Fatalf("executeWorkerAssignment with explicit CpuUnits: %v", err)
	}
	// Positive proof that the explicit CpuUnits actually went through
	// runqueue.AdmitExplicit's budget-sum pool (internal/runqueue.Acquire),
	// not runqueue.Admit's argv-classified path (review finding, mutant
	// M14: worker.go:215's AdmitExplicit->Admit swap previously survived
	// because nothing here observed which path ran). Acquire's slot lock
	// file is created with os.O_CREATE and is only ever unlocked and
	// closed by Lease.Release, never removed (internal/runqueue/queue.go),
	// so it is still on disk here. This assignment's argv is
	// []string{"go", "version"}, which runqueue.Classify reports as
	// KindNone (no test/vet/build verb) — under the M14 mutant,
	// runqueue.Admit would take the KindNone branch and return an
	// immediate no-op Admission without ever calling Acquire, so no slot
	// lock file would exist. This does not depend on timing or on the
	// host's CPU count: KindNone is Admit's first, unconditional case.
	slotLocks, globErr := filepath.Glob(filepath.Join(runqueue.QueueDirForTest(root), "slot-*.lock"))
	if globErr != nil {
		t.Fatalf("glob CPU admission slot locks: %v", globErr)
	}
	if len(slotLocks) == 0 {
		t.Fatal("no CPU admission slot lock file was left behind; the explicit CpuUnits assignment did not go through runqueue.AdmitExplicit's budget-sum pool")
	}
	completed, err := client.GetOperation(ctx, connect.NewRequest(&daemonv1.GetOperationRequest{
		OperationId: operation.Msg.OperationId,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if completed.Msg.State != daemonv1.OperationState_OPERATION_STATE_SUCCEEDED {
		t.Fatalf("operation state = %v (error %q)", completed.Msg.State, completed.Msg.Error)
	}
}
