package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// cgxc2BrokenHookRepoFixture creates a projects root with one repository
// whose .git is a bare, empty directory -- present enough for discovery to
// find it, broken enough that hooks.Check/hooks.Apply fail against it.
func cgxc2BrokenHookRepoFixture(t *testing.T) (projectsRoot string) {
	t.Helper()
	projectsRoot = t.TempDir()
	repoPath := filepath.Join(projectsRoot, "github.com", "acme", "app")
	if err := os.MkdirAll(filepath.Join(repoPath, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return projectsRoot
}

func cgxc2TestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "cgxc2"}
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	return cmd
}

// TestCgxc2ApplyHooksFleetCountsAndReportsFailures proves the applyErr != nil
// branch of applyHooksFleet: a repository hooks.Apply cannot process is
// counted into failed and the command still returns a failures-summary
// error rather than a false "0 failed" success.
func TestCgxc2ApplyHooksFleetCountsAndReportsFailures(t *testing.T) {
	t.Parallel()
	projectsRoot := cgxc2BrokenHookRepoFixture(t)
	inv := &invocation{projectsRoot: projectsRoot}
	cmd := cgxc2TestCommand()
	err := applyHooksFleet(inv, cmd, "", false, false)
	if err == nil {
		t.Fatalf("expected a failures-summary error against a broken repo, got nil")
	}
	if !strings.Contains(err.Error(), "failed in") {
		t.Errorf("err = %v", err)
	}
}

// TestCgxc2CheckHooksFleetCountsAndReportsProblems proves checkHooksFleet's
// checkErr != nil branch: hooks.Check failing for a repository is recorded
// as a problem and the fleet-level *hooksCheckError is returned.
func TestCgxc2CheckHooksFleetCountsAndReportsProblems(t *testing.T) {
	t.Parallel()
	projectsRoot := cgxc2BrokenHookRepoFixture(t)
	inv := &invocation{projectsRoot: projectsRoot}
	cmd := cgxc2TestCommand()
	err := checkHooksFleet(inv, cmd, "", false)
	if err == nil {
		t.Fatalf("expected a *hooksCheckError against a broken repo, got nil")
	}
	if _, ok := err.(*hooksCheckError); !ok {
		t.Fatalf("err = %v (%T), want *hooksCheckError", err, err)
	}
}
