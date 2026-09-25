package streams

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// stCovFakeGitHub installs a fake `gh` on PATH and returns the production
// GitHub port, the path of the invocation log, and a setter for the scripted
// responses. Every call is recorded, so a test can assert the exact argument
// vector WB sends the installed tool — the layer that must not depend on the
// presentation of a specific gh release.
func stCovFakeGitHub(t *testing.T) (ExecGitHub, string, func(string, string)) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh helper is a POSIX shell script")
	}
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "gh.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$STCOV_GH_LOG"
case "$1 $2" in
  "pr create")
    if [ -n "$STCOV_GH_FAIL_CREATE" ]; then echo 'create failed' >&2; exit 1; fi
    exit 0 ;;
  "pr list")
    if [ -n "$STCOV_GH_FAIL_LIST" ]; then echo 'list failed' >&2; exit 1; fi
    printf '%s' "$STCOV_GH_LIST" ;;
  "pr view")
    if [ -n "$STCOV_GH_MISSING_VIEW" ]; then echo 'could not resolve to a PullRequest' >&2; exit 1; fi
    if [ -n "$STCOV_GH_FAIL_VIEW" ]; then echo 'view failed' >&2; exit 1; fi
    printf '%s' "$STCOV_GH_VIEW" ;;
  "pr close")
    if [ -n "$STCOV_GH_FAIL_CLOSE" ]; then echo 'close failed' >&2; exit 1; fi
    exit 0 ;;
  "pr edit")
    if [ -n "$STCOV_GH_FAIL_EDIT" ]; then echo 'edit failed' >&2; exit 1; fi
    exit 0 ;;
  "run list")
    if [ -n "$STCOV_GH_FAIL_RUN" ]; then echo 'run failed' >&2; exit 1; fi
    printf '%s' "$STCOV_GH_RUN" ;;
  *) echo "unexpected gh call: $*" >&2; exit 2 ;;
esac
`
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STCOV_GH_LOG", log)
	t.Setenv("STCOV_GH_LIST", "[]")
	t.Setenv("STCOV_GH_VIEW", "{}")
	t.Setenv("STCOV_GH_RUN", "[]")
	set := func(kind, value string) { t.Setenv("STCOV_GH_"+kind, value) }
	return ExecGitHub{Timeout: time.Minute}, log, set
}

func stCovGhInvocations(t *testing.T, log string) []string {
	t.Helper()
	contents, err := os.ReadFile(log)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var lines []string
	for _, line := range strings.Split(string(contents), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

// stCovPullRequestJSON is one gh --json payload in the shape the port decodes.
func stCovPullRequestJSON(number int, title, state, base string) string {
	numberText := strconv.Itoa(number)
	return `{"number":` + numberText + `,"url":"https://example.test/pull/` + numberText + `",` +
		`"title":"` + title + `","isDraft":true,"state":"` + state + `",` +
		`"headRefName":"stream/x","baseRefName":"` + base + `",` +
		`"headRefOid":"0123456789012345678901234567890123456789",` +
		`"mergeCommit":{"oid":"abcdefabcdefabcdefabcdefabcdefabcdefabcd"}}`
}

func TestExecGitHubPullRequestForBranchReadsTheOpenPullRequest(t *testing.T) {
	hub, log, set := stCovFakeGitHub(t)
	ctx := context.Background()
	dir := t.TempDir()

	set("LIST", "["+stCovPullRequestJSON(7, "stream work", "OPEN", "main")+"]")
	pullRequest, found, err := hub.PullRequestForBranch(ctx, dir, "stream/x")
	if err != nil || !found {
		t.Fatalf("PullRequestForBranch = found %t, err %v; want the open pull request", found, err)
	}
	if pullRequest.Number != 7 || pullRequest.Title != "stream work" || pullRequest.Head != "stream/x" ||
		pullRequest.Base != "main" || !pullRequest.Draft || pullRequest.State != "OPEN" {
		t.Fatalf("pull request = %#v", pullRequest)
	}
	if pullRequest.HeadSHA != "0123456789012345678901234567890123456789" || pullRequest.MergeSHA != "abcdefabcdefabcdefabcdefabcdefabcdefabcd" {
		t.Fatalf("pull request identities = %#v", pullRequest)
	}
	want := "pr list --head stream/x --state open --json " + pullRequestFields
	if got := stCovGhInvocations(t, log); len(got) != 1 || got[0] != want {
		t.Fatalf("gh invocations = %v, want [%s]", got, want)
	}

	set("LIST", "[]")
	if _, found, err := hub.PullRequestForBranch(ctx, dir, "stream/x"); err != nil || found {
		t.Fatalf("empty list = found %t, err %v; want not found", found, err)
	}

	set("LIST", "{not json")
	if _, _, err := hub.PullRequestForBranch(ctx, dir, "stream/x"); err == nil || !strings.Contains(err.Error(), "parse pull requests for stream/x") {
		t.Fatalf("unparseable list error = %v", err)
	}

	set("FAIL_LIST", "1")
	if _, _, err := hub.PullRequestForBranch(ctx, dir, "stream/x"); err == nil || !strings.Contains(err.Error(), "list pull requests for stream/x") {
		t.Fatalf("failed list error = %v", err)
	}
}

func TestExecGitHubOpenPullRequestsTargetingListsEveryPullRequest(t *testing.T) {
	hub, log, set := stCovFakeGitHub(t)
	ctx := context.Background()
	set("LIST", "["+stCovPullRequestJSON(1, "first", "OPEN", "main")+","+stCovPullRequestJSON(2, "second", "OPEN", "main")+"]")

	pullRequests, err := hub.OpenPullRequestsTargeting(ctx, t.TempDir(), "main")
	if err != nil {
		t.Fatalf("OpenPullRequestsTargeting: %v", err)
	}
	if len(pullRequests) != 2 || pullRequests[0].Number != 1 || pullRequests[1].Number != 2 {
		t.Fatalf("pull requests = %#v", pullRequests)
	}
	want := "pr list --base main --state open --json " + pullRequestFields
	if got := stCovGhInvocations(t, log); len(got) != 1 || got[0] != want {
		t.Fatalf("gh invocations = %v, want [%s]", got, want)
	}

	set("LIST", "not json")
	if _, err := hub.OpenPullRequestsTargeting(ctx, t.TempDir(), "main"); err == nil || !strings.Contains(err.Error(), "parse pull requests targeting main") {
		t.Fatalf("unparseable list error = %v", err)
	}
	set("FAIL_LIST", "1")
	if _, err := hub.OpenPullRequestsTargeting(ctx, t.TempDir(), "main"); err == nil || !strings.Contains(err.Error(), "list pull requests targeting main") {
		t.Fatalf("failed list error = %v", err)
	}
}

func TestExecGitHubPullRequestDistinguishesAbsentFromUnreadable(t *testing.T) {
	hub, log, set := stCovFakeGitHub(t)
	ctx := context.Background()
	dir := t.TempDir()

	set("VIEW", stCovPullRequestJSON(9, "read me", "OPEN", "main"))
	pullRequest, found, err := hub.PullRequest(ctx, dir, 9)
	if err != nil || !found || pullRequest.Number != 9 || pullRequest.Title != "read me" {
		t.Fatalf("PullRequest = %#v, found %t, err %v", pullRequest, found, err)
	}
	want := "pr view 9 --json " + pullRequestFields
	if got := stCovGhInvocations(t, log); len(got) != 1 || got[0] != want {
		t.Fatalf("gh invocations = %v, want [%s]", got, want)
	}

	// "could not resolve" is absence, not a failure.
	set("MISSING_VIEW", "1")
	if _, found, err := hub.PullRequest(ctx, dir, 9); err != nil || found {
		t.Fatalf("absent pull request = found %t, err %v; want not found with no error", found, err)
	}
	set("MISSING_VIEW", "")

	set("VIEW", "{not json")
	if _, _, err := hub.PullRequest(ctx, dir, 9); err == nil || !strings.Contains(err.Error(), "parse pull request 9") {
		t.Fatalf("unparseable view error = %v", err)
	}
	set("FAIL_VIEW", "1")
	if _, _, err := hub.PullRequest(ctx, dir, 9); err == nil || !strings.Contains(err.Error(), "read pull request 9") {
		t.Fatalf("failed view error = %v", err)
	}
}

func TestExecGitHubCreateDraftPullRequestVerifiesWhatItOpened(t *testing.T) {
	hub, log, set := stCovFakeGitHub(t)
	ctx := context.Background()
	dir := t.TempDir()
	set("LIST", "["+stCovPullRequestJSON(3, "the title", "OPEN", "main")+"]")

	pullRequest, err := hub.CreateDraftPullRequest(ctx, dir, "main", "stream/x", "the title", "the body")
	if err != nil {
		t.Fatalf("CreateDraftPullRequest: %v", err)
	}
	if pullRequest.Number != 3 || pullRequest.Title != "the title" {
		t.Fatalf("created pull request = %#v", pullRequest)
	}
	got := stCovGhInvocations(t, log)
	if len(got) != 2 || got[0] != "pr create --draft --base main --head stream/x --title the title --body the body" {
		t.Fatalf("gh invocations = %v, want the draft create followed by the verifying read", got)
	}
	if !strings.HasPrefix(got[1], "pr list --head stream/x --state open --json ") {
		t.Fatalf("verifying read = %q", got[1])
	}

	set("FAIL_CREATE", "1")
	if _, err := hub.CreateDraftPullRequest(ctx, dir, "main", "stream/x", "t", "b"); err == nil || !strings.Contains(err.Error(), "open draft pull request for stream/x") {
		t.Fatalf("failed create error = %v", err)
	}
	set("FAIL_CREATE", "")

	// An exit 0 that opened nothing must not be reported as a pull request.
	set("LIST", "[]")
	if _, err := hub.CreateDraftPullRequest(ctx, dir, "main", "stream/x", "t", "b"); err == nil || !strings.Contains(err.Error(), "does not resolve") {
		t.Fatalf("unresolved create error = %v", err)
	}
}

func TestExecGitHubUpdatePullRequestTitleAssertsTheEffect(t *testing.T) {
	hub, log, set := stCovFakeGitHub(t)
	ctx := context.Background()
	dir := t.TempDir()

	set("VIEW", stCovPullRequestJSON(5, "new title", "OPEN", "main"))
	if err := hub.UpdatePullRequestTitle(ctx, dir, 5, "new title"); err != nil {
		t.Fatalf("UpdatePullRequestTitle: %v", err)
	}
	got := stCovGhInvocations(t, log)
	if len(got) != 2 || got[0] != "pr edit 5 --title new title" {
		t.Fatalf("gh invocations = %v, want the edit followed by the verifying read", got)
	}

	// The edit "succeeded" but the title is not the one requested.
	set("VIEW", stCovPullRequestJSON(5, "old title", "OPEN", "main"))
	err := hub.UpdatePullRequestTitle(ctx, dir, 5, "new title")
	if err == nil || !strings.Contains(err.Error(), `"old title"`) || !strings.Contains(err.Error(), `"new title"`) {
		t.Fatalf("unapplied-title error = %v", err)
	}

	set("FAIL_EDIT", "1")
	if err := hub.UpdatePullRequestTitle(ctx, dir, 5, "new title"); err == nil || !strings.Contains(err.Error(), "update pull request 5 title") {
		t.Fatalf("failed edit error = %v", err)
	}
	set("FAIL_EDIT", "")

	set("MISSING_VIEW", "1")
	if err := hub.UpdatePullRequestTitle(ctx, dir, 5, "new title"); err == nil || !strings.Contains(err.Error(), "no longer resolves") {
		t.Fatalf("missing verify error = %v", err)
	}
}

func TestExecGitHubClosePullRequestAssertsTheEffect(t *testing.T) {
	hub, log, set := stCovFakeGitHub(t)
	ctx := context.Background()
	dir := t.TempDir()

	set("VIEW", stCovPullRequestJSON(4, "closing", "CLOSED", "main"))
	if err := hub.ClosePullRequest(ctx, dir, 4, "superseded"); err != nil {
		t.Fatalf("ClosePullRequest: %v", err)
	}
	got := stCovGhInvocations(t, log)
	if len(got) != 2 || got[0] != "pr close 4 --comment superseded" {
		t.Fatalf("gh invocations = %v, want the close followed by the verifying read", got)
	}

	// An exit 0 whose PR is still OPEN must not be recorded as closed.
	set("VIEW", stCovPullRequestJSON(4, "closing", "OPEN", "main"))
	if err := hub.ClosePullRequest(ctx, dir, 4, "superseded"); err == nil || !strings.Contains(err.Error(), "still OPEN") {
		t.Fatalf("still-open error = %v", err)
	}

	set("FAIL_CLOSE", "1")
	if err := hub.ClosePullRequest(ctx, dir, 4, "superseded"); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("failed close error = %v", err)
	}
	set("FAIL_CLOSE", "")

	set("MISSING_VIEW", "1")
	if err := hub.ClosePullRequest(ctx, dir, 4, "superseded"); err == nil || !strings.Contains(err.Error(), "no longer resolves") {
		t.Fatalf("missing verify error = %v", err)
	}
}

func TestExecGitHubRetargetPullRequestAssertsTheEffect(t *testing.T) {
	hub, log, set := stCovFakeGitHub(t)
	ctx := context.Background()
	dir := t.TempDir()

	set("VIEW", stCovPullRequestJSON(6, "retarget", "OPEN", "release"))
	if err := hub.RetargetPullRequest(ctx, dir, 6, "release"); err != nil {
		t.Fatalf("RetargetPullRequest: %v", err)
	}
	got := stCovGhInvocations(t, log)
	if len(got) != 2 || got[0] != "pr edit 6 --base release" {
		t.Fatalf("gh invocations = %v, want the retarget followed by the verifying read", got)
	}

	set("VIEW", stCovPullRequestJSON(6, "retarget", "OPEN", "main"))
	if err := hub.RetargetPullRequest(ctx, dir, 6, "release"); err == nil || !strings.Contains(err.Error(), `"main"`) {
		t.Fatalf("unapplied-retarget error = %v", err)
	}

	set("FAIL_EDIT", "1")
	if err := hub.RetargetPullRequest(ctx, dir, 6, "release"); err == nil || !strings.Contains(err.Error(), "edit failed") {
		t.Fatalf("failed retarget error = %v", err)
	}
	set("FAIL_EDIT", "")

	set("MISSING_VIEW", "1")
	if err := hub.RetargetPullRequest(ctx, dir, 6, "release"); err == nil || !strings.Contains(err.Error(), "no longer resolves") {
		t.Fatalf("missing verify error = %v", err)
	}
}

func TestExecGitHubDefaultBranchStatusReadsTheConclusion(t *testing.T) {
	hub, log, set := stCovFakeGitHub(t)
	ctx := context.Background()
	dir := t.TempDir()

	set("RUN", `[{"conclusion":"success"}]`)
	conclusion, err := hub.DefaultBranchStatus(ctx, dir, "main")
	if err != nil || conclusion != "success" {
		t.Fatalf("DefaultBranchStatus = %q, %v; want success", conclusion, err)
	}
	want := "run list --branch main --status completed --limit 1 --json conclusion"
	if got := stCovGhInvocations(t, log); len(got) != 1 || got[0] != want {
		t.Fatalf("gh invocations = %v, want [%s]", got, want)
	}

	// No completed run is reported as an empty conclusion, never assumed green.
	set("RUN", "[]")
	if conclusion, err := hub.DefaultBranchStatus(ctx, dir, "main"); err != nil || conclusion != "" {
		t.Fatalf("no-run status = %q, %v; want an empty conclusion", conclusion, err)
	}

	set("RUN", "not json")
	if _, err := hub.DefaultBranchStatus(ctx, dir, "main"); err == nil || !strings.Contains(err.Error(), "parse default-branch run status") {
		t.Fatalf("unparseable run error = %v", err)
	}
	set("FAIL_RUN", "1")
	if _, err := hub.DefaultBranchStatus(ctx, dir, "main"); err == nil || !strings.Contains(err.Error(), "run failed") {
		t.Fatalf("failed run error = %v", err)
	}
}

// runBounded must still bound a child when the caller left the timeout unset.
func TestStreamsRunBoundedDefaultsAnUnsetTimeout(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go is not installed")
	}
	if _, err := runBounded(context.Background(), 0, t.TempDir(), "go", "version"); err != nil {
		t.Fatalf("runBounded with a zero timeout: %v", err)
	}
}

func TestExecGitDefaultBranchReadsRemoteHeadThenFallsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	git := ExecGit{Timeout: time.Minute}

	for _, branch := range []string{"main", "master"} {
		t.Run("clone of "+branch, func(t *testing.T) {
			t.Parallel()
			base := t.TempDir()
			remote := filepath.Join(base, "origin.git")
			runStreamGit(t, "", "init", "--bare", "--initial-branch="+branch, remote)
			testenv.ConfigureGitAutoMaintenanceOff(t, remote)
			work := filepath.Join(base, "work")
			runStreamGit(t, "", "clone", remote, work)
			commitStreamFile(t, work, "a.txt", "a\n", "feat: a")
			runStreamGit(t, work, "push", "-u", "origin", branch)
			// A clone of an empty repository has no origin/HEAD, so create it
			// the way a clone of a populated repository would.
			runStreamGit(t, work, "remote", "set-head", "origin", branch)

			// The clone's origin/HEAD is the authoritative answer.
			resolved, err := git.DefaultBranch(ctx, work)
			if err != nil || resolved != branch {
				t.Fatalf("DefaultBranch from origin/HEAD = %q, %v; want %s", resolved, err, branch)
			}
			// Without it, the local candidate scan must still find the branch.
			// symbolic-ref -d deletes the symref itself; update-ref without
			// --no-deref would delete the branch it points at instead.
			runStreamGit(t, work, "update-ref", "--no-deref", "-d", "refs/remotes/origin/HEAD")
			resolved, err = git.DefaultBranch(ctx, work)
			if err != nil || resolved != branch {
				t.Fatalf("DefaultBranch from the candidate scan = %q, %v; want %s", resolved, err, branch)
			}
		})
	}

	t.Run("unresolvable default branch is an error", func(t *testing.T) {
		t.Parallel()
		base := t.TempDir()
		remote := filepath.Join(base, "origin.git")
		runStreamGit(t, "", "init", "--bare", "--initial-branch=trunk", remote)
		testenv.ConfigureGitAutoMaintenanceOff(t, remote)
		work := filepath.Join(base, "work")
		runStreamGit(t, "", "clone", remote, work)
		commitStreamFile(t, work, "a.txt", "a\n", "feat: a")
		runStreamGit(t, work, "push", "-u", "origin", "trunk")
		runStreamGit(t, work, "update-ref", "--no-deref", "-d", "refs/remotes/origin/HEAD")
		if _, err := git.DefaultBranch(ctx, work); err == nil {
			t.Fatal("DefaultBranch guessed a default branch from local state that names neither main nor master")
		}
	})
}

func TestExecGitLocalBranchHeadReportsAbsenceAndUnreadableState(t *testing.T) {
	t.Parallel()
	root, git := gitFixture(t)
	ctx := context.Background()

	sha, ok, err := git.LocalBranchHead(ctx, root, "stream/fixture")
	if err != nil || !ok || len(sha) != 40 {
		t.Fatalf("LocalBranchHead = %q, %t, %v; want the local branch head", sha, ok, err)
	}
	if _, ok, err := git.LocalBranchHead(ctx, root, "absent"); err != nil || ok {
		t.Fatalf("LocalBranchHead of an absent branch = %t, %v; want not found with no error", ok, err)
	}
	if _, _, err := git.LocalBranchHead(ctx, t.TempDir(), "any"); err == nil {
		t.Fatal("LocalBranchHead of a directory that is not a repository reported no error")
	}
}

func TestExecGitIsAncestorDistinguishesDirection(t *testing.T) {
	t.Parallel()
	root, git := gitFixture(t)
	ctx := context.Background()

	ancestor, err := git.IsAncestor(ctx, root, "main", "stream/fixture")
	if err != nil || !ancestor {
		t.Fatalf("IsAncestor(main, stream/fixture) = %t, %v; want true", ancestor, err)
	}
	descendant, err := git.IsAncestor(ctx, root, "stream/fixture", "main")
	if err != nil || descendant {
		t.Fatalf("IsAncestor(stream/fixture, main) = %t, %v; want false", descendant, err)
	}
	if _, err := git.IsAncestor(ctx, root, "no-such-commit", "main"); err == nil {
		t.Fatal("IsAncestor with an unresolvable commit reported no error")
	}
}

func TestExecGitReportsUnreadableRepositoryState(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	git := ExecGit{Timeout: time.Minute}
	ctx := context.Background()

	if _, err := git.CurrentBranch(ctx, dir); err == nil {
		t.Error("CurrentBranch of a directory that is not a repository reported no error")
	}
	if _, err := git.LocalHead(ctx, dir); err == nil {
		t.Error("LocalHead of a directory that is not a repository reported no error")
	}
	if _, err := git.DirtyPaths(ctx, dir); err == nil {
		t.Error("DirtyPaths of a directory that is not a repository reported no error")
	}
	if _, err := git.Tags(ctx, dir, "v*"); err == nil {
		t.Error("Tags of a directory that is not a repository reported no error")
	}
	if _, err := git.LogSubjects(ctx, dir, "", "HEAD"); err == nil {
		t.Error("LogSubjects of a directory that is not a repository reported no error")
	}
}

// The recovery path repairs a behind checkout; it must refuse when the stream
// branch is not the one that is checked out rather than fast-forwarding
// whatever happens to be current.
func TestPushBranchRefusesToFastForwardABranchThatIsNotCheckedOut(t *testing.T) {
	t.Parallel()
	local, other := newPublishedStreamFixture(t)
	runStreamGit(t, local, "checkout", "main")
	runStreamGit(t, other, "fetch", "origin")
	runStreamGit(t, other, "checkout", "stream/recovery")
	commitStreamFile(t, other, "remote.txt", "remote\n", "feat: remote advance")
	runStreamGit(t, other, "push", "origin", "stream/recovery")

	git := ExecGit{Timeout: time.Minute}
	_, err := git.PushBranch(context.Background(), local, "stream/recovery")
	if err == nil || !strings.Contains(err.Error(), "cannot fast-forward") || !strings.Contains(err.Error(), "main") {
		t.Fatalf("not-checked-out error = %v, want an explicit refusal naming the current branch", err)
	}
	if branch := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "--abbrev-ref", "HEAD")); branch != "main" {
		t.Fatalf("current branch changed to %s", branch)
	}
}

func TestPushBranchReportsAnUnbornHead(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	remote := filepath.Join(base, "empty.git")
	runStreamGit(t, "", "init", "--bare", "--initial-branch=main", remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	work := filepath.Join(base, "work")
	runStreamGit(t, "", "clone", remote, work)

	git := ExecGit{Timeout: time.Minute}
	if _, err := git.PushBranch(context.Background(), work, "stream/x"); err == nil || !strings.Contains(err.Error(), "read HEAD") {
		t.Fatalf("unborn-head error = %v, want a HEAD read failure", err)
	}
}

// A reachable fetch URL with an unreachable push URL is the shape of a
// credential or permission failure: the local ahead extension must be reported
// as a failed push, never as a published branch.
func TestPushBranchReportsAFailedPush(t *testing.T) {
	t.Parallel()
	local, _ := newPublishedStreamFixture(t)
	commitStreamFile(t, local, "local.txt", "local\n", "feat: local advance")
	runStreamGit(t, local, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))

	git := ExecGit{Timeout: time.Minute}
	_, err := git.PushBranch(context.Background(), local, "stream/recovery")
	if err == nil || !strings.Contains(err.Error(), "push stream/recovery from") {
		t.Fatalf("failed-push error = %v", err)
	}
}

func TestDeleteRemoteBranchRefusesAnEmptyExpectedSHA(t *testing.T) {
	t.Parallel()
	local, _ := newPublishedStreamFixture(t)
	git := ExecGit{Timeout: time.Minute}
	err := git.DeleteRemoteBranch(context.Background(), local, "stream/recovery", "")
	if err == nil || !strings.Contains(err.Error(), "without an expected remote SHA") {
		t.Fatalf("empty-expected error = %v", err)
	}
	if remote := strings.TrimSpace(runStreamGit(t, local, "ls-remote", "--heads", "origin", "refs/heads/stream/recovery")); remote == "" {
		t.Fatal("the refused deletion removed the remote ref")
	}
}

// When the deletion push fails but every resolved push destination still
// reports the expected SHA, the caller must see the push failure rather than a
// silent success: the ref is demonstrably not deleted.
func TestDeleteRemoteBranchReportsARejectedDeletion(t *testing.T) {
	t.Parallel()
	local, _ := newPublishedStreamFixture(t)
	remote := filepath.Join(filepath.Dir(local), "origin.git")
	expected := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "refs/remotes/origin/stream/recovery"))
	runStreamGit(t, remote, "config", "receive.denyDeletes", "true")

	git := ExecGit{Timeout: time.Minute}
	err := git.DeleteRemoteBranch(context.Background(), local, "stream/recovery", expected)
	if err == nil || !strings.Contains(err.Error(), "delete origin/stream/recovery") {
		t.Fatalf("rejected-deletion error = %v", err)
	}
	remoteRef := strings.TrimSpace(runStreamGit(t, local, "ls-remote", "--heads", "origin", "refs/heads/stream/recovery"))
	if fields := strings.Fields(remoteRef); len(fields) != 2 || fields[0] != expected {
		t.Fatalf("remote ref = %q, want the protected %s", remoteRef, expected)
	}
}

// An unreadable push destination is unknown, not absent: the idempotent-retry
// path must fail closed instead of reporting the branch retired.
func TestDeleteRemoteBranchFailsClosedWhenThePushDestinationIsUnreadable(t *testing.T) {
	t.Parallel()
	local, _ := newPublishedStreamFixture(t)
	expected := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "refs/remotes/origin/stream/recovery"))
	runStreamGit(t, local, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))

	git := ExecGit{Timeout: time.Minute}
	err := git.DeleteRemoteBranch(context.Background(), local, "stream/recovery", expected)
	if err == nil || !strings.Contains(err.Error(), "authoritative push-destination reread failed") {
		t.Fatalf("unreadable-destination error = %v, want a fail-closed refusal", err)
	}
}

func TestExecGitCommitSubjectsAndPatchIDsHandleAnEmptyInput(t *testing.T) {
	t.Parallel()
	root, git := gitFixture(t)
	ctx := context.Background()

	subjects, err := git.commitSubjects(ctx, root, nil)
	if err != nil || len(subjects) != 0 {
		t.Fatalf("commitSubjects(nil) = %v, %v; want no subjects", subjects, err)
	}
	identities, err := git.patchIDs(ctx, root, "main", "main")
	if err != nil || len(identities) != 0 {
		t.Fatalf("patchIDs over an empty range = %v, %v; want no identities", identities, err)
	}
}

func TestPushBranchReportsADetachedHeadDuringFastForward(t *testing.T) {
	t.Parallel()
	local, other := newPublishedStreamFixture(t)
	runStreamGit(t, other, "fetch", "origin")
	runStreamGit(t, other, "checkout", "stream/recovery")
	commitStreamFile(t, other, "remote.txt", "remote\n", "feat: remote advance")
	runStreamGit(t, other, "push", "origin", "stream/recovery")
	streamHead := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "stream/recovery"))
	// Detach at the local stream commit: it is an ancestor of the remote
	// advance, but there is no current branch to fast-forward.
	runStreamGit(t, local, "checkout", "--detach", streamHead)

	git := ExecGit{Timeout: time.Minute}
	_, err := git.PushBranch(context.Background(), local, "stream/recovery")
	if err == nil || !strings.Contains(err.Error(), "current branch") {
		t.Fatalf("detached-head error = %v, want a refusal naming the unreadable current branch", err)
	}
	if head := strings.TrimSpace(runStreamGit(t, local, "rev-parse", "HEAD")); head != streamHead {
		t.Fatalf("detached head moved from %s to %s", streamHead, head)
	}
}

func TestExecGitCommitsNotInReportsAnUnreadableBase(t *testing.T) {
	t.Parallel()
	root, git := gitFixture(t)
	commits, err := git.CommitsNotIn(context.Background(), root, "stream/fixture", "no-such-base")
	if err == nil {
		t.Fatalf("CommitsNotIn against an unreadable base = %#v, want an error", commits)
	}
	if !strings.Contains(err.Error(), "no-such-base") {
		t.Fatalf("error = %v, want it to name the base it could not read", err)
	}
}

func TestExecGitCommitSubjectsAndPatchIDsReportUnreadableInput(t *testing.T) {
	t.Parallel()
	root, git := gitFixture(t)
	ctx := context.Background()

	if _, err := git.commitSubjects(ctx, root, []string{"0123456789012345678901234567890123456789"}); err == nil {
		t.Fatal("commitSubjects reported success for a commit that does not exist")
	}
	if _, err := git.patchIDs(ctx, root, "no-such-base", "stream/fixture"); err == nil {
		t.Fatal("patchIDs reported success for a range it could not read")
	}
}

func TestRemoteHeadsOnOriginPushDestinationsFailsClosedWithoutOrigin(t *testing.T) {
	t.Parallel()
	root, git := gitFixture(t)
	if _, err := git.remoteHeadsOnOriginPushDestinations(context.Background(), root, "stream/fixture"); err == nil {
		t.Fatal("the push-destination reread reported success in a repository with no origin")
	}
}

func TestExecGitHubCreateDraftPullRequestReportsAnUnreadableVerification(t *testing.T) {
	hub, _, set := stCovFakeGitHub(t)
	set("LIST", "{not json")
	if _, err := hub.CreateDraftPullRequest(context.Background(), t.TempDir(), "main", "stream/x", "t", "b"); err == nil {
		t.Fatal("CreateDraftPullRequest reported success when the verifying read could not be parsed")
	}
}

func TestExecGitHubUpdatePullRequestTitleReportsAnUnreadableVerification(t *testing.T) {
	hub, _, set := stCovFakeGitHub(t)
	set("FAIL_VIEW", "1")
	err := hub.UpdatePullRequestTitle(context.Background(), t.TempDir(), 5, "new title")
	if err == nil || !strings.Contains(err.Error(), "verify pull request 5 title") {
		t.Fatalf("failed verification error = %v", err)
	}
}

func TestExecGitHubClosePullRequestReportsAnUnreadableVerification(t *testing.T) {
	hub, _, set := stCovFakeGitHub(t)
	set("FAIL_VIEW", "1")
	err := hub.ClosePullRequest(context.Background(), t.TempDir(), 4, "superseded")
	if err == nil || !strings.Contains(err.Error(), "verify pull request 4 closed") {
		t.Fatalf("failed verification error = %v", err)
	}
}

func TestExecGitHubRetargetPullRequestReportsAnUnreadableVerification(t *testing.T) {
	hub, _, set := stCovFakeGitHub(t)
	set("FAIL_VIEW", "1")
	err := hub.RetargetPullRequest(context.Background(), t.TempDir(), 6, "release")
	if err == nil || !strings.Contains(err.Error(), "verify pull request 6 retargeted") {
		t.Fatalf("failed verification error = %v", err)
	}
}

// A push destination that is not the fetch destination can accept the branch
// while the fetch URL cannot yet read it back; that re-read failure is
// reported rather than silently claiming the push landed.
func TestPushBranchReportsAFailedRereadAfterThePush(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	base := t.TempDir()
	fetchRemote := filepath.Join(base, "fetch.git")
	pushRemote := filepath.Join(base, "push.git")
	for _, remote := range []string{fetchRemote, pushRemote} {
		runStreamGit(t, "", "init", "--bare", "--initial-branch=main", remote)
		testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	}
	work := filepath.Join(base, "work")
	runStreamGit(t, "", "clone", fetchRemote, work)
	commitStreamFile(t, work, "base.txt", "base\n", "feat: base")
	runStreamGit(t, work, "push", "-u", "origin", "main")
	runStreamGit(t, work, "checkout", "-b", "stream/reread")
	runStreamGit(t, work, "remote", "set-url", "--push", "origin", pushRemote)

	git := ExecGit{Timeout: time.Minute}
	_, err := git.PushBranch(context.Background(), work, "stream/reread")
	if err == nil || !strings.Contains(err.Error(), "re-read origin/stream/reread after pushing") {
		t.Fatalf("error = %v, want the failed re-read reported", err)
	}
	if remote := strings.TrimSpace(runStreamGit(t, work, "ls-remote", "--heads", pushRemote, "refs/heads/stream/reread")); remote == "" {
		t.Fatal("the push itself did not land on the push destination")
	}
}

// A post-receive hook that rewrites the pushed ref makes the push exit 0 while
// origin holds a different commit; the verification must catch that.
func TestPushBranchReportsAnOriginThatDisagreesAfterThePush(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not installed")
	}
	base := t.TempDir()
	remote := filepath.Join(base, "origin.git")
	runStreamGit(t, "", "init", "--bare", "--initial-branch=main", remote)
	testenv.ConfigureGitAutoMaintenanceOff(t, remote)
	work := filepath.Join(base, "work")
	runStreamGit(t, "", "clone", remote, work)
	commitStreamFile(t, work, "base.txt", "base\n", "feat: base")
	runStreamGit(t, work, "push", "-u", "origin", "main")
	runStreamGit(t, work, "checkout", "-b", "stream/hooked")
	// main advances past the stream branch, so the hook has a different commit
	// to point the pushed ref at.
	runStreamGit(t, work, "checkout", "main")
	commitStreamFile(t, work, "later.txt", "later\n", "feat: later")
	runStreamGit(t, work, "push", "origin", "main")
	runStreamGit(t, work, "checkout", "stream/hooked")
	mainSHA := strings.TrimSpace(runStreamGit(t, work, "rev-parse", "main"))

	hook := filepath.Join(remote, "hooks", "post-receive")
	if err := testenv.WriteExecutableFile(hook, []byte("#!/bin/sh\ngit update-ref refs/heads/stream/hooked refs/heads/main\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	git := ExecGit{Timeout: time.Minute}
	_, err := git.PushBranch(context.Background(), work, "stream/hooked")
	if err == nil || !strings.Contains(err.Error(), "did not land the intended commit") {
		t.Fatalf("error = %v, want the diverged origin reported", err)
	}
	remoteRef := strings.TrimSpace(runStreamGit(t, work, "ls-remote", "--heads", remote, "refs/heads/stream/hooked"))
	if fields := strings.Fields(remoteRef); len(fields) != 2 || fields[0] != mainSHA {
		t.Fatalf("origin/stream/hooked = %q, want the hook's %s", remoteRef, mainSHA)
	}
}

// A worktree whose status cannot be read must refuse the fast-forward before
// it runs any merge.
func TestFastForwardBranchFailsClosedWhenStatusIsUnreadable(t *testing.T) {
	local, _ := newPublishedStreamFixture(t)
	gitDir := filepath.Join(local, ".git")
	// A git directory with a work tree that does not exist answers symbolic-ref
	// but cannot answer status, which is exactly the fail-closed shape.
	t.Setenv("GIT_DIR", gitDir)
	t.Setenv("GIT_WORK_TREE", filepath.Join(t.TempDir(), "absent"))

	git := ExecGit{Timeout: time.Minute}
	err := git.fastForwardBranch(context.Background(), local, "stream/recovery", "remote-sha")
	if err == nil || !strings.Contains(err.Error(), "status") {
		t.Fatalf("error = %v, want the unreadable status reported", err)
	}
}
