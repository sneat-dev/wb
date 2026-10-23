//go:build e2e && !windows

package hooks

import (
	"context"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // Only the isolated self-reexec child changes cwd; the parent runs in parallel.
func TestE2EHookPathResolutionRetainsNativeCwdPermissionFailure(t *testing.T) {
	if os.Getenv("WB_HOOK_CWD_REFUSAL_CHILD") == "1" {
		repo := initRepo(t)
		config := filepath.Join(t.TempDir(), "hooks.yaml")
		if err := os.WriteFile(config, []byte("version: 1\nprofiles:\n  exclude: [worktree]\n"), 0600); err != nil {
			t.Fatal(err)
		}
		executable := testWBExecutable(t, "wb")
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
				t.Error(err)
			}
		}()
		if err := os.Chmod(working, 0); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Chmod(working, 0700); err != nil {
				t.Error(err)
			}
		}()
		if _, err := os.Getwd(); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("native cwd refusal=%v", err)
		}
		checks := []struct {
			name, prefix string
			call         func() error
		}{
			{"projects root", "resolve projects root", func() error { _, err := absoluteProjectsRoot("relative/projects"); return err }},
			{"check", "resolve projects root", func() error { _, err := Check(repo, config, executable, "relative/projects"); return err }},
			{"apply", "resolve projects root", func() error {
				_, err := Apply(ApplyOptions{RepoPath: repo, ConfigPath: config, WBExecutable: executable, ProjectsRoot: "relative/projects"})
				return err
			}},
			{"refresh", "resolve projects root", func() error { _, err := RefreshManagedShims(repo, config, executable, "relative/projects"); return err }},
			{"launcher", "resolve WB executable", func() error { _, err := normalizedWBLauncher("relative-wb"); return err }},
		}
		for _, check := range checks {
			if err := check.call(); !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), check.prefix) {
				t.Fatalf("%s refusal=%v", check.name, err)
			}
		}
		return
	}
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2EHookPathResolutionRetainsNativeCwdPermissionFailure$")
	command.Env = append(os.Environ(), "WB_HOOK_CWD_REFUSAL_CHILD=1")
	if f := flag.Lookup("test.gocoverdir"); testing.CoverMode() != "" && f != nil && f.Value.String() != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+f.Value.String())
		command.Env = append(command.Env, "GOCOVERDIR="+f.Value.String())
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("cwd permission child=%v\n%s", err, output)
	}
}
