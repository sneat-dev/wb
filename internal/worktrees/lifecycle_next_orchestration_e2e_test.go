//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // Native merged-task and GitHub fixtures configure process-wide environment.
func TestE2ELifecycleNextCleanupPropagatesNativeLinkStoreReadFailure(t *testing.T) {
	const task = "cleanup-unreadable-link-store"
	fixture, created, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	store := filepath.Join(fixture.home, "streams")
	if err := os.WriteFile(store, []byte("retained stream root occupant"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main", Now: func() time.Time { return mergedAt.Add(time.Hour) }}
	run := &cleanupRun{ctx: context.Background(), normalized: options, now: options.Now(), listed: ListOutcome{Results: []ListResult{{Task: task, Repository: created.Repository, WorktreeDir: created.WorktreeDir, Branch: created.Branch, HeadSHA: head, Clean: true, IntegratedAtOrigin: true}}}}
	if err := run.buildResults(); err == nil || !strings.Contains(err.Error(), "check live link sources") || len(run.outcome.Results) != 0 {
		t.Fatalf("link-source guard=%+v %v", run.outcome, err)
	}
	outcome, err := Cleanup(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "check live link sources") || len(outcome.Results) != 0 || outcome.ReportPath != "" {
		t.Fatalf("public link-source guard=%+v %v", outcome, err)
	}
	if data, err := os.ReadFile(store); err != nil || string(data) != "retained stream root occupant" {
		t.Fatalf("store occupant changed: %q %v", data, err)
	}
	if got := gitTestOutput(t, created.WorktreeDir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("read refusal changed checkout head: %s", got)
	}
}

//nolint:paralleltest // Native merged-task, process-death and GitHub fixtures configure process-wide environment.
func TestE2ELifecycleNextRecoveredCleanupCallbackRefusalPreservesAuthority(t *testing.T) {
	const task = "cleanup-native-resume-refusal"
	fixture, created, head, mergedAt := prepareMergedTask(t, task)
	installMergedPullRequestFixture(t, head, mergedAt)
	taskDir := filepath.Dir(logicalTaskLockPathForTest(t, fixture, created, task))
	lockPath := filepath.Join(taskDir, ".lock")
	if err := os.WriteFile(lockPath, []byte(fmt.Sprintf("operation=%s\npid=%d\n", task, killedLifecycleProcessPID(t))), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing-resume-evidence")
	var cause error
	observed := ""
	var heldBytes []byte
	var heldInfo os.FileInfo
	outcome, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: task, ResumeInterrupted: true, Apply: true, DeleteRemote: true, Now: func() time.Time { return mergedAt.Add(time.Hour) }, afterResumeInterruptedLock: func(path string) error {
		observed = path
		var err error
		heldBytes, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		heldInfo, err = os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		_, cause = os.ReadFile(missing)
		return cause
	}})
	if observed == "" || cause == nil || !errors.Is(err, cause) || len(outcome.Results) != 0 {
		t.Fatalf("callback refusal=%+v %v native=%v observed=%s", outcome, err, cause, observed)
	}
	if data, err := os.ReadFile(observed); err != nil || string(data) != string(heldBytes) {
		t.Fatalf("refusal lost recovered authority: %q %v", data, err)
	}
	if info, err := os.Stat(observed); err != nil || heldInfo == nil || !os.SameFile(info, heldInfo) {
		t.Fatalf("refusal replaced recovered authority: %v %v", info, err)
	}
	if got := gitTestOutput(t, created.WorktreeDir, "rev-parse", "HEAD"); got != head {
		t.Fatalf("callback refusal removed/changed checkout: %s", got)
	}
	if got := gitTestOutput(t, fixture.remote, "rev-parse", "refs/heads/"+created.Branch); got != head {
		t.Fatalf("callback refusal changed remote branch: %s", got)
	}
}
