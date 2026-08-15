package orchestrate

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestAcquireOperationLockResumesExactLegacyRemnantOnlyWithResume(t *testing.T) {
	// This is the exact durable state a killed dependency campaign leaves: the
	// kernel dropped its flock but the legacy operation=<id>\npid=<pid>\n file
	// remains. Resume is the explicit operator authorization to reclaim it.
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	githubDir := t.TempDir()
	const operation = "deps-bump-go-00120bc1438c"
	path := operationLockTestPath(t, githubDir, operation)
	legacy := "operation=deps-bump-go-00120bc1438c\npid=" + strconv.Itoa(killedProcessPID(t)) + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := AcquireOperationLock(githubDir, operation, false); err == nil || !strings.Contains(err.Error(), "ambiguous or interrupted") {
		t.Fatalf("non-resume acquisition error = %v", err)
	}
	if contents, err := os.ReadFile(path); err != nil || string(contents) != legacy {
		t.Fatalf("non-resume changed stale lock: contents=%q err=%v", contents, err)
	}

	lock, err := AcquireOperationLock(githubDir, operation, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	if lock.lock == nil || !lock.lock.ReclaimedInterrupted() {
		t.Fatal("resume did not hold the exact interrupted lock")
	}
}

func killedProcessPID(t *testing.T) int {
	t.Helper()
	process := exec.Command("sh", "-c", "sleep 60")
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	pid := process.Process.Pid
	if err := process.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err == nil {
		t.Fatal("killed child unexpectedly exited without a signal")
	}
	return pid
}

func TestAcquireOperationLockRefusesLiveOwner(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	githubDir := t.TempDir()
	const operation = "live-operation"
	owner, err := AcquireOperationLock(githubDir, operation, false)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Release()

	if _, err := AcquireOperationLock(githubDir, operation, true); err == nil || !strings.Contains(err.Error(), "already active") {
		t.Fatalf("live-owner acquisition error = %v", err)
	}
}

func TestAcquireOperationLockRefusesLiveLegacyOwner(t *testing.T) {
	// A pre-descriptor-lock WB process used O_EXCL only. Its PID is the only
	// liveness evidence available, and an alive PID must remain fail-closed.
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	githubDir := t.TempDir()
	const operation = "live-legacy-operation"
	path := operationLockTestPath(t, githubDir, operation)
	contents := "operation=live-legacy-operation\npid=" + strconv.Itoa(os.Getpid()) + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireOperationLock(githubDir, operation, true); err == nil || !strings.Contains(err.Error(), "already active or ownership is ambiguous") {
		t.Fatalf("live legacy acquisition error = %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != contents {
		t.Fatalf("live legacy lock changed: contents=%q err=%v", got, err)
	}
}

func TestAcquireOperationLockPreservesInvalidMetadata(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	githubDir := t.TempDir()
	for _, test := range []struct {
		name     string
		contents string
	}{
		{name: "other operation", contents: "operation=another-operation\npid=6954\n"},
		{name: "nonpositive pid", contents: "operation=invalid-metadata\npid=0\n"},
		{name: "trailing data", contents: "operation=invalid-metadata\npid=6954\nextra\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			const operation = "invalid-metadata"
			path := operationLockTestPath(t, githubDir, operation)
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := AcquireOperationLock(githubDir, operation, true); err == nil || !strings.Contains(err.Error(), "ambiguous lock") {
				t.Fatalf("invalid metadata acquisition error = %v", err)
			}
			if contents, err := os.ReadFile(path); err != nil || string(contents) != test.contents {
				t.Fatalf("invalid metadata was changed: contents=%q err=%v", contents, err)
			}
		})
	}
}

func TestAcquireOperationLockPreservesAmbiguousLinks(t *testing.T) {
	for _, test := range []struct {
		name string
		link func(string, string) error
	}{
		{name: "symlink", link: os.Symlink},
		{name: "hardlink", link: os.Link},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(wbhome.EnvOverride, t.TempDir())
			githubDir := t.TempDir()
			const operation = "linked-remnant"
			path := operationLockTestPath(t, githubDir, operation)
			target := filepath.Join(t.TempDir(), "foreign-lock")
			contents := "operation=linked-remnant\npid=6954\n"
			if err := os.WriteFile(target, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := test.link(target, path); err != nil {
				t.Fatal(err)
			}

			if _, err := AcquireOperationLock(githubDir, operation, true); err == nil || !strings.Contains(err.Error(), "ambiguous or interrupted") {
				t.Fatalf("linked remnant acquisition error = %v", err)
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != contents {
				t.Fatalf("foreign lock changed: contents=%q err=%v", got, err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "symlink" && info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("symlink remnant was replaced: mode=%v", info.Mode())
			}
			if test.name == "hardlink" && info.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("hardlink remnant was replaced: mode=%v", info.Mode())
			}
		})
	}
}

func TestOperationLockReleasePreservesLateReplacement(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	githubDir := t.TempDir()
	const operation = "late-replacement"
	path := operationLockTestPath(t, githubDir, operation)
	lock, err := AcquireOperationLock(githubDir, operation, false)
	if err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(filepath.Dir(path), ".held-before-replacement")
	if err := os.Rename(path, original); err != nil {
		t.Fatal(err)
	}
	successor := "operation=successor\npid=1\n"
	if err := os.WriteFile(path, []byte(successor), 0o600); err != nil {
		t.Fatal(err)
	}
	lock.Release()
	if contents, err := os.ReadFile(path); err != nil || string(contents) != successor {
		t.Fatalf("late successor was removed: contents=%q err=%v", contents, err)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("original held inode was not preserved: %v", err)
	}
}

func TestRunResumesExactLegacyOperationLock(t *testing.T) {
	t.Setenv(wbhome.EnvOverride, t.TempDir())
	githubDir := t.TempDir()
	const operation = "resume-through-run"
	path := operationLockTestPath(t, githubDir, operation)
	if err := os.WriteFile(path, []byte("operation=resume-through-run\npid="+strconv.Itoa(killedProcessPID(t))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), nil, textHandler{}, Options{GitHubDir: githubDir, Operation: operation, Resume: true}); err != nil {
		t.Fatal(err)
	}
}

func operationLockTestPath(t *testing.T, githubDir, operation string) string {
	t.Helper()
	home, err := wbhome.EnsureRoot(githubDir)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := worktrees.OpenOperationLockDirectory(filepath.Join(home, "worktrees", operation))
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(home, "worktrees", operation, ".lock")
}
