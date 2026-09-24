package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// orchCovPullRequestView decodes a pull request view from the wire shape, which
// is how these tests reach the anonymous head/base repository fields a
// hand-written composite literal cannot spell.
func orchCovPullRequestView(t *testing.T, raw string) PullRequestView {
	t.Helper()
	var view PullRequestView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

// orchCovScriptState writes a gh script whose answers are read from files and
// returns the state directory.
func orchCovScriptState(t *testing.T, script string) orchCovGHState {
	t.Helper()
	state := orchCovInstallGH(t, script)
	t.Setenv("ORCHCOV_GH_STATE", state.dir)
	return state
}

// orchCovPutMergeScript answers the merge write. exit/stdout/stderr are files
// under the state directory.
const orchCovPutMergeScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = api ] && [ "$2" = --method ] && [ "$3" = PUT ]; then
  if [ -s "$S/stderr" ]; then cat "$S/stderr" >&2; fi
  if [ -s "$S/stdout" ]; then cat "$S/stdout"; fi
  exit "$(cat "$S/exit")"
fi
echo "unexpected gh args: $*" >&2
exit 30
`

func TestOrchCovMergePullRequestReturnsTheLandedCommit(t *testing.T) {
	state := orchCovScriptState(t, orchCovPutMergeScript)
	state.answer(t, "exit", "0")
	state.answer(t, "stdout", `{"sha":"0123456789abcdef0123456789abcdef01234567","merged":true}`)
	state.answer(t, "stderr", "")

	sha, refusal, err := mergePullRequest(context.Background(), "acme/app", "7", "candidate", "squash", "feat: the change", "body")
	if err != nil || refusal != nil {
		t.Fatalf("merge = %q refusal=%+v err=%v", sha, refusal, err)
	}
	if sha != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("merge sha = %q", sha)
	}
}

func TestOrchCovMergePullRequestClassifiesEveryRefusal(t *testing.T) {
	for _, test := range []struct {
		name     string
		exit     string
		stdout   string
		stderr   string
		wantCode string
		wantIn   string
	}{
		{
			name: "head moved", exit: "1",
			stdout:   `{"message":"Head branch was modified. Review and try the merge again."}`,
			wantCode: LandRefusalHeadMoved, wantIn: "the branch moved after its checks were observed",
		},
		{
			name: "base moved", exit: "1",
			stdout:   `{"message":"Base branch was modified. Review and try the merge again."}`,
			wantCode: LandRefusalHeadMoved, wantIn: "Base branch was modified",
		},
		{
			name: "rejected with a message", exit: "1",
			stdout:   `{"message":"Pull Request is not mergeable"}`,
			wantCode: LandRefusalMergeRejected, wantIn: "Pull Request is not mergeable",
		},
		{
			name: "rejected with only stderr", exit: "1",
			stderr:   "gh: Not Found (HTTP 404)",
			wantCode: LandRefusalMergeRejected, wantIn: "gh: Not Found (HTTP 404)",
		},
		{
			name: "rejected with only stdout", exit: "1",
			stdout:   "the server exploded",
			wantCode: LandRefusalMergeRejected, wantIn: "the server exploded",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := orchCovScriptState(t, orchCovPutMergeScript)
			state.answer(t, "exit", test.exit)
			state.answer(t, "stdout", test.stdout)
			state.answer(t, "stderr", test.stderr)

			sha, refusal, err := mergePullRequest(context.Background(), "acme/app", "7", "candidate", "squash", "subject", "body")
			if err != nil {
				t.Fatal(err)
			}
			if refusal == nil || refusal.code != test.wantCode || sha != "" {
				t.Fatalf("merge refusal = %+v (sha %q)", refusal, sha)
			}
			if !strings.Contains(refusal.reason, test.wantIn) {
				t.Fatalf("refusal reason %q does not contain %q", refusal.reason, test.wantIn)
			}
		})
	}
}

func TestMergePullRequestLeavesTransientWriteOutcomeResumable(t *testing.T) {
	for _, stderr := range []string{"gh: Internal Server Error (HTTP 500)", "gh: Bad Gateway (HTTP 502)"} {
		t.Run(stderr, func(t *testing.T) {
			state := orchCovScriptState(t, orchCovPutMergeScript)
			state.answer(t, "exit", "1")
			state.answer(t, "stdout", "")
			state.answer(t, "stderr", stderr)

			sha, refusal, err := mergePullRequest(context.Background(), "acme/app", "7", "candidate", "squash", "subject", "body")
			if sha != "" || refusal != nil {
				t.Fatalf("transient merge write = sha %q refusal %+v, want neither", sha, refusal)
			}
			if err == nil || !errors.Is(err, githubobserver.ErrTransientMutationOutcomeUnknown) {
				t.Fatalf("transient merge write error = %v, want resumable unknown-outcome error", err)
			}
		})
	}
}

func TestMergePullRequestTreatsCallerDeadlineAfterDispatchAsUnknown(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "mutation-dispatched")
	script := `#!/bin/sh
if [ "$1" = api ] && [ "$2" = --method ] && [ "$3" = PUT ]; then
  printf dispatched >"$ORCHCOV_MUTATION_MARKER"
  sleep 5
fi
exit 30
`
	orchCovInstallGH(t, script)
	t.Setenv("ORCHCOV_MUTATION_MARKER", marker)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	sha, refusal, err := mergePullRequest(ctx, "acme/app", "7", "candidate", "squash", "subject", "body")
	if sha != "" || refusal != nil || err == nil || !errors.Is(err, githubobserver.ErrTransientMutationOutcomeUnknown) {
		t.Fatalf("deadline after merge dispatch = sha %q refusal=%+v err=%v", sha, refusal, err)
	}
	if contents, readErr := os.ReadFile(marker); readErr != nil || string(contents) != "dispatched" {
		t.Fatalf("merge mutation was not dispatched before deadline: contents=%q err=%v", contents, readErr)
	}
}

const orchCovOneEndpointScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
cat "$S/body"
exit "$(cat "$S/exit")"
`

func TestOrchCovCommitIsOnBranchReadsTheComparisonStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		status string
		want   bool
	}{
		{name: "identical", status: "identical", want: true},
		{name: "ahead", status: "ahead", want: true},
		{name: "behind", status: "behind", want: false},
		{name: "diverged", status: "diverged", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := orchCovScriptState(t, orchCovOneEndpointScript)
			state.answer(t, "body", `{"status":"`+test.status+`"}`)
			state.answer(t, "exit", "0")
			onBase, err := commitIsOnBranch(context.Background(), "acme/app", "0123456789abcdef", "main")
			if err != nil {
				t.Fatal(err)
			}
			if onBase != test.want {
				t.Fatalf("status %s reported on-base=%t", test.status, onBase)
			}
		})
	}
}

func TestOrchCovCommitIsOnBranchFailsClosedOnAnUnusableComparison(t *testing.T) {
	state := orchCovScriptState(t, orchCovOneEndpointScript)
	state.answer(t, "body", "not json")
	state.answer(t, "exit", "0")
	if _, err := commitIsOnBranch(context.Background(), "acme/app", "0123456789abcdef", "main"); err == nil ||
		!strings.Contains(err.Error(), "decode comparison of 0123456789ab with main") {
		t.Fatalf("undecodable comparison error = %v", err)
	}

	orchCovInstallGH(t, orchCovNotFound)
	if _, err := commitIsOnBranch(context.Background(), "acme/app", "0123456789abcdef", "main"); err == nil ||
		!strings.Contains(err.Error(), "compare 0123456789ab with main") {
		t.Fatalf("unreadable comparison error = %v", err)
	}
}

const orchCovDeleteBranchScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = api ]; then
  if [ -s "$S/check-stdout" ]; then cat "$S/check-stdout"; fi
  if [ -s "$S/check-stderr" ]; then cat "$S/check-stderr" >&2; fi
  exit "$(cat "$S/check-exit")"
fi
echo "unexpected gh args: $*" >&2
exit 30
`

const orchCovDeleteBranchGitScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$*" = "remote get-url --push origin" ]; then
  printf '%s\n' "$*" > "$S/git-remote-args"
  cat "$S/git-remote"
  exit 0
fi
if [ "$1 $2" = "config --get-regexp" ]; then
  exit 1
fi
printf '%s\n' "$*" > "$S/git-args"
if [ -s "$S/git-stdout" ]; then cat "$S/git-stdout"; fi
if [ -s "$S/git-stderr" ]; then cat "$S/git-stderr" >&2; fi
exit "$(cat "$S/git-exit")"
`

func orchCovInstallDeleteGit(t *testing.T, state orchCovGHState) {
	t.Helper()
	gh, err := exec.LookPath("gh")
	if err != nil {
		t.Fatal(err)
	}
	if err := testenv.WriteExecutableFile(filepath.Join(filepath.Dir(gh), "git"), []byte(orchCovDeleteBranchGitScript), 0o755); err != nil {
		t.Fatal(err)
	}
	state.answer(t, "git-stdout", "")
	state.answer(t, "git-stderr", "")
	state.answer(t, "git-remote", "https://user:secret@example.test/acme/app.git")
}

func TestOrchCovDeleteRemoteBranchNeverTouchesAForkHead(t *testing.T) {
	t.Parallel()
	view := orchCovPullRequestView(t, `{"head":{"ref":"candidate"},"base":{"ref":"main"}}`)
	landed := orchCovPullRequestView(t, `{"base":{"ref":"main"}}`)
	if deleted, err := deleteRemoteBranch(context.Background(), ".", "acme/app", view, landed, view.Head.SHA); err != nil || deleted {
		t.Fatalf("head with no repository deleted=%t err=%v", deleted, err)
	}
	view = orchCovPullRequestView(t, `{"head":{"ref":"candidate","repo":{"full_name":"fork/app"}},"base":{"ref":"main"}}`)
	if deleted, err := deleteRemoteBranch(context.Background(), ".", "acme/app", view, landed, view.Head.SHA); err != nil || deleted {
		t.Fatalf("fork head deleted=%t err=%v", deleted, err)
	}
	view = orchCovPullRequestView(t, `{"head":{"ref":"main","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}`)
	if deleted, err := deleteRemoteBranch(context.Background(), ".", "acme/app", view, landed, view.Head.SHA); err != nil || deleted {
		t.Fatalf("target branch deleted=%t err=%v", deleted, err)
	}
}

func TestOrchCovDeleteRemoteBranchVerifiesTheEffect(t *testing.T) {
	view := orchCovPullRequestView(t, `{"head":{"ref":"candidate","sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}`)
	landed := orchCovPullRequestView(t, `{"base":{"ref":"main"}}`)
	t.Run("deleted and absent", func(t *testing.T) {
		state := orchCovScriptState(t, orchCovDeleteBranchScript)
		orchCovInstallDeleteGit(t, state)
		state.answer(t, "git-exit", "0")
		state.answer(t, "check-exit", "1")
		state.answer(t, "check-stdout", "")
		state.answer(t, "check-stderr", "Not Found")
		deleted, err := deleteRemoteBranch(context.Background(), ".", "acme/app", view, landed, view.Head.SHA)
		if err != nil || !deleted {
			t.Fatalf("deleted=%t err=%v", deleted, err)
		}
		args, err := os.ReadFile(filepath.Join(state.dir, "git-args"))
		if err != nil {
			t.Fatal(err)
		}
		gotArgs := strings.TrimSpace(string(args))
		if !strings.HasPrefix(gotArgs, "push --force-with-lease=refs/heads/candidate:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa wb-landing-") ||
			!strings.HasSuffix(gotArgs, " :refs/heads/candidate") {
			t.Fatalf("git args = %q, want an isolated wb-landing-* remote", gotArgs)
		}
		remoteArgs, err := os.ReadFile(filepath.Join(state.dir, "git-remote-args"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(remoteArgs)) != "remote get-url --push origin" {
			t.Fatalf("remote args = %q", strings.TrimSpace(string(remoteArgs)))
		}
	})
	t.Run("verification read failed", func(t *testing.T) {
		state := orchCovScriptState(t, orchCovDeleteBranchScript)
		orchCovInstallDeleteGit(t, state)
		state.answer(t, "git-exit", "0")
		state.answer(t, "check-exit", "1")
		state.answer(t, "check-stdout", "")
		state.answer(t, "check-stderr", "gh: HTTP 502 Bad Gateway")
		if _, err := deleteRemoteBranch(context.Background(), ".", "acme/app", view, landed, view.Head.SHA); err == nil ||
			!strings.Contains(err.Error(), "verify branch candidate is absent") {
			t.Fatalf("verification error = %v", err)
		}
	})
	t.Run("already gone", func(t *testing.T) {
		state := orchCovScriptState(t, orchCovDeleteBranchScript)
		orchCovInstallDeleteGit(t, state)
		state.answer(t, "git-exit", "1")
		state.answer(t, "git-stderr", "stale info")
		state.answer(t, "check-exit", "1")
		state.answer(t, "check-stdout", "")
		state.answer(t, "check-stderr", "Not Found")
		deleted, err := deleteRemoteBranch(context.Background(), ".", "acme/app", view, landed, view.Head.SHA)
		if err != nil || !deleted {
			t.Fatalf("deleted=%t err=%v", deleted, err)
		}
	})
	t.Run("delete refused", func(t *testing.T) {
		state := orchCovScriptState(t, orchCovDeleteBranchScript)
		orchCovInstallDeleteGit(t, state)
		state.answer(t, "git-exit", "1")
		state.answer(t, "git-stderr", "remote rejected")
		state.answer(t, "check-exit", "0")
		state.answer(t, "check-stdout", `{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
		state.answer(t, "check-stderr", "")
		if _, err := deleteRemoteBranch(context.Background(), ".", "acme/app", view, landed, view.Head.SHA); err == nil ||
			!strings.Contains(err.Error(), "delete branch candidate at merged head") {
			t.Fatalf("refused deletion error = %v", err)
		}
	})
	t.Run("delete failure redacts push URL credentials", func(t *testing.T) {
		state := orchCovScriptState(t, orchCovDeleteBranchScript)
		orchCovInstallDeleteGit(t, state)
		state.answer(t, "git-exit", "1")
		state.answer(t, "git-stderr", "fatal: unable to access https://user:secret@example.test/acme/app.git")
		state.answer(t, "check-exit", "0")
		state.answer(t, "check-stdout", `{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
		state.answer(t, "check-stderr", "")
		_, err := deleteRemoteBranch(context.Background(), ".", "acme/app", view, landed, view.Head.SHA)
		if err == nil {
			t.Fatal("credential-bearing push failure succeeded")
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "user:") {
			t.Fatalf("push credentials leaked: %v", err)
		}
		if !strings.Contains(err.Error(), "<redacted-origin-push-url>") {
			t.Fatalf("redaction marker missing: %v", err)
		}
	})
	t.Run("still present", func(t *testing.T) {
		state := orchCovScriptState(t, orchCovDeleteBranchScript)
		orchCovInstallDeleteGit(t, state)
		state.answer(t, "git-exit", "0")
		state.answer(t, "check-exit", "0")
		state.answer(t, "check-stdout", `{"object":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
		state.answer(t, "check-stderr", "")
		if _, err := deleteRemoteBranch(context.Background(), ".", "acme/app", view, landed, view.Head.SHA); err == nil ||
			!strings.Contains(err.Error(), "still exists on origin") {
			t.Fatalf("unverified deletion error = %v", err)
		}
	})
	t.Run("advanced after merge", func(t *testing.T) {
		state := orchCovScriptState(t, orchCovDeleteBranchScript)
		orchCovInstallDeleteGit(t, state)
		state.answer(t, "git-exit", "1")
		state.answer(t, "git-stderr", "stale info")
		state.answer(t, "check-exit", "0")
		state.answer(t, "check-stdout", `{"object":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}`)
		state.answer(t, "check-stderr", "")
		if _, err := deleteRemoteBranch(context.Background(), ".", "acme/app", view, landed, view.Head.SHA); err == nil ||
			!strings.Contains(err.Error(), "stale info") {
			t.Fatalf("advanced branch error = %v", err)
		}
	})
}

func TestDeleteRemoteBranchIgnoresAPreexistingLandingRemote(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed")
	canonical := filepath.Join(root, "canonical")
	origin := filepath.Join(root, "origin.git")
	malicious := filepath.Join(root, "malicious.git")
	writeEngineFile(t, filepath.Join(seed, "README.md"), "initial\n")
	runEngineGit(t, seed, "init", "-b", "main")
	runEngineGit(t, seed, "config", "user.name", "WB Test")
	runEngineGit(t, seed, "config", "user.email", "wb@example.test")
	runEngineGit(t, seed, "add", "README.md")
	runEngineGit(t, seed, "commit", "-m", "initial")
	runEngineGit(t, root, "clone", "--bare", seed, origin)
	testenv.ConfigureGitAutoMaintenanceOff(t, origin)
	runEngineGit(t, root, "clone", "--bare", seed, malicious)
	testenv.ConfigureGitAutoMaintenanceOff(t, malicious)
	runEngineGit(t, root, "clone", origin, canonical)
	runEngineGit(t, canonical, "config", "user.name", "WB Test")
	runEngineGit(t, canonical, "config", "user.email", "wb@example.test")
	runEngineGit(t, canonical, "checkout", "-b", "candidate")
	writeEngineFile(t, filepath.Join(canonical, "candidate.txt"), "candidate\n")
	runEngineGit(t, canonical, "add", "candidate.txt")
	runEngineGit(t, canonical, "commit", "-m", "candidate")
	head := strings.TrimSpace(runEngineGit(t, canonical, "rev-parse", "HEAD"))
	runEngineGit(t, canonical, "push", "origin", "candidate")
	runEngineGit(t, canonical, "push", malicious, "candidate")
	runEngineGit(t, canonical, "remote", "add", "wb-landing", malicious)
	runEngineGit(t, canonical, "remote", "set-url", "--add", "--push", "wb-landing", malicious)

	state := orchCovScriptState(t, orchCovDeleteBranchScript)
	state.answer(t, "check-exit", "1")
	state.answer(t, "check-stdout", "")
	state.answer(t, "check-stderr", "Not Found")
	view := orchCovPullRequestView(t, fmt.Sprintf(`{"head":{"ref":"candidate","sha":%q,"repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}`, head))
	landed := orchCovPullRequestView(t, `{"base":{"ref":"main"}}`)
	deleted, err := deleteRemoteBranch(context.Background(), canonical, "acme/app", view, landed, head)
	if err != nil || !deleted {
		t.Fatalf("deleted=%t err=%v", deleted, err)
	}
	if got := strings.TrimSpace(runEngineGit(t, canonical, "ls-remote", origin, "refs/heads/candidate")); got != "" {
		t.Fatalf("origin candidate still exists: %s", got)
	}
	if got := strings.TrimSpace(runEngineGit(t, canonical, "ls-remote", malicious, "refs/heads/candidate")); !strings.HasPrefix(got, head+"\t") {
		t.Fatalf("pre-existing wb-landing target was changed: %s", got)
	}
}

func TestOrchCovBranchAlreadyGoneRecognisesBothSpellings(t *testing.T) {
	t.Parallel()
	if !branchAlreadyGone([]byte("Reference does not exist"), nil) {
		t.Fatal("stdout spelling was not recognised")
	}
	if !branchAlreadyGone(nil, []byte("gh: Not Found (HTTP 404)")) {
		t.Fatal("stderr spelling was not recognised")
	}
	if branchAlreadyGone([]byte("HTTP 403 Forbidden"), nil) {
		t.Fatal("an authoritative failure was read as an absent branch")
	}
}

func TestOrchCovLandPreflightRefusalNamesEachBlockedState(t *testing.T) {
	t.Parallel()
	notMergeable := false
	for _, test := range []struct {
		name string
		view PullRequestView
		want string
	}{
		{name: "already merged", view: PullRequestView{Merged: true, MergeCommitSHA: "0123456789abcdef"}, want: LandRefusalNotOpen},
		{name: "closed", view: PullRequestView{State: "closed"}, want: LandRefusalNotOpen},
		{name: "draft", view: PullRequestView{State: "open", Draft: true}, want: LandRefusalDraft},
		{name: "locked", view: PullRequestView{State: "open", Locked: true}, want: LandRefusalLocked},
		{name: "not mergeable", view: PullRequestView{State: "open", Mergeable: &notMergeable, MergeableState: "dirty"}, want: LandRefusalNotMergeable},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			refusal := landPreflightRefusal(test.view, "acme/app", "7")
			if refusal == nil || refusal.code != test.want {
				t.Fatalf("preflight refusal = %+v, want %s", refusal, test.want)
			}
			if refusal.command == "" || refusal.reason == "" {
				t.Fatalf("refusal must name a sanctioned command and a reason: %+v", refusal)
			}
			if !strings.Contains(refusal.command, "acme/app") && !strings.Contains(refusal.command, "wb worktree gc") {
				t.Fatalf("sanctioned command does not address the repository: %q", refusal.command)
			}
		})
	}
	if refusal := landPreflightRefusal(PullRequestView{State: "open"}, "acme/app", "7"); refusal != nil {
		t.Fatalf("landable pull request refused: %+v", refusal)
	}
}

func TestOrchCovPullRequestLandResumeCommandCarriesEveryOption(t *testing.T) {
	t.Parallel()
	options := PullRequestLandOptions{
		Repository: "acme/app", PullRequest: "acme/app#7",
		MergeMethod: "squash", MergeMethodExplicit: true,
		Keep: true, AllowUnfenced: true,
		KeepCommits: []string{"aaa111", "bbb222"},
		Reason:      `has "quotes"`, Subject: "the subject",
		ApprovedBy: "reviewer@example.test",
	}
	got := pullRequestLandResumeCommand(options, "7", "")
	want := `wb pr land acme/app#7 --merge-method squash --keep --allow-unfenced ` +
		`--keep-commits aaa111,bbb222 --reason 'has "quotes"' --subject 'the subject' ` +
		`--approved-by 'reviewer@example.test'`
	if got != want {
		t.Fatalf("resume command =\n%s\nwant\n%s", got, want)
	}
	bare := pullRequestLandResumeCommand(PullRequestLandOptions{Repository: "acme/app"}, "7", "")
	if bare != "wb pr land acme/app#7" {
		t.Fatalf("bare resume command = %q", bare)
	}
	// #584: the checks-pending resume additionally carries a --timeout floor,
	// printed first so the budget is the first thing a caller sees.
	withTimeout := pullRequestLandResumeCommand(PullRequestLandOptions{Repository: "acme/app"}, "7", "45m")
	if withTimeout != "wb pr land acme/app#7 --timeout 45m" {
		t.Fatalf("timeout-carrying resume command = %q", withTimeout)
	}
}

func TestOrchCovWithPullRequestLandResumeGuidanceAnnotatesOnlyTransientFailures(t *testing.T) {
	t.Parallel()
	if got := withPullRequestLandResumeGuidance(nil, PullRequestLandOptions{}, PullRequestLandResult{}); got != nil {
		t.Fatalf("nil error became %v", got)
	}
	plain := errors.New("an authoritative refusal")
	if got := withPullRequestLandResumeGuidance(plain, PullRequestLandOptions{}, PullRequestLandResult{}); !errors.Is(got, plain) {
		t.Fatalf("authoritative error was rewritten to %v", got)
	}
	exhausted := fmt.Errorf("%w: gh api failed after 3 attempts", githubobserver.ErrTransientRetriesExhausted)
	got := withPullRequestLandResumeGuidance(exhausted, PullRequestLandOptions{Repository: "acme/app", PullRequest: "acme/app#7", Keep: true}, PullRequestLandResult{})
	if !errors.Is(got, githubobserver.ErrTransientRetriesExhausted) ||
		!strings.Contains(got.Error(), "resumable: wb pr land acme/app#7 --keep") {
		t.Fatalf("exhausted transient error = %v", got)
	}
	unknownMutation := fmt.Errorf("%w: HTTP 502", githubobserver.ErrTransientMutationOutcomeUnknown)
	got = withPullRequestLandResumeGuidance(unknownMutation, PullRequestLandOptions{Repository: "acme/app", PullRequest: "7"}, PullRequestLandResult{})
	if !errors.Is(got, githubobserver.ErrTransientMutationOutcomeUnknown) ||
		!strings.Contains(got.Error(), "resumable: wb pr land acme/app#7") {
		t.Fatalf("unknown transient mutation error = %v", got)
	}
	// An unaddressable selector cannot name a resume command, so the error is
	// returned unchanged rather than decorated with a guess.
	if got := withPullRequestLandResumeGuidance(exhausted, PullRequestLandOptions{}, PullRequestLandResult{}); strings.Contains(got.Error(), "resumable") {
		t.Fatalf("unaddressable selector gained resume guidance: %v", got)
	}
}

// TestOrchCovWithPullRequestLandResumeGuidancePrePostTransientNeverEchoesReviewText
// proves round 4's second half of B4: a transient GitHub read failure that
// happens BEFORE the identity form's comment is ever posted — in
// ReadPullRequest, pullRequestChangedFiles, or lane acquisition — leaves
// result.ReviewCommentURL empty, so the earlier fix (swap in the posted
// URL) never triggers. The review's own literal text — which can contain a
// backtick or "$(...)" — must still never appear in the printed resume
// command; a placeholder takes its place instead.
func TestOrchCovWithPullRequestLandResumeGuidancePrePostTransientNeverEchoesReviewText(t *testing.T) {
	t.Parallel()
	exhausted := fmt.Errorf("%w: gh api failed after 3 attempts", githubobserver.ErrTransientRetriesExhausted)
	options := PullRequestLandOptions{
		Repository:    "acme/app",
		PullRequest:   "acme/app#7",
		ApprovedBy:    "opus@codex@run-42",
		ReviewComment: "looks good, do not run `rm -rf $HOME` or $(whoami) please",
	}
	// result.ReviewCommentURL is empty: the comment was never posted.
	got := withPullRequestLandResumeGuidance(exhausted, options, PullRequestLandResult{})
	if got == nil {
		t.Fatal("expected a wrapped error")
	}
	message := got.Error()
	for _, fragment := range []string{"looks good", "rm -rf", "whoami", "$HOME", "$(whoami)"} {
		if strings.Contains(message, fragment) {
			t.Fatalf("resume guidance echoed the review text (%q): %q", fragment, message)
		}
	}
	if strings.ContainsRune(message, '`') || strings.ContainsRune(message, '$') {
		t.Fatalf("resume guidance contains an unquoted backtick or $: %q", message)
	}
	if !strings.Contains(message, reviewCommentFilePlaceholder) {
		t.Fatalf("resume guidance = %q, want the review-comment-file placeholder", message)
	}
}

func TestOrchCovLandPullRequestRejectsUnusableOptions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		options PullRequestLandOptions
		wantIn  string
	}{
		{name: "no selector", options: PullRequestLandOptions{Repository: "acme/app"}, wantIn: "pull request selector is required"},
		{name: "no repository", options: PullRequestLandOptions{PullRequest: "7"}, wantIn: "repository is required"},
		{name: "unsupported method", options: PullRequestLandOptions{Repository: "acme/app", PullRequest: "7", MergeMethod: "octopus"}, wantIn: "unsupported merge method"},
		{name: "subject without squash", options: PullRequestLandOptions{Repository: "acme/app", PullRequest: "7", Subject: "a subject"}, wantIn: "--subject requires --merge-method squash"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := LandPullRequest(context.Background(), test.options); err == nil || !strings.Contains(err.Error(), test.wantIn) {
				t.Fatalf("error = %v, want %q", err, test.wantIn)
			}
		})
	}
}

func TestOrchCovWaitForPullRequestLandChecksRejectsAnUnusableBudget(t *testing.T) {
	t.Parallel()
	noop := func(context.Context, PullRequestWaitOptions) (PullRequestWaitResult, error) {
		return PullRequestWaitResult{}, nil
	}
	if _, err := waitForPullRequestLandChecksWith(context.Background(), PullRequestWaitOptions{}, noop); err == nil ||
		!strings.Contains(err.Error(), "timeout must be positive") {
		t.Fatalf("empty budget error = %v", err)
	}
	options := PullRequestWaitOptions{Slice: time.Second, CheckPollInterval: time.Second}
	if _, err := waitForPullRequestLandChecksWith(context.Background(), options, noop); err == nil ||
		!strings.Contains(err.Error(), "poll interval must be shorter") {
		t.Fatalf("poll interval error = %v", err)
	}
}

func TestOrchCovWaitForPullRequestLandChecksDrainsEverySlice(t *testing.T) {
	t.Parallel()
	options := PullRequestWaitOptions{Slice: 20 * time.Minute, CheckPollInterval: time.Millisecond}
	var observed []time.Duration
	waited, err := waitForPullRequestLandChecksWith(context.Background(), options,
		func(_ context.Context, current PullRequestWaitOptions) (PullRequestWaitResult, error) {
			observed = append(observed, current.Slice)
			return PullRequestWaitResult{Status: PullRequestWaitPending, Reason: "still pending"}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{9 * time.Minute, 9 * time.Minute, 2 * time.Minute}
	if len(observed) != 3 || observed[0] != want[0] || observed[1] != want[1] || observed[2] != want[2] {
		t.Fatalf("observed slices = %v, want %v", observed, want)
	}
	if waited.Status != PullRequestWaitPending || waited.Reason != "still pending" {
		t.Fatalf("drained result = %+v", waited)
	}
}

func TestOrchCovWaitForPullRequestLandChecksStopsOnError(t *testing.T) {
	t.Parallel()
	failure := errors.New("the read failed")
	options := PullRequestWaitOptions{Slice: time.Minute, CheckPollInterval: time.Millisecond}
	if _, err := waitForPullRequestLandChecksWith(context.Background(), options,
		func(context.Context, PullRequestWaitOptions) (PullRequestWaitResult, error) {
			return PullRequestWaitResult{}, failure
		}); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
}

func TestOrchCovApproximateTokensSwitchesToThousands(t *testing.T) {
	t.Parallel()
	if got := approximateTokens(999); got != "999" {
		t.Fatalf("sub-thousand estimate = %q", got)
	}
	if got := approximateTokens(12345); got != "12.3k" {
		t.Fatalf("thousand estimate = %q", got)
	}
}

func TestOrchCovLimitStringsReportsTheRemainder(t *testing.T) {
	t.Parallel()
	values := []string{"a", "b", "c"}
	if got := limitStrings(values, 5); len(got) != 3 {
		t.Fatalf("short list = %v", got)
	}
	got := limitStrings(values, 2)
	if len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "and 1 more" {
		t.Fatalf("limited list = %v", got)
	}
}

func TestOrchCovWithSavingsNeverCountsARefusal(t *testing.T) {
	t.Parallel()
	refused := withSavings(PullRequestLandResult{Outcome: LandRefused, AbsorbedPolls: 40})
	if refused.SavedToolCalls != 0 || refused.SavedTokensEstimate != 0 {
		t.Fatalf("refusal savings = %+v", refused)
	}
	// A --keep landing that absorbed nothing still counts as one saved call
	// rather than a negative one.
	kept := withSavings(PullRequestLandResult{Outcome: LandSuccess, Kept: true})
	if kept.SavedToolCalls != 0 || kept.SavedTokensEstimate != 0 {
		t.Fatalf("kept savings = %+v", kept)
	}
	polled := withSavings(PullRequestLandResult{
		Outcome: LandSuccess, AbsorbedPolls: 4, ChangedFiles: []string{"go.mod", "go.sum"},
		Checks: &PullRequestWaitResult{Checks: []RemoteCheck{{Name: "CI", Link: "https://example.test/ci", Bucket: "pass"}}},
	})
	if polled.SavedToolCalls <= 0 || polled.SavedTokensEstimate <= 0 {
		t.Fatalf("absorbed polls produced no savings: %+v", polled)
	}
}

func TestOrchCovAggregatedCommitMessageCarriesCommitBodiesAndProvenance(t *testing.T) {
	t.Parallel()
	view := PullRequestView{Number: 7, Title: "feat: the change", Body: "Summary line.\n\n## Details\nhidden\n"}
	commits := []SourceCommit{
		{SHA: "0123456789abcdef0123456789abcdef01234567", Subject: "change one", Body: "why one changed\n\nCo-Authored-By: someone <x@example.test>\n"},
	}
	message := aggregatedCommitMessage(view, commits, "reviewer@example.test", "the first commit stands alone")
	for _, want := range []string{
		"Summary line.", "Source commits:", "0123456789ab change one", "  why one changed",
		"Commits kept separate because: the first commit stands alone",
		"Pull request: #7", "Review: reviewer@example.test",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("aggregate is missing %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, "Co-Authored-By") || strings.Contains(message, "## Details") {
		t.Fatalf("aggregate leaked trailers or the review template:\n%s", message)
	}
	mechanical := aggregatedCommitMessage(PullRequestView{Number: 7}, nil, "", "")
	if !strings.Contains(mechanical, "mechanical dependency bump") {
		t.Fatalf("mechanical aggregate = %q", mechanical)
	}
}

func TestOrchCovRepositoryOfNamesTheBaseRepository(t *testing.T) {
	t.Parallel()
	view := orchCovPullRequestView(t, `{"head":{"ref":"candidate","repo":{"full_name":"fork/app"}},"base":{"ref":"main","repo":{"full_name":"acme/app"}}}`)
	if got := repositoryOf(view); got != "acme/app" {
		t.Fatalf("repositoryOf = %q, want the base repository", got)
	}
	head := orchCovPullRequestView(t, `{"head":{"repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}`)
	if got := repositoryOf(head); got != "acme/app" {
		t.Fatalf("repositoryOf = %q, want the head repository", got)
	}
	if got := repositoryOf(PullRequestView{}); got != "" {
		t.Fatalf("repositoryOf with no repository = %q", got)
	}
}

const orchCovPagesScript = `#!/bin/sh
S="$ORCHCOV_GH_STATE"
if [ "$1" = api ]; then cat "$S/body"; exit "$(cat "$S/exit")"; fi
echo "unexpected gh args: $*" >&2
exit 30
`

func TestOrchCovPullRequestChangedFilesDropsEmptyAndDuplicateNames(t *testing.T) {
	state := orchCovScriptState(t, orchCovPagesScript)
	state.answer(t, "body", `[{"filename":"b.txt"},{"filename":""},{"filename":"b.txt"},{"filename":"a.txt"}]`)
	state.answer(t, "exit", "0")
	files, err := pullRequestChangedFiles(context.Background(), "acme/app", "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Filename != "a.txt" || files[1].Filename != "b.txt" {
		t.Fatalf("changed files = %+v", files)
	}
}

func TestOrchCovPullRequestChangedFilesFailsClosed(t *testing.T) {
	state := orchCovScriptState(t, orchCovPagesScript)
	state.answer(t, "body", "not json")
	state.answer(t, "exit", "0")
	if _, err := pullRequestChangedFiles(context.Background(), "acme/app", "7"); err == nil ||
		!strings.Contains(err.Error(), "decode changed files for acme/app#7") {
		t.Fatalf("undecodable changed files error = %v", err)
	}
	orchCovInstallGH(t, orchCovNotFound)
	if _, err := pullRequestChangedFiles(context.Background(), "acme/app", "7"); err == nil ||
		!strings.Contains(err.Error(), "read changed files for acme/app#7") {
		t.Fatalf("unreadable changed files error = %v", err)
	}
}

func TestOrchCovPullRequestCommitsSplitsSubjectFromBody(t *testing.T) {
	state := orchCovScriptState(t, orchCovPagesScript)
	state.answer(t, "body", `[{"sha":"aaa","commit":{"message":"the subject\n\nthe body\n"}},{"sha":"bbb","commit":{"message":"only a subject"}}]`)
	state.answer(t, "exit", "0")
	commits, err := pullRequestCommits(context.Background(), "acme/app", "7")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 || commits[0].Subject != "the subject" || commits[0].Body != "the body" ||
		commits[1].Subject != "only a subject" || commits[1].Body != "" {
		t.Fatalf("commits = %+v", commits)
	}
}

func TestOrchCovPullRequestCommitsFailsClosed(t *testing.T) {
	state := orchCovScriptState(t, orchCovPagesScript)
	state.answer(t, "body", "not json")
	state.answer(t, "exit", "0")
	if _, err := pullRequestCommits(context.Background(), "acme/app", "7"); err == nil ||
		!strings.Contains(err.Error(), "decode commits of acme/app#7") {
		t.Fatalf("undecodable commits error = %v", err)
	}
	orchCovInstallGH(t, orchCovNotFound)
	if _, err := pullRequestCommits(context.Background(), "acme/app", "7"); err == nil ||
		!strings.Contains(err.Error(), "read commits of acme/app#7") {
		t.Fatalf("unreadable commits error = %v", err)
	}
}

func TestOrchCovAppendLandEventRecordsARefusalWithItsReason(t *testing.T) {
	t.Parallel()
	recorder := &recordingEvents{}
	options := PullRequestLandOptions{
		Repository: "acme/app", PullRequest: "7", Stream: "stream-1", Events: recorder,
	}
	result := PullRequestLandResult{
		Outcome: LandRefused, RefusalCode: LandRefusalDraft,
		Reason: "pull request is a draft", HeadSHA: "0123456789abcdef",
		KeptCommits: []string{"aaa111"}, CleanedTasks: []string{"task-1"},
		MergeSHA: "fedcba9876543210", ApprovedBy: "reviewer",
	}
	appendLandEvent(options, result, time.Now(), nil)
	if len(recorder.events) != 1 {
		t.Fatalf("recorded events = %d", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Outcome != string(LandRefused) || event.RefusalCode != LandRefusalDraft ||
		event.Evidence["pull_request"] != "acme/app#7" || event.Evidence["kept_commits"] != "aaa111" ||
		event.Evidence["cleaned_tasks"] != "task-1" || event.Evidence["merge_commit"] != "fedcba9876543210" ||
		event.Evidence["approved_by"] != "reviewer" {
		t.Fatalf("recorded event = %+v", event)
	}
	// An error that reached the caller replaces the detail and the outcome.
	appendLandEvent(options, PullRequestLandResult{}, time.Now(), errors.New("the read failed"))
	if len(recorder.events) != 2 || recorder.events[1].Outcome != string(LandFindings) ||
		recorder.events[1].Detail != "the read failed" {
		t.Fatalf("recorded error event = %+v", recorder.events)
	}
	// A nil appender discards.
	appendLandEvent(PullRequestLandOptions{}, result, time.Now(), nil)
}

func TestOrchCovRefuseLinkedWorktreeIgnoresAnUnlinkedCheckout(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	checkout := filepath.Join(t.TempDir(), "checkout")
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	refusal := refuseLinkedWorktree(projectsRoot, worktrees.ListResult{
		Task: "unlinked-task", Repository: "acme/app", Branch: "wb/keep/candidate", WorktreeDir: checkout,
	})
	if refusal != nil {
		t.Fatalf("unlinked checkout refused: %+v", refusal)
	}
}

func TestOrchCovPreflightLandingCleanupRefusesAnUnreadableInventory(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	view := orchCovPullRequestView(t, `{"head":{"ref":"candidate"},"base":{"ref":"main"}}`)
	refusal := preflightLandingCleanup(context.Background(), PullRequestLandOptions{
		Repository: "acme/app", ProjectsRoot: file,
	}, view, "7", true)
	if refusal == nil || refusal.code != "cleanup-unverifiable" {
		t.Fatalf("unreadable inventory refusal = %+v", refusal)
	}
}

func TestOrchCovChecksFailedSanctionedCommandBuildsAGHCommandFromAnActionsJobURL(t *testing.T) {
	t.Parallel()
	got := checksFailedSanctionedCommand([]CIFailureDetail{
		{Check: "CI", JobURL: "https://github.com/acme/app/actions/runs/123/job/456"},
	}, "acme/app", "7")
	want := "gh run view 123 --job 456 --repo acme/app --log-failed"
	if got != want {
		t.Fatalf("checksFailedSanctionedCommand = %q, want %q", got, want)
	}
}

func TestOrchCovChecksFailedSanctionedCommandNeverEmitsAProviderURL(t *testing.T) {
	t.Parallel()
	// A commit status's Link is the provider-controlled TargetURL, which is
	// not necessarily a GitHub Actions job URL (#584 round 3, minor 8). The
	// sanctioned command must never be a bare URL.
	got := checksFailedSanctionedCommand([]CIFailureDetail{
		{Check: "sonar", JobURL: "https://sonar.example.test/dashboard?id=acme_app"},
	}, "acme/app", "7")
	want := "gh pr view 7 --repo acme/app --web"
	if got != want {
		t.Fatalf("checksFailedSanctionedCommand = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, "http") {
		t.Fatalf("checksFailedSanctionedCommand must never be a bare URL: %q", got)
	}
}

func TestOrchCovChecksFailedSanctionedCommandFallsBackWhenThereAreNoDetails(t *testing.T) {
	t.Parallel()
	got := checksFailedSanctionedCommand(nil, "acme/app", "7")
	want := "gh pr view 7 --repo acme/app --web"
	if got != want {
		t.Fatalf("checksFailedSanctionedCommand = %q, want %q", got, want)
	}
}
