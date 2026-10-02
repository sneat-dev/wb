package sessionlaunch

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionauthority"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestPinnedWorktreePreservesNativeStatusFailure(t *testing.T) {
	t.Parallel()
	plan := slCovPlan("handoff-status")
	executable := slCovExecutable(t, t.TempDir(), "git")
	sentinel := errors.New("native status observation refused")
	var verbs []string
	output := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		verbs = append(verbs, args[2])
		switch args[2] {
		case "rev-parse":
			return []byte(plan.PinnedCommit), nil
		case "symbolic-ref":
			return []byte(plan.PinnedBranch), nil
		default:
			return nil, sentinel
		}
	}
	if err := verifyPinnedWorktreeWithGit(context.Background(), plan, func(string) (string, error) { return executable, nil }, output); !errors.Is(err, sentinel) {
		t.Fatalf("status refusal = %v", err)
	}
	if strings.Join(verbs, ",") != "rev-parse,symbolic-ref,status" {
		t.Fatalf("native observation order = %v", verbs)
	}
}

func TestPrivateLocalRootRetainsDirectoryOpenFailure(t *testing.T) {
	t.Parallel()
	state, plan, bundle := parkedLaunchForBoundaryTest(t, nil)
	sentinel := errors.New("current directory unavailable")
	bundle.ParkedSessionID = plan.HandoffID
	err := verifyPrivateLocalRootWithObservations(state, bundle, plan, func(string) (string, error) { t.Fatal("unexpected git lookup"); return "", nil }, func(path string) (*os.File, error) {
		if path != "." {
			t.Fatalf("open path = %q", path)
		}
		return nil, sentinel
	}, launchGitOutput)
	if !errors.Is(err, sentinel) {
		t.Fatalf("directory open = %v", err)
	}
}

func TestPrivateLocalRootRefusesInvalidResolvedGit(t *testing.T) {
	t.Parallel()
	state, _ := slCovOpenState(t)
	root := t.TempDir()
	bundle := sessionpark.Bundle{Worktrees: []sessionpark.Worktree{{WorktreeDir: root}}}
	plan := launchPlan{RootMode: string(sessionauthority.LaunchRootParkedLocal), WorktreeDir: root}
	err := verifyPrivateLocalRootWithObservations(state, bundle, plan, func(string) (string, error) { return root, nil }, os.Open, launchGitOutput)
	if err == nil {
		t.Fatal("directory accepted as git executable")
	}
}
