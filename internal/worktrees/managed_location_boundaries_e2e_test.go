//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// The path grammar rejects unsafe shapes before trying to infer Git identity.
// A path with valid grammar still needs a real linked checkout before it can
// claim the canonical repository's authority.
func TestE2EManagedLocationRejectsInvalidShapeAndMissingGitAuthority(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	storeRoot := filepath.Join(t.TempDir(), "worktrees")
	localRoot := filepath.Join(t.TempDir(), ".worktrees")
	for _, tc := range []struct {
		name, root, relative, wantError string
		local                           bool
	}{
		{"one segment in nonlocal store", storeRoot, "task", "resolver-recognized worktrees hierarchy", false},
		{"local checkout without Git", localRoot, "task", "derive linked worktree identity", true},
		{"central invalid host", storeRoot, "task/not-a-host/acme/app", "must be at", false},
		{"central checkout without Git", storeRoot, "task/github.com/acme/app", "derive linked worktree identity", false},
		{"three-level invalid owner", storeRoot, "task/.invalid/app", "must be at", false},
		{"legacy direct invalid repository", storeRoot, "task/bad!repo", "must be at", false},
		{"legacy direct checkout without Git", storeRoot, "task/renamed", "legacy direct worktree", false},
	} {
		layout := wbhome.Layout{WorktreesRoot: tc.root, Local: tc.local}
		path := filepath.Join(tc.root, tc.relative)
		location, err := locateManagedWorktree(context.Background(), projectsRoot, path, []wbhome.Layout{layout})
		if err == nil || !strings.Contains(err.Error(), tc.wantError) || location != (managedWorktreeLocation{}) {
			t.Errorf("%s: location=%+v err=%v, want %q without authority", tc.name, location, err, tc.wantError)
		}
	}
}

//nolint:paralleltest // newGitFixture sets process-wide HOME/XDG/Git environment for its real linked checkouts.
func TestE2EManagedLocationBindsEveryLayoutToCanonicalGitIdentity(t *testing.T) {
	fixture := newGitFixture(t)
	ctx := context.Background()
	// A legacy two-level canonical clone takes its central-store host from its
	// configured origin spelling. No network operation is needed by this test.
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", "https://github.com/acme/app.git")
	mainSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "main")
	storeRoot := filepath.Join(t.TempDir(), "worktrees")
	localRoot := filepath.Join(fixture.canonical, ".worktrees")
	for index, tc := range []struct {
		name, root, relative, wantError, wantPathRepository string
		local                                               bool
		wantLocation                                        bool
	}{
		{"repository-local", localRoot, "local-task", "", "", true, true},
		{"central host mismatch", storeRoot, "host-task/gitlab.com/acme/app", "path host \"gitlab.com\" but canonical clone address host \"github.com\"", "", false, false},
		{"central owner mismatch", storeRoot, "owner-task/github.com/other/app", "path owner \"other\" but canonical clone owner \"acme\"", "", false, false},
		{"central repository rename", storeRoot, "rename-task/github.com/acme/old-app", "", "old-app", false, false},
		{"central accepted", storeRoot, "central-task/github.com/acme/app", "", "", false, true},
		{"three-level stage accepted", storeRoot, "stage-task/.wb-stage-candidate/checkout", "", "", false, true},
		{"three-level invalid repository", storeRoot, "invalid-task/acme/bad!repo", "must be at", "", false, false},
		{"three-level repository rename", storeRoot, "old-task/acme/old-app", "", "old-app", false, false},
		{"legacy direct repository mismatch", storeRoot, "legacy-task/old-app", "legacy direct worktree", "", false, false},
	} {
		path := filepath.Join(tc.root, tc.relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("%s: create checkout parent: %v", tc.name, err)
		}
		gitTest(t, fixture.canonical, "worktree", "add", "-b", fmt.Sprintf("feature/location-%d", index), path, "main")
		layout := wbhome.Layout{Home: fixture.home, WorktreesRoot: tc.root, Local: tc.local}
		location, err := locateManagedWorktree(ctx, fixture.projectsRoot, path, []wbhome.Layout{layout})
		switch {
		case tc.wantPathRepository != "":
			var mismatch *RepositoryRenameMismatchError
			if !errors.As(err, &mismatch) || location != (managedWorktreeLocation{}) || mismatch.Worktree != path || mismatch.Owner != "acme" ||
				mismatch.PathRepository != tc.wantPathRepository || mismatch.CanonicalRepository != "app" {
				t.Errorf("%s: location=%+v mismatch=%+v err=%v", tc.name, location, mismatch, err)
			}
		case tc.wantError != "":
			if err == nil || !strings.Contains(err.Error(), tc.wantError) || location != (managedWorktreeLocation{}) {
				t.Errorf("%s: location=%+v err=%v, want %q", tc.name, location, err, tc.wantError)
			}
		case tc.wantLocation:
			want := managedWorktreeLocation{Layout: layout, Task: strings.Split(tc.relative, "/")[0],
				StoreHost: "github.com", Owner: "acme", Repository: "app", Worktree: path}
			if err != nil || location != want {
				t.Errorf("%s: location=%+v err=%v, want %+v", tc.name, location, err, want)
			}
		default:
			t.Errorf("%s: case has no expected result", tc.name)
		}
		if got := gitTestOutput(t, path, "rev-parse", "HEAD"); got != mainSHA {
			t.Errorf("%s: linked checkout HEAD = %q, want canonical main %q", tc.name, got, mainSHA)
		}
	}
}
