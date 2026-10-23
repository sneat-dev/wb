//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2ELifecycleNextCleanupExecutableAdmissionPreservesAuthority(t *testing.T) {
	t.Parallel()
	for _, role := range []string{"wb", "git"} {
		t.Run(role, func(t *testing.T) {
			t.Parallel()
			path := t.TempDir()
			if err := os.Mkdir(filepath.Join(path, ".git"), 0o700); err != nil {
				t.Fatal(err)
			}
			canonical, err := openCanonicalRepository(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(canonical.close)
			missing := filepath.Join(t.TempDir(), "missing-cleanup-program")
			var cause error
			resolveMissing := func() (string, error) { resolved, err := exec.LookPath(missing); cause = err; return resolved, err }
			resolveWB := os.Executable
			var resolveGit func() (string, error)
			if role == "wb" {
				resolveWB = resolveMissing
				resolveGit = func() (string, error) { t.Fatal("Git resolved after failed WB admission"); return "", nil }
			} else {
				resolveGit = resolveMissing
			}
			err = runSecureCleanupGitHelperWithExecutables(context.Background(), canonical, nil, nil, "", path, resolveWB, resolveGit, "status")
			if cause == nil || !errors.Is(err, cause) {
				t.Fatalf("native lookup cause lost: %v / %v", err, cause)
			}
			if role == "wb" && !strings.Contains(err.Error(), "locate WB cleanup Git helper") {
				t.Fatalf("diagnostic changed: %v", err)
			}
			if err := canonical.validate(); err != nil {
				t.Fatalf("lookup invalidated ownership: %v", err)
			}
			if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("lookup created executable: %v", err)
			}
		})
	}
}

//nolint:paralleltest // HOME and XDG_CONFIG_HOME configure native WB admission; no collaborator globals are replaced.
func TestE2ELifecycleNextCleanupCapabilityAdmissionPrecedesPublication(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	missing := filepath.Join(t.TempDir(), "missing-capability-program")
	var cause error
	called := 0
	admission := func() error { called++; _, cause = exec.LookPath(missing); return cause }
	run, err := newCleanupRunWithCapability(context.Background(), CleanupOptions{ProjectsRoot: root, Task: "selected-task", Apply: true}, admission)
	if run != nil || cause == nil || !errors.Is(err, cause) || called != 1 {
		t.Fatalf("apply admission=%v %v native=%v calls=%d", run, err, cause, called)
	}
	if _, err := os.Lstat(filepath.Join(root, ".wb")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed capability published home: %v", err)
	}
	run, err = newCleanupRunWithCapability(context.Background(), CleanupOptions{ProjectsRoot: root, Task: "selected-task"}, admission)
	if run == nil || err != nil || called != 1 {
		t.Fatalf("read-only plan required write capability: %v %v calls=%d", run, err, called)
	}
}

func TestE2ELifecycleNextCleanupRetainsInvalidSelectionRefusal(t *testing.T) {
	t.Parallel()
	options := CleanupOptions{Task: "invalid/task", ProjectsRoot: t.TempDir()}
	if _, err := normalizeCleanupOptions(CleanupOptions{Task: "selected-task", Base: "invalid base", ProjectsRoot: options.ProjectsRoot}); err == nil {
		t.Fatal("invalid inventory base admitted")
	}
	outcome, err := Cleanup(context.Background(), options)
	if err == nil || len(outcome.Results) != 0 || outcome.ReportPath != "" {
		t.Fatalf("invalid selection=%+v %v", outcome, err)
	}
}

func TestE2ELifecycleNextCleanupHelperRetainsMissingLocalPushDirectory(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	gitTest(t, path, "init")
	canonical, err := openCanonicalRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(canonical.close)
	missing := filepath.Join(t.TempDir(), "missing-local-push")
	_, native := filepath.EvalSymlinks(missing)
	var cause *os.PathError
	if !errors.As(native, &cause) {
		t.Fatalf("native missing push-directory prerequisite=%v", native)
	}
	err = runSecureCleanupGitHelperWithExecutables(context.Background(), canonical, nil, nil, "", path, os.Executable, trustedGitExecutable, "push", missing, "HEAD:refs/heads/main")
	if !errors.Is(err, cause.Err) || !strings.Contains(err.Error(), "resolve local push remote directory") {
		t.Fatalf("local push refusal=%v control=%v", err, native)
	}
	if err := canonical.validate(); err != nil {
		t.Fatalf("refusal closed borrowed canonical authority: %v", err)
	}
	if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refusal published push directory: %v", err)
	}
}
