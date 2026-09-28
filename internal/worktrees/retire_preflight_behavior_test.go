package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestRetireRejectsInvalidRequestsBeforeFindingACheckout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, tc := range []struct {
		name   string
		change func(*RetireOptions)
		want   string
	}{
		{"unsupported preservation", func(o *RetireOptions) { o.Preserve = "copy" }, "unsupported --preserve"},
		{"unsafe task", func(o *RetireOptions) { o.Task = "../escape" }, "invalid retirement task"},
		{"invalid repository", func(o *RetireOptions) { o.Repository = "bad-repository" }, "must be owner/name"},
		{"missing checkout", func(*RetireOptions) {}, "requires exactly one managed repository"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := RetireOptions{ProjectsRoot: root, Task: "missing-task"}
			tc.change(&options)
			if _, err := Retire(context.Background(), options); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Retire error = %v, want %q", err, tc.want)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture sets process-wide WB and Git configuration variables.
func TestRetirePreflightRefusesMissingAuthorityAndPullRequestUncertainty(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retire-preflight", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	before := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	options := RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-preflight", Apply: true,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	}
	for _, tc := range []struct {
		name   string
		change func(*RetireOptions)
		want   string
	}{
		{"missing remote owner check", func(*RetireOptions) {}, "authoritative remote owner check"},
		{"remote owner refusal", func(o *RetireOptions) {
			o.RemoteOwnership = func(context.Context, string) error { return errors.New("remote owner unavailable") }
		}, "remote owner unavailable"},
		{"pull request query failure", func(o *RetireOptions) {
			o.RemoteOwnership = retireAllowRemoteOwner
			o.OpenPullRequests = func(context.Context, string, string, string) (bool, error) {
				return false, errors.New("pull request inventory unavailable")
			}
		}, "pull request inventory unavailable"},
		{"open pull request", func(o *RetireOptions) {
			o.RemoteOwnership = retireAllowRemoteOwner
			o.OpenPullRequests = func(context.Context, string, string, string) (bool, error) { return true, nil }
		}, "has an open pull request"},
	} {
		//nolint:paralleltest // Cases deliberately reuse one unchanged checkout and process environment.
		t.Run(tc.name, func(t *testing.T) {
			attempt := options
			tc.change(&attempt)
			if _, err := Retire(context.Background(), attempt); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Retire error = %v, want %q", err, tc.want)
			}
			if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != before {
				t.Fatalf("refusal moved checkout from %s to %s", before, got)
			}
			if _, err := os.Stat(retireReportPath(fixture.home, RetireResult{Task: options.Task, Repository: "acme/app"})); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refusal published retirement report: %v", err)
			}
		})
	}
}

func TestRetirementReceiptRejectsInvalidOrSubstitutedEvidence(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	date := time.Date(2026, time.September, 28, 0, 0, 0, 0, time.UTC)
	base := RetireResult{Version: 1, Task: "retire-task", Repository: "acme/app", Branch: "retire-task", SourceSHA: sha, Phase: "planned"}
	base.RetiredRef = retiredBranchDestination(date, base.Branch, sha)
	base.ArchiveRef = retireArchiveRef(base)
	for _, tc := range []struct {
		name   string
		change func(*RetireResult)
		want   string
	}{
		{"valid", func(*RetireResult) {}, ""},
		{"unsupported version", func(r *RetireResult) { r.Version = 2 }, "invalid retirement receipt"},
		{"non-object source", func(r *RetireResult) { r.SourceSHA = "not-a-commit" }, "invalid retirement receipt"},
		{"unknown preservation", func(r *RetireResult) { r.Preserve = "copy" }, "invalid retirement receipt"},
		{"unbound deletion intent", func(r *RetireResult) { r.DeleteIntentSHA = sha }, "invalid retirement original-ref deletion intent"},
		{"deleted without intent", func(r *RetireResult) { r.Phase = "original_deleted"; r.OriginalRemoteSHA = sha }, "no durable intent"},
		{"invalid commit intent", func(r *RetireResult) { r.Phase = "commit_intent" }, "invalid retirement commit intent"},
		{"wrong ref namespace", func(r *RetireResult) { r.RetiredRef = "other/ref" }, "invalid retirement receipt"},
		{"wrong archive ref", func(r *RetireResult) { r.ArchiveRef = "retired/another" }, "invalid retirement receipt"},
		{"invalid retired date", func(r *RetireResult) { r.RetiredRef = "retired/x"; r.ArchiveRef = retireArchiveRef(*r) }, "invalid retired ref date"},
		{"unparseable retired date", func(r *RetireResult) {
			r.RetiredRef = "retired/00000000-task-a"
			r.ArchiveRef = retireArchiveRef(*r)
		}, "does not bind branch and commit"},
		{"ref for another branch", func(r *RetireResult) { r.Branch = "another" }, "does not bind branch and commit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "receipt.json")
			receipt := base
			tc.change(&receipt)
			body, err := json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := readRetireReport(path)
			if tc.want == "" {
				if err != nil || got.SourceSHA != sha {
					t.Fatalf("valid receipt = (%+v, %v)", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("readRetireReport error = %v, want %q", err, tc.want)
			}
		})
	}
	t.Run("malformed JSON", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "receipt.json")
		if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRetireReport(path); err == nil {
			t.Fatal("malformed receipt was accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		target := filepath.Join(dir, "target.json")
		if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "receipt.json")
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := readRetireReport(path); err == nil {
			t.Fatal("symlinked receipt was accepted")
		}
	})
	t.Run("oversized file", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "receipt.json")
		if err := os.WriteFile(path, make([]byte, (1<<20)+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readRetireReport(path); err == nil || !strings.Contains(err.Error(), "invalid retirement receipt file") {
			t.Fatalf("oversized receipt error = %v", err)
		}
	})
	t.Run("valid commit intent", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "receipt.json")
		intent := base
		intent.Phase = "commit_intent"
		intent.IntentParentSHA = sha
		intent.IntentTreeSHA = sha
		intent.IntentMessage = "record pending source commit"
		intent.IntentAt = date
		intent.RetiredRef = ""
		intent.ArchiveRef = ""
		body, err := json.Marshal(intent)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := readRetireReport(path); err != nil || got.IntentMessage != intent.IntentMessage {
			t.Fatalf("durable commit intent = (%+v, %v)", got, err)
		}
	})
}

func TestRetireCommitIntentRequiresExactCommitAndCleanCheckout(t *testing.T) {
	t.Parallel()
	const worktree = "/fixture/retiring-worktree"
	const parent = "1111111111111111111111111111111111111111"
	const tree = "2222222222222222222222222222222222222222"
	const head = "3333333333333333333333333333333333333333"
	intent := RetireResult{IntentParentSHA: parent, IntentTreeSHA: tree, IntentMessage: "retire source"}
	for _, tc := range []struct {
		name, observedParent, observedTree, observedMessage, status, want string
		failCall                                                          int
	}{
		{name: "valid", observedParent: parent, observedTree: tree, observedMessage: intent.IntentMessage},
		{name: "parent changed", observedParent: head, observedTree: tree, observedMessage: intent.IntentMessage, want: "parent does not match"},
		{name: "tree changed", observedParent: parent, observedTree: head, observedMessage: intent.IntentMessage, want: "tree does not match"},
		{name: "message changed", observedParent: parent, observedTree: tree, observedMessage: "other message", want: "message does not match"},
		{name: "checkout dirty", observedParent: parent, observedTree: tree, observedMessage: intent.IntentMessage, status: " M pending.txt", want: "checkout changed"},
		{name: "parent query fails", observedParent: parent, observedTree: tree, observedMessage: intent.IntentMessage, failCall: 1, want: "parent does not match"},
		{name: "tree query fails", observedParent: parent, observedTree: tree, observedMessage: intent.IntentMessage, failCall: 2, want: "tree does not match"},
		{name: "message query fails", observedParent: parent, observedTree: tree, observedMessage: intent.IntentMessage, failCall: 3, want: "message does not match"},
		{name: "status query fails", observedParent: parent, observedTree: tree, observedMessage: intent.IntentMessage, failCall: 4, want: "checkout changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := runnertest.New(t)
			for _, command := range []struct {
				args   []string
				output string
			}{
				{[]string{"rev-parse", "HEAD^"}, tc.observedParent},
				{[]string{"rev-parse", "HEAD^{tree}"}, tc.observedTree},
				{[]string{"log", "-1", "--format=%B"}, tc.observedMessage},
				{[]string{"status", "--porcelain=v1"}, tc.status},
			} {
				argv := append([]string{"git", "-C", worktree}, command.args...)
				fake.ExpectArgv(argv, runner.Result{CombinedOutput: command.output + "\n"}, nil)
			}
			if tc.failCall != 0 {
				fake.FailCall(tc.failCall, errors.New("injected Git failure"))
			}
			err := retireValidateIntentCommit(withGitRunner(context.Background(), fake), worktree, intent, head)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid retirement intent rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("retireValidateIntentCommit error = %v, want %q", err, tc.want)
			}
		})
	}
	if err := retireValidateIntentCommit(context.Background(), worktree, intent, parent); err == nil || !strings.Contains(err.Error(), "was not created") {
		t.Fatalf("uncommitted intent error = %v", err)
	}
}

//nolint:paralleltest // The fake gh executable is selected through process-wide PATH and case environment.
func TestRetireDefaultPullRequestCheckIncludesHeadAndBaseUse(t *testing.T) {
	bin := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(bin, "gh"), []byte(`#!/bin/sh
if [ "$WB_TEST_GH_FAIL" = 1 ]; then
  echo 'query failed' >&2
  exit 1
fi
case "$*" in
  *head=*) printf '%s' "$WB_TEST_GH_HEAD" ;;
  *base=*)
    if [ "$WB_TEST_GH_BASE_FAIL" = 1 ]; then
      echo 'base query failed' >&2
      exit 1
    fi
    printf '%s' "$WB_TEST_GH_BASE" ;;
  *) exit 2 ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	worktree := t.TempDir()
	for _, tc := range []struct {
		name, head, base string
		fail, baseFail   bool
		wantOpen         bool
		wantError        string
	}{
		{name: "query failure", fail: true, wantError: "exit status 1"},
		{name: "malformed head response", head: "{", wantError: "unexpected end of JSON input"},
		{name: "open head pull request", head: `[{"state":"open"}]`, wantOpen: true},
		{name: "closed head and open base", head: `[{"state":"closed"}]`, base: `[{"state":"open","base":{"ref":"retire-task"}}]`, wantOpen: true},
		{name: "no open pull request", head: `[{"state":"closed"}]`, base: `[]`},
		{name: "malformed base response", head: `[]`, base: `{`, wantError: "decode open pull requests with base"},
		{name: "base query failure", head: `[]`, baseFail: true, wantError: "query open pull requests with base"},
	} {
		//nolint:paralleltest // Every case changes process-wide fake-gh response variables.
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WB_TEST_GH_HEAD", tc.head)
			t.Setenv("WB_TEST_GH_BASE", tc.base)
			if tc.fail {
				t.Setenv("WB_TEST_GH_FAIL", "1")
			} else {
				t.Setenv("WB_TEST_GH_FAIL", "0")
			}
			if tc.baseFail {
				t.Setenv("WB_TEST_GH_BASE_FAIL", "1")
			} else {
				t.Setenv("WB_TEST_GH_BASE_FAIL", "0")
			}
			open, err := retireOpenPullRequests(context.Background(), worktree, "acme/app", "retire-task")
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("pull request query error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil || open != tc.wantOpen {
				t.Fatalf("pull request query = (%t, %v), want (%t, nil)", open, err, tc.wantOpen)
			}
		})
	}
	if open, err := retireOpenPullRequests(context.Background(), worktree, "invalid-repository", "retire-task"); err == nil || open {
		t.Fatalf("invalid repository = (%t, %v), want refusal", open, err)
	}
}

func TestRetireRechecksOwnerAndArchiveBeforeMutation(t *testing.T) {
	t.Parallel()
	entry := ListResult{Owners: []OwnerView{{OwnerRegistration: OwnerRegistration{PID: os.Getpid() + 100000}, PIDStatus: "active"}}}
	if err := retireCheckOwner(entry); err == nil || !strings.Contains(err.Error(), "competing active claim") {
		t.Fatalf("active competing owner error = %v", err)
	}
	if err := retireCheckOwner(ListResult{}); err != nil {
		t.Fatalf("owner-free worktree was refused: %v", err)
	}
	result := RetireResult{Repository: "acme/app", ArchiveRepository: "wrong/archive"}
	inspect := func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
		return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
	}
	if err := retireCheckPrivateArchive(context.Background(), result, inspect); err == nil || !strings.Contains(err.Error(), "no longer the configured private repository") {
		t.Fatalf("changed archive binding error = %v", err)
	}
}

func TestRemovedRetirementResumeRequiresOneMatchingDurableReceipt(t *testing.T) {
	t.Parallel()
	sha := strings.Repeat("a", 40)
	date := time.Date(2026, time.September, 28, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name       string
		receipts   int
		allReports bool
		change     func(*RetireResult)
		wantDetail string
	}{
		{name: "no report directory", wantDetail: "no managed checkout or retirement receipt"},
		{name: "empty report directory", receipts: -1, wantDetail: "exactly one receipt"},
		{name: "two reports", receipts: 2, allReports: true, wantDetail: "exactly one receipt"},
		{name: "repository filter selects one report", receipts: 2, wantDetail: "no such file"},
		{name: "repository mismatch", receipts: 1, change: func(r *RetireResult) { r.Repository = "acme/other" }, wantDetail: "repository mismatch"},
		{name: "task mismatch", receipts: 1, change: func(r *RetireResult) { r.Task = "other-task" }, wantDetail: "does not authorize removed-checkout resume"},
		{name: "phase not removed", receipts: 1, change: func(r *RetireResult) { r.Phase = "planned" }, wantDetail: "does not authorize removed-checkout resume"},
		{name: "missing immutable claim", receipts: 1, wantDetail: "no such file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			dir := filepath.Join(home, "reports", "worktree-retire", "retire-task")
			if tc.receipts != 0 {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < tc.receipts; i++ {
				receipt := RetireResult{Version: 1, Task: "retire-task", Repository: "acme/app", Branch: "retire-task",
					SourceSHA: sha, Phase: "complete", EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("b", 64)}
				receipt.RetiredRef = retiredBranchDestination(date, receipt.Branch, sha)
				receipt.ArchiveRef = retireArchiveRef(receipt)
				receipt.ReportPath = filepath.Join(dir, "acme-app.json")
				if i == 1 {
					receipt.ReportPath = filepath.Join(dir, "acme-second.json")
				}
				if tc.change != nil {
					tc.change(&receipt)
					receipt.ArchiveRef = retireArchiveRef(receipt)
				}
				body, err := json.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(receipt.ReportPath, body, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			options := RetireOptions{Task: "retire-task", Repository: "acme/app"}
			if tc.allReports {
				options.Repository = ""
			}
			selected, err := retireResumeRemoved(context.Background(), home, options)
			if err == nil || !strings.Contains(err.Error(), tc.wantDetail) {
				t.Fatalf("removed-checkout resume error = %v, want %q", err, tc.wantDetail)
			}
			if tc.name == "repository filter selects one report" && (selected.Repository != options.Repository || selected.ReportPath != filepath.Join(dir, "acme-app.json")) {
				t.Fatalf("repository filter selected receipt = %+v", selected)
			}
		})
	}
}
