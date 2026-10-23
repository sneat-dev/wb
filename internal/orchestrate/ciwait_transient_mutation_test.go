package orchestrate

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/githubobserver/testfixture"
)

// TestPullRequestCommitParentsDecodesTheParentSHAsFromGitHub covers
// worktree_merge_pr_land.go's pullRequestCommitParents: its one GitHub read
// (a plain `gh api repos/.../commits/<sha>`) and the parents it decodes from
// the response had no unit-tier test reaching them at all -- every existing
// caller-level test for the update-branch-proof path scripts the fake gh
// only up to the local git/merge-tree steps that resolve the ordinary case
// without ever needing a live commit-parents read. installTransientReadTestGH
// (this file) already puts a fake gh on PATH without needing
// runnertest.AllowRealProcess, since it goes through a direct exec.Command
// in internal/githubobserver rather than through task-24's guarded runner.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestPullRequestCommitParentsDecodesTheParentSHAsFromGitHub(t *testing.T) {
	testfixture.InstallTransientReadGH(t, `#!/bin/sh
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/commits/deadbeefcafe' ]; then
  echo '{"sha":"deadbeefcafe","parents":[{"sha":"parent1sha"},{"sha":"parent2sha"}]}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	parents, err := pullRequestCommitParents(context.Background(), "acme/app", "deadbeefcafe")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"parent1sha", "parent2sha"}; !slices.Equal(parents, want) {
		t.Fatalf("parents = %v, want %v", parents, want)
	}
}

// TestCommitTreeSHAReadsTheTreeSHAFromGitHub covers
// worktree_merge_stranded.go's commitTreeSHA: the same kind of gap as
// pullRequestCommitParents above, a plain `gh api repos/.../git/commits/<sha>`
// read whose decoded tree SHA no unit-tier test exercised.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestCommitTreeSHAReadsTheTreeSHAFromGitHub(t *testing.T) {
	testfixture.InstallTransientReadGH(t, `#!/bin/sh
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/git/commits/deadbeefcafe' ]; then
  echo '{"sha":"deadbeefcafe","tree":{"sha":"treeshavalue"}}'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	tree, err := commitTreeSHA(context.Background(), "acme/app", "deadbeefcafe")
	if err != nil {
		t.Fatal(err)
	}
	if tree != "treeshavalue" {
		t.Fatalf("tree = %q, want %q", tree, "treeshavalue")
	}
}

// TestPullRequestCommitParentsSurfacesADecodeError covers
// pullRequestCommitParents' own json.Unmarshal error return: a `gh api`
// call that succeeds (exit 0) but returns a body GitHub's own commit schema
// never produces is a decode failure, not an absent commit, so it must come
// back as an error rather than an empty parent list.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestPullRequestCommitParentsSurfacesADecodeError(t *testing.T) {
	testfixture.InstallTransientReadGH(t, `#!/bin/sh
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/commits/deadbeefcafe' ]; then
  echo 'not valid json'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	_, err := pullRequestCommitParents(context.Background(), "acme/app", "deadbeefcafe")
	if err == nil {
		t.Fatal("err = nil, want a decode error for a body that is not valid JSON")
	}
	if !strings.Contains(err.Error(), "decode commit parents") {
		t.Fatalf("err = %v, want it to name the decode failure", err)
	}
}

// TestCommitTreeSHASurfacesADecodeError covers commitTreeSHA's own
// json.Unmarshal error return, the same shape as the test above.
//
//nolint:paralleltest // calls t.Setenv via installTransientReadTestGH, which Go's testing package forbids combined with t.Parallel
func TestCommitTreeSHASurfacesADecodeError(t *testing.T) {
	testfixture.InstallTransientReadGH(t, `#!/bin/sh
if [ "$1" = api ] && [ "$2" = 'repos/acme/app/git/commits/deadbeefcafe' ]; then
  echo 'not valid json'; exit 0
fi
echo "unexpected gh args: $*" >&2; exit 30
`)
	_, err := commitTreeSHA(context.Background(), "acme/app", "deadbeefcafe")
	if err == nil {
		t.Fatal("err = nil, want a decode error for a body that is not valid JSON")
	}
	if !strings.Contains(err.Error(), "decode commit") {
		t.Fatalf("err = %v, want it to name the decode failure", err)
	}
}
