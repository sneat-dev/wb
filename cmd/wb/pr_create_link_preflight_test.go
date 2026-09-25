package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// prCreateLinkPreflightFixture builds a real canonical repository with a
// working origin remote and a scripted `gh` that answers exactly the calls
// `orchestrate.CreatePullRequest` makes for a plain, auto-mergeable
// mechanical change, so `wb pr create --auto-merge` can complete for real
// through the actual CLI command, reaching the arming step that runs
// LinkPreflight (pr_create.go:205) immediately before it. It mirrors
// internal/orchestrate's own createFixture/installGH test helpers; that
// fixture's fields and script are package-private there, so this is a
// deliberate, minimal port rather than a shared import.
type prCreateLinkPreflightFixture struct {
	root, projects, canonical, remote, state, log string
}

func newPRCreateLinkPreflightFixture(t *testing.T) *prCreateLinkPreflightFixture {
	t.Helper()
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	projects := filepath.Join(root, "projects")
	canonical := filepath.Join(projects, "acme", "app")
	writeCLIWorktreeFile(t, filepath.Join(seed, "go.mod"), "module example.test/app\n\ngo 1.24\n")
	runCLIWorktreeGit(t, seed, "init", "-b", "main")
	runCLIWorktreeGit(t, seed, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, seed, "config", "user.email", "wb@example.test")
	runCLIWorktreeGit(t, seed, "add", "-A")
	runCLIWorktreeGit(t, seed, "commit", "-m", "initial")
	runCLIWorktreeGit(t, root, "clone", "--bare", seed, remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	runCLIWorktreeGit(t, remote, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, remote, "config", "user.email", "wb@example.test")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runCLIWorktreeGit(t, root, "clone", remote, canonical)
	runCLIWorktreeGit(t, canonical, "config", "user.name", "WB Test")
	runCLIWorktreeGit(t, canonical, "config", "user.email", "wb@example.test")

	fixture := &prCreateLinkPreflightFixture{
		root: root, projects: projects, canonical: canonical, remote: remote,
		state: filepath.Join(root, "state"), log: filepath.Join(root, "gh.log"),
	}
	if err := os.MkdirAll(fixture.state, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture.writeState(t, "files", `[{"filename":"go.sum","status":"modified","patch":"@@ -1,1 +1,1 @@\n-old h1:x=\n+new h1:y=\n"}]`)
	fixture.writeState(t, "commits", `[{"sha":"aaaaaaa","commit":{"message":"change go.sum"}}]`)
	fixture.writeState(t, "next-number", "9")
	fixture.writeState(t, "pr-state", "open")
	fixture.writeState(t, "merged", "false")
	fixture.writeState(t, "check-conclusion", "success")
	fixture.installGH(t)
	return fixture
}

func (fixture *prCreateLinkPreflightFixture) writeState(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.state, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// createWorktree creates a real WB worktree on a fresh branch off main and
// writes one mechanical (go.sum) commit into it, mirroring
// internal/orchestrate's createFixture.createWorktree so the arming
// journey's mechanical-diff classification takes the same, already-proven
// path.
func (fixture *prCreateLinkPreflightFixture) createWorktree(t *testing.T, task, branch string) string {
	t.Helper()
	created, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: task,
		Branch: branch, BranchChosen: true, Base: "main",
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %#v", created)
	}
	worktree := created[0].WorktreeDir
	writeCLIWorktreeFile(t, filepath.Join(worktree, "go.sum"), "change go.sum\n")
	runCLIWorktreeGit(t, worktree, "add", "-A")
	runCLIWorktreeGit(t, worktree, "commit", "-m", "change go.sum")
	t.Setenv("WB_CREATE_BRANCH", branch)
	return worktree
}

func (fixture *prCreateLinkPreflightFixture) installGH(t *testing.T) {
	t.Helper()
	bin := filepath.Join(fixture.root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":0,"workflow_runs":[]}'
  exit 0
fi
printf '%s\n' "$*" >>"$WB_CREATE_LOG"
S="$WB_CREATE_STATE"
number=$(cat "$S/next-number")
state=$(cat "$S/pr-state")
merged=$(cat "$S/merged")
draft=false
head=$(git --git-dir="$WB_CREATE_REMOTE" rev-parse "refs/heads/$WB_CREATE_BRANCH" 2>/dev/null)
case "$*" in
  'pr list --repo acme/app --head '*' --state open --json url,baseRefName,body --jq '*)
    if [ -f "$S/existing-pr" ]; then cat "$S/existing-pr"; fi ;;
  'pr create --repo acme/app '*)
    printf 'https://github.com/acme/app/pull/%s\n' "$number" ;;
  'pr edit https://github.com/acme/app/pull/'*' --repo acme/app --body '*)
    printf '%s\n' "$*" >>"$S/pr-edit-calls" ;;
  'api repos/acme/app/pulls/'*'/files?per_page=100 --include'|'api repos/acme/app/pulls/'*'/files?per_page=100')
    cat "$S/files" ;;
  'api repos/acme/app/pulls/'*'/commits?per_page=100 --include'|'api repos/acme/app/pulls/'*'/commits?per_page=100')
    cat "$S/commits" ;;
  'api repos/acme/app/pulls/'*' --include'|'api repos/acme/app/pulls/'*)
    merge_sha=""
    if [ "$merged" = true ]; then merge_sha=$(git --git-dir="$WB_CREATE_REMOTE" rev-parse refs/heads/main); fi
    printf '{"number":%s,"node_id":"PR_kwDOtest%s","state":"%s","draft":%s,"locked":false,"title":"feat: the change","body":"","merged":%s,"merge_commit_sha":"%s","mergeable":true,"mergeable_state":"clean","head":{"ref":"%s","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}\n' \
      "$number" "$number" "$state" "$draft" "$merged" "$merge_sha" "$WB_CREATE_BRANCH" "$head" ;;
  'api repos/acme/app/branches/main/protection/required_status_checks --include'|'api repos/acme/app/branches/main/protection/required_status_checks')
    printf '{"strict":true,"contexts":[],"checks":[{"context":"CI","app_id":42}]}\n' ;;
  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main')
    printf '{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}\n' ;;
  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100')
    printf '[]' ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100')
    printf '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"%s","app":{"id":42}}]}\n' "$(cat "$S/check-conclusion")" ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main')
    printf '{"object":{"sha":"%s"}}\n' "$(git --git-dir="$WB_CREATE_REMOTE" rev-parse refs/heads/main)" ;;
  'api repos/acme/app/compare/'*)
    pair="${2#*compare/}"
    left="${pair%%...*}"
    right="${pair#*...}"
    if git --git-dir="$WB_CREATE_REMOTE" merge-base --is-ancestor "$left" "$right" 2>/dev/null; then
      status=ahead
      if [ "$(git --git-dir="$WB_CREATE_REMOTE" rev-parse "$left")" = "$(git --git-dir="$WB_CREATE_REMOTE" rev-parse "$right")" ]; then status=identical; fi
    else
      status=behind
    fi
    printf '{"status":"%s","base_commit":{"sha":"%s"},"merge_base_commit":{"sha":"%s"}}\n' \
      "$status" "$(git --git-dir="$WB_CREATE_REMOTE" rev-parse "$left")" "$(git --git-dir="$WB_CREATE_REMOTE" merge-base "$left" "$right" 2>/dev/null || git --git-dir="$WB_CREATE_REMOTE" rev-parse "$left")" ;;
  'api graphql'*)
    case "$*" in
      *enablePullRequestAutoMerge*)
        printf 'armed' >"$S/auto-merge"
        printf '{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"autoMergeRequest":{"enabledAt":"2026-09-18T00:00:00Z"}}}}}\n' ;;
      *) echo "unexpected graphql: $*" >&2; exit 2 ;;
    esac ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_CREATE_STATE", fixture.state)
	t.Setenv("WB_CREATE_LOG", fixture.log)
	t.Setenv("WB_CREATE_REMOTE", fixture.remote)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (fixture *prCreateLinkPreflightFixture) ghLog(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(fixture.log)
	if err != nil {
		return ""
	}
	return string(raw)
}

// TestPRCreateAutoMergeArmsAndRunsTheLiveLinkPreflight proves pr_create.go:205
// (the LinkPreflight closure threading refuseLinkedRepositoryWorktrees(inv,
// repository) into orchestrate.CreatePullRequest) is reached: no existing
// `wb pr create` test completes a real, successful arming journey, so the
// closure was never invoked. This drives the real CLI command
// (newPRCreateCmd) against a real worktree and a scripted `gh` answering
// exactly the calls a mechanical, auto-mergeable change needs, and asserts
// auto-merge was actually armed on GitHub's (faked) API - the same outcome
// internal/orchestrate's own TestCreateAutoMergeArmsOnAMechanicalDiff proves
// at the package level, now proven reachable from the command tree.
func TestPRCreateAutoMergeArmsAndRunsTheLiveLinkPreflight(t *testing.T) {
	fixture := newPRCreateLinkPreflightFixture(t)
	worktree := fixture.createWorktree(t, "pr-create-automerge", "feature/pr-create-automerge")

	command := newPRCreateCmd(&invocation{projectsRoot: fixture.projects})
	command.SilenceUsage = true
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	command.SetArgs([]string{"--auto-merge", "--format", "json", worktree})
	if err := command.Execute(); err != nil {
		t.Fatalf("pr create --auto-merge: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(fixture.ghLog(t), "enablePullRequestAutoMerge") {
		t.Fatalf("auto-merge was never armed; gh log:\n%s", fixture.ghLog(t))
	}
}
