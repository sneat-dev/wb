package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

// These cases exercise recovery refusals with actual directory entries. In
// particular, a saved path is never authority to retire a replacement inode.
//
//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestRetiredStageRefusals(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	for _, task := range []string{"", "../outside"} {
		if _, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: task}); err == nil {
			t.Fatalf("accepted task %q", task)
		}
	}
	if _, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: "safe", Stage: "other"}); err == nil {
		t.Fatal("accepted non-retired stage name")
	}
	if got := retiredStageReceiptPath(projectsRoot, nil); got != "" {
		t.Fatalf("empty receipt path = %q", got)
	}

	task := "recovery-batch"
	stageName := ".wb-retired-stage-33333333333333333333333333333333"
	stage := filepath.Join(worktreesRoot, task, stageName)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := inspectRetiredStage(context.Background(), worktreesRoot, task, stage, stageName)
	if !result.Eligible {
		t.Fatalf("stage inspection = %#v", result)
	}

	//nolint:paralleltest // these cases mutate the same retired stage directory.
	t.Run("inventory changed", func(t *testing.T) {
		changed := result
		if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		applyRetiredStageRecovery(filepath.Join(projectsRoot, ".wb"), &changed)
		if changed.Eligible || changed.Applied || !strings.Contains(changed.Reason, "changed after inventory") {
			t.Fatalf("changed inventory = %#v", changed)
		}
		if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	//nolint:paralleltest // these cases mutate the same retired stage directory.
	t.Run("replacement inode", func(t *testing.T) {
		replaced := result
		old := stage + "-old"
		if err := os.Rename(stage, old); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Rename(old, stage) })
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		applyRetiredStageRecovery(filepath.Join(projectsRoot, ".wb"), &replaced)
		if replaced.Eligible || replaced.Applied || !strings.Contains(replaced.Reason, "identity changed") {
			t.Fatalf("replacement inode = %#v", replaced)
		}
		if err := os.RemoveAll(stage); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(old, stage); err != nil {
			t.Fatal(err)
		}
	})
	//nolint:paralleltest // these cases mutate the same retired stage directory.
	t.Run("existing deterministic archive", func(t *testing.T) {
		blocked := inspectRetiredStage(context.Background(), worktreesRoot, task, stage, stageName)
		archive := retiredStageArchivePath(filepath.Join(projectsRoot, ".wb"), blocked)
		if err := os.MkdirAll(archive, 0o700); err != nil {
			t.Fatal(err)
		}
		applyRetiredStageRecovery(filepath.Join(projectsRoot, ".wb"), &blocked)
		if blocked.Eligible || blocked.Applied || !strings.Contains(blocked.Reason, "archive already exists") {
			t.Fatalf("existing archive = %#v", blocked)
		}
	})
	if _, err := os.Stat(filepath.Join(stage, "evidence")); err != nil {
		t.Fatalf("recovery changed original evidence: %v", err)
	}
}

func TestStageInventoryAndReceiptFallback(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(stage, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "file"), []byte("recover"), 0o600); err != nil {
		t.Fatal(err)
	}
	inventory, err := inventoryStage(stage)
	if err != nil || inventory.Files != 2 || inventory.Symlinks != 1 || !inventory.Ambiguous || inventory.Bytes != 7 {
		t.Fatalf("symlink inventory = %#v, %v", inventory, err)
	}
	if stageContentIsDurable(context.Background(), stage, "invalid", inventory.Ambiguous) {
		t.Fatal("ambiguous stage was considered durable")
	}
	if _, err := inventoryStage(filepath.Join(root, "absent")); err == nil {
		t.Fatal("missing stage was inventoried")
	}
	missing := inspectRetiredStage(context.Background(), root, "task", filepath.Join(root, "absent"), ".wb-retired-stage-44444444444444444444444444444444")
	if missing.Eligible || !strings.Contains(missing.Reason, "cannot inspect") {
		t.Fatalf("missing stage = %#v", missing)
	}

	home := filepath.Join(root, "home")
	reportDir := filepath.Join(home, "reports", "worktree-stage-recovery", "task")
	if err := os.MkdirAll(reportDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportDir, "receipt-fallback.json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := findRetiredStageReceipt(home, "task", "stage"); got != filepath.Join(reportDir, "receipt-fallback.json") {
		t.Fatalf("fallback receipt = %q", got)
	}
	if got := findRetiredStageReceipt(home, "absent", "stage"); got != "" {
		t.Fatalf("unexpected receipt = %q", got)
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestTransferReceiptRefusals(t *testing.T) {
	for _, scenario := range []string{"wrong path", "bad json", "wrong version", "wrong identity", "replacement inode", "symlink quarantine", "missing origin"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			projectsRoot := filepath.Join(root, "projects")
			t.Setenv(wbhome.EnvOverride, projectsRoot)
			dest := filepath.Join(projectsRoot, "newco", "renamed")
			quarantine := filepath.Join(projectsRoot, "newco", ".wb-replaced-renamed-123456789abc")
			if err := os.MkdirAll(quarantine, 0o700); err != nil {
				t.Fatal(err)
			}
			held, err := openAbsoluteDirectoryNoFollow(quarantine, false)
			if err != nil {
				t.Fatal(err)
			}
			options := RepositoryRelocateOptions{ProjectsRoot: projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: "file:///remote", DefaultBranch: "main", Now: time.Now}
			result := RepositoryRelocateResult{SourceRepository: options.SourceRepository, DestinationRepository: options.DestinationRepository, DestinationDir: dest, RetiredDestinationDir: quarantine, RemoteURL: options.RemoteURL, DefaultBranch: options.DefaultBranch}
			receipt, pending, err := recordRepositoryTransferCleanupIntent(options, result, "0123456789abcdef0123456789abcdef01234567", held)
			_ = held.Close()
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "wrong path" {
				pending = filepath.Join(root, "pending.json")
			}
			if scenario == "bad json" {
				if err := os.WriteFile(pending, []byte("{bad"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "wrong version" || scenario == "wrong identity" {
				if scenario == "wrong version" {
					receipt.Version++
				} else {
					receipt.DestinationDir = filepath.Join(root, "other")
				}
				content, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(pending, content, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "replacement inode" {
				if err := os.Remove(quarantine); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(quarantine, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "symlink quarantine" {
				if err := os.Remove(quarantine); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), quarantine); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "missing origin" {
				if err := os.Remove(quarantine); err != nil {
					t.Fatal(err)
				}
			}
			got, recoverErr := RecoverRepositoryTransferCleanup(context.Background(), RepositoryTransferCleanupOptions{ProjectsRoot: projectsRoot, ReceiptPath: pending, Apply: true})
			switch scenario {
			case "wrong path", "bad json", "wrong version", "wrong identity", "symlink quarantine":
				if recoverErr == nil {
					t.Fatalf("%s: accepted %#v", scenario, got)
				}
			case "replacement inode", "missing origin":
				if recoverErr != nil || got.Applied || got.Eligible || got.Reason == "" {
					t.Fatalf("%s: outcome %#v, %v", scenario, got, recoverErr)
				}
			}
		})
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestTransferTerminalIsImmutable(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	if err := os.MkdirAll(filepath.Join(projectsRoot, ".wb", "reports", "repository-transfers"), 0o700); err != nil {
		t.Fatal(err)
	}
	receipt := repositoryTransferCleanupReceipt{OperationID: "batch-terminal", QuarantineDevice: 12, QuarantineInode: 34}
	if _, err := recordRepositoryTransferCleanupTerminal(projectsRoot, receipt, "invalid", time.Now()); err == nil {
		t.Fatal("accepted invalid status")
	}
	first, err := recordRepositoryTransferCleanupTerminal(projectsRoot, receipt, repositoryTransferCleanupRetired, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := recordRepositoryTransferCleanupTerminal(projectsRoot, receipt, repositoryTransferCleanupRetired, time.Now().Add(time.Second))
	if err != nil || second != first {
		t.Fatalf("idempotent terminal = %q, %v", second, err)
	}
	receipt.QuarantineInode++
	if _, err := recordRepositoryTransferCleanupTerminal(projectsRoot, receipt, repositoryTransferCleanupRetired, time.Now()); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("conflicting immutable receipt: %v", err)
	}
	if err := os.WriteFile(first, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := recordRepositoryTransferCleanupTerminal(projectsRoot, receipt, repositoryTransferCleanupRetired, time.Now()); err == nil {
		t.Fatal("accepted unreadable terminal receipt")
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestStageRefusalReleasesLockForRetry(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	task := "retry-stage"
	name := ".wb-retired-stage-88888888888888888888888888888888"
	stage := filepath.Join(worktreesRoot, task, name)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	planned := inspectRetiredStage(context.Background(), worktreesRoot, task, stage, name)
	if !planned.Eligible {
		t.Fatalf("stage inspection = %#v", planned)
	}
	if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	applyRetiredStageRecovery(filepath.Join(projectsRoot, ".wb"), &planned)
	if planned.Eligible || planned.Applied {
		t.Fatalf("changed stage applied = %#v", planned)
	}
	if _, err := os.Lstat(filepath.Join(worktreesRoot, task, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no-op refusal retained operation lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: task, Apply: true})
	if err != nil || len(got.Results) != 1 || !got.Results[0].Applied {
		t.Fatalf("retry recovery = %#v, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(worktreesRoot, task, ".lock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful recovery retained operation lock: %v", err)
	}
}

func TestTransferDescriptorBoundaries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	quarantine := filepath.Join(root, "quarantine")
	if err := os.Mkdir(quarantine, 0o700); err != nil {
		t.Fatal(err)
	}
	parent, err := openAbsoluteDirectoryNoFollow(root, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	if _, absent, err := openRepositoryTransferCleanupQuarantine(parent, root, "absent"); err != nil || !absent {
		t.Fatalf("absent = %v, %v", absent, err)
	}
	if err := os.WriteFile(filepath.Join(root, "regular"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openRepositoryTransferCleanupQuarantine(parent, root, "regular"); err == nil {
		t.Fatal("opened regular entry as directory")
	}
	held, absent, err := openRepositoryTransferCleanupQuarantine(parent, root, "quarantine")
	if err != nil || absent {
		t.Fatalf("held = %v, %v", absent, err)
	}
	t.Cleanup(func() { _ = held.Close() })
	if err := retireRepositoryTransferReplacement(quarantine, held); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(quarantine); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine survives: %v", err)
	}
	if err := retireRepositoryTransferReplacement(quarantine, held); err == nil {
		t.Fatal("retired absent quarantine twice")
	}
	if err := os.Mkdir(quarantine, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := retireRepositoryTransferReplacement(quarantine, held); err == nil {
		t.Fatal("retired replacement inode")
	}
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("replacement was removed: %v", err)
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestReceiptAndIdentityErrors(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeRetiredStageReceiptInjected(filepath.Join(blocker, "receipt.json"), RetiredStageRecoveryOutcome{}, (*filewrite.Injector)(nil)); err == nil {
		t.Fatal("wrote receipt under file")
	}
	if _, err := repositoryTransferCleanupDirectory(blocker); err == nil {
		t.Fatal("created repository transfer receipt directory under file")
	}
	if _, err := recordRepositoryTransferCleanupTerminal(blocker, repositoryTransferCleanupReceipt{OperationID: "x"}, repositoryTransferCleanupRetired, time.Now()); err == nil {
		t.Fatal("wrote terminal under file")
	}
	closed, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repositoryTransferCleanupIdentity(closed); err == nil {
		t.Fatal("read identity from closed descriptor")
	}
	options := RepositoryRelocateOptions{ProjectsRoot: root, Now: time.Now}
	if _, _, err := recordRepositoryTransferCleanupIntent(options, RepositoryRelocateResult{}, "", closed); err == nil {
		t.Fatal("recorded intent with closed descriptor")
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestEmptyShellRechecks(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	task := "recheck-shell"
	path := filepath.Join(worktreesRoot, task)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: projectsRoot, Filter: "absent"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "late-evidence"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := RetiredShell{WorktreesRoot: worktreesRoot, Task: task, Path: path, Eligible: true}
	applyTaskShellRetirement(&result)
	if result.Applied || result.Reason == "" {
		t.Fatalf("changed shell = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(path, "late-evidence")); err != nil {
		t.Fatalf("changed evidence removed: %v", err)
	}
	if empty, reason := taskShellIsEmpty(filepath.Join(path, "missing"), task, false); empty || !strings.Contains(reason, "stat task") {
		t.Fatalf("missing shell = %v, %q", empty, reason)
	}
	if empty, err := ownerDirectoryIsProvablyEmpty(filepath.Join(path, "missing")); empty || err == nil {
		t.Fatalf("missing owner = %v, %v", empty, err)
	}

	owner := filepath.Join(path, "owner")
	if err := os.Mkdir(owner, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(owner, "repo")); err != nil {
		t.Fatal(err)
	}
	if empty, err := ownerDirectoryIsProvablyEmpty(owner); empty || err != nil {
		t.Fatalf("symlink repository = %v, %v", empty, err)
	}
	if err := os.Remove(filepath.Join(owner, "repo")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(owner, "repo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(owner, "repo", "checkout"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if empty, err := ownerDirectoryIsProvablyEmpty(owner); empty || err != nil {
		t.Fatalf("nonempty repository = %v, %v", empty, err)
	}
}

func TestUnscopedStageRefusesBadRootAndIneligibleArtifact(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stage := filepath.Join(root, ".wb-retired-stage-55555555555555555555555555555555")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	eligible := LifecycleArtifact{WorktreesRoot: root, Path: stage, Kind: lifecycleArtifactKindStage, State: "quarantined", Disposition: dispositionEmptyUnscopedLocalRetiredStage, Eligible: true}
	artifacts := []LifecycleArtifact{eligible, {WorktreesRoot: root, Path: "ignored", Kind: lifecycleArtifactKindStage, Eligible: false}}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	retireEmptyUnscopedLocalStages(artifacts)
	if artifacts[0].Eligible || artifacts[0].Applied || !strings.Contains(artifacts[0].Reason, "open canonical-local") {
		t.Fatalf("missing root = %#v", artifacts[0])
	}
	if artifacts[1].Applied {
		t.Fatalf("ineligible artifact changed = %#v", artifacts[1])
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestStageDiscoveryAndDurabilityErrors(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	task := "stage-discovery"
	if err := os.WriteFile(filepath.Join(worktreesRoot, task), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: task}); err == nil {
		t.Fatal("read stage list from regular file")
	}
	if err := os.Remove(filepath.Join(worktreesRoot, task)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(worktreesRoot, task), 0o700); err != nil {
		t.Fatal(err)
	}
	name := ".wb-retired-stage-66666666666666666666666666666666"
	stage := filepath.Join(worktreesRoot, task, name)
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "data"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stageContentIsDurable(context.Background(), stage, "0000000000000000000000000000000000000000", false) {
		t.Fatal("unresolvable head was considered durable")
	}
	if err := os.Mkdir(filepath.Join(worktreesRoot, task, "ordinary"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{ProjectsRoot: projectsRoot, Task: task, Stage: name})
	if err != nil || len(got.Results) != 1 {
		t.Fatalf("filtered discovery = %#v, %v", got, err)
	}
	if err := os.RemoveAll(stage); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stage, []byte("regular"), 0o600); err != nil {
		t.Fatal(err)
	}
	inspected := inspectRetiredStage(context.Background(), worktreesRoot, task, stage, name)
	if inspected.Eligible || !strings.Contains(inspected.Reason, "symlink or not") {
		t.Fatalf("regular stage = %#v", inspected)
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestCleanupArtifactPreparationRejectsChangedEntry(t *testing.T) {
	_, worktreesRoot := setUpShellRetirementFixture(t)
	taskPath := filepath.Join(worktreesRoot, "artifact-preflight")
	if err := os.Mkdir(taskPath, 0o700); err != nil {
		t.Fatal(err)
	}
	task, err := acquireCleanupTaskAt(worktreesRoot, "artifact-preflight")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = task.lock.release(); task.close() }()
	artifacts := []LifecycleArtifact{{Path: filepath.Join(taskPath, "ordinary"), Kind: lifecycleArtifactKindStage}}
	if _, _, _, err := prepareCleanupLifecycleArtifacts(filepath.Dir(worktreesRoot), task, []int{0}, artifacts); err == nil || !strings.Contains(err.Error(), "reserved WB identity") {
		t.Fatalf("unrecognized artifact = %v", err)
	}
	artifacts[0].Path = filepath.Join(taskPath, ".wb-retired-stage-77777777777777777777777777777777")
	if _, _, _, err := prepareCleanupLifecycleArtifacts(filepath.Dir(worktreesRoot), task, []int{0}, artifacts); err == nil || !strings.Contains(err.Error(), "open cleanup lifecycle artifact") {
		t.Fatalf("missing artifact = %v", err)
	}
	if err := os.Mkdir(artifacts[0].Path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifacts[0].Path, "evidence"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := prepareCleanupLifecycleArtifacts(filepath.Dir(worktreesRoot), task, []int{0}, artifacts); err == nil || !strings.Contains(err.Error(), "became non-empty") {
		t.Fatalf("changed artifact = %v", err)
	}
	if _, err := os.Stat(filepath.Join(artifacts[0].Path, "evidence")); err != nil {
		t.Fatalf("artifact evidence changed: %v", err)
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestStageApplyFilesystemBoundaries(t *testing.T) {
	for _, scenario := range []string{
		"lock held", "stage disappeared", "stage path replaced", "descriptor closed",
		"archive parent blocked", "archive stat denied", "move refused",
		"move partially completed", "lock retirement failed",
	} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
			task := "boundary-" + strings.ReplaceAll(scenario, " ", "-")
			name := ".wb-retired-stage-99999999999999999999999999999999"
			stage := filepath.Join(worktreesRoot, task, name)
			if err := os.MkdirAll(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			planned := inspectRetiredStage(context.Background(), worktreesRoot, task, stage, name)
			if !planned.Eligible {
				t.Fatalf("stage inspection = %#v", planned)
			}
			home := filepath.Join(projectsRoot, ".wb")
			var hooks retiredStageRecoveryHooks
			switch scenario {
			case "lock held":
				if err := os.WriteFile(filepath.Join(worktreesRoot, task, ".lock"), []byte("busy"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "stage disappeared":
				hooks.afterLock = func(*cleanupTaskHandle) {
					if err := os.RemoveAll(stage); err != nil {
						t.Fatal(err)
					}
				}
			case "stage path replaced":
				hooks.afterStageOpen = func(*os.File) {
					if err := os.Rename(stage, stage+"-saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(stage, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			case "descriptor closed":
				hooks.beforeIdentityCheck = func(stage *os.File) {
					if err := stage.Close(); err != nil {
						t.Fatal(err)
					}
				}
			case "archive parent blocked":
				hooks.beforeArchiveOpen = func() {
					if err := os.WriteFile(filepath.Join(home, "reports"), []byte("blocked"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "archive stat denied":
				archiveParent := filepath.Dir(retiredStageArchivePath(home, planned))
				hooks.afterArchiveOpen = func() {
					if err := os.Chmod(archiveParent, 0); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { _ = os.Chmod(archiveParent, 0o700) })
			case "move refused":
				hooks.beforeMove = func() {
					if err := os.Rename(stage, stage+"-saved"); err != nil {
						t.Fatal(err)
					}
				}
			case "move partially completed":
				hooks.afterMove = func() {
					if err := os.Mkdir(stage, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			case "lock retirement failed":
				hooks.afterLock = func(task *cleanupTaskHandle) {
					task.lock.beforeRelease = func() {
						if err := os.Remove(filepath.Join(task.taskPath, ".lock")); err != nil {
							t.Fatal(err)
						}
					}
				}
			}
			applyRetiredStageRecoveryWithHooks(home, &planned, hooks)
			if planned.Applied || planned.Eligible || planned.Reason == "" {
				t.Fatalf("%s: unsafe apply = %#v", scenario, planned)
			}
			if scenario != "stage disappeared" && scenario != "move partially completed" && scenario != "lock retirement failed" {
				path := stage
				if scenario == "stage path replaced" || scenario == "move refused" {
					path = stage + "-saved"
				}
				if _, err := os.Stat(filepath.Join(path, "evidence")); err != nil {
					t.Fatalf("%s: original evidence lost: %v", scenario, err)
				}
			}
			if scenario == "move partially completed" {
				if planned.ArchivePath != "" {
					t.Fatalf("partial move should require manual archive discovery: %#v", planned)
				}
				if _, err := os.Stat(filepath.Join(retiredStageArchivePath(home, planned), "evidence")); err != nil {
					t.Fatalf("partial move archive lost: %v", err)
				}
			}
		})
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestTransferPreviewAndReceiptValidation(t *testing.T) {
	for _, scenario := range []string{"present preview", "restored preview", "wrong status", "missing receipt", "report directory replaced", "parent replaced"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			projectsRoot := filepath.Join(root, "projects")
			t.Setenv(wbhome.EnvOverride, projectsRoot)
			destination := filepath.Join(projectsRoot, "newco", "renamed")
			quarantine := filepath.Join(projectsRoot, "newco", ".wb-replaced-renamed-abcdef123456")
			if err := os.MkdirAll(quarantine, 0o700); err != nil {
				t.Fatal(err)
			}
			held, err := openAbsoluteDirectoryNoFollow(quarantine, false)
			if err != nil {
				t.Fatal(err)
			}
			options := RepositoryRelocateOptions{ProjectsRoot: projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: "file:///remote", DefaultBranch: "main", Now: time.Now}
			result := RepositoryRelocateResult{SourceRepository: options.SourceRepository, DestinationRepository: options.DestinationRepository, DestinationDir: destination, RetiredDestinationDir: quarantine, RemoteURL: options.RemoteURL, DefaultBranch: options.DefaultBranch}
			receipt, pending, err := recordRepositoryTransferCleanupIntent(options, result, strings.Repeat("1", 40), held)
			_ = held.Close()
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "restored preview":
				if err := os.Rename(quarantine, destination); err != nil {
					t.Fatal(err)
				}
			case "wrong status":
				receipt.Status = repositoryTransferCleanupRestored
				content, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(pending, content, 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing receipt":
				if err := os.Remove(pending); err != nil {
					t.Fatal(err)
				}
			case "report directory replaced":
				reports := filepath.Dir(pending)
				if err := os.Rename(reports, reports+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(reports+"-saved", reports); err != nil {
					t.Fatal(err)
				}
			case "parent replaced":
				parent := filepath.Dir(quarantine)
				if err := os.Rename(parent, parent+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+"-saved", parent); err != nil {
					t.Fatal(err)
				}
			}
			got, recoverErr := RecoverRepositoryTransferCleanup(context.Background(), RepositoryTransferCleanupOptions{ProjectsRoot: projectsRoot, ReceiptPath: pending})
			switch scenario {
			case "present preview", "restored preview":
				if recoverErr != nil || !got.Eligible || got.Applied {
					t.Fatalf("%s = %#v, %v", scenario, got, recoverErr)
				}
				if scenario == "restored preview" && (got.Outcome != repositoryTransferCleanupRestored || !strings.Contains(got.Reason, "terminal evidence")) {
					t.Fatalf("restored preview = %#v", got)
				}
			default:
				if recoverErr == nil {
					t.Fatalf("%s accepted = %#v", scenario, got)
				}
			}
		})
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestShellUnreadableDirectories(t *testing.T) {
	projectsRoot, worktreesRoot := setUpShellRetirementFixture(t)
	blocker := filepath.Join(projectsRoot, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RetireTaskShells(context.Background(), RetireShellsOptions{ProjectsRoot: blocker}); err == nil {
		t.Fatal("resolved shell root below regular file")
	}
	taskPath := filepath.Join(worktreesRoot, "unreadable-shell")
	ownerPath := filepath.Join(taskPath, "owner")
	if err := os.MkdirAll(ownerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(taskPath, 0); err != nil {
		t.Fatal(err)
	}
	if empty, reason := taskShellIsEmpty(taskPath, "unreadable-shell", false); empty || !strings.Contains(reason, "read task directory") {
		t.Fatalf("unreadable shell = %v, %q", empty, reason)
	}
	if err := os.Chmod(taskPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ownerPath, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ownerPath, 0o700) })
	if empty, err := ownerDirectoryIsProvablyEmpty(ownerPath); empty || err == nil {
		t.Fatalf("unreadable owner = %v, %v", empty, err)
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestTransferReceiptFilesystemFailures(t *testing.T) {
	for _, scenario := range []string{"intent root", "intent directory", "intent write", "terminal directory", "terminal write"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			projectsRoot := filepath.Join(root, "projects")
			t.Setenv(wbhome.EnvOverride, projectsRoot)
			quarantine := filepath.Join(root, "quarantine")
			if err := os.Mkdir(quarantine, 0o700); err != nil {
				t.Fatal(err)
			}
			held, err := os.Open(quarantine)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = held.Close() }()
			options := RepositoryRelocateOptions{ProjectsRoot: projectsRoot, Now: time.Now}
			result := RepositoryRelocateResult{SourceRepository: "a/b", DestinationRepository: "c/d", RetiredDestinationDir: quarantine}
			if scenario == "intent root" {
				if err := os.WriteFile(projectsRoot, []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "intent directory" {
				if err := os.MkdirAll(filepath.Join(projectsRoot, ".wb", "reports"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(projectsRoot, ".wb", "reports", "repository-transfers"), []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "intent write" {
				dir := filepath.Join(projectsRoot, ".wb", "reports", "repository-transfers")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			if strings.HasPrefix(scenario, "intent") {
				if _, _, err := recordRepositoryTransferCleanupIntent(options, result, "head", held); err == nil {
					t.Fatalf("%s unexpectedly published intent", scenario)
				}
				return
			}
			if scenario == "terminal directory" {
				if err := os.MkdirAll(filepath.Join(projectsRoot, ".wb", "reports"), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(projectsRoot, ".wb", "reports", "repository-transfers"), []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "terminal write" {
				dir := filepath.Join(projectsRoot, ".wb", "reports", "repository-transfers")
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(dir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
			}
			if _, err := recordRepositoryTransferCleanupTerminal(projectsRoot, repositoryTransferCleanupReceipt{OperationID: "blocked-terminal"}, repositoryTransferCleanupRetired, time.Now()); err == nil {
				t.Fatalf("%s unexpectedly published terminal", scenario)
			}
		})
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestTransferRetirementNeedsTerminalEvidence(t *testing.T) {
	for _, scenario := range []string{"retired but receipt blocked", "restored but receipt blocked", "retirement refused"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			projectsRoot := filepath.Join(root, "projects")
			t.Setenv(wbhome.EnvOverride, projectsRoot)
			destination := filepath.Join(projectsRoot, "newco", "renamed")
			quarantine := filepath.Join(projectsRoot, "newco", ".wb-replaced-renamed-fedcba123456")
			if err := os.MkdirAll(quarantine, 0o700); err != nil {
				t.Fatal(err)
			}
			held, err := openAbsoluteDirectoryNoFollow(quarantine, false)
			if err != nil {
				t.Fatal(err)
			}
			options := RepositoryRelocateOptions{ProjectsRoot: projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: "file:///remote", DefaultBranch: "main", Now: time.Now}
			result := RepositoryRelocateResult{SourceRepository: options.SourceRepository, DestinationRepository: options.DestinationRepository, DestinationDir: destination, RetiredDestinationDir: quarantine, RemoteURL: options.RemoteURL, DefaultBranch: options.DefaultBranch}
			_, pending, err := recordRepositoryTransferCleanupIntent(options, result, strings.Repeat("1", 40), held)
			_ = held.Close()
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "restored but receipt blocked" {
				if err := os.Rename(quarantine, destination); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "retirement refused" {
				child := filepath.Join(quarantine, "unreadable")
				if err := os.Mkdir(child, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(child, "evidence"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(child, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(child, 0o700) })
			} else {
				reportDir := filepath.Dir(pending)
				if err := os.Chmod(reportDir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(reportDir, 0o700) })
			}
			got, recoverErr := RecoverRepositoryTransferCleanup(context.Background(), RepositoryTransferCleanupOptions{ProjectsRoot: projectsRoot, ReceiptPath: pending, Apply: true})
			if scenario == "retirement refused" {
				if recoverErr != nil || got.Applied || got.Reason == "" {
					t.Fatalf("retirement refusal = %#v, %v", got, recoverErr)
				}
				if _, err := os.Stat(quarantine); err != nil {
					t.Fatalf("quarantine disappeared: %v", err)
				}
			} else {
				if recoverErr == nil || got.Applied {
					t.Fatalf("missing terminal receipt accepted = %#v, %v", got, recoverErr)
				}
			}
			if _, err := os.Stat(pending); err != nil {
				t.Fatalf("pending receipt lost: %v", err)
			}
		})
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestShellApplyFilesystemBoundaries(t *testing.T) {
	for _, scenario := range []string{
		"lock held", "recheck changed", "task reread denied", "stage remove refused",
		"stage removed", "owner reread denied", "repository remove refused",
		"owner remove refused", "lock release refused", "task remove refused",
	} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			_, worktreesRoot := setUpShellRetirementFixture(t)
			task := "shell-boundary"
			path := filepath.Join(worktreesRoot, task)
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(path, ".wb-retired-stage-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
			owner := filepath.Join(path, "owner")
			repository := filepath.Join(owner, "repo")
			switch scenario {
			case "stage remove refused", "stage removed":
				if err := os.Mkdir(stage, 0o700); err != nil {
					t.Fatal(err)
				}
			case "owner reread denied", "repository remove refused", "owner remove refused":
				if err := os.MkdirAll(repository, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			result := RetiredShell{WorktreesRoot: worktreesRoot, Task: task, Path: path, Eligible: true}
			var hooks taskShellRetirementHooks
			switch scenario {
			case "lock held":
				if err := os.WriteFile(filepath.Join(path, ".lock"), []byte("busy"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "recheck changed":
				hooks.afterLock = func() {
					if err := os.WriteFile(filepath.Join(path, "evidence"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "task reread denied":
				hooks.afterRecheck = func() {
					if err := os.Chmod(path, 0); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
			case "stage remove refused":
				hooks.beforeStageRemoval = func(stage string) {
					if err := os.WriteFile(filepath.Join(stage, "evidence"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "owner reread denied":
				hooks.beforeOwnerRead = func(owner string) {
					if err := os.Chmod(owner, 0); err != nil {
						t.Fatal(err)
					}
				}
				t.Cleanup(func() { _ = os.Chmod(owner, 0o700) })
			case "repository remove refused":
				hooks.beforeRepositoryRemove = func(repository string) {
					if err := os.WriteFile(filepath.Join(repository, "evidence"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "owner remove refused":
				hooks.beforeOwnerRemove = func(owner string) {
					if err := os.WriteFile(filepath.Join(owner, "evidence"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "lock release refused":
				hooks.beforeLockRelease = func() {
					if err := os.Remove(filepath.Join(path, ".lock")); err != nil {
						t.Fatal(err)
					}
				}
			case "task remove refused":
				hooks.beforeTaskRemove = func() {
					if err := os.WriteFile(filepath.Join(path, "late"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			applyTaskShellRetirementWithHooks(&result, hooks)
			if scenario == "stage removed" {
				if !result.Applied || result.Error != "" {
					t.Fatalf("stage shell retirement = %#v", result)
				}
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("retired shell remains: %v", err)
				}
				return
			}
			if result.Applied || (result.Error == "" && result.Reason == "") {
				t.Fatalf("%s: unsafe retirement = %#v", scenario, result)
			}
		})
	}
}

func TestUnscopedStageDescriptorBoundaries(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"root replaced", "stage replaced", "first inspection failed", "partial isolation",
		"isolated inspection failed", "isolated became nonempty", "isolated entry changed", "link stat failed",
	} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "root")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			name := ".wb-retired-stage-cccccccccccccccccccccccccccccccc"
			stage := filepath.Join(root, name)
			if err := os.Mkdir(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			artifacts := []LifecycleArtifact{{WorktreesRoot: root, Path: stage, Kind: lifecycleArtifactKindStage, State: "quarantined", Disposition: dispositionEmptyUnscopedLocalRetiredStage, Eligible: true}}
			var hooks gcRetiredStageHooks
			switch scenario {
			case "root replaced":
				hooks.afterRootOpen = func(*os.File) {
					if err := os.Rename(root, root+"-saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(root, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			case "stage replaced":
				hooks.afterStageOpen = func(*os.File) {
					if err := os.Rename(stage, stage+"-saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(stage, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			case "first inspection failed":
				hooks.afterStageMatch = func(directory *os.File) {
					if err := directory.Close(); err != nil {
						t.Fatal(err)
					}
				}
			case "partial isolation":
				hooks.afterMove = func(string) {
					if err := os.Mkdir(stage, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			case "isolated inspection failed":
				hooks.afterIsolation = func(_ string, moved *os.File) {
					if err := moved.Close(); err != nil {
						t.Fatal(err)
					}
				}
			case "isolated became nonempty":
				hooks.afterIsolation = func(retired string, _ *os.File) {
					if err := os.WriteFile(filepath.Join(root, retired, "evidence"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "isolated entry changed":
				hooks.afterIsolation = func(retired string, _ *os.File) {
					from := filepath.Join(root, retired)
					if err := os.Rename(from, from+"-saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(from, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			case "link stat failed":
				hooks.beforeLinkStat = func(moved *os.File) {
					if err := moved.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			retireEmptyUnscopedLocalStagesWithBoundaryHooks(artifacts, nil, nil, hooks)
			got := artifacts[0]
			if got.Applied || got.Eligible || got.Reason == "" {
				t.Fatalf("%s: unsafe retirement = %#v", scenario, got)
			}
			if scenario == "root replaced" {
				if _, err := os.Stat(filepath.Join(root+"-saved", name)); err != nil {
					t.Fatalf("original root stage lost: %v", err)
				}
			} else if scenario == "stage replaced" {
				if _, err := os.Stat(stage + "-saved"); err != nil {
					t.Fatalf("original stage lost: %v", err)
				}
			} else if scenario == "partial isolation" || strings.HasPrefix(scenario, "isolated") || scenario == "link stat failed" {
				if got.ArchivePath != "" {
					if _, err := os.Stat(got.ArchivePath); err != nil && scenario != "isolated entry changed" {
						t.Fatalf("isolated stage lost: %v", err)
					}
				}
			}
		})
	}
}

func TestStageIsolationNameCollisions(t *testing.T) {
	t.Parallel()
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	call := 0
	token := func(int) string { return "dddddddddddddddddddddddddddddddd" }
	move := func(_ *os.File, _ string, _ *os.File, _ string, _ *os.File, _ func(), _ ...func()) (*os.File, error) {
		call++
		if call == 1 {
			return nil, unix.EEXIST
		}
		return root, nil
	}
	name, moved, err := moveEmptyRetiredStageForGCWith(root, "stage", root, nil, nil, token, move)
	if err != nil || moved != root || name != ".wb-retired-stage-dddddddddddddddddddddddddddddddd" || call != 2 {
		t.Fatalf("collision retry = %q, %v, %v, calls=%d", name, moved, err, call)
	}
	call = 0
	move = func(_ *os.File, _ string, _ *os.File, _ string, _ *os.File, _ func(), _ ...func()) (*os.File, error) {
		call++
		return nil, unix.EEXIST
	}
	if _, _, err := moveEmptyRetiredStageForGCWith(root, "stage", root, nil, nil, token, move); err == nil || call != 16 {
		t.Fatalf("collision exhaustion = %v, calls=%d", err, call)
	}
}

//nolint:paralleltest // WB fixture configuration and descriptor mutation must remain serial.
func TestCleanupArtifactArchiveBoundary(t *testing.T) {
	_, worktreesRoot := setUpShellRetirementFixture(t)
	taskPath := filepath.Join(worktreesRoot, "archive-boundary")
	stage := filepath.Join(taskPath, ".wb-retired-stage-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	task, err := acquireCleanupTaskAt(worktreesRoot, "archive-boundary")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = task.lock.release(); task.close() }()
	home := filepath.Dir(worktreesRoot)
	if err := os.WriteFile(filepath.Join(home, "reports"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts := []LifecycleArtifact{{Path: stage, Kind: lifecycleArtifactKindStage}}
	if _, _, _, err := prepareCleanupLifecycleArtifacts(home, task, []int{0}, artifacts); err == nil || !strings.Contains(err.Error(), "archive") {
		t.Fatalf("blocked artifact archive = %v", err)
	}
	if _, err := os.Stat(stage); err != nil {
		t.Fatalf("stage changed: %v", err)
	}
}
