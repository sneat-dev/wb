package cmdworktree

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
	"io"
	"reflect"
	"strings"
	"testing"
)

type movementContextKey struct{}

func movementExecute(c *cobra.Command, out, stderr io.Writer, args ...string) error {
	c.SilenceUsage = true
	c.SilenceErrors = true
	c.SetOut(out)
	c.SetErr(stderr)
	c.SetArgs(args)
	return c.Execute()
}
func TestMovementRenamePreservesLazyOptionsBranchesAndPostApplyOrder(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "before", Filter: "before"}
	ctx := context.WithValue(context.Background(), movementContextKey{}, "caller")
	var order []string
	outcome := worktrees.RenameOutcome{ReportPath: "report", Results: []worktrees.RenameResult{{Applied: true, OldTask: "old"}}, Diagnostics: []worktrees.ListDiagnostic{{Task: "old", Path: "candidate", Message: "malformed"}}}
	c := NewRename(shared.Runtime{Flags: func() shared.Flags { return flags }}, RenameDependencies{
		Run: func(got context.Context, o worktrees.RenameOptions) (worktrees.RenameOutcome, error) {
			order = append(order, "run")
			want := worktrees.RenameOptions{ProjectsRoot: "after", OldTask: "old", NewTask: "new", Filter: "selected", Branch: "topic", BranchChosen: true, Base: "base", Force: true, DeleteRemote: true, PreserveCachePaths: []string{"cache", "build"}, Apply: true, ReportDir: "reports", WorkLog: worktrees.WorkLogOptions{EffortID: "effort", RunID: "run", Initiator: "human", AgentID: "agent", AgentRuntime: "runtime", Model: "model", CLI: "cli", Provider: "provider", OriginalPrompt: "prompt", RequireOriginalPrompt: true}}
			if got != ctx || !reflect.DeepEqual(o, want) {
				t.Fatalf("rename context/options=%v/%+v want=%+v", got, o, want)
			}
			return outcome, nil
		},
		Admit: func(c *cobra.Command, mutate bool) (worktrees.AgentIdentity, func(), error) {
			order = append(order, "admit")
			if c.Context() != ctx || !mutate {
				t.Fatal("admission")
			}
			return worktrees.AgentIdentity{AgentID: "actual"}, func() { order = append(order, "release") }, nil
		},
		AfterApply: func(c *cobra.Command, base string, r []worktrees.RenameResult) {
			order = append(order, "markers")
			if c.Context() != ctx || base != "base" || !reflect.DeepEqual(r, outcome.Results) {
				t.Fatal("markers")
			}
		}})
	c.SetContext(ctx)
	flags = shared.Flags{ProjectsRoot: "after", Filter: "selected"}
	var out, stderr bytes.Buffer
	if err := movementExecute(c, &out, &stderr, "old", "new", "--branch", "topic", "--base", "base", "--force", "--remote", "--preserve-cache", "cache,build", "--effort", "effort", "--run", "run", "--initiator", "human", "--agent", "agent", "--agent-runtime", "runtime", "--model", "model", "--cli", "cli", "--provider", "provider", "--original-prompt-file", "prompt", "--apply", "--report-dir", "reports"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"admit", "run", "markers", "release"}) || !strings.Contains(stderr.String(), "warning: rename skipped malformed candidate") || !strings.HasSuffix(out.String(), "report: report\n") {
		t.Fatalf("order=%v stderr=%q out=%q", order, stderr.String(), out.String())
	}
}
func TestMovementRelocatePreservesLazyOptionsProgressAndJSONShortcut(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "before"}
	ctx := context.WithValue(context.Background(), movementContextKey{}, "caller")
	var order []string
	outcome := worktrees.RelocateOutcome{SchemaVersion: 1, Results: []worktrees.RelocateResult{{Eligible: true}}, Diagnostics: []worktrees.ListDiagnostic{{Task: "task", Path: "candidate", Message: "malformed"}}}
	c := NewRelocate(shared.Runtime{Flags: func() shared.Flags { return flags }}, RelocateDependencies{
		Run: func(got context.Context, o worktrees.RelocateOptions) (worktrees.RelocateOutcome, error) {
			order = append(order, "run")
			if got != ctx || !reflect.DeepEqual(o, worktrees.RelocateOptions{ProjectsRoot: "after", Task: "task", Filter: "selected", To: "shared", Apply: true}) {
				t.Fatal(o)
			}
			return outcome, nil
		},
		Admit: func(c *cobra.Command, mutate bool) (worktrees.AgentIdentity, func(), error) {
			order = append(order, "admit")
			if c.Context() != ctx || !mutate {
				t.Fatal("admission")
			}
			return worktrees.AgentIdentity{}, func() { order = append(order, "release") }, nil
		},
		AfterApply: func(c *cobra.Command, r []worktrees.RelocateResult) {
			order = append(order, "markers")
			if c.Context() != ctx || !reflect.DeepEqual(r, outcome.Results) {
				t.Fatal("markers")
			}
		},
		StartProgress: func(out io.Writer, task string) func() {
			order = append(order, "progress")
			if task != "task" {
				t.Fatal(task)
			}
			return func() { order = append(order, "stop") }
		}})
	c.SetContext(ctx)
	flags = shared.Flags{ProjectsRoot: "after", Filter: "selected"}
	var out, stderr bytes.Buffer
	// JSON preserves the existing outcome envelope and bypasses text-only unfulfilled-plan refusal.
	if err := movementExecute(c, &out, &stderr, "task", "--to", "shared", "--apply", "--json", "--format", "json"); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(outcome)
	if out.String() != string(want)+"\n" || !reflect.DeepEqual(order, []string{"admit", "progress", "run", "markers", "stop", "release"}) || !strings.Contains(stderr.String(), "warning: relocate skipped malformed candidate") {
		t.Fatalf("out=%q order=%v stderr=%q", out.String(), order, stderr.String())
	}
}
func TestMovementValidationOperationAndWriterFailurePrecedence(t *testing.T) {
	t.Parallel()
	boom := errors.New("failure")
	runtime := shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}
	for _, name := range []string{"rename", "relocate"} {
		for _, variant := range []string{"dry", "apply", "json", "format", "deny", "backend", "stdout", "json-writer", "stderr", "report"} {
			apply := variant == "apply"
			released, stopped, markers := 0, 0, 0
			called := false
			admit := func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
				if variant == "deny" {
					return worktrees.AgentIdentity{}, nil, boom
				}
				return worktrees.AgentIdentity{}, func() { released++ }, nil
			}
			var c *cobra.Command
			var args []string
			if name == "rename" {
				args = []string{"old", "new"}
				c = NewRename(runtime, RenameDependencies{Run: func(context.Context, worktrees.RenameOptions) (worktrees.RenameOutcome, error) {
					called = true
					if variant == "backend" {
						return worktrees.RenameOutcome{}, boom
					}
					out := worktrees.RenameOutcome{Diagnostics: []worktrees.ListDiagnostic{{Task: "old"}}, Results: []worktrees.RenameResult{{Eligible: true}}}
					if variant == "report" {
						out.ReportPath = "report"
					}
					return out, nil
				}, Admit: admit, AfterApply: func(*cobra.Command, string, []worktrees.RenameResult) { markers++ }})
			} else {
				args = []string{"task"}
				c = NewRelocate(runtime, RelocateDependencies{Run: func(context.Context, worktrees.RelocateOptions) (worktrees.RelocateOutcome, error) {
					called = true
					if variant == "backend" {
						return worktrees.RelocateOutcome{}, boom
					}
					return worktrees.RelocateOutcome{Diagnostics: []worktrees.ListDiagnostic{{Task: "task"}}, Results: []worktrees.RelocateResult{{Eligible: true}}}, nil
				}, Admit: admit, AfterApply: func(*cobra.Command, []worktrees.RelocateResult) { markers++ }, StartProgress: func(io.Writer, string) func() { return func() { stopped++ } }})
			}
			if apply {
				args = append(args, "--apply")
			}
			if variant == "format" {
				args = append(args, "--format", "bad")
			}
			if variant == "json" || variant == "json-writer" {
				args = append(args, "--format", "json")
			}
			var out bytes.Buffer
			var stdout, stderr io.Writer = &out, io.Discard
			if variant == "stdout" || variant == "json-writer" {
				stdout = movementRejectedWriter{}
			}
			if variant == "stderr" {
				stderr = movementRejectedWriter{}
			}
			if variant == "report" && name == "rename" {
				stdout = &activeLimitedWriter{Allow: 2}
			}
			err := movementExecute(c, stdout, stderr, args...)
			switch {
			case variant == "format":
				if err == nil || called || released != 0 {
					t.Fatal("format order")
				}
			case variant == "deny":
				if err != boom || called || released != 0 {
					t.Fatal("denial order")
				}
			case variant == "backend":
				if err != boom || out.Len() != 0 {
					t.Fatal("backend precedence")
				}
			case variant == "stdout" || variant == "json-writer" || variant == "report" && name == "rename":
				if err == nil {
					t.Fatal("stdout/report failure lost")
				}
			case variant == "stderr" && name == "rename":
				if err == nil {
					t.Fatal("rename stderr failure lost")
				}
			case variant == "apply":
				if err == nil {
					t.Fatal("unfulfilled apply accepted")
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if called && released != 1 {
				t.Fatal("release")
			}
			if name == "relocate" && called && stopped != 1 {
				t.Fatal("reporter stop")
			}
			if markers != 0 && !apply {
				t.Fatal("dry run markers")
			}
		}
	}
	// Exact branch and shortcut validation precedes even admission.
	for _, args := range [][]string{{"old", "new", "--branch=", "--branch-prefix=p"}, {"old", "new", "--branch="}, {"old"}} {
		if err := movementExecute(NewRename(runtime, RenameDependencies{}), io.Discard, io.Discard, args...); err == nil {
			t.Fatal("invalid rename accepted")
		}
	}
	if err := movementExecute(NewRelocate(runtime, RelocateDependencies{}), io.Discard, io.Discard, "task", "--json", "--format", "text"); err == nil || err.Error() != "--json cannot be combined with --format=text" {
		t.Fatalf("shortcut conflict=%v", err)
	}
}
