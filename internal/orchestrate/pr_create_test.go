package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// createFixture is a real repository plus a scripted GitHub, built for `wb pr
// create`: a canonical clone on main, its bare remote, and a fake `gh` that
// answers `pr list`, `pr create`, the pull-request read/files/commits reads,
// and the auto-merge GraphQL mutation from files under state/.
type createFixture struct {
	root, projects, canonical, remote, state, log string
	repository                                    string
}

func newCreateFixture(t *testing.T) *createFixture {
	t.Helper()
	root := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", filepath.Join(root, "projects"))
	seed := filepath.Join(root, "seed")
	remote := filepath.Join(root, "remote.git")
	projects := filepath.Join(root, "projects")
	canonical := filepath.Join(projects, "acme", "app")
	writeEngineFile(t, filepath.Join(seed, "go.mod"), "module example.test/app\n\ngo 1.24\n")
	runEngineGit(t, seed, "init", "-b", "main")
	runEngineGit(t, seed, "config", "user.name", "WB Test")
	runEngineGit(t, seed, "config", "user.email", "wb@example.test")
	runEngineGit(t, seed, "add", "-A")
	runEngineGit(t, seed, "commit", "-m", "initial")
	runEngineGit(t, root, "clone", "--bare", seed, remote)
	runEngineGit(t, remote, "config", "user.name", "WB Test")
	runEngineGit(t, remote, "config", "user.email", "wb@example.test")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runEngineGit(t, root, "clone", remote, canonical)
	runEngineGit(t, canonical, "config", "user.name", "WB Test")
	runEngineGit(t, canonical, "config", "user.email", "wb@example.test")
	// A second base branch, so a --base override has somewhere real to fetch.
	runEngineGit(t, canonical, "checkout", "-b", "develop")
	runEngineGit(t, canonical, "push", "-u", "origin", "develop")
	runEngineGit(t, canonical, "checkout", "main")

	fixture := &createFixture{
		root: root, projects: projects, canonical: canonical, remote: remote,
		state: filepath.Join(root, "state"), log: filepath.Join(root, "gh.log"),
		repository: "acme/app",
	}
	if err := os.MkdirAll(fixture.state, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture.writeState(t, "files", `[{"filename":"main.go","status":"modified","patch":"@@ -1,2 +1,3 @@\n package app\n+// changed\n"}]`)
	fixture.writeState(t, "commits", `[{"sha":"aaaaaaa","commit":{"message":"change main.go"}}]`)
	fixture.writeState(t, "next-number", "9")
	fixture.writeState(t, "pr-state", "open")
	fixture.writeState(t, "merged", "false")
	fixture.writeState(t, "check-conclusion", "success")
	fixture.installGH(t)
	return fixture
}

func (fixture *createFixture) writeState(t *testing.T, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.state, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (fixture *createFixture) ghLog(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(fixture.log)
	if err != nil {
		return ""
	}
	return string(raw)
}

// createWorktree creates a real WB worktree on a fresh branch off base
// (worktrees.Create, exactly what `wb worktree create` runs), writes one
// commit per named file into it, and returns its directory. Zero files
// leaves the branch exactly at base, for the nothing-to-push refusal.
func (fixture *createFixture) createWorktree(t *testing.T, task, branch, base string, files ...string) string {
	t.Helper()
	created, err := worktrees.Create(context.Background(), []string{fixture.repository}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: task,
		Branch: branch, BranchChosen: true, Base: base,
		WorkLog: worktrees.WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %#v", created)
	}
	worktree := created[0].WorktreeDir
	for _, file := range files {
		writeEngineFile(t, filepath.Join(worktree, file), "change "+file+"\n")
		runEngineGit(t, worktree, "add", "-A")
		runEngineGit(t, worktree, "commit", "-m", "change "+file)
	}
	// The fake gh reads the branch's real remote tip once pushed, rather than
	// a state file this helper would otherwise have to keep in sync by hand.
	t.Setenv("WB_CREATE_BRANCH", branch)
	return worktree
}

func (fixture *createFixture) installGH(t *testing.T) {
	t.Helper()
	bin := filepath.Join(fixture.root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
printf '%s\n' "$*" >>"$WB_CREATE_LOG"
S="$WB_CREATE_STATE"
number=$(cat "$S/next-number")
state=$(cat "$S/pr-state")
merged=$(cat "$S/merged")
head=$(git --git-dir="$WB_CREATE_REMOTE" rev-parse "refs/heads/$WB_CREATE_BRANCH" 2>/dev/null)
case "$*" in
  'pr list --head '*' --state open --json url --jq .[0].url')
    if [ -f "$S/existing-pr" ]; then cat "$S/existing-pr"; fi ;;
  'pr create '*)
    printf 'https://github.com/acme/app/pull/%s\n' "$number" ;;
  'api repos/acme/app/pulls/'*'/files?per_page=100 --include'|'api repos/acme/app/pulls/'*'/files?per_page=100')
    cat "$S/files" ;;
  'api repos/acme/app/pulls/'*'/commits?per_page=100 --include'|'api repos/acme/app/pulls/'*'/commits?per_page=100')
    cat "$S/commits" ;;
  'api repos/acme/app/pulls/'*' --include'|'api repos/acme/app/pulls/'*)
    merge_sha=""
    if [ "$merged" = true ]; then merge_sha=$(git --git-dir="$WB_CREATE_REMOTE" rev-parse refs/heads/main); fi
    printf '{"number":%s,"node_id":"PR_kwDOtest%s","state":"%s","draft":false,"locked":false,"title":"feat: the change","body":"","merged":%s,"merge_commit_sha":"%s","mergeable":true,"mergeable_state":"clean","head":{"ref":"%s","sha":"%s","repo":{"full_name":"acme/app"}},"base":{"ref":"main","sha":""}}\n' \
      "$number" "$number" "$state" "$merged" "$merge_sha" "$WB_CREATE_BRANCH" "$head" ;;
  'api repos/acme/app/branches/main/protection/required_status_checks --include'|'api repos/acme/app/branches/main/protection/required_status_checks'|'api repos/acme/app/branches/develop/protection/required_status_checks --include'|'api repos/acme/app/branches/develop/protection/required_status_checks')
    if [ -f "$S/unfenced" ]; then strict=false; else strict=true; fi
    printf '{"strict":%s,"contexts":[],"checks":[{"context":"CI","app_id":42}]}\n' "$strict" ;;
  'api repos/acme/app/branches/main --include'|'api repos/acme/app/branches/main'|'api repos/acme/app/branches/develop --include'|'api repos/acme/app/branches/develop')
    printf '{"protected":true,"protection":{"required_status_checks":{"checks":[{"context":"CI","app_id":42}]}}}\n' ;;
  'api repos/acme/app/rules/branches/main?per_page=100 --include'|'api repos/acme/app/rules/branches/main?per_page=100'|'api repos/acme/app/rules/branches/develop?per_page=100 --include'|'api repos/acme/app/rules/branches/develop?per_page=100')
    printf '[]' ;;
  *'/check-runs?per_page=100 --include'|*'/check-runs?per_page=100')
    printf '{"total_count":1,"check_runs":[{"name":"CI","status":"completed","conclusion":"%s","app":{"id":42}}]}\n' "$(cat "$S/check-conclusion")" ;;
  *'/status?per_page=100 --include'|*'/status?per_page=100') printf '%s\n' '{"total_count":0,"statuses":[]}' ;;
  'api repos/acme/app/git/ref/heads/main --include'|'api repos/acme/app/git/ref/heads/main')
    printf '{"object":{"sha":"%s"}}\n' "$(git --git-dir="$WB_CREATE_REMOTE" rev-parse refs/heads/main)" ;;
  'api repos/acme/app/git/ref/heads/'*)
    ref="${2#*git/ref/heads/}"; ref="${ref% --include}"
    if git --git-dir="$WB_CREATE_REMOTE" show-ref --verify --quiet "refs/heads/$ref"; then
      printf '{"object":{"sha":"%s"}}\n' "$(git --git-dir="$WB_CREATE_REMOTE" rev-parse "refs/heads/$ref")"
    else
      printf '{"message":"Not Found"}\n'; exit 1
    fi ;;
  'api --method DELETE repos/acme/app/git/refs/heads/'*)
    ref="${4#*git/refs/heads/}"
    git --git-dir="$WB_CREATE_REMOTE" update-ref -d "refs/heads/$ref" ;;
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
        printf '%s' "$*" >"$S/auto-merge-args"
        if [ -f "$S/auto-merge-unavailable" ]; then
          printf '{"errors":[{"message":"Pull request Auto merge is not allowed for this repository"}]}\n' >&2
          exit 1
        fi
        printf 'armed' >"$S/auto-merge"
        printf '{"data":{"enablePullRequestAutoMerge":{"pullRequest":{"autoMergeRequest":{"enabledAt":"2026-09-18T00:00:00Z"}}}}}\n' ;;
      *) echo "unexpected graphql: $*" >&2; exit 2 ;;
    esac ;;
  'api --method PUT repos/acme/app/pulls/'*'/merge'*)
    if [ "$merged" = true ]; then
      printf '{"message":"Pull Request is not mergeable"}\n'; exit 1
    fi
    requested=""
    for arg in "$@"; do
      case "$arg" in sha=*) requested="${arg#sha=}" ;; esac
    done
    if [ "$requested" != "$head" ]; then
      printf '{"message":"Head branch was modified. Review and try the merge again."}\n'; exit 1
    fi
    git --git-dir="$WB_CREATE_REMOTE" update-ref refs/heads/main "$requested"
    printf 'true' >"$S/merged"
    printf 'closed' >"$S/pr-state"
    printf '{"sha":"%s","merged":true,"message":"Pull Request successfully merged"}\n' "$requested" ;;
  *) echo "unexpected gh command: $*" >&2; exit 2 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(withEmptyActionsRuns(script)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_CREATE_STATE", fixture.state)
	t.Setenv("WB_CREATE_LOG", fixture.log)
	t.Setenv("WB_CREATE_REMOTE", fixture.remote)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCreateRefusesTheCanonicalClone(t *testing.T) {
	fixture := newCreateFixture(t)
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: fixture.canonical, ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalCanonicalClone {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.ExitCode() != 2 {
		t.Fatalf("exit code = %d, want 2", result.ExitCode())
	}
}

func TestCreateRefusesADirtyWorktreeAndListsPaths(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "dirty-task", "feature/dirty", "main", "main.go")
	writeEngineFile(t, filepath.Join(worktree, "untracked.txt"), "oops\n")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalDirtyWorktree {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !strings.Contains(result.Reason, "untracked.txt") {
		t.Fatalf("reason %q does not name the dirty path", result.Reason)
	}
}

func TestCreateRefusesABranchWithNoCommitAheadOfBase(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "empty-task", "feature/empty", "main")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalNothingToPush {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if strings.Contains(fixture.ghLog(t), "pr create") {
		t.Fatal("a refused invocation must never reach GitHub")
	}
}

func TestCreateOpensAPullRequestAndPrintsTheNextCommand(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "open-task", "feature/open", "main", "main.go")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateSuccess {
		t.Fatalf("outcome=%s reason=%s", result.Outcome, result.Reason)
	}
	if result.Repository != "acme/app" || result.PullRequest != 9 {
		t.Fatalf("repository/number = %s#%d, want acme/app#9", result.Repository, result.PullRequest)
	}
	if result.NextCommand != "wb pr land acme/app#9" {
		t.Fatalf("next command = %q", result.NextCommand)
	}
	if strings.Count(fixture.ghLog(t), "pr create") != 1 {
		t.Fatalf("gh log = %q, want exactly one pr create", fixture.ghLog(t))
	}
	pushed := runEngineGit(t, fixture.remote, "log", "--format=%s", "refs/heads/feature/open")
	if !strings.Contains(pushed, "change main.go") {
		t.Fatalf("branch was not pushed through the normal path: %q", pushed)
	}
}

func TestCreateAdoptsAnExistingOpenPullRequestWithoutCreatingASecondOne(t *testing.T) {
	fixture := newCreateFixture(t)
	fixture.writeState(t, "existing-pr", "https://github.com/acme/app/pull/9\n")
	worktree := fixture.createWorktree(t, "adopt-task", "feature/adopt", "main", "main.go")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateSuccess || !result.Adopted {
		t.Fatalf("outcome=%s adopted=%v reason=%s", result.Outcome, result.Adopted, result.Reason)
	}
	if strings.Contains(fixture.ghLog(t), "pr create") {
		t.Fatalf("adoption must never call gh pr create: %q", fixture.ghLog(t))
	}
}

func TestCreateTitleFromASingleCommit(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "single-task", "feature/single", "main", "main.go")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Title != "change main.go" {
		t.Fatalf("title = %q, want the single commit's own subject", result.Title)
	}
}

func TestCreateTitleForASingleWipCommitIsNotWip(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "wip-task", "feature/wip", "main")
	writeEngineFile(t, filepath.Join(worktree, "main.go"), "package app\n")
	runEngineGit(t, worktree, "add", "-A")
	runEngineGit(t, worktree, "commit", "-m", "wip: quick fix")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(strings.ToLower(result.Title), "wip") {
		t.Fatalf("title = %q, must never be a wip subject", result.Title)
	}
}

func TestCreateTitleForSeveralCommitsIsNotAWipSubject(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "multi-task", "feature/multi", "main")
	writeEngineFile(t, filepath.Join(worktree, "a.go"), "package app\n")
	runEngineGit(t, worktree, "add", "-A")
	runEngineGit(t, worktree, "commit", "-m", "wip: exploring")
	writeEngineFile(t, filepath.Join(worktree, "b.go"), "package app\n\n// b\n")
	runEngineGit(t, worktree, "add", "-A")
	runEngineGit(t, worktree, "commit", "-m", "feat: the real change")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(strings.ToLower(result.Title), "wip") {
		t.Fatalf("title = %q, must never be a wip subject", result.Title)
	}
	if !strings.Contains(result.Title, "feat: the real change") {
		t.Fatalf("title = %q, want it to carry the real change", result.Title)
	}
}

func TestCreateHonoursTitleBodyFileDraftAndBase(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "override-task", "feature/override", "develop", "main.go")
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("a custom body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		Title: "custom title", BodyFile: bodyFile, Draft: true, Base: "develop",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateSuccess {
		t.Fatalf("outcome=%s reason=%s", result.Outcome, result.Reason)
	}
	if result.Title != "custom title" {
		t.Fatalf("title = %q", result.Title)
	}
	if result.BaseRef != "develop" {
		t.Fatalf("base = %q", result.BaseRef)
	}
	log := fixture.ghLog(t)
	if !strings.Contains(log, "custom title") || !strings.Contains(log, "a custom body") {
		t.Fatalf("gh log = %q, missing the overridden title/body", log)
	}
	if !strings.Contains(log, "--draft") {
		t.Fatalf("gh log = %q, missing --draft", log)
	}
}

func TestCreateBodyFlagIsLiteralAndWinsOverCommitBody(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "body-task", "feature/body", "main", "main.go")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects, Body: "literal body text",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateSuccess {
		t.Fatalf("outcome=%s reason=%s", result.Outcome, result.Reason)
	}
	if !strings.Contains(fixture.ghLog(t), "literal body text") {
		t.Fatalf("gh log = %q, missing the literal --body", fixture.ghLog(t))
	}
}

func TestCreateBindsTheTaskToThePullRequestDurably(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "bind-task", "feature/bind", "main", "main.go")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Task != "bind-task" || result.ClaimID == "" {
		t.Fatalf("task=%q claimID=%q, want the binding to resolve the active claim", result.Task, result.ClaimID)
	}
	if reason, found := result.Evidence["claim_binding_error"]; found {
		t.Fatalf("claim binding failed: %s", reason)
	}
	home, err := wbhome.Root(fixture.projects)
	if err != nil {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(home, "worklogs", "*", "runs", "*", "claims", "*.pull_request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("pull-request binding sidecars = %v, want exactly one", matches)
	}
	contents, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "acme/app") || !strings.Contains(string(contents), "pull/9") {
		t.Fatalf("binding sidecar = %s", contents)
	}
}

func TestCreateEnvelopeJSONFieldsAreStable(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "json-task", "feature/json", "main", "main.go")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || result.Verb != "pr create" {
		t.Fatalf("envelope identity = %#v", result)
	}
	if result.ExitCode() != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode())
	}
}

func TestCreateAutoMergeRefusesANonMechanicalChangeWithoutApproval(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "unapproved-task", "feature/unapproved", "main", "main.go")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects, AutoMerge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalUnapprovedPatch {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if result.AutoMergeArmed {
		t.Fatal("auto-merge must not be armed without approval")
	}
	if result.PullRequest == 0 {
		t.Fatal("the pull request must still exist even though auto-merge was refused")
	}
}

func TestCreateAutoMergeArmsOnAMechanicalDiff(t *testing.T) {
	fixture := newCreateFixture(t)
	fixture.writeState(t, "files", `[{"filename":"go.sum","status":"modified","patch":"@@ -1,1 +1,1 @@\n-old h1:x=\n+new h1:y=\n"}]`)
	worktree := fixture.createWorktree(t, "mechanical-task", "feature/mechanical", "main", "go.sum")
	linkPreflightCalled := false
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects, AutoMerge: true,
		LinkPreflight: func(string) error { linkPreflightCalled = true; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateSuccess || !result.AutoMergeArmed {
		t.Fatalf("outcome=%s armed=%v reason=%s", result.Outcome, result.AutoMergeArmed, result.Reason)
	}
	if !linkPreflightCalled {
		t.Fatal("the live-link preflight must run before arming")
	}
	if result.NextCommand != "wb wait pr acme/app#9 --until closed" {
		t.Fatalf("next command = %q", result.NextCommand)
	}
	if !strings.Contains(fixture.ghLog(t), "enablePullRequestAutoMerge") {
		t.Fatal("auto-merge was never armed")
	}
}

func TestCreateAutoMergeArmsWithApprovedByOnANonMechanicalDiff(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "approved-task", "feature/approved", "main", "main.go")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects, AutoMerge: true, ApprovedBy: "review.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateSuccess || !result.AutoMergeArmed {
		t.Fatalf("outcome=%s armed=%v reason=%s", result.Outcome, result.AutoMergeArmed, result.Reason)
	}
	args, readErr := os.ReadFile(filepath.Join(fixture.state, "auto-merge-args"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(args), "head=deadbeef") && !strings.Contains(string(args), "head="+result.HeadSHA) {
		t.Fatalf("graphql args = %s, want the observed head pinned", args)
	}
	if !strings.Contains(string(args), "subject=feat: the change (#9)") {
		t.Fatalf("graphql args = %s, want the pull request's own title as the commit subject", args)
	}
}

func TestCreateAutoMergeIsNotArmedOnAnUnfencedTarget(t *testing.T) {
	fixture := newCreateFixture(t)
	fixture.writeState(t, "unfenced", "1")
	worktree := fixture.createWorktree(t, "unfenced-task", "feature/unfenced", "main", "main.go")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects, AutoMerge: true, ApprovedBy: "review.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateFindings {
		t.Fatalf("outcome=%s reason=%s", result.Outcome, result.Reason)
	}
	if result.AutoMergeArmed {
		t.Fatal("must not arm on an unfenced target without --allow-unfenced")
	}
	if result.AutoMergeReason == "" {
		t.Fatal("must name the guard it would have bypassed")
	}
	if result.PullRequest == 0 {
		t.Fatal("the pull request must still exist")
	}
	if _, statErr := os.Stat(filepath.Join(fixture.state, "auto-merge-args")); statErr == nil {
		t.Fatal("auto-merge must never have been asked for")
	}
}

func TestCreateCommitStagedCommitsOnlyTheIndex(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "commit-staged-task", "feature/commit-staged", "main")
	writeEngineFile(t, filepath.Join(worktree, "staged.go"), "package app\n")
	runEngineGit(t, worktree, "add", "staged.go")
	writeEngineFile(t, filepath.Join(worktree, "unstaged.go"), "package app\n// untouched\n")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		CommitStaged: true, Message: "feat: staged only",
	})
	if err != nil {
		t.Fatal(err)
	}
	// unstaged.go is untracked and never touched by --commit-staged, so the
	// worktree is still dirty afterward and the ordinary refusal fires —
	// which is itself the proof that only the index was committed.
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalDirtyWorktree {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !strings.Contains(result.Reason, "unstaged.go") {
		t.Fatalf("reason = %q, want it to name the untouched file", result.Reason)
	}
	if len(result.CommittedPaths) != 1 || result.CommittedPaths[0] != "staged.go" {
		t.Fatalf("committed paths = %v, want exactly staged.go", result.CommittedPaths)
	}
	subject := strings.TrimSpace(runEngineGit(t, worktree, "log", "-1", "--format=%s"))
	if subject != "feat: staged only" {
		t.Fatalf("last commit subject = %q", subject)
	}
}

func TestCreateCommitAllCommitsTheUntrackedFileAndListsIt(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "commit-all-task", "feature/commit-all", "main")
	writeEngineFile(t, filepath.Join(worktree, "new.go"), "package app\n")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		CommitAll: true, Message: "feat: add new file",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateSuccess {
		t.Fatalf("outcome=%s reason=%s", result.Outcome, result.Reason)
	}
	if len(result.CommittedPaths) != 1 || result.CommittedPaths[0] != "new.go" {
		t.Fatalf("committed paths = %v, want exactly new.go", result.CommittedPaths)
	}
}

func TestCreateCommitAllRefusesASecretLikePath(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "secret-task", "feature/secret", "main")
	writeEngineFile(t, filepath.Join(worktree, ".env"), "SECRET=1\n")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		CommitAll: true, Message: "feat: oops",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalSecretPath {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !strings.Contains(result.Reason, ".env") {
		t.Fatalf("reason = %q, want it to name .env", result.Reason)
	}
	status := runEngineGit(t, worktree, "status", "--porcelain")
	if !strings.Contains(status, "?? .env") {
		t.Fatalf("status = %q, .env must remain unstaged", status)
	}
}

func TestCreateCommitAllStopsBeforeAnyPushWhenTheHookFails(t *testing.T) {
	fixture := newCreateFixture(t)
	hooksDir := filepath.Join(fixture.canonical, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := "#!/bin/sh\necho blocked by hook >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	worktree := fixture.createWorktree(t, "hook-task", "feature/hook", "main")
	writeEngineFile(t, filepath.Join(worktree, "new.go"), "package app\n")
	_, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		CommitAll: true, Message: "feat: blocked",
	})
	if err == nil {
		t.Fatal("a failing pre-commit hook must stop the invocation")
	}
	if !strings.Contains(err.Error(), "blocked by hook") {
		t.Fatalf("error = %v, want the hook's own output", err)
	}
	if strings.Contains(fixture.ghLog(t), "pr create") {
		t.Fatal("a failed hook must never let the push or the pull request continue")
	}
}

func TestCreateCommitStagedWithLandRefusesLeftoversBeforeCommitting(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "leftover-task", "feature/leftover", "main")
	writeEngineFile(t, filepath.Join(worktree, "staged.go"), "package app\n")
	runEngineGit(t, worktree, "add", "staged.go")
	writeEngineFile(t, filepath.Join(worktree, "untracked.go"), "package app\n// untouched\n")
	beforeHead := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		CommitStaged: true, Message: "feat: staged only", Land: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalLeftoverBeforeLanding {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !strings.Contains(result.Reason, "untracked.go") {
		t.Fatalf("reason = %q, want it to name the leftover file", result.Reason)
	}
	afterHead := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	if afterHead != beforeHead {
		t.Fatal("a commit happened before the leftover refusal fired")
	}
}

func TestCreateAddCommitsOnlyTheNamedPathsAndLeavesOtherStagedFileOut(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "add-task", "feature/add", "main")
	writeEngineFile(t, filepath.Join(worktree, "wanted.go"), "package app\n")
	writeEngineFile(t, filepath.Join(worktree, "other.go"), "package app\n// other\n")
	runEngineGit(t, worktree, "add", "other.go")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		Add: []string{"wanted.go"}, Message: "feat: only wanted.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	// other.go is still staged and never named by --add, so the ordinary
	// dirty-worktree refusal fires afterward — itself the proof that only
	// wanted.go was committed.
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalDirtyWorktree {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !strings.Contains(result.Reason, "other.go") {
		t.Fatalf("reason = %q, want it to name other.go", result.Reason)
	}
	if len(result.CommittedPaths) != 1 || result.CommittedPaths[0] != "wanted.go" {
		t.Fatalf("committed paths = %v, want exactly wanted.go", result.CommittedPaths)
	}
	subject := strings.TrimSpace(runEngineGit(t, worktree, "log", "-1", "--format=%s"))
	if subject != "feat: only wanted.go" {
		t.Fatalf("last commit subject = %q", subject)
	}
}

func TestCreateAddCommitsADeletion(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "add-delete-task", "feature/add-delete", "main", "doomed.go")
	if err := os.Remove(filepath.Join(worktree, "doomed.go")); err != nil {
		t.Fatal(err)
	}
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		Add: []string{"doomed.go"}, Message: "feat: remove doomed.go",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateSuccess {
		t.Fatalf("outcome=%s reason=%s", result.Outcome, result.Reason)
	}
	if len(result.CommittedPaths) != 1 || result.CommittedPaths[0] != "doomed.go" {
		t.Fatalf("committed paths = %v, want exactly doomed.go", result.CommittedPaths)
	}
}

func TestCreateAddRefusesADotDotEscape(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "add-escape-task", "feature/add-escape", "main")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		Add: []string{"../outside.go"}, Message: "feat: escape",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalInvalidPath {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !strings.Contains(result.Reason, "../outside.go") {
		t.Fatalf("reason = %q, want it to name the escaping path", result.Reason)
	}
}

func TestCreateAddRefusesANonexistentPath(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "add-missing-task", "feature/add-missing", "main")
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		Add: []string{"nowhere.go"}, Message: "feat: nowhere",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalInvalidPath {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !strings.Contains(result.Reason, "nowhere.go") {
		t.Fatalf("reason = %q, want it to name the missing path", result.Reason)
	}
}

func TestCreateAddWithLandRefusesLeftoversBeforeCommitting(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "add-leftover-task", "feature/add-leftover", "main")
	writeEngineFile(t, filepath.Join(worktree, "wanted.go"), "package app\n")
	writeEngineFile(t, filepath.Join(worktree, "untracked.go"), "package app\n// untouched\n")
	beforeHead := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects,
		Add: []string{"wanted.go"}, Message: "feat: only wanted.go", Land: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateRefused || result.RefusalCode != CreateRefusalLeftoverBeforeLanding {
		t.Fatalf("outcome=%s refusal=%s reason=%s", result.Outcome, result.RefusalCode, result.Reason)
	}
	if !strings.Contains(result.Reason, "untracked.go") {
		t.Fatalf("reason = %q, want it to name the leftover file", result.Reason)
	}
	afterHead := strings.TrimSpace(runEngineGit(t, worktree, "rev-parse", "HEAD"))
	if afterHead != beforeHead {
		t.Fatal("a commit happened before the leftover refusal fired")
	}
}

func TestCreateResolvesADotWorktreeArgumentToAnAbsolutePath(t *testing.T) {
	fixture := newCreateFixture(t)
	worktree := fixture.createWorktree(t, "dot-task", "feature/dot", "main", "main.go")
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(worktree); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	}()
	// "." is the CLI's own default worktree argument. ReadManifest (and the
	// secure directory helpers it uses) refuse a relative path outright, so
	// this proves the manifest's own repository is used rather than falling
	// through to the repopath-from-canonical-dir guess, which would fail on
	// this fixture's legacy <org>/<repo> layout (no host segment) exactly the
	// way it failed against a real, unmigrated canonical clone.
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: ".", ProjectsRoot: fixture.projects,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateSuccess || result.Repository != fixture.repository {
		t.Fatalf("outcome=%s repository=%q reason=%s", result.Outcome, result.Repository, result.Reason)
	}
}

func TestCreateLandEndToEndCreatesArmsWaitsAndMerges(t *testing.T) {
	fixture := newCreateFixture(t)
	fixture.writeState(t, "files", `[{"filename":"go.sum","status":"modified","patch":"@@ -1,1 +1,1 @@\n-old h1:x=\n+new h1:y=\n"}]`)
	worktree := fixture.createWorktree(t, "land-task", "feature/land", "main", "go.sum")
	linkPreflightCalled := false
	result, err := CreatePullRequest(context.Background(), PullRequestCreateOptions{
		Worktree: worktree, ProjectsRoot: fixture.projects, Land: true,
		LinkPreflight: func(string) error { linkPreflightCalled = true; return nil },
		LandOptions: &PullRequestLandOptions{
			// Keep: this test proves create -> arm -> wait -> merge, which is
			// this brief's own scope; worktree cleanup after landing is
			// `wb pr land`'s existing, separately tested behavior.
			ProjectsRoot: fixture.projects, Keep: true, CheckPollInterval: time.Millisecond, Slice: 10 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != CreateSuccess {
		t.Fatalf("outcome=%s reason=%s", result.Outcome, result.Reason)
	}
	if !linkPreflightCalled {
		t.Fatal("the live-link preflight must run before landing")
	}
	if result.LandResult == nil || result.LandResult.Outcome != LandSuccess {
		t.Fatalf("land result = %#v", result.LandResult)
	}
	if result.ExitCode() != 0 {
		t.Fatalf("exit code = %d, want 0", result.ExitCode())
	}
	mainHead := strings.TrimSpace(runEngineGit(t, fixture.remote, "rev-parse", "refs/heads/main"))
	if mainHead != result.HeadSHA {
		t.Fatalf("main = %s, pushed head = %s: main was not advanced to the landed head", mainHead, result.HeadSHA)
	}
	if !result.LandResult.BranchDeleted {
		t.Fatal("a landed branch is retired by default")
	}
}
