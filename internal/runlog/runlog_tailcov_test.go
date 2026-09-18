package runlog

import (
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// tailCovManagedWorktree builds a scratch managed worktree so tests can drive
// Begin/ReadCurrent through the real manifest-discovery path instead of
// hand-rolling the on-disk layout.
func tailCovManagedWorktree(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	git := exec.Command("git", "init", "-b", "main")
	git.Dir = root
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "tailcov", EffortKind: worktrees.EffortKindFeature,
		Repository: "acme/tailcov", Worktree: root, Branch: "tailcov", Base: "main",
		BaseSHA: strings.Repeat("c", 40), CreatedAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
		RunID: "run-tailcov", ClaimID: strings.Repeat("d", 64), Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.WriteManifest(root, manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return root
}

func tailCovWantError(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: got nil error, want one containing %q", what, want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: error = %q, want it to contain %q", what, err, want)
	}
}

// TestTailCovRecorderRecordsAdmissionLoadAndFailureDetails drives the recorder
// through a zero-time Begin/Finish pair and asserts every admission field
// survives into the terminal event, including the failed-exit-code state.
func TestTailCovRecorderRecordsAdmissionLoadAndFailureDetails(t *testing.T) {
	t.Parallel()
	root := tailCovManagedWorktree(t)

	recorder, err := Begin(root, []string{"go", "test", "./..."}, time.Time{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if recorder.Path == "" {
		t.Fatalf("Begin inside a managed worktree returned no log path: %#v", recorder)
	}

	recorder.RecordAdmission(3, 1500*time.Millisecond)
	recorder.RecordLoadOverride(true)
	recorder.RecordLoadFloorSkipped("env")
	admittedAt := time.Date(2026, 9, 8, 10, 0, 0, 500000000, time.UTC)
	recorder.RecordQueueAdmittedAt(admittedAt)

	if err := recorder.Finish(7, 11*time.Millisecond, 4*time.Millisecond, time.Time{}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	events, err := Read(recorder.Path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	terminal := events[1]
	if terminal.State != "failed" {
		t.Errorf("State = %q, want failed for a non-zero exit code", terminal.State)
	}
	if terminal.ExitCode == nil || *terminal.ExitCode != 7 {
		t.Errorf("ExitCode = %v, want 7", terminal.ExitCode)
	}
	if terminal.CPUUnits != 3 {
		t.Errorf("CPUUnits = %d, want 3", terminal.CPUUnits)
	}
	if terminal.QueueWaitMS != 1500 {
		t.Errorf("QueueWaitMS = %d, want 1500", terminal.QueueWaitMS)
	}
	if !terminal.LoadOverride {
		t.Error("LoadOverride = false, want true")
	}
	if terminal.LoadFloorSkipped != "env" {
		t.Errorf("LoadFloorSkipped = %q, want env", terminal.LoadFloorSkipped)
	}
	if terminal.AdmittedAt == nil || !terminal.AdmittedAt.Equal(admittedAt) {
		t.Errorf("AdmittedAt = %v, want %v", terminal.AdmittedAt, admittedAt)
	}
	if terminal.UserCPUMS != 11 || terminal.SystemCPUMS != 4 {
		t.Errorf("CPU durations = %d/%d, want 11/4", terminal.UserCPUMS, terminal.SystemCPUMS)
	}
}

// TestTailCovRecordLoadOverrideCanBeCleared proves the override flag is a
// faithful copy of the caller's value rather than a latched one.
func TestTailCovRecordLoadOverrideCanBeCleared(t *testing.T) {
	t.Parallel()
	recorder := Recorder{}
	recorder.RecordLoadOverride(true)
	if !recorder.event.LoadOverride {
		t.Fatal("RecordLoadOverride(true) did not set the flag")
	}
	recorder.RecordLoadOverride(false)
	if recorder.event.LoadOverride {
		t.Fatal("RecordLoadOverride(false) left the flag set")
	}
}

// TestTailCovFinishWithoutManagedWorktreeIsNoop covers the unmanaged-path
// contract: no log path means Finish writes nothing and reports no error.
func TestTailCovFinishWithoutManagedWorktreeIsNoop(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	recorder, err := Begin(root, []string{"git", "status"}, time.Time{})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if recorder.Path != "" {
		t.Fatalf("recorder.Path = %q, want empty outside a managed worktree", recorder.Path)
	}
	if err := recorder.Finish(1, time.Second, time.Second, time.Time{}); err != nil {
		t.Fatalf("Finish outside a managed worktree must be a no-op, got %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("unmanaged directory changed: %v", entries)
	}
}

// TestTailCovBeginSurfacesAppendFailure proves Begin reports a broken run log
// rather than silently dropping the requested event.
func TestTailCovBeginSurfacesAppendFailure(t *testing.T) {
	t.Parallel()
	root := tailCovManagedWorktree(t)
	runPath := filepath.Join(root, ".wb", "local", "run")
	if err := os.WriteFile(runPath, []byte("a file where the directory belongs\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	recorder, err := Begin(root, []string{"go", "test"}, time.Time{})
	tailCovWantError(t, "Begin", err, "create run event directory")
	if recorder.OperationID == "" {
		t.Fatalf("recorder lost its operation ID on failure: %#v", recorder)
	}
}

// TestTailCovAppendRejectsUnencodableEvent covers the JSON encoding guard: a
// timestamp outside RFC3339's four-digit year range must be reported, and no
// partial line may be written.
func TestTailCovAppendRejectsUnencodableEvent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	err := Append(path, Event{
		SchemaVersion: EventSchemaVersion,
		Timestamp:     time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	tailCovWantError(t, "Append", err, "encode run event")
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("an unencodable event must not create a log file: stat err = %v", statErr)
	}
}

// TestTailCovAppendCreateAndOpenFailures covers the Append failures that
// happen before the file is locked. The lock failure itself is covered by
// TestTailCovAppendReportsLockFailure, which needs a Unix-only fault seam.
func TestTailCovAppendCreateAndOpenFailures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Append(filepath.Join(blocker, "events.jsonl"), Event{})
	tailCovWantError(t, "Append (missing parent)", err, "create run event directory")

	asDirectory := filepath.Join(dir, "as-directory")
	if err := os.Mkdir(asDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	err = Append(asDirectory, Event{})
	tailCovWantError(t, "Append (path is a directory)", err, "open run event log")

	if err := Append(filepath.Join(dir, "events.jsonl"), Event{SchemaVersion: EventSchemaVersion, State: "requested"}); err != nil {
		t.Fatalf("Append (success path): %v", err)
	}
}

// TestTailCovReadHandlesMissingUnreadableMalformedAndFutureLogs covers every
// branch of the parser's failure handling.
func TestTailCovReadHandlesMissingUnreadableMalformedAndFutureLogs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	events, err := Read(filepath.Join(dir, "absent.jsonl"))
	if err != nil || events != nil {
		t.Fatalf("Read of a missing log = %#v, %v; want nil, nil", events, err)
	}

	if _, err := Read(dir); err == nil {
		t.Fatal("Read of a directory must report the I/O failure")
	} else if strings.Contains(err.Error(), "parse run event") {
		t.Fatalf("a read failure must not be reported as a parse failure: %v", err)
	}

	malformed := filepath.Join(dir, "malformed.jsonl")
	if err := os.WriteFile(malformed, []byte("\n   \n{not json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Read(malformed)
	tailCovWantError(t, "Read (malformed)", err, "parse run event line 3")

	future := filepath.Join(dir, "future.jsonl")
	payload := `{"schema_version":2,"operation_id":"wbo-future","state":"requested"}` + "\n"
	if err := os.WriteFile(future, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Read(future)
	tailCovWantError(t, "Read (future schema)", err, "uses schema version 2")

	mixed := filepath.Join(dir, "mixed.jsonl")
	content := "\n" +
		`{"schema_version":1,"operation_id":"wbo-1","state":"requested"}` + "\n" +
		"  \n" +
		`{"schema_version":1,"operation_id":"wbo-1","state":"succeeded"}` + "\n"
	if err := os.WriteFile(mixed, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err = Read(mixed)
	if err != nil {
		t.Fatalf("Read (mixed): %v", err)
	}
	if len(events) != 2 || events[0].State != "requested" || events[1].State != "succeeded" {
		t.Fatalf("blank lines must be skipped, got %#v", events)
	}
}

// TestTailCovReadCurrentManagedAndUnmanaged covers both halves of the
// worktree-discovery contract used by `wb run --summary`.
func TestTailCovReadCurrentManagedAndUnmanaged(t *testing.T) {
	t.Parallel()
	if _, _, err := ReadCurrent(t.TempDir()); err == nil {
		t.Fatal("ReadCurrent outside a managed worktree must fail")
	} else if !strings.Contains(err.Error(), "is not inside a managed WB worktree") {
		t.Fatalf("unexpected error: %v", err)
	}

	root := tailCovManagedWorktree(t)
	started := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	recorder, err := Begin(root, []string{"go", "build", "./..."}, started)
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	nested := filepath.Join(root, "internal", "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}

	events, path, err := ReadCurrent(nested)
	if err != nil {
		t.Fatalf("ReadCurrent: %v", err)
	}
	if path != recorder.Path {
		t.Fatalf("ReadCurrent path = %q, want %q", path, recorder.Path)
	}
	if len(events) != 1 || events[0].OperationID != recorder.OperationID || events[0].State != "requested" {
		t.Fatalf("ReadCurrent events = %#v", events)
	}
}

// TestTailCovSummarizeFiltersWindowAndAggregatesKinds covers the summary's
// window filter, failure counters, per-kind accumulators, and both sort
// comparators.
func TestTailCovSummarizeFiltersWindowAndAggregatesKinds(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{OperationID: "old", Timestamp: since.Add(-time.Minute), State: "succeeded", Kind: "go/vet", DurationMS: 9999},
		{OperationID: "a", Timestamp: since.Add(time.Minute), State: "requested", Kind: "go/test"},
		{OperationID: "a", Timestamp: since.Add(2 * time.Minute), State: "succeeded", Kind: "go/test", DurationMS: 100, UserCPUMS: 10, SystemCPUMS: 5},
		{OperationID: "b", Timestamp: since.Add(time.Minute), State: "requested", Kind: "go/test"},
		{OperationID: "b", Timestamp: since.Add(3 * time.Minute), State: "failed", Kind: "go/test", DurationMS: 300, UserCPUMS: 20, SystemCPUMS: 7},
		{OperationID: "c", Timestamp: since.Add(4 * time.Minute), State: "succeeded", Kind: "git/commit", DurationMS: 50},
		{OperationID: "d", Timestamp: since.Add(5 * time.Minute), State: "requested", Kind: "go/build"},
	}

	summary := Summarize(events, since)
	if !summary.Since.Equal(since.UTC()) {
		t.Errorf("Since = %v, want %v", summary.Since, since.UTC())
	}
	if summary.Operations != 3 || summary.Failed != 1 || summary.Running != 1 {
		t.Fatalf("counters = %+v, want 3 operations, 1 failed, 1 running", summary)
	}
	if summary.WallMS != 450 {
		t.Errorf("WallMS = %d, want 450", summary.WallMS)
	}
	if summary.UserCPUMS != 30 || summary.SystemCPUMS != 12 {
		t.Errorf("CPU totals = %d/%d, want 30/12", summary.UserCPUMS, summary.SystemCPUMS)
	}
	if len(summary.Kinds) != 2 {
		t.Fatalf("Kinds = %#v, want 2 entries", summary.Kinds)
	}
	if summary.Kinds[0].Kind != "git/commit" || summary.Kinds[1].Kind != "go/test" {
		t.Fatalf("Kinds are not sorted by name: %#v", summary.Kinds)
	}
	goTest := summary.Kinds[1]
	if goTest.Operations != 2 || goTest.Failed != 1 || goTest.WallMS != 400 {
		t.Errorf("go/test summary = %+v, want 2 operations, 1 failed, 400ms", goTest)
	}
	if goTest.P50MS != 100 || goTest.P95MS != 300 {
		t.Errorf("go/test percentiles = %d/%d, want 100/300", goTest.P50MS, goTest.P95MS)
	}
	if summary.Kinds[0].P50MS != 50 || summary.Kinds[0].P95MS != 50 {
		t.Errorf("git/commit percentiles = %d/%d, want 50/50", summary.Kinds[0].P50MS, summary.Kinds[0].P95MS)
	}

	// An event with no matching start is not counted as running twice.
	if summary.Running != 1 {
		t.Errorf("Running = %d, want 1", summary.Running)
	}
}

func TestTailCovPercentileHandlesEmptyAndZeroQuantile(t *testing.T) {
	t.Parallel()
	if got := percentile(nil, 0.5); got != 0 {
		t.Fatalf("percentile(nil) = %d, want 0", got)
	}
	if got := percentile([]int64{}, 0.95); got != 0 {
		t.Fatalf("percentile(empty) = %d, want 0", got)
	}
	if got := percentile([]int64{7, 9}, 0); got != 7 {
		t.Fatalf("percentile(q=0) = %d, want the first value", got)
	}
	if got := percentile([]int64{7, 9}, 1); got != 9 {
		t.Fatalf("percentile(q=1) = %d, want the last value", got)
	}
}

// TestTailCovManagedWorktreeIgnoresUnreadableManifest proves a present but
// unparseable manifest degrades to "unmanaged" (no path, no error) rather
// than recording telemetry against an unknown worktree.
func TestTailCovManagedWorktreeIgnoresUnreadableManifest(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-b", "main")
	git.Dir = root
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	local := filepath.Join(root, ".wb", "local")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "manifest.yaml"), []byte("not: [valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	recorder, err := Begin(root, []string{"go", "test"}, time.Time{})
	if err != nil {
		t.Fatalf("Begin with an unreadable manifest: %v", err)
	}
	if recorder.Path != "" {
		t.Fatalf("recorder.Path = %q, want empty for an unreadable manifest", recorder.Path)
	}
}

// TestTailCovClassifyCoversToolsVerbsAndFallbacks pins the classification
// table's observable outputs, including the flag-skipping and stop-at-first-
// unknown-verb rules.
func TestTailCovClassifyCoversToolsVerbsAndFallbacks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		argv []string
		want string
	}{
		{"empty argv", nil, "unknown"},
		{"unknown tool", []string{"cargo", "build"}, "other"},
		{"flags before the verb are skipped", []string{"GO", "-v", "TEST", "./..."}, "go/test"},
		{"flags only", []string{"go", "--help"}, "go"},
		{"an unknown verb stops the scan", []string{"go", "bogus", "test"}, "go"},
		{"known tool without a verb", []string{"/usr/local/bin/git"}, "git"},
		{"git verb", []string{"git", "worktree", "merge"}, "git/worktree"},
		{"specscore verb", []string{"SPECSCORE", "Feature", "list"}, "specscore/feature"},
		{"pnpm verb", []string{"pnpm", "run", "test"}, "pnpm/run"},
		{"npm verb", []string{"npm", "ci"}, "npm/ci"},
	}
	for _, tc := range cases {
		if got := classify(tc.argv); got != tc.want {
			t.Errorf("%s: classify(%q) = %q, want %q", tc.name, tc.argv, got, tc.want)
		}
	}
}

func TestTailCovNewOperationIDIsUniqueAndWellFormed(t *testing.T) {
	t.Parallel()
	first, err := newOperationID()
	if err != nil {
		t.Fatalf("newOperationID: %v", err)
	}
	second, err := newOperationID()
	if err != nil {
		t.Fatalf("newOperationID: %v", err)
	}
	if !strings.HasPrefix(first, "wbo-") {
		t.Fatalf("operation ID %q is missing the wbo- prefix", first)
	}
	raw := strings.TrimPrefix(first, "wbo-")
	if len(raw) != 32 {
		t.Fatalf("operation ID %q encodes %d hex chars, want 32", first, len(raw))
	}
	if _, err := hex.DecodeString(raw); err != nil {
		t.Fatalf("operation ID %q is not hex: %v", first, err)
	}
	if first == second {
		t.Fatalf("two operation IDs collided: %q", first)
	}
}
