package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
	"github.com/spf13/cobra"
)

func streamSyncCommandFixture(t *testing.T) (string, *streams.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := streams.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(streams.Stream{Name: "release-batch", Members: []streams.Member{
		{Repository: "acme/lib", Role: streams.RoleLibrary, Worktree: root + "/lib", Branch: "stream/release-batch", Base: "main", Lease: streams.Lease{RecordedHead: "old-lib"}},
		{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: root + "/app", Branch: "stream/release-batch", Base: "develop", Lease: streams.Lease{RecordedHead: "old-app"}},
		{Repository: "acme/unavailable", Role: streams.RoleConsumer, Base: "main"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return root, store
}

func TestStreamSyncCommandPassesMemberSpecificOptionsAndPersistsRemoteHead(t *testing.T) {
	root, store := streamSyncCommandFixture(t)
	var calls []streamsync.Options
	runner := func(_ context.Context, options streamsync.Options) (streamsync.Result, error) {
		calls = append(calls, options)
		return streamsync.Result{
			Stream: options.Stream, Repository: options.Repository,
			StreamRebase:       streamsync.RebaseResult{Branch: options.Branch, Rebased: true},
			RecordedRemoteHead: "fetched-" + options.Repository,
		}, nil
	}
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command {
		return newStreamSyncCmdWithRunner(testInvocation(t, root), runner)
	}, "release-batch", "--base", "release", "--library", "github.com/acme/lib@v1.2.3", "--verify", "--allow-mid-review", "--push", "--reason", "checkpoint", "--timeout", "2s", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("sync calls = %+v, want library and consumer only", calls)
	}
	for i, repository := range []string{"acme/lib", "acme/app"} {
		got := calls[i]
		if got.Stream != "release-batch" || got.Repository != repository || got.Branch != "stream/release-batch" || got.Base != "release" ||
			!got.Verify || !got.AllowMidReview || got.PushTrigger != streamsync.TriggerExplicit || got.PushReason != "checkpoint" || got.Timeout != 2*time.Second {
			t.Errorf("options for %s = %+v", repository, got)
		}
	}
	if calls[0].Worktree != root+"/lib" || calls[0].RecordedRemoteHead != "old-lib" || len(calls[0].Libraries) != 0 {
		t.Errorf("library options = %+v", calls[0])
	}
	if calls[1].Worktree != root+"/app" || calls[1].RecordedRemoteHead != "old-app" ||
		!reflect.DeepEqual(calls[1].Libraries, []streamsync.Library{{Name: "github.com/acme/lib", Target: "v1.2.3", Ecosystem: string(streams.EcosystemGo)}}) {
		t.Errorf("consumer options = %+v", calls[1])
	}
	var results []streamsync.Result
	if err := json.Unmarshal([]byte(stdout), &results); err != nil || len(results) != 2 {
		t.Fatalf("JSON result = %q, decoded %+v, error %v", stdout, results, err)
	}
	persisted, err := store.Load("release-batch")
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"acme/lib", "acme/app"} {
		member, ok := persisted.Member(repository)
		if !ok || member.Lease.RecordedHead != "fetched-"+repository {
			t.Errorf("persisted head for %s = %+v, found %t", repository, member, ok)
		}
	}
}

func TestStreamSyncCommandUsesMemberBaseAndReportsRefusal(t *testing.T) {
	root, _ := streamSyncCommandFixture(t)
	var calls []streamsync.Options
	runner := func(_ context.Context, options streamsync.Options) (streamsync.Result, error) {
		calls = append(calls, options)
		if options.Repository == "acme/app" {
			return streamsync.Result{}, &streamsync.Refusal{Code: "dirty-worktree", Message: "dirty checkout"}
		}
		return streamsync.Result{Repository: options.Repository, StreamRebase: streamsync.RebaseResult{Branch: options.Branch}}, nil
	}
	_, _, err := cwCovExec(t, root, func() *cobra.Command {
		return newStreamSyncCmdWithRunner(testInvocation(t, root), runner)
	}, "release-batch")
	if code := exitCodeOf(t, err); code != exitUsage || !strings.Contains(err.Error(), "acme/app: dirty checkout") {
		t.Fatalf("refusal = %v, exit %d", err, code)
	}
	if len(calls) != 2 || calls[0].Base != "main" || calls[1].Base != "develop" || calls[0].PushTrigger != "" || calls[1].Verify {
		t.Fatalf("default member options = %+v", calls)
	}
}

func TestStreamSyncCommandPropagatesEngineError(t *testing.T) {
	root, _ := streamSyncCommandFixture(t)
	want := errors.New("injected sync failure")
	_, _, err := cwCovExec(t, root, func() *cobra.Command {
		return newStreamSyncCmdWithRunner(testInvocation(t, root), func(context.Context, streamsync.Options) (streamsync.Result, error) {
			return streamsync.Result{}, want
		})
	}, "release-batch")
	if !errors.Is(err, want) {
		t.Fatalf("sync error = %v, want %v", err, want)
	}
}

func TestBatchVerifierMapsChecksAndFailureEvidence(t *testing.T) {
	called := false
	verifier := batchVerifier{timeout: 3 * time.Second, verify: func(_ context.Context, repository, path string, checks []quality.Check, options quality.RunOptions) quality.VerificationReport {
		called = true
		if repository != "/fixture" || path != "/fixture" || !reflect.DeepEqual(checks, []quality.Check{quality.CheckLint, quality.CheckBuild, quality.CheckTest}) ||
			options.Timeout != 3*time.Second || !options.SingleWorker || !containsString(options.Env, "CI=1") {
			t.Errorf("verification inputs: repository=%q path=%q checks=%v options=%+v", repository, path, checks, options)
		}
		return quality.VerificationReport{Status: quality.StatusFailed, Results: []quality.VerificationEntry{
			{Module: "app", Check: quality.CheckLint, Command: "go vet ./...", Status: quality.StatusPassed},
			{Module: "app", Check: quality.CheckBuild, Command: "go build ./...", Status: quality.StatusFailed, Detail: "compiler error"},
			{Module: "app", Check: quality.CheckTest, Command: "", Status: quality.StatusSkipped},
		}}
	}}
	run, err := verifier.Verify(context.Background(), "/fixture")
	if err != nil || !called || run.Passed || run.Command != "go vet ./...; go build ./..." ||
		!reflect.DeepEqual(run.Details, []string{"app build: compiler error"}) ||
		!reflect.DeepEqual(run.Skipped, []string{"-race"}) || run.Duration < 0 {
		t.Fatalf("verification run = %+v, called=%t, error=%v", run, called, err)
	}
}

func TestBatchVerifierTreatsNonfailedReportAsPassed(t *testing.T) {
	verifier := batchVerifier{verify: func(context.Context, string, string, []quality.Check, quality.RunOptions) quality.VerificationReport {
		return quality.VerificationReport{Status: quality.StatusSkipped, Results: []quality.VerificationEntry{{Status: quality.StatusSkipped, Command: "unused"}}}
	}}
	run, err := verifier.Verify(context.Background(), t.TempDir())
	if err != nil || !run.Passed || run.Command != "" || len(run.Details) != 0 {
		t.Fatalf("skipped verification = %+v, error=%v", run, err)
	}
}
