package cmdworktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktreerun"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"reflect"
	"strings"
	"testing"
)

type createContextKey struct{}
type createRejectedIO struct{ err error }

func (r createRejectedIO) Read([]byte) (int, error)  { return 0, r.err }
func (r createRejectedIO) Write([]byte) (int, error) { return 0, r.err }
func createExecute(c *cobra.Command, in io.Reader, out, stderr io.Writer, args ...string) error {
	c.SetIn(in)
	c.SetOut(out)
	c.SetErr(stderr)
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetArgs(args)
	return c.Execute()
}

// These defaults record domain boundaries; native Git/claim/session journeys
// remain the executable's original tests.
func recordingCreate() CreateDependencies {
	return CreateDependencies{
		Run: func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			return nil, nil
		},
		OriginSlug:         func(context.Context, string) (string, error) { return "acme/app", nil },
		PrepareLog:         func(_, _ string, o worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) { return o, nil },
		RegisteredIdentity: func() (worktrees.AgentIdentity, bool) { return worktrees.AgentIdentity{}, true },
		BeforeCreate:       func(string, []string) error { return nil },
		Claim: func(*cobra.Command, bool, string, string) worktreerun.RemoteClaimOutcome {
			return worktreerun.RemoteClaimOutcome{Outcome: "disabled"}
		},
		AfterCreate: func(*cobra.Command, string, []worktrees.CreateResult) {},
	}
}
func createRuntime() shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: "projects"} }}
}
func TestCreateAdapterPreservesLazyOptionsExactPromptAndLifecycleOrder(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "before"}
	ctx := context.WithValue(context.Background(), createContextKey{}, "request")
	var order []string
	prompt := "  exact private request\n"
	expectedPrompt, promptErr := (worktrees.WorkLogOptions{EffortID: "effort", RunID: "run", Initiator: "human", AgentID: "agent", AgentRuntime: "runtime", Model: "model", CLI: "cli", Provider: "provider", TaskSummary: "summary", RequireOriginalPrompt: true}).WithOriginalPromptFromStdin([]byte(prompt))
	if promptErr != nil {
		t.Fatal(promptErr)
	}
	results := []worktrees.CreateResult{{Action: "created", Repository: "acme/app", WorktreeDir: "checkout", Branch: "topic", Base: "base"}}
	deps := recordingCreate()
	deps.RegisteredIdentity = func() (worktrees.AgentIdentity, bool) {
		order = append(order, "identity")
		return worktrees.AgentIdentity{AgentID: "live"}, true
	}
	deps.PrepareLog = func(root, task string, o worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
		order = append(order, "prepare")
		if root != "after" || task != "task" || !o.RequireOriginalPrompt || o.TaskSummary != "summary" || o.EffortID != "effort" || o.RunID != "run" || o.Initiator != "human" || o.AgentID != "agent" || o.AgentRuntime != "runtime" || o.Model != "model" || o.CLI != "cli" || o.Provider != "provider" || !reflect.DeepEqual(o, expectedPrompt) {
			t.Fatalf("prepare=%s/%s/%+v", root, task, o)
		}
		return o, nil
	}
	deps.BeforeCreate = func(root string, repos []string) error {
		order = append(order, "hooks")
		if root != "after" || !reflect.DeepEqual(repos, []string{"acme/app"}) {
			t.Fatal(root, repos)
		}
		return nil
	}
	deps.Claim = func(c *cobra.Command, no bool, root, task string) worktreerun.RemoteClaimOutcome {
		order = append(order, "claim")
		if c.Context() != ctx || !no || root != "after" || task != "task" {
			t.Fatal("claim options")
		}
		return worktreerun.RemoteClaimOutcome{Outcome: "acquired", Detail: "receipt"}
	}
	deps.Run = func(got context.Context, repos []string, o worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
		order = append(order, "create")
		if got != ctx || !reflect.DeepEqual(repos, []string{"acme/app"}) || o.ProjectsRoot != "after" || o.Operation != "task" || o.Branch != "topic" || !o.BranchChosen || o.BranchPrefixChosen || o.Base != "base" || !o.Resume || !o.SessionRequired || o.WorkLog.EffortID != "effort" || o.WorkLog.RunID != "run" {
			t.Fatalf("create=%v/%v/%+v", got, repos, o)
		}
		return results, nil
	}
	deps.AfterCreate = func(c *cobra.Command, base string, r []worktrees.CreateResult) {
		order = append(order, "markers")
		if c.Context() != ctx || base != "base" || !reflect.DeepEqual(r, results) {
			t.Fatal("markers")
		}
	}
	c := NewCreate(shared.Runtime{Flags: func() shared.Flags { return flags }}, deps)
	c.SetContext(ctx)
	flags.ProjectsRoot = "after"
	var out, stderr bytes.Buffer
	err := createExecute(c, strings.NewReader(prompt), &out, &stderr, "task", "acme/app", "--branch", "topic", "--base", "base", "--mode", "agent", "--resume", "--no-claim", "--effort", "effort", "--run", "run", "--initiator", "human", "--agent", "agent", "--agent-runtime", "runtime", "--model", "model", "--cli", "cli", "--provider", "provider", "--summary", "summary", "--original-prompt-file", "-", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"identity", "prepare", "hooks", "claim", "create", "markers"}) || stderr.Len() != 0 || strings.Contains(out.String(), prompt) {
		t.Fatalf("order=%v out=%q stderr=%q", order, out.String(), stderr.String())
	}
	var document struct {
		Claim   worktreerun.RemoteClaimOutcome `json:"remote_claim"`
		Results []worktrees.CreateResult       `json:"worktrees"`
	}
	if err := json.Unmarshal(out.Bytes(), &document); err != nil || document.Claim.Detail != "receipt" || !reflect.DeepEqual(document.Results, results) {
		t.Fatalf("JSON=%s err=%v", out.String(), err)
	}
}
func TestCreateAdapterDefaultsOriginAndResumeChangedFlags(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		args        []string
		mode        bool
		effort, run string
		prefix      bool
	}{
		{"defaults", nil, false, "generated-effort", "generated-run", false},
		{"implicit resume", []string{"--resume", "--branch-prefix="}, false, "", "", true},
		{"explicit empty resume", []string{"--resume", "--effort=", "--run="}, false, "generated-effort", "generated-run", false},
		{"auto agent", []string{"--agent", " a "}, true, "generated-effort", "generated-run", false},
		{"auto runtime", []string{"--agent-runtime", " r "}, true, "generated-effort", "generated-run", false},
		{"blank mode", []string{"--mode=", "--agent", "a"}, false, "generated-effort", "generated-run", false},
		{"manual", []string{"--mode", "manual", "--initiator", "human"}, false, "generated-effort", "generated-run", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := recordingCreate()
			var order []string
			deps.OriginSlug = func(_ context.Context, path string) (string, error) {
				order = append(order, "origin")
				if path != "." {
					t.Fatal(path)
				}
				return "acme/app", nil
			}
			deps.PrepareLog = func(root, task string, o worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
				order = append(order, "prepare")
				if o.OriginalPrompt != "private-file" {
					t.Fatal(o)
				}
				o.EffortID = "generated-effort"
				o.RunID = "generated-run"
				return o, nil
			}
			deps.RegisteredIdentity = func() (worktrees.AgentIdentity, bool) {
				if !tc.mode {
					t.Fatal("ambient identity must not force auto/blank/manual mode")
				}
				return worktrees.AgentIdentity{}, true
			}
			deps.Run = func(_ context.Context, repos []string, o worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
				if !reflect.DeepEqual(repos, []string{"acme/app"}) || o.Base != "main" || o.BranchChosen || o.BranchPrefixChosen != tc.prefix || o.WorkLog.EffortID != tc.effort || o.WorkLog.RunID != tc.run || o.SessionRequired != tc.mode {
					t.Fatalf("options=%+v", o)
				}
				return []worktrees.CreateResult{{Action: "created", Repository: "acme/app", WorktreeDir: "checkout", Branch: "task", Base: "main"}}, nil
			}
			var out bytes.Buffer
			args := append([]string{"task", "--original-prompt-file", "private-file"}, tc.args...)
			if err := createExecute(NewCreate(createRuntime(), deps), strings.NewReader(""), &out, io.Discard, args...); err != nil || out.String() != "created acme/app: checkout (task from origin/main)\n" || !reflect.DeepEqual(order, []string{"origin", "prepare"}) {
				t.Fatalf("out=%q err=%v order=%v", out.String(), err, order)
			}
		})
	}
}
func TestCreateAdapterRefusalsPreserveErrorIdentityAndPreflightOrder(t *testing.T) {
	t.Parallel()
	boom := errors.New("controlled operation failure")
	for _, tc := range []struct {
		name    string
		args    []string
		setup   func(*CreateDependencies)
		input   io.Reader
		want    string
		wrapped bool
	}{
		{name: "missing task", want: "requires at least 1 arg"},
		{name: "conflicting branch", args: []string{"task", "--branch", "b", "--branch-prefix", "p"}, want: "cannot be used together"},
		{name: "empty branch", args: []string{"task", "--branch="}, want: "must not be empty"},
		{name: "format", args: []string{"task", "--format", "yaml"}, want: "unsupported format"},
		{name: "mode", args: []string{"task", "--mode", "robot"}, want: "unsupported execution mode"},
		{name: "manual audit", args: []string{"task", "--mode", "manual"}, want: "manual execution mode requires"},
		{name: "agent session", args: []string{"task", "--mode", "agent"}, setup: func(d *CreateDependencies) {
			d.RegisteredIdentity = func() (worktrees.AgentIdentity, bool) { return worktrees.AgentIdentity{}, false }
		}, want: "requires a live registered session"},
		{name: "stdin read", args: []string{"task", "--original-prompt-file", "-"}, input: createRejectedIO{boom}, want: "read --original-prompt-file - from stdin", wrapped: true},
		{name: "stdin empty", args: []string{"task", "--original-prompt-file", "-"}, input: strings.NewReader(" \n"), want: "requires non-empty stdin"},
		{name: "origin", args: []string{"task"}, setup: func(d *CreateDependencies) {
			d.OriginSlug = func(context.Context, string) (string, error) { return "", boom }
		}, want: "derive current repository", wrapped: true},
		{name: "repository traversal", args: []string{"task", "../app"}, want: "repository"},
		{name: "prepare", args: []string{"task", "acme/app"}, setup: func(d *CreateDependencies) {
			d.PrepareLog = func(string, string, worktrees.WorkLogOptions) (worktrees.WorkLogOptions, error) {
				return worktrees.WorkLogOptions{}, boom
			}
		}, want: boom.Error(), wrapped: true},
		{name: "hooks", args: []string{"task", "acme/app"}, setup: func(d *CreateDependencies) { d.BeforeCreate = func(string, []string) error { return boom } }, want: boom.Error(), wrapped: true},
		{name: "create", args: []string{"task", "acme/app"}, setup: func(d *CreateDependencies) {
			d.Run = func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
				return nil, boom
			}
		}, want: boom.Error(), wrapped: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			deps := recordingCreate()
			var claims, markers int
			deps.Claim = func(*cobra.Command, bool, string, string) worktreerun.RemoteClaimOutcome {
				claims++
				return worktreerun.RemoteClaimOutcome{Outcome: "disabled"}
			}
			deps.AfterCreate = func(*cobra.Command, string, []worktrees.CreateResult) { markers++ }
			if tc.setup != nil {
				tc.setup(&deps)
			}
			var out bytes.Buffer
			input := tc.input
			if input == nil {
				input = strings.NewReader("")
			}
			err := createExecute(NewCreate(createRuntime(), deps), input, &out, io.Discard, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) || tc.wrapped && !errors.Is(err, boom) || out.Len() != 0 || markers != 0 || claims != map[bool]int{true: 1, false: 0}[tc.name == "create"] {
				t.Fatalf("err=%v out=%q claims=%d markers=%d", err, out.String(), claims, markers)
			}
		})
	}
}
func TestCreateAdapterWriterFailuresKeepCompletedLifecycleAndExactIdentity(t *testing.T) {
	t.Parallel()
	boom := errors.New("writer failed")
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			deps := recordingCreate()
			marked := false
			deps.Run = func(context.Context, []string, worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
				return []worktrees.CreateResult{{Repository: "acme/app"}}, nil
			}
			deps.AfterCreate = func(*cobra.Command, string, []worktrees.CreateResult) { marked = true }
			if err := createExecute(NewCreate(createRuntime(), deps), strings.NewReader(""), createRejectedIO{boom}, io.Discard, "task", "acme/app", "--format", format); !errors.Is(err, boom) || !marked {
				t.Fatalf("writer=%v marked=%t", err, marked)
			}
		})
	}
	var out bytes.Buffer
	if err := createExecute(NewCreate(createRuntime(), recordingCreate()), strings.NewReader(""), &out, io.Discard, "task", "acme/app"); err != nil || out.Len() != 0 {
		t.Fatalf("empty results=%q/%v", out.String(), err)
	}
}
