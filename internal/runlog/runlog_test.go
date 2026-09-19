package runlog

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestRecorderWritesPrivacySafeLifecycleEvents(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-b", "main")
	git.Dir = root
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "telemetry", EffortKind: worktrees.EffortKindFeature,
		Repository: "acme/app", Worktree: root, Branch: "telemetry", Base: "main",
		BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
		RunID: "run-1", ClaimID: strings.Repeat("b", 64), Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.WriteManifest(root, manifest); err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 9, 4, 21, 0, 0, 0, time.UTC)
	secret := "github_pat_should_never_be_logged_1234567890"
	recorder, err := Begin(filepath.Join(root, "nested"), []string{"go", "test", "./...", "-token", secret}, started)
	if err != nil {
		t.Fatal(err)
	}
	if recorder.OperationID == "" || recorder.Path == "" {
		t.Fatalf("recorder = %#v", recorder)
	}
	if err := recorder.Finish(0, 120*time.Millisecond, 30*time.Millisecond, started.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(recorder.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "./...") {
		t.Fatalf("run log contains raw arguments: %s", raw)
	}
	events, err := Read(recorder.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].State != "requested" || events[1].State != "succeeded" {
		t.Fatalf("events = %#v", events)
	}
	if events[1].Kind != "go/test" || events[1].Repository != "acme/app" || events[1].DurationMS != 2000 {
		t.Errorf("completed event = %#v", events[1])
	}
	summary := Summarize(events, started.Add(-time.Minute))
	if summary.Operations != 1 || summary.Failed != 0 || summary.Running != 0 || len(summary.Kinds) != 1 || summary.Kinds[0].P95MS != 2000 {
		t.Errorf("summary = %#v", summary)
	}
}

// TestBeginStampsProvenanceFromEnv is wb#631's `wb run` half of the
// acceptance test: the requested event Begin writes carries every declared
// provenance field plus wb_version, and Finish's terminal event — a copy of
// the same recorder.event — carries it through too.
func TestBeginStampsProvenanceFromEnv(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-run-1")
	t.Setenv("AI_AGENT", "claude-code")
	t.Setenv("CLAUDE_EFFORT", "low")
	t.Setenv("WB_SUBAGENT_ID", "agent-9")
	t.Setenv("WB_SUBAGENT_TOOL_USE_ID", "toolu_3")

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-b", "main")
	git.Dir = root
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "provenance", EffortKind: worktrees.EffortKindFeature,
		Repository: "acme/app", Worktree: root, Branch: "provenance", Base: "main",
		BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
		RunID: "run-1", ClaimID: strings.Repeat("b", 64), Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.WriteManifest(root, manifest); err != nil {
		t.Fatal(err)
	}
	recorder, err := Begin(root, []string{"go", "test", "./..."}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := recorder.Finish(0, time.Millisecond, time.Millisecond, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	events, err := Read(recorder.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	for _, event := range events {
		if event.HarnessSessionID != "sess-run-1" || event.Harness != "claude-code" || event.EffortLevel != "low" ||
			event.AgentID != "agent-9" || event.ToolUseID != "toolu_3" || event.WBVersion == "" {
			t.Fatalf("event did not carry every provenance field: %+v", event)
		}
	}
}

func TestRecorderDoesNotCreateStateOutsideManagedWorktree(t *testing.T) {
	root := t.TempDir()
	recorder, err := Begin(root, []string{"git", "status"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if recorder.OperationID == "" || recorder.Path != "" {
		t.Fatalf("recorder = %#v", recorder)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("unmanaged directory changed: entries=%v err=%v", entries, err)
	}
}

// TestRecordQueueAdmittedAtIsAdditive proves the new admission-time field
// (cmd/wb run.go's queue-visibility receipts) sits alongside the pre-existing
// QueueWaitMS without disturbing any other recorded field or the existing
// event shape.
func TestRecordQueueAdmittedAtIsAdditive(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-b", "main")
	git.Dir = root
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	manifest := worktrees.Manifest{
		Version: 1, EffortID: "queue-admission", EffortKind: worktrees.EffortKindFeature,
		Repository: "acme/app", Worktree: root, Branch: "queue-admission", Base: "main",
		BaseSHA: strings.Repeat("a", 40), CreatedAt: time.Now().UTC(),
		RunID: "run-1", ClaimID: strings.Repeat("b", 64), Provenance: worktrees.ProvenanceCreated,
	}
	if err := worktrees.WriteManifest(root, manifest); err != nil {
		t.Fatal(err)
	}

	started := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)
	recorder, err := Begin(root, []string{"go", "test", "./..."}, started)
	if err != nil {
		t.Fatal(err)
	}
	admittedAt := started.Add(250 * time.Millisecond)
	recorder.RecordAdmission(1, 250*time.Millisecond)
	recorder.RecordQueueAdmittedAt(admittedAt)
	if err := recorder.Finish(0, 10*time.Millisecond, 5*time.Millisecond, started.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	events, err := Read(recorder.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %#v", events)
	}
	completed := events[1]
	if completed.QueueWaitMS != 250 {
		t.Errorf("QueueWaitMS = %d, want 250", completed.QueueWaitMS)
	}
	if completed.AdmittedAt == nil || !completed.AdmittedAt.Equal(admittedAt) {
		t.Errorf("AdmittedAt = %v, want %v", completed.AdmittedAt, admittedAt)
	}
	if completed.Kind != "go/test" || completed.DurationMS != 1000 {
		t.Errorf("unrelated fields disturbed: %#v", completed)
	}
	if events[0].AdmittedAt != nil {
		t.Errorf("requested event = %#v, want AdmittedAt unset before admission", events[0])
	}
}
