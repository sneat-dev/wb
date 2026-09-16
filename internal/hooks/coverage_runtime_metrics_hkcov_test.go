package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func hkCovMetricsPolicy(path string) Policy {
	return Policy{Metrics: MetricsPolicy{Enabled: true, Path: path, Labels: map[string]string{}}}
}

func hkCovPendingLayout(t *testing.T) ExecutionLayout {
	t.Helper()
	root := t.TempDir()
	return ExecutionLayout{
		Root:               root,
		ReportRoot:         filepath.Join(root, "reports"),
		PendingMetricsRoot: filepath.Join(root, "pending-metrics"),
	}
}

func hkCovEvent(t *testing.T) Event {
	t.Helper()
	return Event{
		SchemaVersion: EventSchemaVersion,
		Timestamp:     time.Now(),
		Repository:    "acme/widget",
		Hook:          "pre-commit",
		Action:        "commit-check",
		Outcome:       "passed",
	}
}

// TestHkCovResolveExecutionLayoutReportsHomeFailure pins that an unusable write
// home is reported rather than producing a relative runtime root.
func TestHkCovResolveExecutionLayoutReportsHomeFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	t.Setenv(wbhome.EnvOverride, filepath.Join(blocker, "wb-home"))
	if _, err := ResolveExecutionLayout(t.TempDir(), "/tmp/projects"); err == nil {
		t.Fatal("ResolveExecutionLayout with an unusable home should fail")
	}
}

func TestHkCovResolveExecutionLayoutSanitizesCheckoutSegments(t *testing.T) {
	isolateEnvironment(t)
	home := os.Getenv(wbhome.EnvOverride)
	repo := initRepo(t)
	layout, err := ResolveExecutionLayout(repo, "/tmp/projects")
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(home, "hook-runtime")
	if !strings.HasPrefix(layout.Root, wantPrefix) {
		t.Fatalf("layout.Root = %q, want it under %q", layout.Root, wantPrefix)
	}
	if layout.ReportRoot != filepath.Join(layout.Root, "reports") || layout.PendingMetricsRoot != filepath.Join(layout.Root, "pending-metrics") {
		t.Fatalf("layout = %#v, want reports and pending-metrics beneath the root", layout)
	}

	// A checkout directory whose name needs sanitising produces a runtime
	// segment without the offending characters.
	odd := filepath.Join(t.TempDir(), "space:in name")
	mustMkdirAll(t, odd)
	git(t, odd, "init", "-b", "main")
	oddLayout, err := ResolveExecutionLayout(odd, "/tmp/projects")
	if err != nil {
		t.Fatal(err)
	}
	segment := strings.TrimPrefix(oddLayout.Root, filepath.Join(home, "hook-runtime")+string(filepath.Separator))
	if strings.ContainsAny(segment, ": ") {
		t.Fatalf("runtime segment %q still contains sanitised characters", segment)
	}
	if !strings.Contains(segment, "space_in_name") {
		t.Fatalf("runtime segment %q should contain the sanitised checkout name", segment)
	}
}

func TestHkCovSecureExecutionWriteRoots(t *testing.T) {
	isolateEnvironment(t)
	repo := initRepo(t)
	layout, err := ResolveExecutionLayout(repo, "/tmp/projects")
	if err != nil {
		t.Fatal(err)
	}

	roots, err := SecureExecutionWriteRoots(repo, "", "/tmp/projects")
	if err != nil {
		t.Fatal(err)
	}
	metricsDir := filepath.Dir(filepath.Join(os.Getenv("XDG_STATE_HOME"), "wb", "hook-events.jsonl"))
	if len(roots) != 2 || roots[0] != filepath.Clean(metricsDir) || roots[1] != layout.Root {
		t.Fatalf("SecureExecutionWriteRoots = %v, want [%s %s]", roots, metricsDir, layout.Root)
	}

	// Disabling metrics removes the metrics directory from the write roots.
	isolateEnvironment(t)
	hkCovWriteGlobalYAML(t, "git_hooks:\n  version: 1\n  metrics:\n    enabled: false\n")
	layout, err = ResolveExecutionLayout(repo, "/tmp/projects")
	if err != nil {
		t.Fatal(err)
	}
	roots, err = SecureExecutionWriteRoots(repo, "", "/tmp/projects")
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0] != layout.Root {
		t.Fatalf("SecureExecutionWriteRoots(metrics off) = %v, want only %s", roots, layout.Root)
	}

	if _, err := SecureExecutionWriteRoots(t.TempDir(), "", "/tmp/projects"); err == nil {
		t.Fatal("SecureExecutionWriteRoots(non-repo) should fail")
	}

	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	t.Setenv(wbhome.EnvOverride, filepath.Join(blocker, "wb-home"))
	if _, err := SecureExecutionWriteRoots(repo, "", "/tmp/projects"); err == nil {
		t.Fatal("SecureExecutionWriteRoots with an unusable home should fail")
	}
}

func TestHkCovReplayPendingMetricsPreparesLayout(t *testing.T) {
	isolateEnvironment(t)
	repo := initRepo(t)
	replayed, err := ReplayPendingMetrics(repo, "", "/tmp/projects")
	if err != nil || replayed != 0 {
		t.Fatalf("ReplayPendingMetrics = %d, %v; want 0, nil", replayed, err)
	}
	layout, err := ResolveExecutionLayout(repo, "/tmp/projects")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{layout.Root, layout.ReportRoot, layout.PendingMetricsRoot} {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.IsDir() {
			t.Fatalf("runtime path %s was not created: %v", path, statErr)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("runtime path %s mode = %v, want 0700", path, info.Mode().Perm())
		}
	}

	if _, err := ReplayPendingMetrics(t.TempDir(), "", "/tmp/projects"); err == nil {
		t.Fatal("ReplayPendingMetrics(non-repo) should fail")
	}

	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	t.Setenv(wbhome.EnvOverride, filepath.Join(blocker, "wb-home"))
	if _, err := ReplayPendingMetrics(repo, "", "/tmp/projects"); err == nil {
		t.Fatal("ReplayPendingMetrics with an unusable home should fail")
	}

	// A runtime root occupied by a regular file cannot be prepared.
	isolateEnvironment(t)
	layout, err = ResolveExecutionLayout(repo, "/tmp/projects")
	if err != nil {
		t.Fatal(err)
	}
	mustMkdirAll(t, filepath.Dir(layout.Root))
	mustWrite(t, layout.Root, "occupied\n")
	if _, err := ReplayPendingMetrics(repo, "", "/tmp/projects"); err == nil || !strings.Contains(err.Error(), "create hook runtime path") {
		t.Fatalf("ReplayPendingMetrics(occupied root) error = %v", err)
	}
}

func TestHkCovReplayPendingMetricsDirect(t *testing.T) {
	layout := hkCovPendingLayout(t)
	target := filepath.Join(t.TempDir(), "events.jsonl")

	if replayed, err := replayPendingMetrics(target, layout); err != nil || replayed != 0 {
		t.Fatalf("replayPendingMetrics(missing dir) = %d, %v; want 0, nil", replayed, err)
	}

	blocked := hkCovPendingLayout(t)
	mustWrite(t, blocked.PendingMetricsRoot, "not a directory\n")
	if _, err := replayPendingMetrics(target, blocked); err == nil || !strings.Contains(err.Error(), "read pending hook metrics directory") {
		t.Fatalf("replayPendingMetrics(dir is a file) error = %v", err)
	}

	// A directory entry and a non-JSON file are skipped; a matching receipt is
	// replayed and removed.
	mustMkdirAll(t, layout.PendingMetricsRoot)
	mustMkdirAll(t, filepath.Join(layout.PendingMetricsRoot, "subdir"))
	mustWrite(t, filepath.Join(layout.PendingMetricsRoot, "notes.txt"), "ignored\n")
	receiptPath := filepath.Join(layout.PendingMetricsRoot, "20240101T000000.000000000Z-aa.json")
	hkCovWriteReceipt(t, receiptPath, PendingMetricsReceipt{
		SchemaVersion: pendingMetricsReceiptSchemaVersion,
		RecordedAt:    time.Now().UTC(),
		TargetPath:    target,
		AppendError:   "disk full",
		Events:        []Event{hkCovEvent(t)},
	})
	replayed, err := replayPendingMetrics(target, layout)
	if err != nil || replayed != 1 {
		t.Fatalf("replayPendingMetrics = %d, %v; want 1, nil", replayed, err)
	}
	if _, statErr := os.Stat(receiptPath); !os.IsNotExist(statErr) {
		t.Fatalf("replayed receipt still present: %v", statErr)
	}
	events, err := ReadEvents(target)
	if err != nil || len(events) != 1 || events[0].Repository != "acme/widget" {
		t.Fatalf("replayed events = %#v, %v", events, err)
	}

	// A receipt written for a different target is left alone.
	otherTarget := filepath.Join(t.TempDir(), "events.jsonl")
	otherPath := filepath.Join(layout.PendingMetricsRoot, "20240102T000000.000000000Z-bb.json")
	hkCovWriteReceipt(t, otherPath, PendingMetricsReceipt{
		SchemaVersion: pendingMetricsReceiptSchemaVersion,
		RecordedAt:    time.Now().UTC(),
		TargetPath:    otherTarget,
		AppendError:   "disk full",
		Events:        []Event{hkCovEvent(t)},
	})
	if replayed, err := replayPendingMetrics(target, layout); err != nil || replayed != 0 {
		t.Fatalf("replayPendingMetrics(other target) = %d, %v; want 0, nil", replayed, err)
	}
	if _, statErr := os.Stat(otherPath); statErr != nil {
		t.Fatalf("receipt for another target was removed: %v", statErr)
	}

	// A malformed receipt stops the replay and is reported.
	badPath := filepath.Join(layout.PendingMetricsRoot, "20240103T000000.000000000Z-cc.json")
	mustWrite(t, badPath, "{not json\n")
	if _, err := replayPendingMetrics(target, layout); err == nil || !strings.Contains(err.Error(), "decode pending hook metrics receipt") {
		t.Fatalf("replayPendingMetrics(malformed receipt) error = %v", err)
	}
	_ = os.Remove(badPath)

	// A matching receipt whose target cannot be appended to is reported with
	// the receipt path so an operator can find it.
	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	unwritable := filepath.Join(layout.PendingMetricsRoot, "20240104T000000.000000000Z-dd.json")
	hkCovWriteReceipt(t, unwritable, PendingMetricsReceipt{
		SchemaVersion: pendingMetricsReceiptSchemaVersion,
		RecordedAt:    time.Now().UTC(),
		TargetPath:    filepath.Join(blocker, "events.jsonl"),
		AppendError:   "disk full",
		Events:        []Event{hkCovEvent(t)},
	})
	if _, err := replayPendingMetrics(filepath.Join(blocker, "events.jsonl"), layout); err == nil || !strings.Contains(err.Error(), "replay pending hook metrics") {
		t.Fatalf("replayPendingMetrics(unwritable target) error = %v", err)
	}
}

func TestHkCovReplayPendingMetricsReportsUnremovableReceipt(t *testing.T) {
	if os.Geteuid() == 0 {
		return
	}
	layout := hkCovPendingLayout(t)
	target := filepath.Join(t.TempDir(), "events.jsonl")
	mustMkdirAll(t, layout.PendingMetricsRoot)
	receiptPath := filepath.Join(layout.PendingMetricsRoot, "20240101T000000.000000000Z-ee.json")
	hkCovWriteReceipt(t, receiptPath, PendingMetricsReceipt{
		SchemaVersion: pendingMetricsReceiptSchemaVersion,
		RecordedAt:    time.Now().UTC(),
		TargetPath:    target,
		AppendError:   "disk full",
		Events:        []Event{hkCovEvent(t)},
	})
	if err := os.Chmod(layout.PendingMetricsRoot, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(layout.PendingMetricsRoot, 0o700) })
	_, err := replayPendingMetrics(target, layout)
	if err == nil || !strings.Contains(err.Error(), "remove replayed pending hook metrics receipt") {
		t.Fatalf("replayPendingMetrics(read-only dir) error = %v", err)
	}
	events, readErr := ReadEvents(target)
	if readErr != nil || len(events) != 1 {
		t.Fatalf("the receipt's events should have been appended before removal failed: %#v, %v", events, readErr)
	}
}

func TestHkCovReadPendingMetricsReceiptErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := readPendingMetricsReceipt(filepath.Join(dir, "missing.json")); err == nil || !strings.Contains(err.Error(), "read pending hook metrics receipt") {
		t.Fatalf("readPendingMetricsReceipt(missing) error = %v", err)
	}
	malformed := filepath.Join(dir, "malformed.json")
	mustWrite(t, malformed, "{not json\n")
	if _, err := readPendingMetricsReceipt(malformed); err == nil || !strings.Contains(err.Error(), "decode pending hook metrics receipt") {
		t.Fatalf("readPendingMetricsReceipt(malformed) error = %v", err)
	}
	mismatched := filepath.Join(dir, "mismatched.json")
	mustWrite(t, mismatched, `{"schema_version": 99}`)
	if _, err := readPendingMetricsReceipt(mismatched); err == nil || !strings.Contains(err.Error(), "uses schema version 99") {
		t.Fatalf("readPendingMetricsReceipt(mismatched) error = %v", err)
	}
	valid := filepath.Join(dir, "valid.json")
	want := PendingMetricsReceipt{
		SchemaVersion: pendingMetricsReceiptSchemaVersion,
		RecordedAt:    time.Now().UTC().Truncate(time.Second),
		TargetPath:    "/tmp/events.jsonl",
		AppendError:   "disk full",
		Events:        []Event{hkCovEvent(t)},
	}
	hkCovWriteReceipt(t, valid, want)
	got, err := readPendingMetricsReceipt(valid)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetPath != want.TargetPath || len(got.Events) != 1 || got.Events[0].Repository != "acme/widget" {
		t.Fatalf("receipt = %#v, want %#v", got, want)
	}
}

func TestHkCovPersistPendingMetricsReceipt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "pending")
	now := time.Date(2024, 5, 6, 7, 8, 9, 123456789, time.UTC)
	events := []Event{hkCovEvent(t)}
	path, err := persistPendingMetricsReceipt(root, "/tmp/events.jsonl", events, os.ErrPermission, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(filepath.Base(path), "20240506T070809.123456789Z-") || !strings.HasSuffix(path, ".json") {
		t.Fatalf("receipt path = %q, want a timestamped JSON name", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %v, want 0600", info.Mode().Perm())
	}
	if dirInfo, statErr := os.Stat(root); statErr != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("pending root mode = %v, %v; want 0700", dirInfo.Mode().Perm(), statErr)
	}
	// The receipt must copy the events rather than alias the caller's slice.
	events[0].Repository = "mutated/after"
	receipt, err := readPendingMetricsReceipt(path)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Events[0].Repository != "acme/widget" || receipt.AppendError == "" || receipt.TargetPath != "/tmp/events.jsonl" {
		t.Fatalf("receipt = %#v, want a private copy of the events", receipt)
	}

	blocked := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocked, "not a directory\n")
	if _, err := persistPendingMetricsReceipt(blocked, "/tmp/events.jsonl", events, os.ErrPermission, now); err == nil || !strings.Contains(err.Error(), "create pending hook metrics directory") {
		t.Fatalf("persistPendingMetricsReceipt(file root) error = %v", err)
	}
}

// TestHkCovRecordMetricsBranches drives every observable outcome of the
// metrics recorder, including the pending-receipt fallback when the metrics
// file itself is unwritable.
func TestHkCovRecordMetricsBranches(t *testing.T) {
	events := []Event{hkCovEvent(t)}
	now := time.Now()

	t.Run("no events records nothing", func(t *testing.T) {
		layout := hkCovPendingLayout(t)
		if err := recordMetrics(hkCovMetricsPolicy(filepath.Join(t.TempDir(), "e.jsonl")), layout, nil, now); err != nil {
			t.Fatalf("recordMetrics(no events) = %v, want nil", err)
		}
	})

	t.Run("appends and replays", func(t *testing.T) {
		layout := hkCovPendingLayout(t)
		target := filepath.Join(t.TempDir(), "e.jsonl")
		if err := recordMetrics(hkCovMetricsPolicy(target), layout, events, now); err != nil {
			t.Fatalf("recordMetrics = %v, want nil", err)
		}
		read, err := ReadEvents(target)
		if err != nil || len(read) != 1 {
			t.Fatalf("recorded events = %#v, %v", read, err)
		}
	})

	t.Run("replay failure is reported after a successful append", func(t *testing.T) {
		layout := hkCovPendingLayout(t)
		mustMkdirAll(t, layout.PendingMetricsRoot)
		mustWrite(t, filepath.Join(layout.PendingMetricsRoot, "bad.json"), "{not json\n")
		target := filepath.Join(t.TempDir(), "e.jsonl")
		err := recordMetrics(hkCovMetricsPolicy(target), layout, events, now)
		if err == nil || !strings.Contains(err.Error(), "replay pending hook metrics") {
			t.Fatalf("recordMetrics error = %v, want a replay failure", err)
		}
		if read, readErr := ReadEvents(target); readErr != nil || len(read) != 1 {
			t.Fatalf("the append should still have happened: %#v, %v", read, readErr)
		}
	})

	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	unwritableTarget := filepath.Join(blocker, "e.jsonl")

	t.Run("append and pending receipt both fail", func(t *testing.T) {
		layout := hkCovPendingLayout(t)
		mustWrite(t, layout.PendingMetricsRoot, "not a directory\n")
		err := recordMetrics(hkCovMetricsPolicy(unwritableTarget), layout, events, now)
		if err == nil || !strings.Contains(err.Error(), "append hook metrics") ||
			!strings.Contains(err.Error(), "replay pending hook metrics") ||
			!strings.Contains(err.Error(), "persist pending hook metrics receipt") {
			t.Fatalf("recordMetrics error = %v, want all three failures named", err)
		}
	})

	t.Run("append and replay fail but the receipt is written", func(t *testing.T) {
		layout := hkCovPendingLayout(t)
		mustMkdirAll(t, layout.PendingMetricsRoot)
		mustWrite(t, filepath.Join(layout.PendingMetricsRoot, "bad.json"), "{not json\n")
		err := recordMetrics(hkCovMetricsPolicy(unwritableTarget), layout, events, now)
		if err == nil || !strings.Contains(err.Error(), "wrote replayable pending hook metrics receipt") ||
			!strings.Contains(err.Error(), "replay pending hook metrics") {
			t.Fatalf("recordMetrics error = %v, want a replayable-receipt report", err)
		}
		entries, readErr := os.ReadDir(layout.PendingMetricsRoot)
		if readErr != nil || len(entries) != 2 {
			t.Fatalf("pending receipts = %v, %v; want the bad one plus the new one", entries, readErr)
		}
	})

	t.Run("append fails but the receipt is written", func(t *testing.T) {
		layout := hkCovPendingLayout(t)
		mustMkdirAll(t, layout.PendingMetricsRoot)
		err := recordMetrics(hkCovMetricsPolicy(unwritableTarget), layout, events, now)
		if err == nil || !strings.Contains(err.Error(), "wrote replayable pending hook metrics receipt") ||
			strings.Contains(err.Error(), "replay pending hook metrics") {
			t.Fatalf("recordMetrics error = %v, want only the append failure and receipt path", err)
		}
	})

	t.Run("append fails and the receipt cannot be created", func(t *testing.T) {
		if os.Geteuid() == 0 {
			return
		}
		layout := hkCovPendingLayout(t)
		lockedParent := t.TempDir()
		if err := os.Chmod(lockedParent, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(lockedParent, 0o700) })
		layout.PendingMetricsRoot = filepath.Join(lockedParent, "pending")
		err := recordMetrics(hkCovMetricsPolicy(unwritableTarget), layout, events, now)
		if err == nil || !strings.Contains(err.Error(), "persist pending hook metrics receipt") ||
			strings.Contains(err.Error(), "replay pending hook metrics") {
			t.Fatalf("recordMetrics error = %v, want a persist failure without a replay failure", err)
		}
	})
}

func TestHkCovUniqueSortedPaths(t *testing.T) {
	// A blank entry cleans to ".", which this helper keeps: it is a real
	// directory reference, not an absent path.
	got := uniqueSortedPaths([]string{"  /b  ", "/a", "/a", "", "/c"})
	want := []string{".", "/a", "/b", "/c"}
	if len(got) != len(want) {
		t.Fatalf("uniqueSortedPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uniqueSortedPaths = %v, want %v", got, want)
		}
	}
	if got := uniqueSortedPaths(nil); len(got) != 0 {
		t.Fatalf("uniqueSortedPaths(nil) = %v, want empty", got)
	}
}

func TestHkCovSanitizeRuntimeSegment(t *testing.T) {
	cases := map[string]string{
		"":             "unknown",
		"   ":          "unknown",
		"clean-name_1": "clean-name_1",
		"a b:c/d":      "a_b_c_d",
		"Ünïcode":      "_n_code",
	}
	for input, want := range cases {
		if got := sanitizeRuntimeSegment(input); got != want {
			t.Fatalf("sanitizeRuntimeSegment(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHkCovRandomTokenIsRandomHex(t *testing.T) {
	first, err := randomToken(8)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 16 {
		t.Fatalf("randomToken(8) = %q, want 16 hex characters", first)
	}
	second, err := randomToken(8)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("randomToken returned the same value twice: %q", first)
	}
}

func hkCovWriteReceipt(t *testing.T, path string, receipt PendingMetricsReceipt) {
	t.Helper()
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, string(data)+"\n")
}

func TestHkCovNewEventActionsAndOutcomes(t *testing.T) {
	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	context := eventContext{repository: "acme/widget", commit: "abc123", branch: "main", labels: map[string]string{"dev": "alex"}}
	cases := map[string]string{
		"post-commit": "commit",
		"pre-push":    "push-attempt",
		"pre-commit":  "commit-check",
		"commit-msg":  "commit-msg",
	}
	for hook, wantAction := range cases {
		event := context.newEvent(hook, false, 1500*time.Millisecond, now)
		if event.Action != wantAction {
			t.Fatalf("newEvent(%q).Action = %q, want %q", hook, event.Action, wantAction)
		}
		if event.Outcome != "failed" {
			t.Fatalf("newEvent(%q).Outcome = %q, want failed", hook, event.Outcome)
		}
		if event.Repository != "acme/widget" || event.Commit != "abc123" || event.Branch != "main" {
			t.Fatalf("newEvent(%q) context = %#v", hook, event)
		}
		if event.DurationMS != 1500 || event.SchemaVersion != EventSchemaVersion {
			t.Fatalf("newEvent(%q) = %#v", hook, event)
		}
		if event.Labels["dev"] != "alex" {
			t.Fatalf("newEvent(%q).Labels = %v", hook, event.Labels)
		}
		event.Labels["dev"] = "mutated"
		if context.labels["dev"] != "alex" {
			t.Fatal("newEvent's labels alias the event context's map")
		}
	}
	if passed := context.newEvent("pre-commit", true, 0, now); passed.Outcome != "passed" {
		t.Fatalf("newEvent(success).Outcome = %q, want passed", passed.Outcome)
	}
	block := context.newBlockEvent("pre-push", HookBlock{ID: "go/pre-push", Profile: "go"}, false, time.Second, now)
	if block.Action != "hook-block" || block.Block != "go/pre-push" || block.Profile != "go" {
		t.Fatalf("newBlockEvent = %#v", block)
	}

	plain := eventContext{}.newEvent("pre-commit", true, 0, now)
	if plain.Labels != nil {
		t.Fatalf("newEvent with no labels = %v, want nil", plain.Labels)
	}
}

func TestHkCovAppendEventsRejectsEmptyAndBadPaths(t *testing.T) {
	if err := AppendEvents(filepath.Join(t.TempDir(), "events.jsonl"), nil); err != nil {
		t.Fatalf("AppendEvents(nil) = %v, want nil", err)
	}
	directory := t.TempDir()
	if err := AppendEvent(directory, hkCovEvent(t)); err == nil || !strings.Contains(err.Error(), "open hook metrics") {
		t.Fatalf("AppendEvent(directory) error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "events.jsonl")
	if err := AppendEvents(path, []Event{hkCovEvent(t), hkCovEvent(t)}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("metrics file mode = %v, want 0600", info.Mode().Perm())
	}
	events, err := ReadEvents(path)
	if err != nil || len(events) != 2 {
		t.Fatalf("ReadEvents = %#v, %v; want two events", events, err)
	}
}

func TestHkCovReadEventsErrorBranches(t *testing.T) {
	if events, err := ReadEvents(filepath.Join(t.TempDir(), "missing.jsonl")); err != nil || events != nil {
		t.Fatalf("ReadEvents(missing) = %#v, %v; want nil, nil", events, err)
	}
	blocker := filepath.Join(t.TempDir(), "regular-file")
	mustWrite(t, blocker, "not a directory\n")
	if _, err := ReadEvents(filepath.Join(blocker, "events.jsonl")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("ReadEvents(ENOTDIR) error = %v", err)
	}
	if _, err := ReadEvents(t.TempDir()); err == nil {
		t.Fatal("ReadEvents(directory) should fail while scanning")
	}

	path := filepath.Join(t.TempDir(), "events.jsonl")
	valid := hkCovEvent(t)
	validJSON, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, "\n   \n"+string(validJSON)+"\n")
	events, err := ReadEvents(path)
	if err != nil || len(events) != 1 {
		t.Fatalf("ReadEvents with blank lines = %#v, %v; want one event", events, err)
	}

	mustWrite(t, path, "{not json\n")
	if _, err := ReadEvents(path); err == nil || !strings.Contains(err.Error(), "parse hook metrics") {
		t.Fatalf("ReadEvents(malformed) error = %v", err)
	}

	mustWrite(t, path, `{"schema_version": 99}`+"\n")
	if _, err := ReadEvents(path); err == nil || !strings.Contains(err.Error(), "uses schema version 99") {
		t.Fatalf("ReadEvents(schema) error = %v", err)
	}
}

func TestHkCovSummarizeEdgeCases(t *testing.T) {
	now := time.Date(2024, 3, 10, 12, 0, 0, 0, time.UTC)
	events := []Event{
		{
			SchemaVersion: EventSchemaVersion,
			Timestamp:     now.AddDate(0, 0, -1),
			Repository:    "acme/widget",
			Hook:          "pre-commit",
			Action:        "hook-block",
			Block:         "go/pre-commit",
			Profile:       "go",
			Outcome:       "failed",
			DurationMS:    200,
		},
		{
			SchemaVersion: EventSchemaVersion,
			Timestamp:     now.AddDate(0, 0, -1),
			Repository:    "acme/widget",
			Hook:          "pre-commit",
			Action:        "commit-check",
			Outcome:       "passed",
			DurationMS:    100,
		},
		{
			SchemaVersion: EventSchemaVersion,
			Timestamp:     now.AddDate(0, 0, -40),
			Repository:    "acme/widget",
			Hook:          "pre-commit",
			Action:        "commit-check",
			Outcome:       "passed",
			DurationMS:    999,
		},
		{
			SchemaVersion: EventSchemaVersion,
			Timestamp:     now.AddDate(0, 0, -1),
			Repository:    "other/repo",
			Hook:          "pre-push",
			Action:        "push-attempt",
			Outcome:       "passed",
			DurationMS:    300,
		},
	}
	summary := Summarize(events, 0, "acme/", now)
	if summary.From != "2024-03-10" || summary.Through != "2024-03-10" {
		t.Fatalf("window = %s..%s, want a single day", summary.From, summary.Through)
	}
	if summary.Commits != 0 || summary.PushAttempts != 0 || summary.CommitChecks != 0 || summary.HookRuns != 0 {
		t.Fatalf("summary = %#v, want the out-of-window and filtered events excluded", summary)
	}

	summary = Summarize(events, 3, "acme/", now)
	if summary.CommitChecks != 1 || summary.HookRuns != 1 || summary.HookFailures != 0 {
		t.Fatalf("summary = %#v, want one commit check", summary)
	}
	if summary.AverageDurationMS != 100 {
		t.Fatalf("average = %d, want 100", summary.AverageDurationMS)
	}
	if len(summary.Blocks) != 1 || summary.Blocks[0].ID != "go/pre-commit" || summary.Blocks[0].Failures != 1 || summary.Blocks[0].AverageDurationMS != 200 {
		t.Fatalf("blocks = %#v", summary.Blocks)
	}
	if len(summary.Days) != 3 {
		t.Fatalf("days = %d, want 3", len(summary.Days))
	}
}

func TestHkCovMeasureEdgeCases(t *testing.T) {
	now := time.Date(2024, 3, 10, 12, 0, 0, 0, time.UTC)
	stream := Event{
		SchemaVersion: EventSchemaVersion,
		Timestamp:     now,
		Action:        "push-attempt",
		Ref:           "refs/heads/stream/task",
		DurationMS:    streamPushBudgetMS + 1500,
	}
	delta := Measure([]Event{stream}, 0, "", now)
	if delta.From != "2024-03-10" || delta.Through != "2024-03-10" {
		t.Fatalf("window = %s..%s, want a single day", delta.From, delta.Through)
	}
	if delta.StreamPush.Runs != 1 || delta.StreamPush.MaxDurationMS != streamPushBudgetMS+1500 {
		t.Fatalf("stream push cost = %#v", delta.StreamPush)
	}
	found := false
	for _, reason := range delta.Unmeasured {
		if strings.Contains(reason, "more than a hook that runs no verification should cost") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Unmeasured = %v, want an over-budget stream push warning", delta.Unmeasured)
	}
}
