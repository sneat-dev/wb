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

	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // The parent reuses the existing process-environment fixture; only its isolated child changes cwd.
func TestE2EBranchTransitionNativePathAndHomeRefusals(t *testing.T) {
	mode := os.Getenv("WB_BRANCH_TRANSITION_PATH_CHILD")
	if mode != "" {
		projects, canonical := os.Getenv("WB_BRANCH_TRANSITION_PROJECTS"), os.Getenv("WB_BRANCH_TRANSITION_CANONICAL")
		if mode == "home" {
			// The official child inherited no HOME and no root override. Capture
			// native UserHomeDir failure before the original resolver entry points.
			_, cause := os.UserHomeDir()
			if cause == nil {
				t.Fatal("missing home control succeeded")
			}
			if _, err := wbhome.Resolve(""); err == nil {
				t.Fatal("native root resolver control succeeded")
			}
			if _, _, _, err := IdentifyManagedCheckout("", "/unclaimed"); err == nil {
				t.Fatal("identify admitted missing native home")
			}
			got, err := RecoverRepositoryTransferCleanup(context.Background(), RepositoryTransferCleanupOptions{})
			if got.Applied || err == nil {
				t.Fatalf("transfer cleanup admitted native missing home: %+v %v", got, err)
			}
			return
		}
		// Warm the actual valid-branch admission before denying search of the child cwd.
		if _, err := normalizeBranchCleanupOptions(BranchCleanupOptions{ProjectsRoot: projects, Base: "main", Scope: BranchScopeLocal}); err != nil {
			t.Fatal(err)
		}
		original, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		cwd := t.TempDir()
		// A removed cwd can still have a successful native getcwd result on
		// Darwin. Keep this directory and deny its search permission instead;
		// clearing PWD prevents Go's same-file environment shortcut.
		t.Setenv("PWD", "")
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chdir(original); err != nil {
				t.Error(err)
			}
		})
		if err := os.Chmod(cwd, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Chmod(cwd, 0o700); err != nil {
				t.Error(err)
			}
		})
		_, cause := filepath.Abs("relative")
		if !errors.Is(cause, os.ErrPermission) {
			t.Fatalf("native cwd permission control = %v, want permission refusal", cause)
		}
		if _, err := normalizeBranchCleanupOptions(BranchCleanupOptions{ProjectsRoot: projects, Base: "main", Scope: BranchScopeLocal, ReportDir: "relative"}); !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "report directory") {
			t.Fatalf("report path refusal = %v", err)
		}
		if _, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: projects, Path: "relative"}); !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "resolve relative") {
			t.Fatalf("adoption relative path refusal = %v", err)
		}
		// Arbitrary bytes here are deliberately parser admission through the
		// existing Git Runner, never claimed to be native Git registration output.
		ctx := lifecycleGitContext(t, canonical, lifecycleGitReply{operation: "worktree", output: "worktree relative\n"})
		if _, err := sourceRepositoryRoots(ctx, canonical); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("relative registration output admission = %v", err)
		}
		return
	}
	fixture := newGitFixture(t)
	for _, mode := range []string{"cwd", "home"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			deadline := time.Now().Add(time.Minute)
			if parent, ok := t.Deadline(); ok && parent.Before(deadline) {
				deadline = parent
			}
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			t.Cleanup(cancel)
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2EBranchTransitionNativePathAndHomeRefusals$")
			remove := []string{"WB_BRANCH_TRANSITION_PATH_CHILD", "WB_BRANCH_TRANSITION_PROJECTS", "WB_BRANCH_TRANSITION_CANONICAL"}
			if mode == "home" {
				remove = append(remove, "HOME", "USERPROFILE", wbhome.EnvOverride)
			}
			command.Env = append(omitEnv(os.Environ(), remove), "WB_BRANCH_TRANSITION_PATH_CHILD="+mode, "WB_BRANCH_TRANSITION_PROJECTS="+fixture.projectsRoot, "WB_BRANCH_TRANSITION_CANONICAL="+fixture.canonical)
			if testing.CoverMode() != "" {
				if dir := wtLifeCovCoverDir(); dir != "" {
					command.Args = append(command.Args, "-test.gocoverdir="+dir)
					command.Env = append(command.Env, "GOCOVERDIR="+dir)
				}
			}
			if out, err := command.CombinedOutput(); err != nil {
				t.Fatalf("native %s child = %v\n%s", mode, err, out)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture configures process-wide Git and WB environment.
func TestE2EBranchTransitionAdoptionRechecksNativeHome(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/home-boundary")
	hit := false
	got, err := adoptWithHomeObservation(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Path: path}, func() {
		hit = true
		home := os.Getenv("HOME")
		if home == "" {
			t.Fatal("fixture HOME absent")
		}
		if err := os.MkdirAll(home, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(".wb", filepath.Join(home, ".wb")); err != nil {
			t.Fatal(err)
		}
	})
	if !hit || got != nil || err == nil {
		t.Fatalf("second native home observation = %+v %v hit=%v", got, err, hit)
	}
	if head := gitTestOutput(t, path, "rev-parse", "HEAD"); head == "" {
		t.Fatal("home refusal damaged native checkout")
	}
}
