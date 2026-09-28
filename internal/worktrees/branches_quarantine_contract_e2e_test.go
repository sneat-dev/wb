//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

//nolint:paralleltest // newGitFixture sets HOME, XDG_CONFIG_HOME, and WB_PROJECTS_ROOT for real Git.
func TestContractQuarantinePlanGitSafety(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	missing := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "missing", Reason: "old"}, now)
	if missing.Outcome != "refused" || !strings.Contains(missing.Error, "source ref unavailable") {
		t.Fatalf("missing ref plan = %#v", missing)
	}
	mainSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "main")
	moved := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "main", SHA: strings.Repeat("a", 40), Reason: "old"}, now)
	if moved.Outcome != "refused" || !strings.Contains(moved.Error, "source moved") {
		t.Fatalf("stale SHA plan = %#v", moved)
	}
	protected := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "main", SHA: mainSHA, Reason: "old"}, now)
	if protected.Outcome != "refused" || !strings.Contains(protected.Error, "protected") {
		t.Fatalf("protected plan = %#v", protected)
	}
	linked := filepath.Join(t.TempDir(), "feature-in-use")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/in-use", linked, "main")
	if head := gitTestOutput(t, fixture.canonical, "branch", "--show-current"); head != "main" {
		t.Fatalf("canonical current branch = %q, want main", head)
	}
	checked := planBranchQuarantine(ctx, fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/in-use", Reason: "old"}, now)
	if checked.Outcome != "refused" || !strings.Contains(checked.Error, "source is checked out in a linked worktree") {
		t.Fatalf("linked checkout plan = %#v", checked)
	}
}

//nolint:paralleltest // newGitFixture and t.Setenv change the process Git and gh environment.
func TestContractQuarantinePlanDestinationCollision(t *testing.T) {
	fixture := newGitFixture(t)
	bin := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nprintf '[]\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	gitTest(t, fixture.canonical, "branch", "feature/old")
	sha := gitTestOutput(t, fixture.canonical, "rev-parse", "feature/old")
	now := time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC)
	destination := retiredBranchDestination(now, "feature/old", sha)
	gitTest(t, fixture.canonical, "branch", destination)
	result := planBranchQuarantine(context.Background(), fixture.projectsRoot, fixture.canonical, BranchQuarantineRequest{Repository: "acme/app", Ref: "feature/old", SHA: sha, Reason: "old"}, now)
	if result.Outcome != "refused" || !strings.Contains(result.Error, "destination already exists") {
		t.Fatalf("destination collision plan = %#v", result)
	}
}
