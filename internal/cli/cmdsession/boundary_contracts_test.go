package cmdsession

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
	"github.com/sneat-dev/wb/internal/sessionreceive"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type boundaryContextKey struct{}

type boundaryForbiddenReader struct{ t *testing.T }

func (r boundaryForbiddenReader) Read([]byte) (int, error) {
	r.t.Fatal("input read for invalid format")
	return 0, io.EOF
}

type boundaryOutputFailure struct{ err error }

func (w boundaryOutputFailure) Write([]byte) (int, error) { return 0, w.err }

type boundaryFailWriteNumber struct {
	writes, at int
	err        error
}

func (w *boundaryFailWriteNumber) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.at {
		return 0, w.err
	}
	return len(p), nil
}

func TestSessionRegistryContainsAllVerbsAndPickupAlias(t *testing.T) {
	t.Parallel()
	command := New(shared.Runtime{}, Dependencies{})
	var names []string
	for _, child := range command.Commands() {
		names = append(names, child.Name())
	}
	sort.Strings(names)
	want := []string{"list", "move", "park", "prune", "recall", "receive", "receive-message", "receive-park", "register", "resume", "send"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("children=%v, want %v", names, want)
	}
	command.SetOut(io.Discard)
	command.SetArgs([]string{"--help"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	alias, _, err := command.Find([]string{"pickup"})
	if err != nil || alias.Name() != "resume" {
		t.Fatalf("alias=%v error=%v", alias, err)
	}
}

func TestSessionFormatsRefuseBeforeReadingOrCallingEffects(t *testing.T) {
	t.Parallel()
	builders := []func(shared.Runtime, Dependencies) *cobra.Command{NewList, NewMove, NewPark, NewResume, NewReceive, NewReceivePark}
	for i, build := range builders {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			command := build(shared.Runtime{Flags: func() shared.Flags { t.Fatal("flags read after invalid format"); return shared.Flags{} }}, Dependencies{})
			args := []string{"--format", "invalid"}
			if i == 3 {
				args = append([]string{"park-id"}, args...)
			}
			command.SetArgs(args)
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetIn(boundaryForbiddenReader{t})
			if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "unsupported format") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRegisterJoinsOnlyAfterSuccessfulRegistrationOutput(t *testing.T) {
	t.Parallel()
	want := errors.New("writer stopped")
	for _, at := range []int{1, 2, 0} {
		t.Run(string(rune('a'+at)), func(t *testing.T) {
			joined := 0
			ctx := context.WithValue(context.Background(), boundaryContextKey{}, "private")
			root := "before"
			getterCalls := 0
			command := NewRegister(shared.Runtime{Flags: func() shared.Flags { getterCalls++; return shared.Flags{ProjectsRoot: root} }}, Dependencies{
				Register: func(got context.Context, r sessionrun.RegisterRequest) (session.Record, error) {
					if got != ctx || r.ProjectsRoot != "parsed" || r.Record.PID != 41 {
						t.Fatalf("request=%+v context=%v", r, got)
					}
					return session.Record{PID: 41, Runtime: "codex", WBVersion: "v"}, nil
				},
				Join: func(got context.Context, path string) error {
					if got != ctx || path != "checkout" {
						t.Fatal("join binding")
					}
					joined++
					return nil
				},
			})
			if getterCalls != 0 {
				t.Fatal("eager flags")
			}
			root = "parsed"
			command.SetContext(ctx)
			command.SetArgs([]string{"--pid", "41", "--join", "checkout"})
			command.SetErr(io.Discard)
			writer := &boundaryFailWriteNumber{at: at, err: want}
			command.SetOut(writer)
			err := command.Execute()
			if at > 0 && !errors.Is(err, want) {
				t.Fatalf("error=%v", err)
			}
			if at == 0 && err != nil {
				t.Fatal(err)
			}
			expected := 1
			if at == 1 {
				expected = 0
			}
			if joined != expected || getterCalls != 1 {
				t.Fatalf("joins=%d flags=%d", joined, getterCalls)
			}
		})
	}
	command := NewRegister(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, Dependencies{Register: func(context.Context, sessionrun.RegisterRequest) (session.Record, error) {
		return session.Record{}, nil
	}, Join: func(context.Context, string) error { return want }})
	command.SetArgs([]string{"--join", "x"})
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	if err := command.Execute(); !errors.Is(err, want) || !strings.Contains(err.Error(), "registered but not joined") {
		t.Fatalf("join error=%v", err)
	}
}

func TestSessionLifecyclePropagatesEffectAndWriterFailures(t *testing.T) {
	t.Parallel()
	want := errors.New("bound effect or stream failed")
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "private"} }}
	cases := []struct {
		name  string
		build func() *cobra.Command
		args  []string
	}{
		{"list effect", func() *cobra.Command {
			return NewList(runtime, Dependencies{List: func(context.Context, sessionrun.ListRequest, func(string)) ([]sessionrun.Row, error) {
				return nil, want
			}})
		}, nil},
		{"prune effect", func() *cobra.Command {
			return NewPrune(runtime, Dependencies{Prune: func(context.Context, string) (int, error) { return 0, want }})
		}, nil},
		{"park effect", func() *cobra.Command {
			return NewPark(runtime, Dependencies{Park: func(context.Context, sessionrun.ParkRequest) (sessionrun.ParkResult, error) {
				return sessionrun.ParkResult{}, want
			}})
		}, []string{"--context-file", "-"}},
		{"resume effect", func() *cobra.Command {
			return NewResume(runtime, Dependencies{Resume: func(context.Context, sessionrun.ResumeRequest) (sessionrun.ResumeResult, error) {
				return sessionrun.ResumeResult{}, want
			}})
		}, []string{"park-id"}},
		{"park registration stream", func() *cobra.Command {
			return NewPark(runtime, Dependencies{Park: func(context.Context, sessionrun.ParkRequest) (sessionrun.ParkResult, error) {
				return sessionrun.ParkResult{Output: sessionrun.ParkOutput{RegisteredAtPark: true, WBSessionID: "s"}}, nil
			}})
		}, []string{"--context-file", "-"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			command := test.build()
			command.SetArgs(test.args)
			command.SetIn(strings.NewReader("continuation"))
			command.SetOut(boundaryOutputFailure{want})
			command.SetErr(io.Discard)
			if err := command.Execute(); !errors.Is(err, want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestReceiveRendersEachActualStageAndReturnsWriterErrors(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(t.Context(), boundaryContextKey{}, "receiver")
	receipt := &sessionmove.Receipt{SuccessorWBSessionID: "successor", TmuxName: "tmux", PinnedCommit: "commit"}
	request := sessionmove.Request{HandoffID: "handoff"}
	cases := []struct {
		name   string
		result sessionreceive.Result
		want   string
	}{
		{"no worktree", sessionreceive.Result{Request: request, Phase: sessionmove.PhaseReceived}, "handoff handoff phase received at pinned target worktree \n"},
		{"worktree", sessionreceive.Result{Request: request, Phase: sessionmove.PhaseWorktreeReady, Worktree: &worktrees.SessionReceiveResult{WorktreeDir: "private-target"}}, "handoff handoff phase worktree_ready at pinned target worktree private-target\n"},
		{"successor", sessionreceive.Result{Request: request, Successor: &sessionlaunch.Result{WBSessionID: "successor", TmuxName: "tmux", PinnedCommit: "commit"}}, "handoff handoff started successor successor in tmux tmux at exact commit commit; predecessor custody remains active pending a receipt\n"},
		{"replay", sessionreceive.Result{Request: request, Receipt: receipt}, "replayed completed handoff handoff receipt for successor successor in tmux tmux\n"},
		{"completed", sessionreceive.Result{Request: request, Receipt: receipt, Successor: &sessionlaunch.Result{}}, "completed handoff handoff for successor successor in tmux tmux at exact commit commit; durable target receipt recorded\n"},
	}
	wantErr := errors.New("receiver writer")
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			for _, failed := range []bool{false, true} {
				root := "before"
				calls := 0
				command := NewReceive(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }}, Dependencies{Receive: func(got context.Context, r sessionrun.ReceiveRequest) (sessionreceive.Result, error) {
					calls++
					if got != ctx || r.ProjectsRoot != "parsed" || string(r.Raw) != " \nexact\n" {
						t.Fatalf("request=%+v context=%v", r, got)
					}
					return test.result, nil
				}})
				root = "parsed"
				var out bytes.Buffer
				command.SetOut(&out)
				if failed {
					command.SetOut(boundaryOutputFailure{wantErr})
				}
				command.SetIn(strings.NewReader(" \nexact\n"))
				command.SetErr(io.Discard)
				command.SetArgs(nil)
				err := command.ExecuteContext(ctx)
				if calls != 1 {
					t.Fatalf("calls=%d", calls)
				}
				if failed {
					if !errors.Is(err, wantErr) {
						t.Fatalf("writer error=%v", err)
					}
				} else if err != nil || out.String() != test.want {
					t.Fatalf("output=%q want=%q error=%v", out.String(), test.want, err)
				}
			}
		})
	}
	for _, format := range []string{"text", "json"} {
		command := NewReceivePark(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, Dependencies{ReceivePark: func(context.Context, sessionrun.ReceiveRequest) (sessionparkreceive.Result, error) {
			return sessionparkreceive.Result{Receipt: &sessionpark.Receipt{}, Replay: true}, nil
		}})
		command.SetArgs([]string{"--format", format})
		command.SetIn(strings.NewReader("exact"))
		command.SetOut(boundaryOutputFailure{wantErr})
		command.SetErr(io.Discard)
		if err := command.Execute(); !errors.Is(err, wantErr) {
			t.Fatalf("format=%s error=%v", format, err)
		}
	}
}

func TestReceiveInputFailuresNeverReachOperations(t *testing.T) {
	t.Parallel()
	for _, build := range []func(shared.Runtime, Dependencies) *cobra.Command{NewReceive, NewReceivePark} {
		for _, input := range []io.Reader{cwWtErrorReader{}, strings.NewReader(strings.Repeat("x", sessionpark.MaxEnvelopeBytes+1))} {
			command := build(shared.Runtime{}, Dependencies{})
			command.SetIn(input)
			command.SetOut(&bytes.Buffer{})
			command.SetErr(io.Discard)
			command.SetArgs(nil)
			if err := command.Execute(); err == nil {
				t.Fatal("invalid input accepted")
			}
		}
	}
}

func TestReceiverEffectErrorsRemainExactAndUnreceiptedParkIsNotSuccess(t *testing.T) {
	t.Parallel()
	want := errors.New("actual receiver refused")
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}
	cases := []struct {
		name     string
		command  *cobra.Command
		expected error
		fragment string
	}{
		{"receive", NewReceive(runtime, Dependencies{Receive: func(context.Context, sessionrun.ReceiveRequest) (sessionreceive.Result, error) {
			return sessionreceive.Result{}, want
		}}), want, ""},
		{"park", NewReceivePark(runtime, Dependencies{ReceivePark: func(context.Context, sessionrun.ReceiveRequest) (sessionparkreceive.Result, error) {
			return sessionparkreceive.Result{}, want
		}}), want, ""},
		{"unreceipted park", NewReceivePark(runtime, Dependencies{ReceivePark: func(context.Context, sessionrun.ReceiveRequest) (sessionparkreceive.Result, error) {
			return sessionparkreceive.Result{ResumeID: "resume", Phase: "admitted"}, nil
		}}), nil, "park resume resume ended at phase admitted without a durable receipt"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			test.command.SilenceUsage = true
			test.command.SilenceErrors = true
			test.command.SetIn(strings.NewReader("exact"))
			test.command.SetOut(&out)
			test.command.SetErr(io.Discard)
			test.command.SetArgs(nil)
			err := test.command.Execute()
			if test.expected != nil && !errors.Is(err, test.expected) {
				t.Fatalf("error=%v", err)
			}
			if test.fragment != "" && (err == nil || err.Error() != test.fragment) {
				t.Fatalf("error=%v", err)
			}
			if out.Len() != 0 {
				t.Fatalf("refusal wrote stdout: %q", out.String())
			}
		})
	}
}

func TestSessionListPropagatesDynamicRowAndFlushFailures(t *testing.T) {
	t.Parallel()
	want := errors.New("bound list stream failed")
	for _, model := range []string{"normal model", "model\fcontinued"} {
		t.Run(fmt.Sprintf("model %q", model), func(t *testing.T) {
			row := sessionrun.Row{View: session.View{Record: session.Record{Runtime: "codex", Model: model}}}
			if err := renderSessions(boundaryOutputFailure{want}, []sessionrun.Row{row}); !errors.Is(err, want) {
				t.Fatalf("row/flush error=%v", err)
			}
		})
	}
	var out bytes.Buffer
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}
	command := NewList(runtime, Dependencies{List: func(_ context.Context, _ sessionrun.ListRequest, warn func(string)) ([]sessionrun.Row, error) {
		warn("first diagnostic")
		return nil, nil
	}})
	command.SetOut(&out)
	command.SetErr(boundaryOutputFailure{want})
	command.SetArgs([]string{"--format", "json"})
	if err := command.Execute(); err != nil || strings.TrimSpace(out.String()) != "[]" {
		t.Fatalf("best-effort diagnostic affected empty JSON: %q, %v", out.String(), err)
	}
}

func TestSessionInvalidArgumentsAndInvalidUTF8CannotReachOperations(t *testing.T) {
	t.Parallel()
	for _, build := range []func(shared.Runtime, Dependencies) *cobra.Command{NewRegister, NewList, NewPrune, NewMove, NewPark, NewResume, NewReceive, NewReceivePark, NewSend, NewRecall, NewReceiveMessage} {
		command := build(shared.Runtime{Flags: func() shared.Flags { t.Fatal("flags read for invalid arguments"); return shared.Flags{} }}, Dependencies{})
		command.SetArgs([]string{"extra", "extra", "extra"})
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		if err := command.Execute(); err == nil {
			t.Fatalf("%s accepted invalid arguments", command.Name())
		}
	}
	command := &cobra.Command{}
	if _, err := readSessionMessageBody(command, string([]byte{0xff}), "", true); err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("invalid native message bytes: %v", err)
	}
}

func TestListBindsCurrentFlagsWarningsAndEachConcreteOutput(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(t.Context(), boundaryContextKey{}, "list")
	row := sessionrun.Row{View: session.View{Record: session.Record{WBSessionID: "session", Machine: "private-machine", PID: 73, Runtime: "codex", Model: "model", WBVersion: "version"}, State: "live"}, Efforts: []string{"effort"}, Worktrees: []string{"worktree"}, Branches: []string{"branch"}, Waiting: []string{"waiting"}}
	for _, format := range []string{"text", "json"} {
		for _, empty := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s empty=%t", format, empty), func(t *testing.T) {
				root := "before"
				calls := 0
				flags := 0
				command := NewList(shared.Runtime{Flags: func() shared.Flags { flags++; return shared.Flags{ProjectsRoot: root} }}, Dependencies{List: func(got context.Context, r sessionrun.ListRequest, warn func(string)) ([]sessionrun.Row, error) {
					calls++
					if got != ctx || r.ProjectsRoot != "parsed" || !r.OnlyLive {
						t.Fatalf("request=%+v context=%v", r, got)
					}
					warn("bounded diagnostic\n")
					if empty {
						return nil, nil
					}
					return []sessionrun.Row{row}, nil
				}})
				if flags != 0 {
					t.Fatal("constructor snapshotted flags")
				}
				root = "parsed"
				var out, diagnostics bytes.Buffer
				command.SetOut(&out)
				command.SetErr(&diagnostics)
				command.SetArgs([]string{"--format", format, "--live"})
				if err := command.ExecuteContext(ctx); err != nil {
					t.Fatal(err)
				}
				if calls != 1 || flags != 1 || !strings.HasPrefix(diagnostics.String(), "bounded diagnostic\n") {
					t.Fatalf("calls=%d flags=%d diagnostics=%q", calls, flags, diagnostics.String())
				}
				if empty {
					if format == "json" {
						if strings.TrimSpace(out.String()) != "[]" || !strings.Contains(diagnostics.String(), "no session has registered") {
							t.Fatalf("empty JSON=%q diagnostics=%q", out.String(), diagnostics.String())
						}
					} else if !strings.Contains(out.String(), "no session has registered") {
						t.Fatalf("empty text=%q", out.String())
					}
				} else if !strings.Contains(out.String(), "session") || !strings.Contains(out.String(), "private-machine") || !strings.Contains(out.String(), "waiting") {
					t.Fatalf("row output=%q", out.String())
				}
			})
		}
	}
	// An empty text result must still propagate the bound output's failure.
	want := errors.New("empty list output refused")
	command := NewList(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, Dependencies{List: func(context.Context, sessionrun.ListRequest, func(string)) ([]sessionrun.Row, error) { return nil, nil }})
	command.SetOut(boundaryOutputFailure{want})
	command.SetErr(io.Discard)
	command.SetArgs(nil)
	if err := command.Execute(); !errors.Is(err, want) {
		t.Fatalf("empty output error=%v", err)
	}
}

func TestListColumnCondensingPreservesUnicodeAndCounts(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		values []string
		max    int
		want   string
	}{
		{"none", nil, 4, "-"},
		{"short", []string{"é中"}, 4, "é中"},
		{"bounded unicode", []string{"é中🙂tail"}, 3, "é中🙂…"},
		{"multiple", []string{"first", "second"}, 4, "2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := condense(test.values, test.max); got != test.want {
				t.Fatalf("column=%q, want %q", got, test.want)
			}
		})
	}
}
