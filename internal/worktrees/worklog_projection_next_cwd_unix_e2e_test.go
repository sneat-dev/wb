//go:build e2e && !windows

package worktrees

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"
)

//nolint:paralleltest // Only this isolated self-reexecuted child mutates cwd; the parent is parallel.
func TestE2EWorkLogProjectionNextPlacementRetainsNativeAbsoluteRootFailure(t *testing.T) {
	const marker = "WB_WORKLOG_PROJECTION_CWD_CHILD"
	if os.Getenv(marker) == "1" {
		original, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		working := projectionNextTemp(t)
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
			t.Fatalf("native cwd cause=%v", err)
		}
		if err := legacyRepositoryRelocationPaths("relative", ListResult{}, workLogClaim{Repository: "acme/app"}); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("absolute project root cause=%v", err)
		}
		return
	}
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestE2EWorkLogProjectionNextPlacementRetainsNativeAbsoluteRootFailure$")
	command.Env = append(os.Environ(), marker+"=1")
	if dir := wtLifeCovCoverDir(); testing.CoverMode() != "" && dir != "" {
		command.Args = append(command.Args, "-test.gocoverdir="+dir)
		command.Env = append(command.Env, "GOCOVERDIR="+dir)
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("native cwd child=%v\n%s", err, output)
	}
}
