package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"reflect"
	"strings"
	"testing"
)

type journalContextKey struct{}
type journalRecording struct {
	name    string
	options any
	ctx     context.Context
	err     error
}

func journalRecord[T any](r *journalRecording, name string) func(context.Context, T) (worktrees.LogVerbResult, error) {
	return func(ctx context.Context, o T) (worktrees.LogVerbResult, error) {
		r.name = name
		r.options = o
		r.ctx = ctx
		return worktrees.LogVerbResult{Verb: name, Worktree: "checkout", Applied: true}, r.err
	}
}
func journalOps(r *journalRecording) JournalOperations {
	return JournalOperations{
		Load: func(ctx context.Context, o worktrees.LoadWorkLogOptions) (worktrees.WorkLogView, error) {
			r.name = "load"
			r.options = o
			r.ctx = ctx
			return worktrees.WorkLogView{}, r.err
		},
		Show: func(ctx context.Context, root, path string) (worktrees.WorkLogView, worktrees.LocalWorkLogProjection, error) {
			r.name = "show"
			r.options = []string{root, path}
			r.ctx = ctx
			return worktrees.WorkLogView{}, worktrees.LocalWorkLogProjection{}, r.err
		},
		Init: journalRecord[worktrees.LogInitOptions](r, "init"), Steer: journalRecord[worktrees.LogSteerOptions](r, "steer"), Checkpoint: journalRecord[worktrees.LogCheckpointOptions](r, "checkpoint"), Refresh: journalRecord[worktrees.LogRefreshOptions](r, "refresh"), Integrate: journalRecord[worktrees.LogIntegrateOptions](r, "integrate"), Handoff: journalRecord[worktrees.LogHandoffOptions](r, "handoff"), Recover: journalRecord[worktrees.LogRecoverOptions](r, "recover"), Finalize: journalRecord[worktrees.LogFinalizeOptions](r, "finalize"), Sync: journalRecord[worktrees.LogSyncOptions](r, "sync"), Archive: journalRecord[worktrees.LogArchiveOptions](r, "archive")}
}
func journalExecute(c *cobra.Command, in io.Reader, out io.Writer, args ...string) error {
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetOut(out)
	c.SetErr(io.Discard)
	c.SetIn(in)
	c.SetArgs(args)
	return c.Execute()
}
func journalRuntime(flags *shared.Flags) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return *flags }, ExitError: func(code int, message string) error { return &journalUsageError{code, message} }}
}

type journalUsageError struct {
	code    int
	message string
}

func (e *journalUsageError) Error() string { return e.message }

func TestJournalAllVerbsPreserveOptionsContextAdmissionAndStreams(t *testing.T) {
	t.Parallel()
	verbs := []struct {
		name    string
		args    []string
		mutates bool
	}{
		{"load", nil, false}, {"show", []string{"show"}, false}, {"init", []string{"init", "--prompt", "inline", "--source", "human_declared", "--model", "model", "--cli", "cli", "--provider", "provider"}, true},
		{"steer", []string{"steer", "--prompt", "inline"}, true}, {"checkpoint", []string{"checkpoint", "--input-tokens", "0", "--output-tokens", "0", "--estimated-cost", "0", "--skip-remote"}, true}, {"refresh", []string{"refresh"}, true}, {"integrate", []string{"integrate"}, true}, {"handoff", []string{"handoff"}, true}, {"recover", []string{"recover", "--apply"}, true}, {"finalize", []string{"finalize", "--apply", "--report-stdin"}, true}, {"sync", []string{"sync"}, true}, {"archive", []string{"archive", "--apply"}, true}}
	for _, tc := range verbs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, variant := range []string{"text", "json", "backend", "writer", "format", "deny"} {
				r := &journalRecording{}
				boom := errors.New("failure")
				if variant == "backend" {
					r.err = boom
				}
				flags := shared.Flags{ProjectsRoot: "before"}
				ctx := context.WithValue(context.Background(), journalContextKey{}, "caller")
				admitted, released := 0, 0
				admit := func(c *cobra.Command, mutate bool) (worktrees.AgentIdentity, func(), error) {
					admitted++
					if c.Context() != ctx || mutate != tc.mutates {
						t.Fatalf("admission context/mutation=%v/%v", c.Context(), mutate)
					}
					mode, _ := c.Flags().GetString("mode")
					actor, _ := c.Flags().GetString("initiator")
					if mode != "manual" || actor != "human" {
						t.Fatalf("inherited flags=%s/%s", mode, actor)
					}
					if variant == "deny" {
						return worktrees.AgentIdentity{}, nil, boom
					}
					return worktrees.AgentIdentity{AgentID: "actual"}, func() { released++ }, nil
				}
				c := NewWorkLog(journalRuntime(&flags), journalOps(r), admit)
				c.SetContext(ctx)
				flags.ProjectsRoot = "after"
				args := append(append([]string{}, tc.args...), "checkout", "--mode", "manual", "--initiator", "human")
				format := "text"
				if variant == "json" {
					format = "json"
				}
				if variant == "format" {
					format = "bad"
				}
				args = append(args, "--format", format)
				var out bytes.Buffer
				var writer io.Writer = &out
				if variant == "writer" {
					writer = &activeLimitedWriter{}
				}
				err := journalExecute(c, strings.NewReader("report"), writer, args...)
				switch {
				case variant == "format":
					if err == nil || r.name != "" || admitted != 0 {
						t.Fatalf("format order=%v/%s/%d", err, r.name, admitted)
					}
				case variant == "deny" && tc.mutates:
					if err != boom || r.name != "" || released != 0 {
						t.Fatalf("deny=%v/%s/%d", err, r.name, released)
					}
				case variant == "backend":
					if err != boom || out.Len() != 0 {
						t.Fatalf("backend=%v/%q", err, out.String())
					}
				case variant == "writer":
					if err == nil {
						t.Fatal("writer error lost")
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
					if variant == "json" && !strings.HasPrefix(out.String(), "{") {
						t.Fatalf("json=%q", out.String())
					}
				}
				if r.name != "" {
					if r.name != tc.name || r.ctx != ctx {
						t.Fatalf("record=%+v", r)
					}
					if tc.name == "show" {
						if !reflect.DeepEqual(r.options, []string{"after", "checkout"}) {
							t.Fatal(r.options)
						}
					} else {
						v := reflect.ValueOf(r.options)
						if v.FieldByName("ProjectsRoot").String() != "after" || v.FieldByName("Worktree").String() != "checkout" {
							t.Fatal(r.options)
						}
					}
				}
				if admitted > 0 && variant != "deny" && released != 1 {
					t.Fatalf("release=%d", released)
				}
			}
		})
	}
}

func TestJournalOptionalFlagsHumanSourceAndReadOnlyDefaults(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "root"}
	runtime := journalRuntime(&flags)
	admit := func(_ *cobra.Command, mutate bool) (worktrees.AgentIdentity, func(), error) {
		return worktrees.AgentIdentity{}, func() {}, nil
	}
	for _, explicit := range []bool{false, true} {
		r := &journalRecording{}
		args := []string{"checkpoint"}
		if explicit {
			args = append(args, "--input-tokens", "0", "--output-tokens", "0", "--estimated-cost", "0")
		}
		if err := journalExecute(NewWorkLog(runtime, journalOps(r), admit), nil, io.Discard, args...); err != nil {
			t.Fatal(err)
		}
		o := r.options.(worktrees.LogCheckpointOptions)
		if (o.InputTokens != nil) != explicit || (o.OutputTokens != nil) != explicit || (o.EstimatedCost != nil) != explicit || o.SkipRemote {
			t.Fatalf("checkpoint pointers=%+v", o)
		}
		if explicit && (*o.InputTokens != 0 || *o.OutputTokens != 0 || *o.EstimatedCost != 0) {
			t.Fatal("zero flag lost")
		}
	}
	for _, name := range []string{"recover", "archive"} {
		r := &journalRecording{}
		called := false
		a := func(_ *cobra.Command, mutate bool) (worktrees.AgentIdentity, func(), error) {
			called = true
			if mutate {
				t.Fatal("dry run admitted mutation")
			}
			return worktrees.AgentIdentity{}, func() {}, nil
		}
		if err := journalExecute(NewWorkLog(runtime, journalOps(r), a), nil, io.Discard, name); err != nil || !called {
			t.Fatalf("dry run=%v/%v", err, called)
		}
	}
	for _, args := range [][]string{{"init"}, {"steer", "--prompt", "inline", "--source", "human_declared"}} {
		r := &journalRecording{}
		if err := journalExecute(NewWorkLog(runtime, journalOps(r), admit), nil, io.Discard, args...); err != nil {
			t.Fatal(err)
		}
		if args[0] == "init" && len(r.options.(worktrees.LogInitOptions).Prompt) != 0 {
			t.Fatal("optional prompt")
		}
		if args[0] == "steer" && r.options.(worktrees.LogSteerOptions).Source != "human_declared" {
			t.Fatal("explicit source")
		}
	}
	r := &journalRecording{}
	var out bytes.Buffer
	if err := journalExecute(NewSet(runtime, journalRecord[worktrees.LogSteerOptions](r, "steer")), nil, &out, "checkout", "--prompt", "human"); err != nil {
		t.Fatal(err)
	}
	o := r.options.(worktrees.LogSteerOptions)
	if o.Worktree != "checkout" || o.Source != worktrees.PromptSourceHuman || string(o.Body) != "human" || out.String() != "recorded \n" {
		t.Fatalf("human options=%+v output=%q", o, out.String())
	}
	if _, err := readPromptBody("   ", ""); err == nil {
		t.Fatal("empty inline accepted")
	}
	if _, err := readPromptBody("", ""); err == nil {
		t.Fatal("absent source accepted")
	}
}

func TestJournalStandaloneAdaptersAndInputErrorOrder(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "root"}
	runtime := journalRuntime(&flags)
	boom := errors.New("backend failure")
	for _, name := range []string{"set", "fetch", "identity"} {
		for _, variant := range []string{"text", "json", "backend", "writer", "format", "deny"} {
			if name == "set" && variant == "json" {
				continue
			}
			r := &journalRecording{}
			if variant == "backend" {
				r.err = boom
			}
			released := 0
			admit := func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
				if variant == "deny" {
					return worktrees.AgentIdentity{}, nil, boom
				}
				return worktrees.AgentIdentity{}, func() { released++ }, nil
			}
			var c *cobra.Command
			var args []string
			switch name {
			case "set":
				c = NewSet(runtime, journalRecord[worktrees.LogSteerOptions](r, "steer"))
				args = []string{"--prompt", "human"}
			case "fetch":
				c = NewCheckpointFetch(func(ctx context.Context, o worktrees.FetchRemoteCheckpointOptions) (worktrees.RemoteCheckpointFetchResult, error) {
					r.name = "fetch"
					r.options = o
					return worktrees.RemoteCheckpointFetchResult{}, r.err
				})
				args = []string{"checkout", "--task", "task"}
			case "identity":
				c = NewCorrectIdentity(runtime, func(o worktrees.CorrectExecutionIdentityOptions) (worktrees.ExecutionIdentityCorrectionResult, error) {
					r.name = "identity"
					r.options = o
					return worktrees.ExecutionIdentityCorrectionResult{}, r.err
				}, admit, func(*cobra.Command) string { return "trimmed" })
				args = []string{"effort", "run", "claim", "--model", "model", "--cli=", "--provider=", "--actor", "human", "--reason", "correct", "--event-id", "event"}
			}
			format := "text"
			if variant == "json" {
				format = "json"
			}
			if variant == "format" {
				format = "bad"
			}
			if name != "set" || variant == "format" {
				args = append(args, "--format", format)
			}
			var out bytes.Buffer
			var writer io.Writer = &out
			if variant == "writer" {
				writer = &activeLimitedWriter{}
			}
			err := journalExecute(c, nil, writer, args...)
			switch {
			case variant == "format":
				if err == nil || r.name != "" {
					t.Fatal("format order")
				}
			case variant == "deny" && name == "identity":
				if err != boom || r.name != "" {
					t.Fatal("admission order")
				}
			case variant == "backend":
				if err != boom {
					t.Fatal(err)
				}
			case variant == "writer":
				if err == nil {
					t.Fatal("writer lost")
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if name == "identity" && r.name != "" {
				o := r.options.(worktrees.CorrectExecutionIdentityOptions)
				if o.Model == nil || *o.Model != "model" || o.CLI == nil || *o.CLI != "" || o.Provider == nil || *o.Provider != "" || o.Initiator != "trimmed" || o.EffortID != "effort" || o.RunID != "run" || o.ClaimID != "claim" || o.EventID != "event" || o.Actor != "human" || o.Reason != "correct" {
					t.Fatalf("identity=%+v", o)
				}
				if released != 1 {
					t.Fatal("identity release")
				}
			}
			if name == "fetch" && r.name != "" {
				if o := r.options.(worktrees.FetchRemoteCheckpointOptions); o.Root != "checkout" || o.Task != "task" {
					t.Fatal(o)
				}
			}
		}
	}
	r := &journalRecording{}
	c := NewCorrectIdentity(runtime, func(o worktrees.CorrectExecutionIdentityOptions) (worktrees.ExecutionIdentityCorrectionResult, error) {
		if o.Model != nil || o.CLI != nil || o.Provider != nil {
			t.Fatal("absent identity replacement")
		}
		return worktrees.ExecutionIdentityCorrectionResult{}, nil
	}, func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
		return worktrees.AgentIdentity{}, func() {}, nil
	}, func(*cobra.Command) string { return "" })
	if err := journalExecute(c, nil, io.Discard, "e", "r", "c"); err != nil {
		t.Fatal(err)
	}
	if err := journalExecute(NewCheckpointFetch(nil), nil, io.Discard); err == nil || err.Error() != "--task is required" {
		t.Fatalf("required task=%v", err)
	}
	admit := func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
		return worktrees.AgentIdentity{}, func() {}, nil
	}
	for _, verb := range []string{"init", "steer"} {
		if err := journalExecute(NewWorkLog(runtime, journalOps(r), admit), nil, io.Discard, verb, "--prompt", " "); err == nil {
			t.Fatal("empty prompt accepted")
		}
	}
	if err := journalExecute(NewSet(runtime, nil), nil, io.Discard); err == nil {
		t.Fatal("missing human prompt")
	}
	conflict := journalExecute(NewWorkLog(runtime, journalOps(r), admit), nil, io.Discard, "finalize", "--report", "file", "--report-stdin")
	var usage *journalUsageError
	if !errors.As(conflict, &usage) || usage.code != shared.ExitUsage || usage.message != "supply at most one of --report or --report-stdin" {
		t.Fatalf("conflict=%v", conflict)
	}
	if err := journalExecute(NewWorkLog(runtime, journalOps(r), admit), &journalErrorReader{boom}, io.Discard, "finalize", "--report-stdin"); !errors.Is(err, boom) {
		t.Fatalf("stdin error=%v", err)
	}
	if err := journalExecute(NewWorkLog(runtime, journalOps(r), admit), strings.NewReader(strings.Repeat("x", worktrees.MaxFinalizeReportBytes+1)), io.Discard, "finalize", "--report-stdin"); err == nil {
		t.Fatal("oversized stdin accepted")
	}
	var out bytes.Buffer
	if err := writeJournalResult(&activeLimitedWriter{}, "json", worktrees.LogVerbResult{}); err == nil {
		t.Fatal("JSON writer accepted")
	}
	root := &cobra.Command{Use: "worktree"}
	root.AddCommand(NewWorkLog(runtime, journalOps(r), admit))
	if err := journalExecute(root, nil, &out, "work-log"); err != nil {
		t.Fatal(err)
	}
	if o := r.options.(worktrees.LoadWorkLogOptions); !o.IncludePromptBodies {
		t.Fatal("bare log lost private prompt bodies")
	}
}

type journalErrorReader struct{ err error }

func (r *journalErrorReader) Read([]byte) (int, error) { return 0, r.err }
