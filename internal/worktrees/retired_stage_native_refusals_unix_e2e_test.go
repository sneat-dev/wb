//go:build e2e && !windows

package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestE2ERetiredStageInventoryRetainsNativeSymlinkAndSpecialEvidence(t *testing.T) {
	t.Parallel()
	stage := t.TempDir()
	link := filepath.Join(stage, "link")
	saved := link + "-retained"
	if err := os.Symlink("outside-target", link); err != nil {
		t.Fatal(err)
	}
	observed := false
	inventory, err := inventoryStageObserved(stage, func(current, boundary string) {
		if current == link && boundary == "symlink" {
			observed = true
			if err := os.Rename(link, saved); err != nil {
				t.Fatal(err)
			}
		}
	})
	if !observed || !errors.Is(err, os.ErrNotExist) || inventory != (stageContentInventory{}) {
		t.Fatalf("symlink inventory=%+v observed=%v %v", inventory, observed, err)
	}
	if target, err := os.Readlink(saved); err != nil || target != "outside-target" {
		t.Fatalf("retained symlink=%q %v", target, err)
	}
	if err := unix.Mkfifo(filepath.Join(stage, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	inventory, err = inventoryStage(stage)
	if err != nil || !inventory.Ambiguous || inventory.Symlinks != 1 || inventory.Files != 1 || inventory.Bytes != 0 {
		t.Fatalf("special entry inventory=%+v %v", inventory, err)
	}
}

func TestE2ERetiredStageReceiptDiscoveryPreservesNativeReadRefusal(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	directory := filepath.Join(home, "reports", "worktree-stage-recovery", "task")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	receipt := filepath.Join(directory, "receipt-loop.json")
	if err := os.Symlink(filepath.Base(receipt), receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.ReadFile(receipt); err == nil {
		t.Fatal("native self-loop receipt read unexpectedly succeeded")
	}
	if got := findRetiredStageReceipt(home, "absent", "stage"); got != "" {
		t.Fatalf("unreadable receipt admitted=%q", got)
	}
	if target, err := os.Readlink(receipt); err != nil || target != filepath.Base(receipt) {
		t.Fatalf("receipt evidence=%q %v", target, err)
	}
}

//nolint:paralleltest // Only the isolated self-reexecuted child changes cwd; the parent is parallel.
func TestE2ERetiredStageRecoveryRetainsNativeRootResolutionFailure(t *testing.T) {
	const marker = "WB_RETIRED_STAGE_CWD_CHILD"
	if os.Getenv(marker) == "1" {
		original, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		working := t.TempDir()
		if err := os.Chdir(working); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(original); err != nil {
				t.Fatal(err)
			}
		}()
		if err := os.Chmod(working, 0); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chmod(working, 0700); err != nil {
				t.Fatal(err)
			}
		}()
		if _, err := os.Getwd(); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("native cwd permission=%v", err)
		}
		outcome, err := RecoverRetiredStages(context.Background(), RetiredStageRecoveryOptions{ProjectsRoot: "relative", Task: "task"})
		if !errors.Is(err, os.ErrPermission) || len(outcome.Results) != 0 || outcome.ReceiptPath != "" {
			t.Fatalf("root resolution=%+v %v", outcome, err)
		}
		return
	}
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2ERetiredStageRecoveryRetainsNativeRootResolutionFailure$")
	command.Env = append(os.Environ(), marker+"=1")
	if dir := wtLifeCovCoverDir(); testing.CoverMode() != "" && dir != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+dir)
		command.Env = append(command.Env, "GOCOVERDIR="+dir)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cwd child=%v\n%s", err, strings.TrimSpace(string(output)))
	}
}
