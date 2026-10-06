//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // the existing native Git fixture owns process-wide WB/XDG environment.
func TestE2EOperationBaseAuthenticatesClaimAndPhysicalBranchReceipt(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	ctx := context.Background()
	fixture := newGitFixture(t)
	base := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
	for _, retained := range []bool{false, true} {
		task, branch := "operation-new", "wb/operation-new"
		if retained {
			task, branch = "operation-retained", "wb/operation-retained"
			gitTest(t, fixture.canonical, "branch", branch, base)
		}
		placement, err := ResolveWorktreePlacement(ctx, fixture.projectsRoot, fixture.canonical, base)
		if err != nil {
			t.Fatal(err)
		}
		created, err := CreateWorktreeAtPlacement(ctx, fixture.projectsRoot, fixture.canonical, placement, task, "acme/app", branch, "main", base)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(created.Close)
		if created.BranchCreated == retained {
			t.Fatalf("actual new-branch receipt=%v retained=%v", created.BranchCreated, retained)
		}
		result := CreateResult{Repository: "acme/app", WorktreeDir: created.Path, Branch: branch, Base: "main", BaseSHA: base}
		options := WorkLogOptions{EffortID: task, RunID: "owned-run", Model: "unknown"}
		if sha, present, err := OperationWorkLogBase(fixture.home, task, result, options); err != nil || present || sha != "" {
			t.Fatalf("actual typed missing: %q %v %v", sha, present, err)
		}
		if _, err := EnsureWorkLogClaim(fixture.home, task, result, options); err != nil {
			t.Fatal(err)
		}
		request := result
		request.BaseSHA = strings.Repeat("f", 40)
		if sha, present, err := OperationWorkLogBase(fixture.home, task, request, options); err != nil || !present || sha != base {
			t.Fatalf("authenticated original base: %q %v %v", sha, present, err)
		}
		request.Base = "different"
		if _, _, err := OperationWorkLogBase(fixture.home, task, request, options); err == nil {
			t.Fatal("accepted different requested target")
		}
		if recovered, err := RecoverLegacyWorktreeBase(ctx, fixture.canonical, "acme/app", branch, base); err != nil || recovered != base {
			t.Fatalf("native owned legacy query: %q %v", recovered, err)
		}
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("owned blocker"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OperationWorkLogBase(fixture.home, "task", CreateResult{WorktreeDir: filepath.Join(blocked, "checkout")}, WorkLogOptions{}); err == nil {
		t.Fatal("read failure treated as missing")
	}
	if _, err := EnsureWorkLogClaim(fixture.home, "task", CreateResult{WorktreeDir: filepath.Join(blocked, "checkout")}, WorkLogOptions{}); err == nil {
		t.Fatal("claim publication treated physical read refusal as missing")
	}
	if contents, err := os.ReadFile(blocked); err != nil || string(contents) != "owned blocker" {
		t.Fatalf("read refusal changed physical blocker: %q %v", contents, err)
	}

}
