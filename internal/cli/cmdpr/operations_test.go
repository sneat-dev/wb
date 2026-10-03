package cmdpr

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/orchestrate"
	progresspkg "github.com/sneat-dev/wb/internal/progress"
	"github.com/sneat-dev/wb/internal/streams"
)

func TestCreateThreadsCurrentRuntimeFlagsContextCallbacksAndOptions(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	flags := shared.Flags{ProjectsRoot: "before"}
	runtime := testRuntime()
	runtime.Flags = func() shared.Flags { return flags }
	type contextKey struct{}
	ctx := context.WithValue(t.Context(), contextKey{}, "context")
	var requested orchestrate.PullRequestCreateOptions
	var suggestion, checks, events, lane int
	deps.SuggestCloses = func(c context.Context, root, arg string) []int {
		if c != ctx || root != "current" || arg != "task" {
			t.Fatalf("suggestion inputs %v %s %s", c, root, arg)
		}
		suggestion++
		return []int{7, 9}
	}
	deps.CheckRepository = func(root, repo string) error {
		if root != "current" || repo != "acme/app" {
			t.Fatal(root, repo)
		}
		checks++
		return nil
	}
	deps.Events = func(root, repo string) (streams.EventAppender, string) {
		events++
		return streams.DiscardEvents{}, "named"
	}
	deps.Lane = func(root, command, reason string, takeover bool) orchestrate.LaneGuardRequest {
		lane++
		if root != "current" || command != "wb pr create --land" {
			t.Fatal(root, command)
		}
		return orchestrate.LaneGuardRequest{TakeoverReason: "lane"}
	}
	deps.Create = func(c context.Context, o orchestrate.PullRequestCreateOptions) (orchestrate.PullRequestCreateResult, error) {
		if c != ctx {
			t.Fatal("context")
		}
		requested = o
		if err := o.LinkPreflight("acme/app"); err != nil {
			t.Fatal(err)
		}
		_, name := o.EventsForRepository("acme/app")
		if name != "named" {
			t.Fatal(name)
		}
		return orchestrate.PullRequestCreateResult{Outcome: orchestrate.CreateSuccess}, nil
	}
	command := NewCreate(runtime, deps)
	flags.ProjectsRoot = "current"
	command.SetContext(ctx)
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs([]string{"task", "--land", "--keep", "--auto-merge", "--title", "title", "--body", "body", "--base", "release", "--add", "one,two", "-m", "message", "--closes", "5,6,5", "--approved-by", "review", "--allow-unfenced", "--merge-method", "squash", "--timeout", "8m", "--review-comment", "text"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if requested.Worktree != "task" || requested.ProjectsRoot != "current" || requested.Title != "title" || requested.Body != "body" || requested.Base != "release" || requested.Message != "message" || !requested.Land || !requested.AutoMerge || !requested.AllowUnfenced || requested.ApprovedBy != "review" || requested.MergeMethod != "squash" || !reflect.DeepEqual(requested.Add, []string{"one", "two"}) || !reflect.DeepEqual(requested.Closes, []int{5, 6}) {
		t.Fatalf("options %+v", requested)
	}
	if requested.LandOptions == nil || !requested.LandOptions.Keep || requested.LandOptions.Slice != 8*time.Minute || requested.LandOptions.ReviewComment != "text" || requested.LandOptions.CheckoutUpdated == nil || requested.LandOptions.OperationProgress == nil || requested.LandOptions.Progress == nil {
		t.Fatalf("land %+v", requested.LandOptions)
	}
	if suggestion != 1 || checks != 1 || events != 1 || lane != 1 {
		t.Fatal(suggestion, checks, events, lane)
	}
	if errOut.Len() != 0 {
		t.Fatal("explicit closes must suppress suggestion", errOut.String())
	}
}
func TestCreateAndLandDefaultsAndPerInstanceIsolation(t *testing.T) {
	t.Parallel()
	for _, root := range []string{"one", "two"} {
		deps := testDependencies()
		runtime := testRuntime()
		runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: root, Quiet: true} }
		deps.SuggestCloses = func(context.Context, string, string) []int { t.Fatal("quiet read prompt"); return nil }
		deps.Create = func(_ context.Context, o orchestrate.PullRequestCreateOptions) (orchestrate.PullRequestCreateResult, error) {
			if o.ProjectsRoot != root || o.Worktree != "" || o.Title != "" || o.Body != "" || o.BodyFile != "" || o.Draft || o.AutoMerge || o.Land || o.CommitAll || o.CommitStaged || o.LandOptions != nil || o.Lane.Owner.WBSessionID != "" || o.Add != nil || len(o.Closes) != 0 || o.MergeMethod != "merge" {
				t.Fatalf("defaults %+v", o)
			}
			return orchestrate.PullRequestCreateResult{Outcome: orchestrate.CreateSuccess}, nil
		}
		var out, errOut bytes.Buffer
		if code := executeTest(NewCreate(runtime, deps), nil, &out, &errOut); code != 0 {
			t.Fatal(code, errOut.String())
		}
		deps.Land = func(_ context.Context, o orchestrate.PullRequestLandOptions) (orchestrate.PullRequestLandResult, error) {
			if o.ProjectsRoot != root || o.Repository != "acme/app" || o.PullRequest != "2" || o.MergeMethod != "merge" || o.MergeMethodExplicit || o.Keep || o.NoAutoMerge || o.NoUpdateBranch || o.AllowUnfenced || o.Slice != shared.DefaultCIWaitSlice || o.CheckPollInterval != orchestrate.DefaultCheckPollInterval || len(o.KeepCommits) != 0 {
				t.Fatalf("defaults %+v", o)
			}
			return orchestrate.PullRequestLandResult{Outcome: orchestrate.LandSuccess}, nil
		}
		if code := executeTest(NewLand(runtime, deps), []string{"acme/app#2"}, &out, &errOut); code != 0 {
			t.Fatal(code, errOut.String())
		}
		if errOut.Len() != 0 {
			t.Fatal("quiet progress", errOut.String())
		}
	}
}
func TestInvalidCreateAndLandRequestsNeverCallOperations(t *testing.T) {
	t.Parallel()
	cases := [][]string{{"create", "extra", "arg"}, {"create", "--format", "yaml"}, {"create", "--draft", "--land"}, {"create", "--commit-all"}, {"create", "--commit-staged", "--commit-all", "-m", "x"}, {"create", "--review-comment", "x", "--review-comment-file", "x"}, {"create", "--review-comment-file", "x"}, {"create", "--closes", "zero"}, {"land"}, {"land", "repo#3"}, {"land", "acme/app#3", "--format", "bad"}, {"land", "acme/app#3", "--review-comment", "x", "--review-comment-file", "x"}, {"land", "acme/app#3", "--keep-commits", "a", "--merge-method", "merge"}, {"update", "invalid"}, {"update", "acme/app#3", "--format", "bad"}}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			deps.Create = func(context.Context, orchestrate.PullRequestCreateOptions) (orchestrate.PullRequestCreateResult, error) {
				t.Fatal("create called")
				return orchestrate.PullRequestCreateResult{}, nil
			}
			deps.Land = func(context.Context, orchestrate.PullRequestLandOptions) (orchestrate.PullRequestLandResult, error) {
				t.Fatal("land called")
				return orchestrate.PullRequestLandResult{}, nil
			}
			deps.Update = func(context.Context, orchestrate.PullRequestUpdateOptions) (orchestrate.PullRequestUpdateResult, error) {
				t.Fatal("update called")
				return orchestrate.PullRequestUpdateResult{}, nil
			}
			var out, errOut bytes.Buffer
			if code := executeTest(New(testRuntime(), deps), args, &out, &errOut); code == 0 {
				t.Fatal("accepted invalid args")
			}
		})
	}
}
func TestCreatePartialResultsAndWriterErrorsPrecedeOperationError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("pushed but arm failed")
	for _, format := range []string{"text", "json"} {
		for _, land := range []bool{false, true} {
			deps := testDependencies()
			deps.Create = func(context.Context, orchestrate.PullRequestCreateOptions) (orchestrate.PullRequestCreateResult, error) {
				result := orchestrate.PullRequestCreateResult{Outcome: orchestrate.CreateFindings, CommittedPaths: []string{"x"}, Reason: "partial"}
				if land {
					result.LandResult = &orchestrate.PullRequestLandResult{Outcome: orchestrate.LandFindings, Reason: "pending"}
				}
				return result, sentinel
			}
			args := []string{"--format", format}
			var out, errOut bytes.Buffer
			if code := executeTest(NewCreate(testRuntime(), deps), args, &out, &errOut); code != 1 || out.Len() == 0 || !strings.Contains(errOut.String(), sentinel.Error()) {
				t.Fatal(code, out.String(), errOut.String())
			}
			c := NewCreate(testRuntime(), deps)
			c.SetOut(failingWriter{})
			c.SetErr(io.Discard)
			c.SilenceUsage = true
			c.SetArgs(args)
			if err := c.Execute(); err == nil || err.Error() != "write failed" {
				t.Fatalf("writer precedence %v", err)
			}
		}
	}
}
func TestCreateOutcomeCodesAndSuggestion(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		outcome orchestrate.CreateOutcome
		code    int
	}{{orchestrate.CreateSuccess, 0}, {orchestrate.CreateRefused, 2}, {orchestrate.CreateFindings, 1}, {orchestrate.CreateLandedIncomplete, 3}} {
		deps := testDependencies()
		deps.SuggestCloses = func(context.Context, string, string) []int { return []int{2, 3} }
		deps.Create = func(context.Context, orchestrate.PullRequestCreateOptions) (orchestrate.PullRequestCreateResult, error) {
			return orchestrate.PullRequestCreateResult{Outcome: test.outcome, Reason: "reason", SanctionedCommand: "fix", LandResult: &orchestrate.PullRequestLandResult{ResumeCommand: "resume"}}, nil
		}
		var out, errOut bytes.Buffer
		if code := executeTest(NewCreate(testRuntime(), deps), nil, &out, &errOut); code != test.code || !strings.Contains(errOut.String(), "#2, #3") {
			t.Fatal(code, errOut.String())
		}
	}
}
func TestLandOperationErrorsOptionsAndOutputBranches(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("land failed")
	for _, kind := range []string{"guard", "operation", "json-write", "text-write", "footer-write", "usage", "findings", "json", "success"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			deps.Interactive = func(io.Writer, bool) bool { return true }
			deps.IsTerminal = func(io.Writer) bool { return true }
			deps.CheckRepository = func(string, string) error {
				if kind == "guard" {
					return sentinel
				}
				return nil
			}
			deps.Land = func(_ context.Context, o orchestrate.PullRequestLandOptions) (orchestrate.PullRequestLandResult, error) {
				if o.Subject != "subject" || o.Reason != "reason" || !o.NoUpdateBranch || !o.NoAutoMerge || !o.MergeMethodExplicit || !o.Keep || o.ReviewCommentFile != "file" || !reflect.DeepEqual(o.KeepCommits, []string{"one", "two"}) {
					t.Fatal(o)
				}
				if kind == "operation" {
					return orchestrate.PullRequestLandResult{}, sentinel
				}
				outcome := orchestrate.LandSuccess
				if kind == "usage" {
					outcome = orchestrate.LandRefused
				}
				if kind == "findings" {
					outcome = orchestrate.LandFindings
				}
				return orchestrate.PullRequestLandResult{Outcome: outcome, Reason: "reason", SanctionedCommand: "fix"}, nil
			}
			args := []string{"acme/app#2", "--merge-method", "squash", "--keep-commits", "one,two", "--reason", "reason", "--subject", "subject", "--no-update-branch", "--no-auto-merge", "--keep", "--review-comment-file", "file", "--take-over-lane", "--lane-reason", "why"}
			if kind == "json" || kind == "json-write" {
				args = append(args, "--format", "json")
			}
			c := NewLand(testRuntime(), deps)
			var out, errOut bytes.Buffer
			c.SetOut(&out)
			if kind == "json-write" || kind == "text-write" {
				c.SetOut(failingWriter{})
			}
			if kind == "footer-write" {
				c.SetOut(&failAtCallWriter{failAt: 3})
			}
			c.SetErr(&errOut)
			c.SilenceUsage = true
			c.SilenceErrors = true
			c.SetArgs(args)
			err := c.Execute()
			if kind == "guard" || kind == "operation" {
				if err != sentinel {
					t.Fatal(err)
				}
			} else if strings.HasSuffix(kind, "write") {
				if err == nil {
					t.Fatal("write swallowed")
				}
			} else if kind == "usage" || kind == "findings" {
				if err == nil {
					t.Fatal("outcome swallowed")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestUpdateMissingReceiptAndWriterFailure(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"text", "json"} {
		deps := testDependencies()
		sentinel := errors.New("update failed")
		deps.Update = func(context.Context, orchestrate.PullRequestUpdateOptions) (orchestrate.PullRequestUpdateResult, error) {
			return orchestrate.PullRequestUpdateResult{}, sentinel
		}
		var out, errOut bytes.Buffer
		if code := executeTest(NewUpdate(testRuntime(), deps), []string{"acme/app#2", "--format", format}, &out, &errOut); code != 1 || out.Len() != 0 {
			t.Fatal(code, out.String())
		}
		deps.Update = func(context.Context, orchestrate.PullRequestUpdateOptions) (orchestrate.PullRequestUpdateResult, error) {
			return orchestrate.PullRequestUpdateResult{ReceiptPath: "receipt"}, nil
		}
		c := NewUpdate(testRuntime(), deps)
		c.SetOut(failingWriter{})
		c.SetErr(io.Discard)
		c.SilenceUsage = true
		c.SetArgs([]string{"acme/app#2", "--format", format})
		if err := c.Execute(); err == nil || err.Error() != "write failed" {
			t.Fatal(err)
		}
	}
}
func TestLandingProgressIsSilentUnderQuiet(t *testing.T) {
	t.Parallel()
	for _, quiet := range []bool{false, true} {
		runtime := testRuntime()
		runtime.Flags = func() shared.Flags { return shared.Flags{Quiet: quiet} }
		var out bytes.Buffer
		command := outputCommand(io.Discard)
		command.SetErr(&out)
		progress := landingProgress(runtime, testDependencies(), command, true)
		progress.Start("acme/app", "7", "", "")
		progress.Update("started")
		progress.Report(orchestrate.PullRequestWaitProgress{Observation: 1})
		progress.OperationReporter("pr land")(progresspkg.Event{Phase: "merge"})
		progress.FinishOperation("finished")
		progress.Fail(io.EOF)
		if (out.Len() == 0) != quiet {
			t.Fatalf("quiet=%t output=%q", quiet, out.String())
		}
	}
}
func TestPRCreateOffersNoClosesSuggestionUnderQuiet(t *testing.T) {
	t.Parallel()
	for _, quiet := range []bool{false, true} {
		runtime := testRuntime()
		runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: "root", Quiet: quiet} }
		deps := testDependencies()
		calls := 0
		deps.SuggestCloses = func(context.Context, string, string) []int { calls++; return nil }
		if got := suggestedCloses(runtime, deps, t.Context(), "unreadable"); got != nil || calls != map[bool]int{false: 1, true: 0}[quiet] {
			t.Fatal(quiet, got, calls)
		}
	}
}
