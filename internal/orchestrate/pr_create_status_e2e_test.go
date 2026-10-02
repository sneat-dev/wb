//go:build e2e

package orchestrate

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The reported failure: `wb pr create --add .gitignore --land` in a worktree
// whose only change is an unstaged edit to .gitignore refused with
// leftover-before-landing naming "gitignore".
//
//nolint:paralleltest // createWorktree sets WB_CREATE_BRANCH with t.Setenv
func TestE2ECreateAddOfAnUnstagedRootDotfileWithLandIsNotALeftover(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "dotfile-task", "feature/dotfile", "main", ".gitignore")
	writeEngineFile(t, filepath.Join(worktree, ".gitignore"), "edited\n")
	result, _ := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		Add: []string{".gitignore"}, Message: "chore: ignore", Land: true,
	})
	if result.RefusalCode == CreateRefusalLeftoverBeforeLanding {
		t.Fatalf("refused as a leftover: %s", result.Reason)
	}
	if !reflect.DeepEqual(result.CommittedPaths, []string{".gitignore"}) {
		t.Fatalf("committed = %v, want [.gitignore]; outcome=%s refusal=%s reason=%s", result.CommittedPaths, result.Outcome, result.RefusalCode, result.Reason)
	}
}

//nolint:paralleltest // createWorktree sets WB_CREATE_BRANCH with t.Setenv
func TestE2ECreateAddNamingADotfileWithLandStillRefusesAnUnnamedDotfile(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "dotfile-leftover-task", "feature/dotfile-leftover", "main", ".gitignore", ".editorconfig")
	writeEngineFile(t, filepath.Join(worktree, ".gitignore"), "edited\n")
	writeEngineFile(t, filepath.Join(worktree, ".editorconfig"), "edited\n")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		Add: []string{".gitignore"}, Message: "chore: ignore", Land: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RefusalCode != CreateRefusalLeftoverBeforeLanding || !strings.Contains(result.Reason, ".editorconfig") {
		t.Fatalf("refusal=%s reason=%q, want the unnamed .editorconfig with its leading dot", result.RefusalCode, result.Reason)
	}
}
