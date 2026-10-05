package orchestrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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

func TestPRRouteProofUsesSuppliedRunnerAndNativeDAG(t *testing.T) {
	t.Parallel()
	f := newPRRouteOwnerFixture(t)
	r := f.receipt
	runEngineGit(t, r.Candidate.Worktree, "fetch", "--no-tags", "origin", "+refs/heads/"+r.Candidate.Branch+":refs/remotes/origin/"+r.Candidate.Branch)
	for _, stage := range []string{"positive", "empty worktree", "object error", "target fetch", "target revision", "target ancestry error", "target ancestry false", "merge tree", "head tree", "wrong tree"} {
		t.Run(stage, func(t *testing.T) {
			sentinel := errors.New("selected " + stage)
			git := &prRouteNegativeGit{Git: defaultGit, err: sentinel}
			run := &prRouteObservationRunner{Runner: defaultRunner, dir: r.Candidate.Worktree, err: sentinel}
			worktree, head, parent := r.Candidate.Worktree, f.merged, f.target
			switch stage {
			case "empty worktree":
				worktree = " "
			case "object error":
				git.stage = "exists"
			case "target fetch":
				run.args = []string{"fetch", "--no-tags", "origin", "+refs/heads/main:refs/remotes/origin/main"}
			case "target revision":
				run.args = []string{"rev-parse", "--verify", "refs/remotes/origin/main^{commit}"}
			case "target ancestry error":
				run.args = []string{"merge-base", f.target, f.target}
			case "target ancestry false":
				parent = r.Candidate.SHA
			case "merge tree":
				git.stage = "merge tree"
			case "head tree":
				git.stage = "head tree"
			case "wrong tree":
				head = f.wrongTree
				runEngineGit(t, worktree, "fetch", "origin", "refs/heads/pr-route-wrong-tree")
			}
			got, err := verifyUpdateBranchMergeProof(t.Context(), git, run, worktree, r.Candidate.Branch, r.Target, r.Repository, r.Candidate.SHA, parent, head)
			if stage == "positive" {
				if err != nil || !got {
					t.Fatalf("native proof=%t %v", got, err)
				}
				return
			}
			if got {
				t.Fatalf("negative proof %s accepted", stage)
			}
			if stage == "object error" {
				if !errors.Is(err, sentinel) || !git.seen {
					t.Fatalf("object failure=%v consumed=%t", err, git.seen)
				}
			} else if err != nil {
				t.Fatalf("definitive negative must be false/nil: %v", err)
			}
			if run.args != nil && run.seen != 1 {
				t.Fatalf("supplied Runner not consumed at %s: %d", stage, run.seen)
			}
			if git.stage != "" && !git.seen {
				t.Fatalf("negative Git observation %s not consumed", git.stage)
			}
		})
	}
	if exists, err := commitExistsLocally(t.Context(), defaultGit, r.Candidate.Worktree, " "); exists || err != nil {
		t.Fatalf("empty object=%t %v", exists, err)
	}
}

func TestPRRouteBranchFetchUsesRealObjectsAndReread(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"native fetch", "reread error"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			f := newPRRouteOwnerFixture(t)
			r := f.receipt
			git := &prRouteNegativeGit{Git: defaultGit, err: errors.New("object reread refused")}
			run := &prRouteObservationRunner{Runner: defaultRunner, dir: r.Candidate.Worktree}
			if exists, err := defaultGit.CommitObjectExists(t.Context(), r.Candidate.Worktree, f.merged); err != nil || exists {
				t.Fatalf("remote-only precondition=%t %v", exists, err)
			}
			if stage == "reread error" {
				git.stage = "exists after fetch"
			}
			proved, err := verifyUpdateBranchMergeProof(t.Context(), git, run, r.Candidate.Worktree, r.Candidate.Branch, r.Target, r.Repository, r.Candidate.SHA, f.target, f.merged)
			switch stage {
			case "native fetch":
				if err != nil || !proved {
					t.Fatalf("native fetched proof=%t %v", proved, err)
				}
			case "reread error":
				if proved || !errors.Is(err, git.err) || !git.seen {
					t.Fatalf("reread=%t %v consumed=%t", proved, err, git.seen)
				}

			}
		})
	}
}

//nolint:paralleltest // Hosted provider installation changes PATH and private WB_TEST_* process inputs.
func TestPRRouteHostedProofFallbackAndMergeMethodPolicies(t *testing.T) {
	f := newPRRouteOwnerFixture(t)
	r := f.receipt
	gh := installWorktreeMergeEngineGH(t, f.f, r.Candidate.SHA, r.Candidate.Branch)
	installPRRouteReadOverrides(t, gh)
	for _, row := range []struct {
		name, json, want string
		bad              bool
	}{{"merge", `{"allow_merge_commit":true,"allow_squash_merge":true,"allow_rebase_merge":true}`, "merge", false}, {"squash", `{"allow_squash_merge":true,"allow_rebase_merge":true}`, "squash", false}, {"rebase", `{"allow_rebase_merge":true}`, "rebase", false}, {"none", `{}`, "no supported", true}, {"decode", `{invalid`, "decode repository", true}} {
		t.Run(row.name, func(t *testing.T) {
			prRouteOverride(t, gh, "methods", row.json, 0)
			got, err := repositoryPullRequestMergeMethod(t.Context(), r.Repository)
			if row.bad {
				if err == nil || !strings.Contains(err.Error(), row.want) {
					t.Fatalf("method=%q %v", got, err)
				}
			} else if err != nil || got != row.want {
				t.Fatalf("method=%q %v", got, err)
			}
		})
	}
	t.Run("repository read", func(t *testing.T) {
		_, err := repositoryPullRequestMergeMethod(t.Context(), "unknown/repository")
		if err == nil || !strings.Contains(err.Error(), "read repository") {
			t.Fatalf("repository read=%v", err)
		}
	})
	runEngineGit(t, f.f.repository.CloneURL, "update-ref", "-d", "refs/heads/"+r.Candidate.Branch)
	for _, stage := range []string{"native hosted tree", "transient hosted tree", "definitive hosted tree"} {
		t.Run(stage, func(t *testing.T) {
			head := f.merged
			if stage == "transient hosted tree" {
				marker := filepath.Join(t.TempDir(), "transient")
				if err := os.WriteFile(marker, []byte("1"), 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("WB_TEST_COMMIT_TREE_TRANSIENT", marker)
			}
			if stage == "definitive hosted tree" {
				head = strings.Repeat("f", 40)
			}
			proved, err := verifyUpdateBranchMergeProof(t.Context(), defaultGit, defaultRunner, r.Candidate.Worktree, r.Candidate.Branch, r.Target, r.Repository, r.Candidate.SHA, f.target, head)
			switch stage {
			case "native hosted tree":
				if !proved || err != nil {
					t.Fatalf("actual remote tree=%t %v", proved, err)
				}
			case "transient hosted tree":
				if proved || err == nil || !IsTransientReadFailure(err) {
					t.Fatalf("transient tree=%t %v", proved, err)
				}
			default:
				if proved || err != nil {
					t.Fatalf("definitive tree=%t %v", proved, err)
				}
			}
		})
	}
	if parents, err := pullRequestCommitParents(t.Context(), r.Repository, f.merged); err != nil || !reflect.DeepEqual(parents, []string{r.Candidate.SHA, f.target}) {
		t.Fatalf("native hosted parents=%v %v", parents, err)
	}
	if log := gh.ghLog(t); !strings.Contains(log, "git/commits/"+f.merged) {
		t.Fatalf("fallback not observed: %s", log)
	}
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

//nolint:paralleltest // The actual hosted-provider script owns process PATH and WB_TEST_* inputs.
func TestPRRouteLiveAndResumeUseDistinctNativeAdoptionPolicies(t *testing.T) {
	f := newPRRouteOwnerFixture(t)
	r := f.receipt
	r.PullRequest = "https://example.test/acme/app/pull/41"
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	gh := installWorktreeMergeEngineGH(t, f.f, r.Candidate.SHA, r.Candidate.Branch)
	installPRRouteReadOverrides(t, gh)
	baseline, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"live success", "resume success", "live dirty sync", "resume dirty sync", "live parents read", "live parents decode", "live wrong parents", "live unproved", "resume view failure", "resume equal head", "resume missing head", "resume parents decode", "resume reversed parents", "resume empty worktree", "resume wrong tree", "live persistence"} {
		t.Run(mode, func(t *testing.T) {
			input := r
			input.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			input.UpdatedAt = time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
			input.Failure = "preserved historical failure"
			input.LocalSync = "old note"
			head := f.merged
			if mode == "resume equal head" {
				head = r.Candidate.SHA
			}
			if mode == "resume wrong tree" {
				head = f.wrongTree
			}
			if mode == "resume reversed parents" {
				tree := strings.TrimSpace(runEngineGit(t, f.f.repository.CloneURL, "rev-parse", r.Candidate.SHA+"^{tree}"))
				head = strings.TrimSpace(runEngineGit(t, f.f.repository.CloneURL, "commit-tree", tree, "-p", f.target, "-p", r.Candidate.SHA, "-m", "actual reversed parent merge"))
			}
			restore := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				for _, cmd := range []struct {
					dir  string
					args []string
				}{{r.Candidate.Worktree, []string{"reset", "--hard", r.Candidate.SHA}}, {f.f.repository.CloneURL, []string{"update-ref", "refs/heads/" + r.Candidate.Branch, f.merged}}} {
					result, e := defaultRunner.RunOpts(ctx, cmd.dir, runner.RunOptions{CaptureCombined: true}, "git", cmd.args...)
					if e != nil || result.ExitCode != 0 {
						t.Errorf("restore native baseline: %+v %v", result, e)
					}
					revision, expected := "HEAD", r.Candidate.SHA
					if cmd.dir == f.f.repository.CloneURL {
						revision, expected = cmd.args[1], cmd.args[2]
					}
					result, e = defaultRunner.RunOpts(ctx, cmd.dir, runner.RunOptions{CaptureCombined: true}, "git", "rev-parse", "--verify", revision+"^{commit}")
					if e != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) != expected {
						t.Errorf("native restored HEAD differs: %+v %v", result, e)
					}
				}
				_ = os.Remove(filepath.Join(r.Candidate.Worktree, "dirty-route.txt"))
				if e := os.WriteFile(r.ReceiptPath, baseline, 0600); e != nil {
					t.Error(e)
				}
			}
			t.Cleanup(restore)
			runEngineGit(t, f.f.repository.CloneURL, "update-ref", "refs/heads/"+r.Candidate.Branch, head)
			if strings.Contains(mode, "dirty sync") {
				if e := os.WriteFile(filepath.Join(r.Candidate.Worktree, "dirty-route.txt"), []byte("private dirt"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			switch mode {
			case "live parents read":
				prRouteOverride(t, gh, "parents", "negative hosted parent read", 1)
			case "live parents decode", "resume parents decode":
				prRouteOverride(t, gh, "parents", "{invalid", 0)
			case "live wrong parents":
				prRouteOverride(t, gh, "parents", `{"parents":[]}`, 0)
			case "resume view failure":
				prRouteOverride(t, gh, "view", "negative hosted view read", 1)
			case "resume missing head":
				prRouteOverride(t, gh, "view", `{"number":41,"head":{"sha":""}}`, 0)
			case "resume empty worktree":
				input.Candidate.Worktree = " "
			case "live unproved":
				head = f.wrongTree
				runEngineGit(t, f.f.repository.CloneURL, "update-ref", "refs/heads/"+r.Candidate.Branch, head)
			}
			before := input
			before.Sources = append([]WorktreeMergeSource(nil), input.Sources...)
			git := Git(defaultGit)
			if mode == "live persistence" {
				// A directory at the real destination refuses the actual Rename after proof;
				// custody and all positive proof observations remain native.
				hold := filepath.Join(t.TempDir(), "held-receipt.json")
				restored := false
				restoreReceipt := func() {
					if restored {
						return
					}
					if e := os.RemoveAll(r.ReceiptPath); e != nil {
						t.Error(e)
						return
					}
					if e := os.Rename(hold, r.ReceiptPath); e != nil {
						t.Error(e)
						return
					}
					restored = true
				}
				t.Cleanup(restoreReceipt)
				git = &prRouteAfterTreeGit{Git: defaultGit, after: func() {
					if e := os.Rename(r.ReceiptPath, hold); e != nil {
						t.Fatal(e)
					}
					if e := os.Mkdir(r.ReceiptPath, 0700); e != nil {
						t.Fatal(e)
					}
				}}
				e := adoptWorktreeMergeUpdateBranchAdvance(t.Context(), git, defaultRunner, &input, r.Candidate.SHA, head)
				restoreReceipt()
				if e == nil || !strings.Contains(e.Error(), "persist update-branch advance") {
					t.Fatalf("native late persistence refusal=%v", e)
				}
				if input.Candidate.SHA != f.merged || len(input.TargetRefreshes) != 1 {
					t.Fatalf("proved partial receipt=%+v", input)
				}
				restore()
				return
			}
			live := strings.HasPrefix(mode, "live")
			adopted := false
			var e error
			if live {
				e = adoptWorktreeMergeUpdateBranchAdvance(t.Context(), git, defaultRunner, &input, r.Candidate.SHA, head)
				adopted = e == nil
			} else {
				adopted, e = adoptServerUpdatedWorktreeMergeHead(t.Context(), git, defaultRunner, &input)
			}
			success := strings.HasSuffix(mode, "success") || strings.HasSuffix(mode, "dirty sync")
			if success {
				if e != nil || !adopted || input.Candidate.SHA != f.merged || input.TargetSHA != f.target || input.PublishedCandidateSHA != f.merged || len(input.TargetRefreshes) != 1 {
					t.Fatalf("%s adoption=%+v %t %v", mode, input, adopted, e)
				}
				if input.Status != r.Status || input.Failure != before.Failure {
					t.Fatalf("owner metadata changed: %+v", input)
				}
				if live {
					if !input.UpdatedAt.After(before.UpdatedAt) {
						t.Fatal("live UpdatedAt not advanced")
					}
					stored, se := readWorktreeMergeReceipt(r.ReceiptPath)
					if se != nil || stored.Candidate.SHA != f.merged || stored.LocalSync != before.LocalSync {
						t.Fatalf("persist before local sync=%+v %v", stored, se)
					}
				} else {
					data, re := os.ReadFile(r.ReceiptPath)
					if re != nil || !bytes.Equal(data, baseline) || input.UpdatedAt != before.UpdatedAt {
						t.Fatalf("resume persisted or changed time: %v", re)
					}
				}
				if strings.HasSuffix(mode, "dirty sync") && !strings.Contains(input.LocalSync, "uncommitted changes") {
					t.Fatalf("dirty note=%q", input.LocalSync)
				}
			} else {
				if live && e == nil {
					t.Fatalf("live negative %s accepted", mode)
				}
				if !live && (e != nil || adopted) {
					t.Fatalf("best-effort resume refusal=%t %v", adopted, e)
				}
				if !reflect.DeepEqual(input, before) {
					t.Fatalf("refused %s changed receipt: %+v", mode, input)
				}
			}
			restore()
		})
	}
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

//nolint:paralleltest // Scripted hosted observations require serial PATH/WB_TEST_* changes; all repositories remain private.
func TestPRRouteSharedEnginePreservesFailureAndServerResultContracts(t *testing.T) {
	f := newPRRouteOwnerFixture(t)
	r := f.receipt
	r.PullRequest = "https://example.test/acme/app/pull/41"
	r.PublishedCandidateSHA = r.Candidate.SHA
	gh := installWorktreeMergeEngineGH(t, f.f, r.Candidate.SHA, r.Candidate.Branch)
	installPRRouteReadOverrides(t, gh)
	if err := persistWorktreeMergeReceipt(r); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "invalid number", "view failure", "foreign head", "native text failure", "method refusal", "pending armed", "failed check detail", "strict fence", "wait read refusal", "update proof transient", "behind read refusal", "update refusal", "merge definitive", "merge head moved refusal", "merge transient", "server not merged", "final persistence", "deferred skipped finding", "GitHub already merged", "unrecorded native CAS head", "landing receipt read", "landing receipt decode"} {
		t.Run(mode, func(t *testing.T) {
			// This matrix shares setup synchronously; its receipt and both remote refs
			// are restored before the next row, with an independent cleanup fallback.
			input := r
			input.Sources = append([]WorktreeMergeSource(nil), r.Sources...)
			restore := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				for ref, head := range map[string]string{"refs/heads/main": r.TargetSHA, "refs/heads/" + r.Candidate.Branch: r.Candidate.SHA} {
					result, e := defaultRunner.RunOpts(ctx, f.f.repository.CloneURL, runner.RunOptions{CaptureCombined: true}, "git", "update-ref", ref, head)
					if e != nil || result.ExitCode != 0 {
						t.Errorf("restore private remote %s: %+v %v", ref, result, e)
					}
					result, e = defaultRunner.RunOpts(ctx, f.f.repository.CloneURL, runner.RunOptions{CaptureCombined: true}, "git", "rev-parse", "--verify", ref+"^{commit}")
					if e != nil || result.ExitCode != 0 || strings.TrimSpace(result.CombinedOutput) != head {
						t.Errorf("restored remote %s differs: %+v %v", ref, result, e)
					}
				}
				if e := os.WriteFile(r.ReceiptPath, before, 0600); e != nil {
					t.Error(e)
				}
			}
			t.Cleanup(restore)
			restore()
			for _, name := range []string{"pr-view-fail-count", "advance-on-checks", "update-conflict", "merge-transient", "merge-transient-after-success", "repeated-transient-reads", "github-merges-on-arm", "unfenced", "transient-after-arm", "unmerged-after-native-write", "obstruct-final-receipt", "override-view", "override-view-exit", "override-methods", "override-methods-exit", "override-merge", "override-merge-exit", "native-CAS-race", "override-landing", "override-landing-exit", "override-target", "override-target-exit", "transient-update-parents", "transient-parents-consumed", "update-branch"} {
				if e := os.Remove(filepath.Join(gh.state, name)); e != nil && !os.IsNotExist(e) {
					t.Fatal(e)
				}
			}
			gh.writeState(t, "pr-state", "OPEN")
			gh.writeState(t, "merged", "false")
			gh.writeState(t, "check-conclusion", "success")
			options := wmEngineLandOptions(f.f, r.ReceiptPath)
			options.WaitSlice = 3 * time.Second
			want := WorktreeMergeConflict
			wantText := ""
			success := false
			switch mode {
			case "success":
				success = true
			case "invalid number":
				input.PullRequest = ""
				wantText = "resolve published pull request number"
			case "view failure":
				prRouteOverride(t, gh, "view", "negative hosted view read", 1)
				wantText = "read published pull request"
			case "foreign head":
				runEngineGit(t, f.f.repository.CloneURL, "update-ref", "refs/heads/"+r.Candidate.Branch, f.merged)
				wantText = "does not match exact candidate"
			case "native text failure":
				// Hold the authentic private checkout bytes; the real git-log
				// process cannot enter the recorded checkout until restored.
				hold := filepath.Join(t.TempDir(), "held-candidate")
				restored := false
				restoreCheckout := func() {
					if restored {
						return
					}
					if e := os.Rename(hold, r.Candidate.Worktree); e != nil {
						t.Error(e)
						return
					}
					restored = true
				}
				t.Cleanup(restoreCheckout)
				if e := os.Rename(r.Candidate.Worktree, hold); e != nil {
					t.Fatal(e)
				}
				wantText = "prepare pull-request merge text"
			case "method refusal":
				prRouteOverride(t, gh, "methods", `{}`, 0)
				wantText = "no supported"
			case "pending armed":
				gh.writeState(t, "check-conclusion", "")
				options.WaitSlice = 200 * time.Millisecond
				want = WorktreeMergeChecksPending
				wantText = "auto-merge is armed"
			case "failed check detail":
				gh.writeState(t, "check-conclusion", "failure")
				want = WorktreeMergeChecksFailed
				wantText = "exact-head checks failed"
			case "strict fence":
				gh.writeState(t, "unfenced", "1")
				want = WorktreeMergeChecksFailed
				wantText = "--allow-unfenced"
			case "wait read refusal":
				// The legacy provider emits stderr without an included HTTP response.
				// CI-wait preserves this as a failed observation, rather than an
				// await error with the observer's exhausted-transient identity.
				gh.writeState(t, "transient-after-arm", "1")
				want = WorktreeMergeChecksFailed
				wantText = "exact-head checks failed: read pull request"
			case "update proof transient":
				// Real remote target T forces a genuine server-side update C->M;
				// only its subsequent exact parent read returns HTTP502.
				runEngineGit(t, f.f.repository.CloneURL, "update-ref", "refs/heads/main", f.target)
				if contains, err := isMergeAncestor(t.Context(), f.f.repository.CloneURL, f.target, r.Candidate.SHA); err != nil || contains {
					t.Fatalf("genuine behind preflight=%t %v", contains, err)
				}
				gh.writeState(t, "transient-update-parents", "1")
				want = WorktreeMergeChecksPending
				wantText = "resume with"
			case "behind read refusal":
				prRouteOverride(t, gh, "target", "HTTP/1.1 404 Not Found\r\nContent-Type: application/json\r\n\r\n{\"message\":\"selected target read refusal\"}\n", 1)
				wantText = "determine whether"
			case "update refusal":
				gh.writeState(t, "advance-on-checks", "1")
				gh.writeState(t, "update-conflict", "1")
				wantText = "resume with"
			case "merge definitive":
				prRouteOverride(t, gh, "merge", "negative definitive merge refusal", 1)
				wantText = "negative definitive"
			case "merge head moved refusal":
				prRouteOverride(t, gh, "merge", `{"message":"Head branch was modified. Review and try the merge again."}`, 1)
				wantText = "the branch moved after its checks were observed"
			case "merge transient":
				gh.writeState(t, "merge-transient", "1")
				want = WorktreeMergeChecksPending
				wantText = "resume with"
			case "server not merged":
				gh.writeState(t, "unmerged-after-native-write", "1")
				wantText = "did not report a merged server result"
			case "final persistence":
				held := filepath.Join(t.TempDir(), "held-receipt")
				t.Setenv("WB_TEST_ROUTE_RECEIPT", r.ReceiptPath)
				t.Setenv("WB_TEST_ROUTE_HELD_RECEIPT", held)
				gh.writeState(t, "obstruct-final-receipt", "1")
				t.Cleanup(func() {
					if _, e := os.Stat(held); e == nil {
						if e := os.RemoveAll(r.ReceiptPath); e != nil {
							t.Error(e)
							return
						}
						if e := os.Rename(held, r.ReceiptPath); e != nil {
							t.Error(e)
						}
					}
				})
				wantText = ""
			case "deferred skipped finding":
				input.ValidationDeferral = &WorktreeMergeValidationDeferral{}
				gh.writeState(t, "check-conclusion", "skipped")
				success = true
			case "GitHub already merged":
				gh.writeState(t, "github-merges-on-arm", "1")
				success = true
			case "unrecorded native CAS head":
				runEngineGit(t, f.f.repository.CloneURL, "update-ref", "refs/heads/main", f.target)
				// The recorded candidate C does not yet contain T; raced M is
				// an actual ordinary merge that does contain T, without WB's hook.
				if contains, e := isMergeAncestor(t.Context(), f.f.repository.CloneURL, f.target, f.merged); e != nil || !contains {
					t.Fatalf("native raced merge preflight=%t %v", contains, e)
				}
				t.Setenv("WB_TEST_ROUTE_RACE_HEAD", f.merged)
				gh.writeState(t, "native-CAS-race", "1")
				wantText = "without being recorded on the receipt"
			case "landing receipt read":
				prRouteOverride(t, gh, "landing", "negative final hosted receipt read", 1)
				wantText = "read pull-request landing receipt"
			case "landing receipt decode":
				prRouteOverride(t, gh, "landing", "{invalid", 0)
				wantText = "decode pull-request landing receipt"
			}
			got, landing, e := landWorktreeMergePullRequest(t.Context(), input, options)
			if success {
				if e != nil || landing == "" {
					t.Fatalf("%s result=%+v landing=%q %v", mode, got, landing, e)
				}
				actual := strings.TrimSpace(runEngineGit(t, f.f.repository.CloneURL, "rev-parse", "refs/heads/main"))
				if landing != actual {
					t.Fatalf("server result %s != real remote %s", landing, actual)
				}
				if mode == "deferred skipped finding" && (len(got.Findings) != 1 || got.Findings[0].Code != WorktreeMergeFindingDeferredValidationCheckSkipped) {
					t.Fatalf("deferred finding=%+v", got.Findings)
				}
				if mode == "GitHub already merged" && got.MergedBy == "" {
					t.Fatal("server merger evidence absent")
				}
			} else {
				if e == nil || landing != "" {
					t.Fatalf("%s unexpectedly accepted: %+v %q %v", mode, got, landing, e)
				}
				if mode != "final persistence" && (got.Status != want || !strings.Contains(e.Error(), wantText)) {
					t.Fatalf("%s status=%s error=%v want=%s/%q", mode, got.Status, e, want, wantText)
				}
			}
			if mode == "update proof transient" {
				if !IsTransientReadFailure(e) || !strings.Contains(e.Error(), "read commit parents") || !got.AutoMergeArmed || got.Candidate.SHA != r.Candidate.SHA || len(got.TargetRefreshes) != len(r.TargetRefreshes) {
					t.Fatalf("retryable proof checkpoint=%+v error=%v", got, e)
				}
				if _, err := os.Stat(filepath.Join(gh.state, "transient-parents-consumed")); err != nil {
					t.Fatalf("exact post-update parent fault not consumed: %v", err)
				}
				if strings.TrimSpace(mustReadEngineFile(t, filepath.Join(gh.state, "update-branch"))) != "updated" {
					t.Fatal("native update did not precede the parent-read fault")
				}
			}
			if mode == "final persistence" {
				held := os.Getenv("WB_TEST_ROUTE_HELD_RECEIPT")
				if _, e := os.Stat(held); e != nil {
					t.Fatalf("final fault not consumed: %v", e)
				}
				if e := os.RemoveAll(r.ReceiptPath); e != nil {
					t.Fatal(e)
				}
				if e := os.Rename(held, r.ReceiptPath); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "native text failure" { // Its subtest cleanup restores the authentic checkout before parent restoration.
				return
			}
			restore()
		})
	}
}
