package cmdtask

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/secretscan"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/sneat-dev/wb/internal/taskrun"
	"github.com/spf13/cobra"
)

func runtime(root *string) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: *root} }}
}
func execute(t *testing.T, command *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&diagnostics)
	command.SetArgs(args)
	command.SilenceErrors = true
	command.SilenceUsage = true
	err := command.ExecuteContext(t.Context())
	return out.String(), diagnostics.String(), err
}
func output() taskrun.Result {
	return taskrun.Result{TaskID: "task-id", Task: "review-auth", WorktreeDir: "/private/review", Status: "offloaded"}
}
func moved() sessionrun.MoveResult {
	return sessionrun.MoveResult{Phase: "complete", Receipt: &sessionmove.Receipt{SuccessorWBSessionID: "successor", TmuxName: "tmux"}}
}
func TestTaskFlagsRawInputContextAndTwoJSONDocuments(t *testing.T) {
	t.Parallel()
	root := "before"
	var got taskrun.Request
	op := func(ctx context.Context, req taskrun.Request, warn func([]secretscan.Finding), render func(sessionrun.MoveResult) error) (taskrun.Result, error) {
		got = req
		if ctx != t.Context() {
			t.Fatal("context changed")
		}
		warn([]secretscan.Finding{{}})
		if err := render(moved()); err != nil {
			return taskrun.Result{}, err
		}
		return output(), nil
	}
	cmd := NewOffload(runtime(&root), op, false)
	root = "after"
	raw := "  raw brief\n"
	cmd.SetIn(strings.NewReader(raw))
	out, diagnostics, err := execute(t, cmd, "review-auth", "acme/app", "--context-file=-", "--harness=claude", "--model=opus", "--to=vm", "--format=json")
	if err != nil {
		t.Fatal(err)
	}
	expected := taskrun.Request{ProjectsRoot: "after", Task: "review-auth", Repositories: []string{"acme/app"}, Continuation: []byte(raw), ContextFile: "-", Harness: "claude", Model: "opus", Target: "vm"}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("request = %#v", got)
	}
	if !strings.Contains(diagnostics, "secret scan advisory:") {
		t.Fatal(diagnostics)
	}
	decoder := json.NewDecoder(strings.NewReader(out))
	var move sessionrun.MoveResult
	var task taskrun.Result
	if err := decoder.Decode(&move); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&task); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatal("not exactly two JSON documents", err)
	}
	if move.Phase != "complete" || task != output() {
		t.Fatal(move, task)
	}
}
func TestParkAndOffloadDefaultRequestsAndText(t *testing.T) {
	t.Parallel()
	for _, park := range []bool{false, true} {
		t.Run(map[bool]string{false: "offload", true: "park"}[park], func(t *testing.T) {
			t.Parallel()
			root := "private"
			op := func(_ context.Context, req taskrun.Request, _ func([]secretscan.Finding), _ func(sessionrun.MoveResult) error) (taskrun.Result, error) {
				expected := taskrun.Request{ProjectsRoot: root, Task: "review-auth", Repositories: []string{}, Continuation: []byte("brief"), ContextFile: "-", ParkOnly: park}
				if !reflect.DeepEqual(req, expected) {
					t.Fatalf("default request=%#v", req)
				}
				return output(), nil
			}
			cmd := NewOffload(runtime(&root), op, park)
			cmd.SetIn(strings.NewReader("brief"))
			out, _, err := execute(t, cmd, "review-auth", "--context-file=-")
			if err != nil {
				t.Fatal(err)
			}
			verb := "offloaded"
			if park {
				verb = "parked"
			}
			if out != verb+" task review-auth as task-id in /private/review\n" {
				t.Fatal(out)
			}
		})
	}
}
func TestPickupDefaultsFlagsWarningsAndCurrentStreams(t *testing.T) {
	t.Parallel()
	for _, flags := range []bool{false, true} {
		t.Run(map[bool]string{false: "defaults", true: "flags"}[flags], func(t *testing.T) {
			t.Parallel()
			root := "before"
			want := taskrun.PickupRequest{ProjectsRoot: "after", TaskID: "task-id"}
			args := []string{"task-id"}
			if flags {
				want.Harness = "codex"
				want.Model = "sol"
				want.Target = "vm"
				args = append(args, "--harness=codex", "--model=sol", "--to=vm", "--format=json")
			}
			operation := func(ctx context.Context, req taskrun.PickupRequest, warn func([]secretscan.Finding), render func(sessionrun.MoveResult) error) (taskrun.Result, error) {
				if ctx != t.Context() || req != want {
					t.Fatal(req)
				}
				warn([]secretscan.Finding{{}})
				if err := render(moved()); err != nil {
					return taskrun.Result{}, err
				}
				return output(), nil
			}
			command := NewPickup(runtime(&root), operation)
			root = "after"
			out, diagnostics, err := execute(t, command, args...)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(diagnostics, "secret scan advisory:") {
				t.Fatal(diagnostics)
			}
			if flags {
				if !strings.Contains(out, `"status": "offloaded"`) {
					t.Fatal(out)
				}
			} else {
				if !strings.Contains(out, "moved session") || !strings.Contains(out, "offloaded task review-auth") {
					t.Fatal(out)
				}
			}
		})
	}
}
func TestUsageAndInputErrorsDoNotDelegate(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"offload"}, {"offload", "task"}, {"offload", "task", "--context-file=-", "--format=xml"}, {"park", "task", "--context-file="}, {"offload", "task", "--context-file=/missing/brief"}, {"pickup"}, {"pickup", "id", "extra"}, {"pickup", "id", "--format=xml"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			root := "private"
			called := false
			command := New(runtime(&root), Operations{Offload: func(context.Context, taskrun.Request, func([]secretscan.Finding), func(sessionrun.MoveResult) error) (taskrun.Result, error) {
				called = true
				return output(), nil
			}, Pickup: func(context.Context, taskrun.PickupRequest, func([]secretscan.Finding), func(sessionrun.MoveResult) error) (taskrun.Result, error) {
				called = true
				return output(), nil
			}})
			_, stderr, err := execute(t, command, args...)
			if err == nil || called {
				t.Fatal("invalid request delegated", err)
			}
			if len(args) > 2 && args[2] == "--context-file=" && !strings.Contains(stderr, "why anything was left uncommitted") {
				t.Fatal("missing checklist", stderr)
			}
		})
	}
}
func TestReadNamedContextAndPropagateDelegatedErrors(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(path, []byte("private brief"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := "private"
	want := errors.New("operation unavailable")
	command := NewOffload(runtime(&root), func(_ context.Context, req taskrun.Request, _ func([]secretscan.Finding), _ func(sessionrun.MoveResult) error) (taskrun.Result, error) {
		if string(req.Continuation) != "private brief" {
			t.Fatal(req)
		}
		return taskrun.Result{}, want
	}, false)
	out, _, err := execute(t, command, "task", "--context-file", path)
	if err != want || out != "" {
		t.Fatal(err, out)
	}
	command = NewPickup(runtime(&root), func(context.Context, taskrun.PickupRequest, func([]secretscan.Finding), func(sessionrun.MoveResult) error) (taskrun.Result, error) {
		return taskrun.Result{}, want
	})
	out, _, err = execute(t, command, "id")
	if err != want || out != "" {
		t.Fatal(err, out)
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }
func TestMoveAndFinalWriterFailuresRemainObservable(t *testing.T) {
	t.Parallel()
	for _, pickup := range []bool{false, true} {
		for _, format := range []string{"text", "json"} {
			for _, move := range []bool{false, true} {
				t.Run(strings.Join([]string{map[bool]string{true: "pickup", false: "offload"}[pickup], format, map[bool]string{true: "move", false: "task"}[move]}, "/"), func(t *testing.T) {
					t.Parallel()
					root := "private"
					want := errors.New("writer unavailable")
					operation := func(render func(sessionrun.MoveResult) error) (taskrun.Result, error) {
						if move {
							if err := render(moved()); err != nil {
								return taskrun.Result{}, err
							}
						}
						return output(), nil
					}
					var cmd *cobra.Command
					if pickup {
						cmd = NewPickup(runtime(&root), func(_ context.Context, _ taskrun.PickupRequest, _ func([]secretscan.Finding), render func(sessionrun.MoveResult) error) (taskrun.Result, error) {
							return operation(render)
						})
						cmd.SetArgs([]string{"id", "--format=" + format})
					} else {
						cmd = NewOffload(runtime(&root), func(_ context.Context, _ taskrun.Request, _ func([]secretscan.Finding), render func(sessionrun.MoveResult) error) (taskrun.Result, error) {
							return operation(render)
						}, false)
						cmd.SetIn(strings.NewReader("brief"))
						cmd.SetArgs([]string{"task", "--context-file=-", "--format=" + format})
					}
					cmd.SetOut(errorWriter{want})
					cmd.SetErr(io.Discard)
					cmd.SilenceUsage = true
					cmd.SilenceErrors = true
					if err := cmd.ExecuteContext(t.Context()); !errors.Is(err, want) {
						t.Fatal("writer error lost", err)
					}
				})
			}
		}
	}
}
func TestIndependentCommandsAndRepeatedExecutionKeepRawOptions(t *testing.T) {
	t.Parallel()
	root := "private"
	calls := 0
	op := func(_ context.Context, req taskrun.Request, _ func([]secretscan.Finding), _ func(sessionrun.MoveResult) error) (taskrun.Result, error) {
		calls++
		if req.Harness != "claude" || req.Model != " sonnet " {
			t.Fatal(req)
		}
		return output(), nil
	}
	cmd := NewOffload(runtime(&root), op, true)
	for range 2 {
		cmd.SetIn(strings.NewReader("brief"))
		if _, _, err := execute(t, cmd, "task", "--context-file=-", "--harness=claude", "--model= sonnet "); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	other := NewOffload(runtime(&root), func(_ context.Context, req taskrun.Request, _ func([]secretscan.Finding), _ func(sessionrun.MoveResult) error) (taskrun.Result, error) {
		if req.Harness != "" || req.Model != "" {
			t.Fatal("options leaked", req)
		}
		return output(), nil
	}, true)
	other.SetIn(strings.NewReader("brief"))
	if _, _, err := execute(t, other, "task", "--context-file=-"); err != nil {
		t.Fatal(err)
	}
}

func TestTaskParkJSONPreservesParkedResultAndStdinRequest(t *testing.T) {
	t.Parallel()
	root := "private"
	want := output()
	want.Status = "parked"
	command := NewOffload(runtime(&root), func(_ context.Context, req taskrun.Request, _ func([]secretscan.Finding), _ func(sessionrun.MoveResult) error) (taskrun.Result, error) {
		if !req.ParkOnly || req.ContextFile != "-" || string(req.Continuation) != "Review auth with tests." {
			t.Fatal(req)
		}
		return want, nil
	}, true)
	command.SetIn(strings.NewReader("Review auth with tests."))
	out, _, err := execute(t, command, "review-auth", "acme/app", "--context-file=-", "--format=json")
	if err != nil {
		t.Fatal(err)
	}
	var result taskrun.Result
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	if result != want || result.Status != "parked" || result.TaskID == "" {
		t.Fatalf("park output = %+v", result)
	}
}
