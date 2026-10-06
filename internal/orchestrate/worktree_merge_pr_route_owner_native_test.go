//go:build e2e

package orchestrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/testenv"
)

// The hosted observation is scripted separately. Every commit/ref/tree in this
// fixture is made by actual Git in a private managed repository and bare remote.
type prRouteOwnerFixture struct {
	f                         engineFixture
	receipt                   WorktreeMergeReceipt
	target, merged, wrongTree string
}

func newPRRouteOwnerFixture(t *testing.T) prRouteOwnerFixture {
	t.Helper()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "pr-route-owner-source", "feature/pr-route-owner", "route.txt", "native route\n")
	receipt, err := PrepareWorktreeMerge(t.Context(), WorktreeMergePrepareOptions{ProjectsRoot: f.githubDir, Sources: []string{source.WorktreeDir}, Target: "main", Model: "test-model", AgentRuntime: "test"})
	if err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, receipt.Candidate.Worktree, "push", "origin", "HEAD:refs/heads/"+receipt.Candidate.Branch)
	remote := f.repository.CloneURL
	runEngineGit(t, remote, "config", "user.name", "WB Test")
	runEngineGit(t, remote, "config", "user.email", "wb@example.test")
	baseTree := strings.TrimSpace(runEngineGit(t, remote, "rev-parse", receipt.TargetSHA+"^{tree}"))
	candidateTree := strings.TrimSpace(runEngineGit(t, remote, "rev-parse", receipt.Candidate.SHA+"^{tree}"))
	target := strings.TrimSpace(runEngineGit(t, remote, "commit-tree", baseTree, "-p", receipt.TargetSHA, "-m", "native target advance"))
	merged := strings.TrimSpace(runEngineGit(t, remote, "commit-tree", candidateTree, "-p", receipt.Candidate.SHA, "-p", target, "-m", "native ordinary update branch"))
	wrong := strings.TrimSpace(runEngineGit(t, remote, "commit-tree", baseTree, "-p", receipt.Candidate.SHA, "-p", target, "-m", "native mismatched merge tree"))
	runEngineGit(t, remote, "update-ref", "refs/heads/main", target)
	runEngineGit(t, remote, "update-ref", "refs/heads/"+receipt.Candidate.Branch, merged)
	runEngineGit(t, remote, "update-ref", "refs/heads/pr-route-wrong-tree", wrong)
	return prRouteOwnerFixture{f: f, receipt: receipt, target: target, merged: merged, wrongTree: wrong}
}

type prRouteObservationRunner struct {
	runner.Runner
	dir   string
	args  []string
	err   error
	seen  int
	after func()
}

func (r *prRouteObservationRunner) RunOpts(ctx context.Context, dir string, opts runner.RunOptions, name string, args ...string) (runner.Result, error) {
	if dir == r.dir && name == "git" && reflect.DeepEqual(args, r.args) {
		r.seen++
		if r.err != nil {
			return runner.Result{}, r.err
		}
		result, err := r.Runner.RunOpts(ctx, dir, opts, name, args...)
		if err == nil && result.ExitCode == 0 && r.after != nil {
			r.after()
		}
		return result, err
	}
	return r.Runner.RunOpts(ctx, dir, opts, name, args...)
}

type prRouteNegativeGit struct {
	Git
	stage       string
	err         error
	existsCalls int
	seen        bool
}

func (g *prRouteNegativeGit) CommitObjectExists(ctx context.Context, dir, sha string) (bool, error) {
	g.existsCalls++
	if g.stage == "exists" || (g.stage == "exists after fetch" && g.existsCalls == 2) {
		g.seen = true
		return false, g.err
	}
	return g.Git.CommitObjectExists(ctx, dir, sha)
}
func (g *prRouteNegativeGit) MergeTreeWriteTree(ctx context.Context, dir, a, b string) (string, error) {
	if g.stage == "merge tree" {
		g.seen = true
		return "", g.err
	}
	return g.Git.MergeTreeWriteTree(ctx, dir, a, b)
}
func (g *prRouteNegativeGit) ShowTreeFormat(ctx context.Context, dir, sha string) (string, error) {
	if g.stage == "head tree" {
		g.seen = true
		return "", g.err
	}
	return g.Git.ShowTreeFormat(ctx, dir, sha)
}

// Overrides represent only negative hosted reads/metadata or exact wire-shape
// contracts. Unmatched calls retain the existing provider's actual private Git.
func installPRRouteReadOverrides(t *testing.T, gh *wmEngineGH) {
	t.Helper()
	executable := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "gh")
	native := executable + ".native"
	if err := os.Rename(executable, native); err != nil {
		t.Fatal(err)
	}
	body := `#!/bin/sh
set -eu
S="$WB_TEST_STATE"
case "$*" in
 'api --method PUT repos/acme/app/pulls/41/update-branch'*)
  if [ -f "$S/native-CAS-race" ]; then
   git --git-dir="$WB_TEST_REMOTE" update-ref "refs/heads/$(cat "$S/head-ref")" "$WB_TEST_ROUTE_RACE_HEAD"
   rm "$S/native-CAS-race"
   echo "expected head sha does not match current head" >&2
   exit 1
  fi ;;
esac
case "$*" in
 'pr view '*" --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName") slot=landing ;;
 'api repos/acme/app/pulls/41 --include'|'api repos/acme/app/pulls/41') slot=view ;;
 'api repos/acme/app/commits/'*) slot=parents ;;
 'api repos/acme/app --include'|'api repos/acme/app') slot=methods ;;
 'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main') slot=target ;;
 'api --method PUT repos/acme/app/pulls/41/merge'*) slot=merge ;;
 *) slot=none ;;
esac
# This is the genuine post-update parent read, before WB records the advance.
# Included HTTP headers make the existing observer classify all failed attempts.
if [ "$slot" = parents ] && [ -f "$S/transient-update-parents" ] && [ "$2" = "repos/acme/app/commits/$(cat "$S/head")" ]; then
 printf '1' >"$S/transient-parents-consumed"
 printf 'HTTP/1.1 502 Bad Gateway\r\nContent-Type: application/json\r\n\r\n{"message":"Bad Gateway"}\n'
 exit 1
fi
if [ -f "$S/override-$slot" ]; then cat "$S/override-$slot"; exit "$(cat "$S/override-$slot-exit")"; fi
` + strconv.Quote(native) + ` "$@"
case "$*" in
 'api graphql'*)
  if [ -f "$S/transient-after-arm" ]; then printf '20' >"$S/pr-view-fail-count"; fi ;;
 'api --method PUT repos/acme/app/pulls/41/merge'*)
  if [ -f "$S/unmerged-after-native-write" ]; then printf 'false' >"$S/merged"; printf 'OPEN' >"$S/pr-state"; fi ;;
 'pr view '*" --repo acme/app --json state,mergedAt,mergeCommit,headRefOid,baseRefName")
  if [ -f "$S/obstruct-final-receipt" ] && [ "$(cat "$S/merged")" = true ]; then
   mv "$WB_TEST_ROUTE_RECEIPT" "$WB_TEST_ROUTE_HELD_RECEIPT"
   mkdir "$WB_TEST_ROUTE_RECEIPT"
   rm "$S/obstruct-final-receipt"
  fi ;;
esac
`
	if err := testenv.WriteExecutableFile(executable, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}
func prRouteOverride(t *testing.T, gh *wmEngineGH, slot, body string, exit int) {
	t.Helper()
	gh.writeState(t, "override-"+slot, body)
	gh.writeState(t, "override-"+slot+"-exit", fmt.Sprint(exit))
	t.Cleanup(func() {
		_ = os.Remove(filepath.Join(gh.state, "override-"+slot))
		_ = os.Remove(filepath.Join(gh.state, "override-"+slot+"-exit"))
	})
}

type prRouteAfterTreeGit struct {
	Git
	after func()
	used  bool
}

func (g *prRouteAfterTreeGit) ShowTreeFormat(ctx context.Context, dir, sha string) (string, error) {
	tree, err := g.Git.ShowTreeFormat(ctx, dir, sha)
	if err == nil && !g.used {
		g.used = true
		g.after()
	}
	return tree, err
}
