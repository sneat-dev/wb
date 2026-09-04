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
