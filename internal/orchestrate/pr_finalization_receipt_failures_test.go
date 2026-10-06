package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/streams"
)

func TestPRFinalizationEventsAndNestedFailuresPreserveReceipts(t *testing.T) {
	t.Parallel()
	t.Run("empty_event_outcome", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "events.jsonl")
		log := &streams.FileEventLog{Path: path}
		options := PullRequestLandOptions{Repository: "acme/app", PullRequest: " 7 ", Stream: "owned", Events: log}
		result := PullRequestLandResult{Reason: "empty outcome is findings", HeadSHA: landOwnerHead}
		appendLandEvent(options, result, time.Now(), nil)
		events, err := streams.ReadEvents(path)
		if err != nil || len(events) != 1 {
			t.Fatalf("persisted events %v %v", events, err)
		}
		event := events[0]
		want := map[string]string{"pull_request": "acme/app#7", "head": landOwnerHead, "mechanical": "false", "saved_tool_calls": "0", "kept": "false"}
		if event.Outcome != string(LandFindings) || event.Stream != "owned" || event.Repository != "acme/app" || event.Verb != "pr land" || event.Detail != result.Reason || !reflect.DeepEqual(event.Evidence, want) {
			t.Fatalf("persisted event %+v", event)
		}
		// A real log allocation failure is best effort and preserves the result.
		blocked := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocked, []byte("owned"), 0600); err != nil {
			t.Fatal(err)
		}
		options.Events = &streams.FileEventLog{Path: filepath.Join(blocked, "events.jsonl")}
		before := result
		appendLandEvent(options, result, time.Now(), errors.New("reported failure"))
		if !reflect.DeepEqual(result, before) {
			t.Fatalf("event failure changed receipt %+v", result)
		}
		got, err := os.ReadFile(blocked)
		if err != nil || string(got) != "owned" {
			t.Fatalf("blocked log mutation %q %v", got, err)
		}
	})
	for _, stage := range []string{"preflight", "initial_read", "draft", "pending"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			p := newLandOwnerProtocol(t)
			cause := p.failure
			initial := PullRequestCreateResult{Repository: "retained/repo", Reason: "retained reason", NextCommand: "old command"}
			options := PullRequestCreateOptions{}
			recorder := &recordingEvents{}
			eventsCalls := 0
			options.EventsForRepository = func(repository string) (streams.EventAppender, string) {
				eventsCalls++
				if repository != "acme/app" {
					t.Fatalf("resolved event repository %s", repository)
				}
				return recorder, "resolved-stream"
			}
			landOptions := PullRequestLandOptions{Repository: "old/repo", PullRequest: "999", ProjectsRoot: t.TempDir(), Keep: true, NoAutoMerge: true, NoUpdateBranch: true, Slice: 10 * time.Second, CheckPollInterval: time.Millisecond}
			options.LandOptions = &landOptions
			beforeOptions := landOptions
			if stage == "preflight" {
				options.LinkPreflight = func(repository string) error {
					if repository != "acme/app" {
						t.Fatalf("preflight repo %s", repository)
					}
					return cause
				}
			}
			if stage == "initial_read" {
				p.failEndpoint = "repos/acme/app/pulls/7"
			}
			if stage == "draft" {
				p.draft = true
			}
			if stage == "pending" {
				start := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
				reads := 0
				landOptions.Now = func() time.Time {
					reads++
					if reads == 1 {
						return start
					}
					return start.Add(11 * time.Second)
				}
				beforeOptions = landOptions
			}
			got, err := createPullRequestLand(p.context(), options, initial, "acme/app", "7")
			afterOptions := landOptions
			afterOptions.Now = nil
			beforeOptions.Now = nil
			if !reflect.DeepEqual(afterOptions, beforeOptions) || (stage == "pending" && landOptions.Now == nil) {
				t.Fatal("nested land mutated caller options")
			}
			if stage == "preflight" {
				if !errors.Is(err, cause) || !reflect.DeepEqual(got, initial) || eventsCalls != 0 || len(p.calls) != 0 {
					t.Fatalf("preflight receipt %+v %v calls=%v", got, err, p.calls)
				}
				return
			}
			if eventsCalls != 1 || len(recorder.events) != 1 || recorder.events[0].Stream != "resolved-stream" || recorder.events[0].Evidence["pull_request"] != "acme/app#7" || got.LandResult == nil {
				t.Fatalf("nested resolved receipt %+v events=%+v", got, recorder.events)
			}
			if stage == "initial_read" {
				if !errors.Is(err, cause) || got.Repository != initial.Repository || got.Reason != initial.Reason || got.NextCommand != initial.NextCommand || got.LandResult.Repository != "acme/app" || got.LandResult.HeadSHA != "" || len(p.calls) != 1 {
					t.Fatalf("read receipt %+v %v calls=%v", got, err, p.calls)
				}
				return
			}
			want := CreateRefused
			if stage == "pending" {
				want = CreateFindings
			}
			if err != nil || got.Outcome != want || got.NextCommand != "" || got.Reason != got.LandResult.Reason || got.Mechanical != got.LandResult.Mechanical || got.ApprovedBy != got.LandResult.ApprovedBy || p.arms != 0 || p.merges != 0 {
				t.Fatalf("mapped receipt %+v %v", got, err)
			}
			if stage == "draft" && got.RefusalCode != LandRefusalDraft {
				t.Fatalf("draft code %s", got.RefusalCode)
			}
			if stage == "pending" && (got.LandResult.RefusalCode != LandRefusalChecksPending || !strings.Contains(got.Reason, "budget elapsed")) {
				t.Fatalf("pending receipt %+v", got)
			}
		})
	}
}

func TestPRFinalizationDeleteAndInventoryFailuresPreemptMutation(t *testing.T) {
	t.Parallel()
	t.Run("missing_merged_head", func(t *testing.T) {
		t.Parallel()
		view := orchCovPullRequestView(t, `{ "head":{"ref":"feature/source","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}`)
		deleted, err := deleteRemoteBranch(context.Background(), runnertest.New(t), t.TempDir(), "acme/app", view, view, " ")
		if deleted || err == nil || err.Error() != "refuse to delete branch feature/source without the merged pull request head SHA" {
			t.Fatalf("missing head custody %v %v", deleted, err)
		}
	})
	t.Run("origin_resolution", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cause := errors.New("origin unavailable")
		fake := runnertest.New(t)
		fake.Expect(func(call runnertest.Call) bool {
			return call.Op == "RunOpts" && call.Dir == root && reflect.DeepEqual(call.Argv(), []string{"git", "remote", "get-url", "--push", "origin"}) && reflect.DeepEqual(call.Opts.Env, console.Env()) && call.Opts.CaptureCombined
		}, runner.Result{}, cause)
		ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
			t.Fatal("origin failure must preempt hosted verification")
			return githubobserver.CommandResponse{}
		}})
		view := orchCovPullRequestView(t, `{"head":{"ref":"feature/source","repo":{"full_name":"acme/app"}},"base":{"ref":"main"}}`)
		deleted, err := deleteRemoteBranch(ctx, fake, root, "acme/app", view, view, landOwnerHead)
		if deleted || !errors.Is(err, cause) || !strings.Contains(err.Error(), "resolve origin before deleting branch feature/source") {
			t.Fatalf("origin failure %v %v", deleted, err)
		}
	})
	t.Run("inventory", func(t *testing.T) {
		t.Parallel()
		cleaned, reports, err := cleanupLandedWorktrees(context.Background(), " ", "acme/app", "feature/source", landOwnerHead, "main", landOwnerBase)
		if cleaned != nil || reports != nil || err == nil || !strings.Contains(err.Error(), "projects root is required") {
			t.Fatalf("inventory receipt %v %v %v", cleaned, reports, err)
		}
	})
}

func TestPRFinalizationAwaitFailuresPreserveExactHead(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"nil_evidence", "compare_error", "update_error", "reread_error", "rejected_cas", "wait_validation"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			p := newLandOwnerProtocol(t)
			view := orchCovPullRequestView(t, `{"head":{"ref":"feature/source","sha":"`+p.head+`"},"base":{"ref":"main"}}`)
			p.compareBody = func(string) []byte {
				return []byte(`{"status":"diverged","base_commit":{"sha":"` + p.target + `"},"merge_base_commit":{"sha":"` + p.head + `"}}`)
			}
			start := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
			clockReads := 0
			mutations := 0
			options := PullRequestLandOptions{Repository: "acme/app", NoAutoMerge: true, Slice: 10 * time.Second, CheckPollInterval: time.Millisecond, Now: func() time.Time {
				clockReads++
				if stage == "nil_evidence" || (stage == "rejected_cas" && clockReads >= 3) {
					if clockReads > 1 {
						return start.Add(11 * time.Second)
					}
				}
				return start
			}}
			var evidence map[string]string
			if stage != "nil_evidence" {
				evidence = map[string]string{"retained": "owned"}
			}
			number := "7"
			if stage == "nil_evidence" || stage == "wait_validation" {
				options.NoUpdateBranch = true
			}
			if stage == "wait_validation" {
				number = ""
			}
			if stage == "compare_error" {
				p.failEndpoint = "repos/acme/app/compare/" + p.target + "..." + p.head
			}
			if stage == "reread_error" {
				p.failEndpoint = "repos/acme/app/pulls/7"
			}
			ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{Get: p.get, Read: func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("unexpected hosted Read")
				return nil, p.failure
			}, Execute: func(_ context.Context, dir string, args ...string) githubobserver.CommandResponse {
				mutations++
				want := []string{"api", "--method", "PUT", "repos/acme/app/pulls/7/update-branch", "-f", "expected_head_sha=" + p.head}
				if dir != "" || !reflect.DeepEqual(args, want) {
					t.Fatalf("update CAS %q %v", dir, args)
				}
				message := "permission denied"
				if stage != "update_error" {
					message = "expected head sha does not match"
				}
				return githubobserver.CommandResponse{ExitCode: 1, Err: p.failure, Stderr: []byte(message)}
			}})
			updated, waited, armed, merged, refusal, err := awaitLandablePullRequest(ctx, options, view, number, "subject", "body", evidence)
			if armed || merged || refusal != nil {
				t.Fatalf("failure changed custody flags armed=%v merged=%v refusal=%+v", armed, merged, refusal)
			}
			switch stage {
			case "nil_evidence":
				if err != nil || !reflect.DeepEqual(updated, view) || waited.Status != githubchecks.PullRequestWaitPending || mutations != 0 || len(p.calls) != 0 {
					t.Fatalf("nil evidence %+v %+v %v", updated, waited, err)
				}
			case "compare_error":
				if err == nil || !strings.Contains(err.Error(), "determine whether acme/app#7 is behind main") || !reflect.DeepEqual(updated, view) || mutations != 0 || p.checkReads != 0 || len(p.calls) != 2 {
					t.Fatalf("comparison failure %+v %v calls=%v", updated, err, p.calls)
				}
			case "update_error":
				if err == nil || !strings.Contains(err.Error(), "update acme/app#7 onto main") || !strings.Contains(err.Error(), "permission denied") || !reflect.DeepEqual(updated, view) || mutations != 1 {
					t.Fatalf("update failure %+v %v", updated, err)
				}
			case "reread_error":
				if !errors.Is(err, p.failure) || mutations != 1 || p.calls[len(p.calls)-1] != "repos/acme/app/pulls/7" {
					t.Fatalf("reread failure %+v %v calls=%v", updated, err, p.calls)
				}
			case "rejected_cas":
				if err != nil || updated.Head.SHA != view.Head.SHA || waited.Status != githubchecks.PullRequestWaitPending || mutations != 1 || clockReads != 3 || evidence["updated_onto_target"] != "" || evidence["head"] != shortMergeRevision(p.head) {
					t.Fatalf("CAS rejection %+v %+v %v evidence=%v", updated, waited, err, evidence)
				}
			case "wait_validation":
				if err == nil || err.Error() != "pull request is required" || !reflect.DeepEqual(updated, view) || mutations != 0 || len(p.calls) != 0 {
					t.Fatalf("wait validation %+v %v calls=%v", updated, err, p.calls)
				}
			}
			if evidence != nil && evidence["retained"] != "owned" {
				t.Fatalf("evidence lost %v", evidence)
			}
		})
	}
}
