//go:build e2e

package orchestrate

import (
	"context"
	"github.com/sneat-dev/wb/internal/progress"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // Existing GH fixture owns PATH and XDG_STATE_HOME with t.Setenv.
func TestE2ERunWorktreeMergeLandsItsOwnPreparedReceiptOnExactRemote(t *testing.T) {
	fixture := newEngineFixture(t)
	source := createMergeSource(t, fixture, "run-source", "feature/run-source", "run.txt", "run\n")
	sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	installWorktreeMergeDirectGH(t)
	t.Setenv("WB_TEST_REMOTE", fixture.repository.CloneURL)
	landed, err := RunWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"}, WorktreeMergeLandOptions{ProjectsRoot: filepath.Join(t.TempDir(), "wrong-root"), Receipt: filepath.Join(t.TempDir(), "wrong-receipt.json"), Route: WorktreeMergeRouteDirect, Timeout: 5 * time.Second, CheckPollInterval: time.Millisecond})
	if err != nil {
		t.Fatalf("public prepare/land journey: receipt=%+v err=%v", landed, err)
	}
	if landed.Status != WorktreeMergeLanded || landed.LandingSHA == "" || landed.LandingSHA != landed.Candidate.SHA || landed.Route.Route != WorktreeMergeRouteDirect {
		t.Fatalf("landing=%+v", landed)
	}
	if landed.PushGate == nil || landed.PushGate.Status != "passed" || landed.PushGate.RemoteRef != "refs/heads/main" || landed.PushGate.LocalSHA != landed.LandingSHA {
		t.Fatalf("exact pre-push gate=%+v", landed.PushGate)
	}
	durable, err := readWorktreeMergeReceipt(landed.ReceiptPath)
	if err != nil || durable.LandingSHA != landed.LandingSHA || durable.Candidate != landed.Candidate {
		t.Fatalf("durable=%+v err=%v", durable, err)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != landed.LandingSHA {
		t.Fatalf("canonical=%s want %s", got, landed.LandingSHA)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main")); !strings.HasPrefix(got, landed.LandingSHA+"\t") {
		t.Fatalf("remote=%q want %s", got, landed.LandingSHA)
	}
	if got := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); got != sourceHead {
		t.Fatalf("source changed: %s want %s", got, sourceHead)
	}
}

func TestE2ERunWorktreeMergePrepareRefusalNeverEntersLanding(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var events []progress.Event
	got, err := RunWorktreeMerge(context.Background(), WorktreeMergePrepareOptions{ProjectsRoot: root, Target: "main", Progress: func(e progress.Event) { events = append(events, e) }}, WorktreeMergeLandOptions{ProjectsRoot: filepath.Join(root, "wrong"), Receipt: "missing.json", Progress: func(e progress.Event) { t.Errorf("landing entered after prepare refusal: %+v", e) }})
	if err == nil || !strings.Contains(err.Error(), "at least one source worktree") || got.ReceiptPath != "" {
		t.Fatalf("prepare refusal receipt=%+v err=%v", got, err)
	}
	if len(events) != 1 {
		t.Fatalf("prepare progress=%+v", events)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("prepare refusal mutated root: entries=%v err=%v", entries, err)
	}
}

func TestE2ERunWorktreeMergeReturnsPreparedIdentityOnLandingRefusal(t *testing.T) {
	t.Parallel()
	fixture := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, fixture, "run-refusal-source", "feature/run-refusal", "source.txt", "source\n")
	target := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	sourceHead := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD"))
	got, err := RunWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: fixture.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"}, WorktreeMergeLandOptions{ProjectsRoot: filepath.Join(t.TempDir(), "wrong-root"), Receipt: "missing.json", Route: WorktreeMergeRouteDirect, StopBeforeMerge: true})
	if err == nil || !strings.Contains(err.Error(), "stop-before-merge requires the pull-request route") || got.Status != WorktreeMergePrepared || got.Candidate.SHA == "" {
		t.Fatalf("landing refusal=%+v err=%v", got, err)
	}
	durable, err := readWorktreeMergeReceipt(got.ReceiptPath)
	if err != nil || durable.Candidate != got.Candidate || durable.Status != WorktreeMergePrepared {
		t.Fatalf("durable prepared=%+v err=%v", durable, err)
	}
	if head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); head != target {
		t.Fatalf("canonical changed: %s", head)
	}
	if head := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "HEAD")); head != sourceHead {
		t.Fatalf("source changed: %s", head)
	}
	if remote := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "origin", "refs/heads/main")); !strings.HasPrefix(remote, target+"\t") {
		t.Fatalf("remote changed: %q", remote)
	}
}
