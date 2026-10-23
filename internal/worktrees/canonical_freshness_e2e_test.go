//go:build e2e

package worktrees

import (
	"context"
	"testing"
)

func TestE2EGuardCanonicalFreshnessIsNotReportedOffTheBaseBranch(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "switch", "-c", "feature")
	result, err := Guard(context.Background(), fixture.canonical, GuardOptions{ProjectsRoot: fixture.projectsRoot, CheckFreshness: true})
	if err != nil || result.Freshness != nil {
		t.Fatalf("guard on a feature branch = (%#v, %v), want a result with no freshness receipt", result, err)
	}
}
