package cmdstream

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
	"github.com/sneat-dev/wb/internal/streamrun"
	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestAllStreamVerbsUseLazyRuntimeContextFlagsAndCurrentStreams(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"start", "join", "status", "list", "end", "delete", "sync"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			root := "before"
			runtime := testRuntime()
			runtime.Flags = func() shared.Flags { return shared.Flags{ProjectsRoot: root} }
			deps := testDependencies()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			calls := 0
			check := func(c context.Context, r string) {
				calls++
				if c != ctx || r != "after" {
					t.Fatalf("context=%v root=%s", c, r)
				}
			}
			deps.Start = func(c context.Context, r streamrun.Creation, o streams.StartOptions) (streams.StartResult, error) {
				check(c, r.ProjectsRoot)
				if r.SessionRequired || r.Base != "release" || r.WorkLog.Model != "exact" || !r.WorkLog.RequireOriginalPrompt || o.Name != "batch" || o.Library != "acme/lib" || !reflect.DeepEqual(o.Repositories, []string{"acme/app", "acme/lib"}) {
					t.Fatalf("creation=%+v options=%+v", r, o)
				}
				return streams.StartResult{Stream: streams.Stream{Name: o.Name}}, nil
			}
			deps.Join = func(c context.Context, r streamrun.Creation, o streams.JoinOptions) (streams.StartResult, error) {
				check(c, r.ProjectsRoot)
				if !r.SessionRequired || o.Role != streams.RoleLibrary || o.Base != "release" || o.Name != "batch" || o.Repository != "acme/lib" {
					t.Fatalf("creation=%+v options=%+v", r, o)
				}
				return streams.StartResult{}, nil
			}
			deps.RegisteredSession = func() bool { return true }
			deps.Status = func(c context.Context, r, n string) (streams.Status, error) {
				check(c, r)
				if n != "batch" {
					t.Fatal(n)
				}
				return streams.Status{Stream: n}, nil
			}
			deps.List = func(c context.Context, r string) ([]streams.Stream, []streams.Unreadable, error) {
				check(c, r)
				return nil, nil, nil
			}
			deps.End = func(c context.Context, r string, o streams.EndOptions) (streams.EndResult, error) {
				check(c, r)
				if o.Name != "batch" || !o.Apply || !o.Retarget || !o.ForceUnabsorbed || !o.KeepRemoteBranch || o.Reason != "approved" {
					t.Fatal(o)
				}
				return streams.EndResult{}, nil
			}
			deps.Delete = func(r, n string) error {
				check(ctx, r)
				if n != "batch" {
					t.Fatal(n)
				}
				return nil
			}
			deps.Sync = func(c context.Context, r streamrun.SyncRequest) ([]streamsync.Result, error) {
				check(c, r.ProjectsRoot)
				want := streamrun.SyncRequest{ProjectsRoot: "after", Name: "batch", Base: "release", Libraries: []streamsync.Library{{Name: "github.com/acme/lib", Target: "v1.2.3", Ecosystem: string(streams.EcosystemGo)}}, Verify: true, AllowMidReview: true, PushTrigger: streamsync.TriggerExplicit, PushReason: "checkpoint", Timeout: 2 * time.Second}
				if !reflect.DeepEqual(r, want) {
					t.Fatalf("request=%+v want=%+v", r, want)
				}
				return []streamsync.Result{}, nil
			}
			args := map[string][]string{"start": {"start", "batch", "acme/app", "acme/lib", "--library", "acme/lib", "--base", "release", "--mode", "manual", "--initiator", "human", "--model", "exact", "--original-prompt-file", "/input"}, "join": {"join", "batch", "acme/lib", "--role", "library", "--base", "release", "--mode", "agent", "--agent", "agent", "--model", "exact", "--original-prompt-file", "/input"}, "status": {"status", "batch"}, "list": {"status"}, "end": {"end", "batch", "--apply", "--retarget", "--force-unabsorbed", "--reason", "approved", "--keep-remote-branch"}, "delete": {"delete", "batch"}, "sync": {"sync", "batch", "--base", "release", "--library", "github.com/acme/lib@v1.2.3", "--verify", "--allow-mid-review", "--push", "--reason", "checkpoint", "--timeout", "2s"}}[verb]
			command := New(runtime, deps)
			command.SilenceUsage = true
			command.SilenceErrors = true
			command.SetContext(ctx)
			root = "after"
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(io.Discard)
			command.SetArgs(append(args, "--format", "json"))
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || out.Len() == 0 {
				t.Fatalf("calls%d output=%s", calls, &out)
			}
			if verb == "sync" && out.String() != "[]\n" {
				t.Fatal(out.String())
			}
		})
	}
}
func TestDefaultsAndRepeatedInstancesRemainIndependent(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"start", "join", "end", "sync"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			calls := 0
			deps.Start = func(_ context.Context, r streamrun.Creation, o streams.StartOptions) (streams.StartResult, error) {
				calls++
				if r.Base != "" || r.SessionRequired || o.Library != "" || o.Base != "" {
					t.Fatal(r, o)
				}
				return streams.StartResult{}, nil
			}
			deps.Join = func(_ context.Context, r streamrun.Creation, o streams.JoinOptions) (streams.StartResult, error) {
				calls++
				if r.Base != "" || r.SessionRequired || o.Role != streams.RoleConsumer || o.Base != "" {
					t.Fatal(r, o)
				}
				return streams.StartResult{}, nil
			}
			deps.End = func(_ context.Context, _ string, o streams.EndOptions) (streams.EndResult, error) {
				calls++
				if !reflect.DeepEqual(o, streams.EndOptions{Name: "batch"}) {
					t.Fatal(o)
				}
				return streams.EndResult{}, nil
			}
			deps.Sync = func(_ context.Context, r streamrun.SyncRequest) ([]streamsync.Result, error) {
				calls++
				want := streamrun.SyncRequest{ProjectsRoot: "/fixture", Name: "batch", Timeout: 30 * time.Minute, Libraries: []streamsync.Library{}}
				if !reflect.DeepEqual(r, want) {
					t.Fatalf("defaults=%+v", r)
				}
				return []streamsync.Result{}, nil
			}
			for i := 0; i < 2; i++ {
				command := New(testRuntime(), deps)
				var out bytes.Buffer
				command.SetOut(&out)
				command.SetErr(io.Discard)
				command.SilenceUsage = true
				args := []string{verb, "batch"}
				if verb == "start" || verb == "join" {
					args = append(args, "acme/app", "--initiator", "human", "--model", "unknown", "--original-prompt-file", "/input")
				}
				command.SetArgs(args)
				if err := command.Execute(); err != nil {
					t.Fatal(err)
				}
				if out.Len() == 0 && verb != "sync" {
					t.Fatal("missing independent output")
				}
			}
			if calls != 2 {
				t.Fatal(calls)
			}
		})
	}
}
func TestCommandDelegatedFailuresNeverPrintPartialSyncResults(t *testing.T) {
	t.Parallel()
	failure := errors.New("sentinel operation")
	for _, verb := range []string{"start", "join", "list", "status", "end", "delete", "sync", "sync refusal", "open delete"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			deps.Start = func(context.Context, streamrun.Creation, streams.StartOptions) (streams.StartResult, error) {
				return streams.StartResult{}, failure
			}
			deps.Join = func(context.Context, streamrun.Creation, streams.JoinOptions) (streams.StartResult, error) {
				return streams.StartResult{}, failure
			}
			deps.List = func(context.Context, string) ([]streams.Stream, []streams.Unreadable, error) {
				return nil, nil, failure
			}
			deps.Status = func(context.Context, string, string) (streams.Status, error) { return streams.Status{}, failure }
			deps.End = func(context.Context, string, streams.EndOptions) (streams.EndResult, error) {
				return streams.EndResult{}, failure
			}
			deps.Delete = func(string, string) error {
				if verb == "open delete" {
					return errors.New("stream is still open")
				}
				return failure
			}
			deps.Sync = func(context.Context, streamrun.SyncRequest) ([]streamsync.Result, error) {
				if verb == "sync refusal" {
					return nil, &streamrun.SyncRefusal{Repository: "acme/app", Refusal: &streamsync.Refusal{Message: "dirty checkout"}}
				}
				return []streamsync.Result{{Repository: "partial"}}, failure
			}
			actual := verb
			if actual == "list" {
				actual = "status"
			}
			if actual == "sync refusal" {
				actual = "sync"
			}
			if actual == "open delete" {
				actual = "delete"
			}
			args := []string{actual}
			if verb != "list" {
				args = append(args, "batch")
			}
			if actual == "start" || actual == "join" {
				args = append(args, "acme/app", "--model", "unknown")
			}
			args = append(args, "--format", "json")
			c := New(testRuntime(), deps)
			var out bytes.Buffer
			c.SetOut(&out)
			c.SetErr(io.Discard)
			c.SilenceUsage = true
			c.SilenceErrors = true
			c.SetArgs(args)
			err := c.Execute()
			if err == nil {
				t.Fatal("failure swallowed")
			}
			if actual == "sync" {
				if out.Len() != 0 {
					t.Fatal("partial sync output", out.String())
				}
				if verb == "sync refusal" {
					if exitCodeOfSafe(err) != exitUsage || err.Error() != "acme/app: dirty checkout" {
						t.Fatal(err)
					}
				} else if !errors.Is(err, failure) {
					t.Fatal(err)
				}
			} else if verb == "open delete" {
				if exitCodeOfSafe(err) != exitUsage || !strings.Contains(out.String(), "wb stream end batch --apply") {
					t.Fatal(err, out.String())
				}
			} else if !strings.Contains(err.Error(), failure.Error()) {
				t.Fatal(err)
			}
		})
	}
}

type readFailure struct{}

func (readFailure) Read([]byte) (int, error) { return 0, errors.New("stdin failed") }
func TestWorkLogGuardAndInputFailuresDoNotDelegateMutation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		flags      workLogFlags
		registered bool
		in         io.Reader
		prepErr    error
		want       string
	}{{name: "bad mode", flags: workLogFlags{mode: "telepathy"}, want: "unsupported execution mode"}, {name: "manual requires initiator", flags: workLogFlags{mode: "manual"}, want: "requires --initiator"}, {name: "agent requires session", flags: workLogFlags{mode: "agent"}, want: "live registered session"}, {name: "auto agent requires session", flags: workLogFlags{mode: "auto", agentRuntime: "codex"}, want: "live registered session"}, {name: "stdin failure", flags: workLogFlags{originalPrompt: "-"}, in: readFailure{}, want: "read --original-prompt-file - from stdin: stdin failed"}, {name: "empty stdin", flags: workLogFlags{originalPrompt: "-"}, in: strings.NewReader(""), want: "prompt"}, {name: "prepare failure", flags: workLogFlags{}, prepErr: errors.New("prepare failed"), want: "prepare failed"}, {name: "stdin success", flags: workLogFlags{mode: "auto", originalPrompt: "-", model: "exact", effortID: "effort", runID: "run", initiator: "human", cli: "cli", provider: "provider"}, in: strings.NewReader("exact request\n")}} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			deps := testDependencies()
			deps.RegisteredSession = func() bool { return test.registered }
			called := false
			deps.PrepareWorkLog = func(root, task string, o worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
				called = true
				if root != "/fixture" || task != "batch" || !o.RequireOriginalPrompt {
					t.Fatal(root, task, o)
				}
				return o, test.prepErr
			}
			cmd := cwDepsNewOutCommand(io.Discard)
			if test.in != nil {
				cmd.SetIn(test.in)
			}
			opts, agent, err := streamWorkLog(testRuntime(), deps, cmd, "batch", test.flags)
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error=%v want%s", err, test.want)
				}
				if test.prepErr == nil && called {
					t.Fatal("prepared invalid input")
				}
			} else if err != nil || agent || !called || opts.Model != "exact" || opts.EffortID != "effort" || opts.RunID != "run" || opts.Initiator != "human" || opts.CLI != "cli" || opts.Provider != "provider" {
				t.Fatalf("opts=%+v agent=%t err=%v", opts, agent, err)
			}
		})
	}
}
func TestWritersWinOverFindingsAndRefusals(t *testing.T) {
	t.Parallel()
	runtime := testRuntime()
	command := cwDepsNewOutCommand(cwDepsFailingWriter{})
	for _, run := range []func() error{func() error {
		return streamStartOutput(runtime, command, "stream start", "json", streams.StartResult{})
	}, func() error { return printStreamSync(runtime, command, "json", nil) }, func() error {
		return printStreamSync(runtime, command, "text", []streamsync.Result{{Batch: &streamsync.BatchResult{Passed: true}}})
	}, func() error {
		return streamStartOutput(runtime, command, "stream start", "text", streams.StartResult{Reported: []streams.PreflightFinding{{Check: "failure"}}})
	}} {
		if err := run(); err == nil || !strings.Contains(err.Error(), "write refused") {
			t.Fatal(err)
		}
	}
	for _, fail := range []int{2, 3} {
		writer := &failAfterWriter{allowedWrites: fail - 1}
		command := cwDepsNewOutCommand(writer)
		result := streams.StartResult{Reported: []streams.PreflightFinding{{Check: "check"}}, TransitiveOmissions: []string{"acme/omitted"}}
		if err := streamStartOutput(runtime, command, "start", "text", result); err == nil || !strings.Contains(err.Error(), "forced write failure") {
			t.Fatal(err)
		}
	}
}
func TestFormatAndPreparationRefusalsDoNotInvokeOperations(t *testing.T) {
	t.Parallel()
	for _, verb := range []string{"start", "join", "status", "end", "delete", "sync"} {
		t.Run(verb, func(t *testing.T) {
			t.Parallel()
			deps := noOperationDependencies(t)
			deps.PrepareWorkLog = func(string, string, worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
				t.Fatal("invalid format prepared work log")
				return worktrees.WorkLogOptions{}, nil
			}
			command := New(testRuntime(), deps)
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SilenceUsage = true
			command.SetArgs([]string{verb, "batch", "--format", "toml"})
			if verb == "start" || verb == "join" {
				command.SetArgs([]string{verb, "batch", "acme/app", "--format", "toml"})
			}
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unsupported format") {
				t.Fatal(err)
			}
		})
	}
	command := New(testRuntime(), testDependencies())
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"start", "batch", "acme/app", "--mode", "telepathy"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unsupported execution mode") {
		t.Fatal(err)
	}
}
func TestDeleteTextAndSyncBatchWriteErrorsAreReturned(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	deps.Delete = func(string, string) error { return nil }
	command := New(testRuntime(), deps)
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetArgs([]string{"delete", "batch"})
	if err := command.Execute(); err != nil || out.String() != "deleted stream batch\n" {
		t.Fatal(err, out.String())
	}
	writer := &failAfterWriter{allowedWrites: 1}
	if err := printStreamSync(testRuntime(), cwDepsNewOutCommand(writer), "text", []streamsync.Result{{Batch: &streamsync.BatchResult{Passed: true}}}); err == nil || !strings.Contains(err.Error(), "forced write failure") {
		t.Fatal(err)
	}
}
func TestInvalidArgumentsNamesAndRolesDoNotCallOperations(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"start", "only"}, {"join", "only"}, {"sync"}, {"status", "one", "two"}, {"end"}, {"delete"}, {"start", "bad/name", "acme/app"}, {"join", "bad/name", "acme/app"}, {"join", "name", "acme/app", "--role", "bogus"}, {"sync", "name", "--library", "no-version"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			command := New(testRuntime(), noOperationDependencies(t))
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SilenceUsage = true
			command.SilenceErrors = true
			command.SetArgs(args)
			if err := command.Execute(); err == nil {
				t.Fatal("invalid arguments accepted")
			}
		})
	}
}
