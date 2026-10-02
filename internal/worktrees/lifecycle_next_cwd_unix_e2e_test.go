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
)

//nolint:paralleltest // Only the isolated self-reexec child mutates cwd, HOME and directory permissions; parent is parallel.
func TestE2ELifecycleNextCleanupKeepsNativeAbsolutePathRefusals(t *testing.T) {
	const marker = "WB_LIFECYCLE_NEXT_CWD_CHILD"
	if os.Getenv(marker) == "1" {
		original, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		projects, working := t.TempDir(), t.TempDir()
		// Validate the earlier grammar gate before permission loss; the existing
		// pure branch memo remains unchanged and prevents unrelated Git admission.
		if !validBranch(context.Background(), "main") {
			t.Fatal("native main branch grammar refused")
		}
		if err := os.Chdir(working); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chdir(original); err != nil {
				t.Error(err)
			}
		}()
		if err := os.Chmod(working, 0); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chmod(working, 0o700); err != nil {
				t.Error(err)
			}
		}()
		if _, err := os.Getwd(); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("native cwd refusal=%v", err)
		}
		for _, tc := range []struct {
			options CleanupOptions
			want    string
		}{
			{CleanupOptions{ProjectsRoot: projects, Task: "task", SupersededBy: "relative-receipt"}, "resolve supersession receipt"},
			{CleanupOptions{ProjectsRoot: projects, Task: "task", ReportDir: "relative-report"}, "resolve cleanup report directory"},
		} {
			if _, err := normalizeCleanupOptions(tc.options); !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("absolute-path refusal=%v want %s", err, tc.want)
			}
		}
		if err := os.Chmod(working, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
		loop := filepath.Join(t.TempDir(), "legacy-home-loop")
		if err := os.Symlink(loop, loop); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", loop)
		run := &cleanupRun{ctx: context.Background(), normalized: CleanupOptions{ProjectsRoot: projects, Tasks: []string{"task"}, Base: "main", Filter: "acme/app"}, recovery: &InterruptedLockRecovery{Task: "task"}}
		if err := run.verifyTasksFound(); err == nil || !strings.Contains(err.Error(), "too many links") {
			t.Fatalf("unfiltered recovery inventory refusal=%v", err)
		}
		if err := run.buildResults(); err != nil || len(run.outcome.Results) != 0 {
			t.Fatalf("unavailable optional link-source store changed empty plan: %+v %v", run.outcome, err)
		}
		if entries, err := os.ReadDir(projects); err != nil || len(entries) != 0 {
			t.Fatalf("refusal published state: %v %v", entries, err)
		}
		return
	}
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2ELifecycleNextCleanupKeepsNativeAbsolutePathRefusals$")
	command.Env = append(os.Environ(), marker+"=1")
	if directory := wtLifeCovCoverDir(); testing.CoverMode() != "" && directory != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+directory)
		command.Env = append(command.Env, "GOCOVERDIR="+directory)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native cwd child=%v\n%s", err, output)
	}
}
