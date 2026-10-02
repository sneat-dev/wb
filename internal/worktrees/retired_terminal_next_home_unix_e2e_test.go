//go:build e2e && !windows

package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // Only the isolated child changes HOME/config; parent is parallel.
func TestE2ERetiredTerminalNextNativeConfigurationAndHomeRefusals(t *testing.T) {
	const marker = "WB_RETIRED_TERMINAL_NEXT_HOME_CHILD"
	if os.Getenv(marker) == "1" {
		root := t.TempDir()
		config := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", config)
		path := filepath.Join(config, "wb", "worktrees.yaml")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{"version: [invalid\n", "version: 1\nretirement:\n  archive_repository: invalid/name\n", "version: 1\nretirement:\n  organizations:\n    acme:\n      archive_repository: invalid/name\n"} {
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if got, err := ResolveRetiredArchiveTarget("acme"); err == nil || got != (RetiredArchiveTarget{}) {
				t.Fatalf("invalid native policy=%+v %v", got, err)
			}
			called := false
			if got, err := PlanRetiredArchivePreflight(context.Background(), "acme/app", func(context.Context, string) (RetiredArchiveInspection, error) {
				called = true
				t.Fatal("remote inspector reached after invalid machine policy")
				return RetiredArchiveInspection{}, nil
			}); err == nil || got != (RetiredArchivePlan{}) || called {
				t.Fatalf("invalid policy preflight=%+v %v called=%v", got, err, called)
			}
			if after, err := os.ReadFile(path); err != nil || string(after) != raw {
				t.Fatalf("policy mutated:%q %v", after, err)
			}
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		loop := filepath.Join(t.TempDir(), "native-home-loop")
		if err := os.Symlink(loop, loop); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", loop)
		worktree := filepath.Join(root, "checkout")
		if got, err := FindTerminalCleanupProof("", "acme/app", "main", "task", worktree, "topic"); got != nil || err == nil || !strings.Contains(err.Error(), "projects root is required") {
			t.Fatalf("invalid canonical root=%+v %v", got, err)
		}
		_, control := wbhome.Resolve(root)
		if control == nil {
			t.Fatal("native WB home resolver did not refuse the owned symlink loop")
		}
		got, err := FindTerminalCleanupProof(root, "acme/app", "main", "task", worktree, "topic")
		// EvalSymlinks can allocate an errorString instead of returning a syscall errno.
		// The wrapper must retain the same native resolver's diagnostic and concrete cause type.
		cause := errors.Unwrap(err)
		if got != nil || err == nil || cause == nil || cause.Error() != control.Error() || reflect.TypeOf(cause) != reflect.TypeOf(control) ||
			err.Error() != "resolve WB home for terminal cleanup lookup: "+control.Error() {
			t.Fatalf("native home refusal=%+v %v cause=%v control=%v", got, err, cause, control)
		}
		if target, err := os.Readlink(loop); err != nil || target != loop {
			t.Fatalf("resolver changed native loop evidence: %q %v", target, err)
		}
		return
	}
	t.Parallel()
	deadline := time.Now().Add(time.Minute)
	if parent, ok := t.Deadline(); ok && parent.Before(deadline) {
		deadline = parent
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2ERetiredTerminalNextNativeConfigurationAndHomeRefusals$")
	command.Env = append(omitEnv(os.Environ(), []string{"WB_PROJECTS_ROOT", marker}), marker+"=1")
	if sink := wtLifeCovCoverDir(); testing.CoverMode() != "" && sink != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+sink)
		command.Env = append(command.Env, "GOCOVERDIR="+sink)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native configuration child=%v\n%s", err, output)
	}
}
