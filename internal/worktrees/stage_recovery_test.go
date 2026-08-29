package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoverRetiredStagesInventoriesGitAndArchivesNonEmptyStage(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	task := "stage-recovery"
	stage := filepath.Join(worktreesRoot, task, ".wb-retired-stage-0123456789abcdef0123456789abcdef")
	if err := os.MkdirAll(filepath.Join(stage, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "nested", "evidence"), []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	planned, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{
		ProjectsRoot: projectsRoot, Task: task,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible || planned.Results[0].Applied {
		t.Fatalf("plan = %#v", planned)
	}
	result := planned.Results[0]
	if result.FileCount != 1 || result.ByteCount != int64(len("preserve\n")) || result.ContentDigest == "" {
		t.Fatalf("content inventory = %#v", result)
	}
	if !strings.Contains(result.Reason, "privately archived") {
		t.Fatalf("reason = %q", result.Reason)
	}
	if planned.ReceiptPath == "" {
		t.Fatal("plan did not emit deterministic receipt path")
	}

	applied, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{
		ProjectsRoot: projectsRoot, Task: task, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || !applied.Results[0].Applied || applied.Results[0].ArchivePath == "" {
		t.Fatalf("apply = %#v", applied)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("stage remains after archive: %v", err)
	}
	archived := filepath.Join(applied.Results[0].ArchivePath, "nested", "evidence")
	contents, err := os.ReadFile(archived)
	if err != nil || string(contents) != "preserve\n" {
		t.Fatalf("archive contents = %q, err=%v", contents, err)
	}
	receipt, err := os.ReadFile(applied.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	var decoded RetiredStageRecoveryOutcome
	if err := json.Unmarshal(receipt, &decoded); err != nil || len(decoded.Results) != 1 || !decoded.Results[0].Applied {
		t.Fatalf("receipt = %s, err=%v", receipt, err)
	}
}

func TestRecoverRetiredStagesRecordsDurableGitIdentity(t *testing.T) {
	fixture := newGitFixture(t)
	task := "stage-recovery-git"
	stageName := ".wb-retired-stage-22222222222222222222222222222222"
	stage := filepath.Join(fixture.home, "worktrees", task, stageName)
	if err := os.MkdirAll(filepath.Dir(stage), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", stage, "-b", "stage-recovery")

	outcome, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 {
		t.Fatalf("git stage inventory = %#v", outcome.Results)
	}
	result := outcome.Results[0]
	if result.HeadSHA == "" || result.GitDir == "" || result.Branch != "stage-recovery" || !result.Durable {
		t.Fatalf("git identity = %#v", result)
	}
}

func TestRecoverRetiredStagesNeverFollowsSymlink(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	task := "stage-recovery-symlink"
	taskPath := filepath.Join(worktreesRoot, task)
	if err := os.MkdirAll(taskPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(taskPath, ".wb-retired-stage-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.Symlink(outside, stage); err != nil {
		t.Fatal(err)
	}

	outcome, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: task, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.Results) != 1 || outcome.Results[0].Applied || outcome.Results[0].Eligible || !strings.Contains(outcome.Results[0].Reason, "symlink") {
		t.Fatalf("symlink outcome = %#v", outcome.Results)
	}
	if _, err := os.Stat(filepath.Join(outside, "secret")); err != nil {
		t.Fatalf("outside evidence changed: %v", err)
	}
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("symlink was removed: %v", err)
	}
}

func TestRecoverRetiredStagesResumeSeesDeterministicArchive(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	task := "stage-recovery-resume"
	stage := filepath.Join(worktreesRoot, task, ".wb-retired-stage-11111111111111111111111111111111")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("resume\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: task, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: task, Stage: filepath.Base(stage), Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if first.ReceiptPath == "" || second.ReceiptPath != first.ReceiptPath {
		t.Fatalf("receipt paths are not resumable: %q vs %q", first.ReceiptPath, second.ReceiptPath)
	}
}
