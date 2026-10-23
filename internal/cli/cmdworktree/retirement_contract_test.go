package cmdworktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func retirementRuntime(root *string) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: *root, Filter: "only", Quiet: true} }, ExitError: func(code int, message string) error { return fmt.Errorf("coded %d: %s", code, message) }}
}
func retirementExecute(command *cobra.Command, args []string, out, errOut io.Writer) error {
	command.SetArgs(args)
	command.SetOut(out)
	command.SetErr(errOut)
	command.SilenceUsage = true
	command.SilenceErrors = true
	return command.Execute()
}
func retirementProgress() InventoryProgress {
	return InventoryProgress{Report: func(worktrees.ListProgress) {}, Finish: func() {}}
}
func retirementCleanupDeps(out worktrees.CleanupOutcome) CleanupDependencies {
	return CleanupDependencies{Run: func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) { return out, nil }, Progress: func(*cobra.Command, bool) InventoryProgress { return retirementProgress() }, Release: func(*cobra.Command, string, string) bool { return false }}
}

func TestRetirementCleanupSelectorAndEarlyErrors(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"--recover-stages"}, {"x", "--recover-stages", "--all-merged"}, {"x", "--recover-stages", "--retire-shells"}, {"x", "--retire-shells"}, {"--retire-shells", "--all-merged"}, {"x", "--all-merged"}, {"x", "--format=invalid"}, {"--all-merged", "--resume-interrupted"}, {"x", "y", "--resume-interrupted"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			command := NewCleanup(shared.Runtime{Flags: func() shared.Flags { t.Fatal("flags read for invalid selectors/format"); return shared.Flags{} }}, CleanupDependencies{}) // resume-count validation reads flags after format.
			if strings.Contains(strings.Join(args, " "), "resume-interrupted") {
				command = NewCleanup(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{} }}, CleanupDependencies{})
			}
			if err := retirementExecute(command, args, io.Discard, io.Discard); err == nil {
				t.Fatal("invalid selector accepted")
			}
		})
	}
}
func TestRetirementCleanupOptionsAndCompletion(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"logical", "--apply", "--remote", "--resume-interrupted", "--base=chosen", "--report-dir=report", "--absorbed-by=42", "--superseded-by=receipt", "--parallel=3", "--verbose"}, {"physical", "--older-than=2h", "--workers=2", "--format=json"}, {"--all-merged"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			root := "before"
			deps := retirementCleanupDeps(worktrees.CleanupOutcome{ResolvedTasks: []string{"physical"}, Results: []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Task: "physical"}, Applied: true}}})
			calls, finishes, releases := 0, 0, 0
			deps.Run = func(ctx context.Context, o worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
				calls++
				first, second := o.Now(), o.Now()
				if o.ProjectsRoot != "parsed" || o.Filter != "only" || first.IsZero() || first != second {
					t.Fatalf("options %+v", o)
				}
				o.Progress(worktrees.ListProgress{})
				if args[0] == "logical" && (!o.Apply || !o.DeleteRemote || !o.ResumeInterrupted || !o.ExplicitBase || o.Base != "chosen" || o.ReportDir != "report" || o.AbsorbedBy != "42" || o.SupersededBy != "receipt" || o.Workers != 3 || o.OlderThan != 0 || !o.RequireRemoteRetirement) {
					t.Fatalf("explicit options %+v", o)
				}
				if args[0] == "physical" && (o.OlderThan != 2*time.Hour || o.Workers != 2 || o.RequireRemoteRetirement) {
					t.Fatalf("age options %+v", o)
				}
				if args[0] == "--all-merged" && (!o.AllMerged || o.OlderThan != 24*time.Hour || len(o.Tasks) != 0) {
					t.Fatalf("sweep options %+v", o)
				}
				return worktrees.CleanupOutcome{ResolvedTasks: []string{"physical"}, Results: []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Task: "physical"}, Applied: true}}}, nil
			}
			deps.Progress = func(_ *cobra.Command, verbose bool) InventoryProgress {
				return InventoryProgress{Report: func(worktrees.ListProgress) {}, Finish: func() { finishes++ }}
			}
			deps.Release = func(_ *cobra.Command, r, task string) bool {
				releases++
				if r != "parsed" || task != "logical" {
					t.Fatal("logical release selector lost")
				}
				return false
			}
			command := NewCleanup(retirementRuntime(&root), deps)
			root = "parsed"
			if err := retirementExecute(command, args, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || finishes != 1 || releases != map[bool]int{true: 1, false: 0}[args[0] == "logical"] {
				t.Fatalf("calls %d finish %d releases %d", calls, finishes, releases)
			}
		})
	}
}
func TestRetirementCleanupFailuresDiagnosticsAndReleaseOrder(t *testing.T) {
	t.Parallel()
	root := "private"
	want := errors.New("domain refused")
	deps := retirementCleanupDeps(worktrees.CleanupOutcome{})
	deps.Run = func(context.Context, worktrees.CleanupOptions) (worktrees.CleanupOutcome, error) {
		return worktrees.CleanupOutcome{}, want
	}
	if err := retirementExecute(NewCleanup(retirementRuntime(&root), deps), []string{"task"}, io.Discard, io.Discard); err != want {
		t.Fatalf("domain error %v", err)
	}
	outcomes := []worktrees.CleanupOutcome{
		{Diagnostics: []worktrees.ListDiagnostic{{Task: "task", Path: "path", Message: "bad"}}},
		{Quarantined: []worktrees.LifecycleBacklogQuarantine{{Task: "task", Path: "path", Reason: "bad"}}},
		{Artifacts: []worktrees.LifecycleArtifact{{Task: "task", Path: "changed", Applied: true}, {Task: "task", Path: "quiet", Applied: false}}, ReportPath: "report"},
		{Results: []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Task: "task"}, Applied: true}}, ReportPath: "report"},
	}
	for i, outcome := range outcomes {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			for _, format := range []string{"text", "json"} {
				for _, stream := range []string{"out", "err"} {
					for allow := 0; allow < 5; allow++ {
						deps := retirementCleanupDeps(outcome)
						command := NewCleanup(retirementRuntime(&root), deps)
						out, errOut := io.Writer(io.Discard), io.Writer(io.Discard)
						writer := &cwWtFailWriter{Allow: allow}
						if stream == "out" {
							out = writer
						} else {
							errOut = writer
						}
						err := retirementExecute(command, []string{"task", "--format=" + format}, out, errOut)
						if err != nil && !errors.Is(err, errCwWtWrite) {
							t.Fatalf("unexpected writer result %v", err)
						}
					}
				}
			}
		})
	}
	for _, kind := range []string{"none", "named", "leak", "multi", "partial"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			outcome := worktrees.CleanupOutcome{}
			args := []string{"task", "--apply"}
			switch kind {
			case "named", "leak":
				outcome.Results = []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Task: "task"}, Applied: true}}
			case "multi", "partial":
				args = []string{"task", "other", "--apply"}
				outcome.Results = []worktrees.CleanupResult{{ListResult: worktrees.ListResult{Task: "task"}, Applied: true}}
			}
			deps := retirementCleanupDeps(outcome)
			var out bytes.Buffer
			released := 0
			deps.Release = func(*cobra.Command, string, string) bool {
				released++
				if out.Len() == 0 {
					t.Fatal("release preceded report")
				}
				return kind == "leak"
			}
			err := retirementExecute(NewCleanup(retirementRuntime(&root), deps), args, &out, io.Discard)
			if (kind == "none" || kind == "leak") != (err != nil) {
				t.Fatalf("kind %s error %v", kind, err)
			}
			if kind == "none" && released != 0 {
				t.Fatal("unsafe release")
			}
			if kind != "none" && released != 1 {
				t.Fatalf("release count %d", released)
			}
		})
	}
}
func TestRetirementShellAndStageBranches(t *testing.T) {
	t.Parallel()
	root := "private"
	want := errors.New("native stage refused")
	for _, mode := range []string{"shell", "stage"} {
		for _, format := range []string{"text", "json"} {
			for _, fail := range []bool{false, true} {
				t.Run(fmt.Sprint(mode, format, fail), func(t *testing.T) {
					t.Parallel()
					deps := retirementCleanupDeps(worktrees.CleanupOutcome{})
					args := []string{"--retire-shells", "--apply", "--format=" + format}
					deps.Shells = func(_ context.Context, o worktrees.RetireShellsOptions) (worktrees.RetireShellsOutcome, error) {
						if o.ProjectsRoot != root || o.Filter != "only" || !o.Apply {
							t.Fatalf("shell options %+v", o)
						}
						if fail {
							return worktrees.RetireShellsOutcome{}, want
						}
						return worktrees.RetireShellsOutcome{}, nil
					}
					deps.Recover = func(_ context.Context, o worktrees.RetiredStageRecoveryOptions) (worktrees.RetiredStageRecoveryOutcome, error) {
						if o.ProjectsRoot != root || !o.Apply {
							t.Fatalf("stage options %+v", o)
						}
						if fail {
							return worktrees.RetiredStageRecoveryOutcome{}, want
						}
						return worktrees.RetiredStageRecoveryOutcome{ReceiptPath: "private receipt", Results: []worktrees.RetiredStageRecoveryResult{{Applied: true}, {Eligible: true}, {}}}, nil
					}
					if mode == "stage" {
						args = []string{"one", "two", "--recover-stages", "--apply", "--format=" + format}
					}
					for allow := 0; allow < 10; allow++ {
						err := retirementExecute(NewCleanup(retirementRuntime(&root), deps), args, &cwWtFailWriter{Allow: allow}, io.Discard)
						if fail {
							if err != want {
								t.Fatalf("effect error %v", err)
							}
						} else if err != nil && !errors.Is(err, errCwWtWrite) {
							t.Fatal(err)
						}
					}
				})
			}
		}
	}
}
func TestRetirementAbortOptionsStatesAndReleasePrecedence(t *testing.T) {
	t.Parallel()
	root := "before"
	want := errors.New("abort refused")
	calls := 0
	deps := AbortDependencies{Run: func(_ context.Context, o worktrees.AbortOptions) ([]worktrees.AbortResult, error) {
		calls++
		if o.ProjectsRoot != "parsed" || o.Task != "task" || o.Filter != "only" || o.Base != "chosen" || o.Disposition != worktrees.AbortHandoff || o.Successor != "next" || !o.All || o.AbsorbedBy != "42" || o.ClosedPullRequest != "closed" || o.ClaimID != "claim" || o.Actor != "actor" || o.Reason != "why" || o.SuccessorIdentity != (worktrees.ClaimExecutionIdentity{Model: "model", CLI: "client", Provider: "route"}) || !o.Apply || !o.DeleteRemote {
			t.Fatalf("options %+v", o)
		}
		return nil, want
	}}
	command := NewAbort(retirementRuntime(&root), deps)
	root = "parsed"
	if err := retirementExecute(command, []string{"task", "--base=chosen", "--disposition=handoff", "--successor=next", "--all", "--absorbed-by=42", "--closed-pr=closed", "--claim=claim", "--actor=actor", "--reason=why", "--model=model", "--cli=client", "--provider=route", "--apply", "--remote"}, io.Discard, io.Discard); err != want || calls != 1 {
		t.Fatalf("error %v calls %d", err, calls)
	}
	if err := retirementExecute(NewAbort(shared.Runtime{}, AbortDependencies{}), []string{"task", "--format=invalid"}, io.Discard, io.Discard); err == nil {
		t.Fatal("format accepted")
	}
	rows := []worktrees.AbortResult{{Excluded: true}, {Excluded: true, Reason: "selected out"}, {Eligible: false}, {Eligible: true}, {Eligible: true, Applied: true, Disposition: worktrees.AbortOrphaned}, {Eligible: true, Applied: true, WorktreeGone: true}, {Eligible: true, Applied: true, ClosedPullRequest: &worktrees.ClosedPullRequestEvidence{Repository: "repo", Number: 42, AuditPath: "audit"}}}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			for _, disposition := range []string{"discarded", "handoff", "not_landed"} {
				for _, excluded := range []bool{false, true} {
					for allow := 0; allow < 14; allow++ {
						values := rows
						if !excluded {
							values = rows[2:]
						}
						release, skip := 0, 0
						deps := AbortDependencies{Run: func(context.Context, worktrees.AbortOptions) ([]worktrees.AbortResult, error) { return values, nil }, Release: func(*cobra.Command, string, string) bool { release++; return true }, SkipRelease: func(*cobra.Command, string) { skip++ }}
						err := retirementExecute(NewAbort(retirementRuntime(&root), deps), []string{"task", "--disposition=" + disposition, "--apply", "--format=" + format}, &cwWtFailWriter{Allow: allow}, io.Discard)
						if err != nil && !errors.Is(err, errCwWtWrite) && !strings.Contains(err.Error(), "claim release failed") {
							t.Fatal(err)
						}
						if disposition != "not_landed" && ((excluded && skip != 1) || (!excluded && release != 1)) {
							t.Fatalf("release=%d skip=%d", release, skip)
						}
					}
				}
			}
		})
	}
	deps = AbortDependencies{Run: func(context.Context, worktrees.AbortOptions) ([]worktrees.AbortResult, error) {
		return []worktrees.AbortResult{{Quarantined: []worktrees.LifecycleBacklogQuarantine{{Path: "bad"}}}}, nil
	}}
	if err := retirementExecute(NewAbort(retirementRuntime(&root), deps), []string{"task"}, io.Discard, &cwWtFailWriter{}); err != errCwWtWrite {
		t.Fatalf("warning failure %v", err)
	}
	deps.Run = func(context.Context, worktrees.AbortOptions) ([]worktrees.AbortResult, error) { return nil, nil }
	if err := retirementExecute(NewAbort(retirementRuntime(&root), deps), []string{"task"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
}
func TestRetirementGCOptionsFinishAndFindings(t *testing.T) {
	t.Parallel()
	root := "before"
	want := errors.New("gc refused")
	for _, kind := range []string{"error", "empty", "refused", "writer", "json", "negative", "format"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			r := root
			finishes := 0
			deps := GCDependencies{Progress: func(*cobra.Command, bool) InventoryProgress {
				return InventoryProgress{Report: func(worktrees.ListProgress) {}, Finish: func() { finishes++ }}
			}, Run: func(_ context.Context, o worktrees.GCOptions) (worktrees.GCOutcome, error) {
				if o.ProjectsRoot != "parsed" || o.Filter != "only" || !reflect.DeepEqual(o.Tasks, []string{"task"}) || !o.Apply || !o.AllowResidue || o.SupersededBy != "receipt" || !o.SkipDetached || !o.SkipSizes || !o.DeleteRemote || o.Base != "chosen" || o.OlderThan != time.Hour || o.TTL != 2*time.Hour || o.SessionFreshness != worktrees.DisableSessionFreshness || o.ResidueDepth != 4 || o.Workers != 2 {
					t.Fatalf("options %+v", o)
				}
				o.Progress(worktrees.ListProgress{})
				if kind == "error" {
					return worktrees.GCOutcome{}, want
				}
				return worktrees.GCOutcome{Totals: map[string]int{"refused": map[bool]int{true: 1, false: 0}[kind == "refused"]}}, nil
			}}
			command := NewGC(retirementRuntime(&r), deps)
			r = "parsed"
			args := []string{"task", "--apply", "--allow-residue", "--superseded-by=receipt", "--skip-detached", "--skip-sizes", "--remote", "--base=chosen", "--older-than=1h", "--ttl=2h", "--session-freshness=0", "--residue-depth=4", "--parallel=2", "--verbose"}
			out := io.Writer(io.Discard)
			if kind == "json" {
				args = append(args, "--format=json")
				out = &cwWtFailWriter{Allow: 1}
			}
			if kind == "writer" {
				out = &cwWtFailWriter{}
			}
			if kind == "negative" {
				args = append(args, "--session-freshness=-1s")
			}
			if kind == "format" {
				args = append(args, "--format=invalid")
			}
			err := retirementExecute(command, args, out, io.Discard)
			if kind == "error" && err != want {
				t.Fatal(err)
			}
			if (kind == "empty" || kind == "json") != (err == nil) {
				t.Fatalf("kind %s error %v", kind, err)
			}
			expected := 2
			if kind == "error" {
				expected = 1
			}
			if kind == "negative" || kind == "format" {
				expected = 0
			}
			if finishes != expected {
				t.Fatalf("finishes %d want %d", finishes, expected)
			}
		})
	}
	// JSON writer errors win over findings.
	deps := GCDependencies{Progress: func(*cobra.Command, bool) InventoryProgress { return retirementProgress() }, Run: func(context.Context, worktrees.GCOptions) (worktrees.GCOutcome, error) {
		return worktrees.GCOutcome{Totals: map[string]int{"refused": 1}}, nil
	}}
	if err := retirementExecute(NewGC(retirementRuntime(&root), deps), []string{"--format=json"}, &cwWtFailWriter{}, io.Discard); err != errCwWtWrite {
		t.Fatal(err)
	}
}
func TestRetirementRetireAdmissionOwnershipReleaseAndOutput(t *testing.T) {
	t.Parallel()
	root := "private"
	want := errors.New("retire refused")
	for _, kind := range []string{"format", "admit", "domain", "plan", "complete", "leak", "json", "writer", "jsonwriter"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			var order []string
			deps := RetireDependencies{Admit: func(_ *cobra.Command, apply bool) (worktrees.AgentIdentity, func(), error) {
				order = append(order, "admit")
				if !apply {
					t.Fatal("apply lost")
				}
				if kind == "admit" {
					return worktrees.AgentIdentity{}, nil, want
				}
				return worktrees.AgentIdentity{}, func() { order = append(order, "finish") }, nil
			}, CheckOwnership: func(_ context.Context, r, task string) error {
				order = append(order, "ownership")
				if r != root || task != "task" {
					t.Fatal("ownership binding")
				}
				return want
			}, ReleaseWhenComplete: func(*cobra.Command, string, string) bool { order = append(order, "release"); return kind == "leak" }, Run: func(ctx context.Context, o worktrees.RetireOptions) (worktrees.RetireResult, error) {
				order = append(order, "run")
				if o.ProjectsRoot != root || o.Task != "task" || o.Repository != "only" || o.Message != "message" || o.Preserve != "tag" || !o.Apply {
					t.Fatalf("options %+v", o)
				}
				if err := o.RemoteOwnership(ctx, "task"); err != want {
					t.Fatal(err)
				}
				if kind == "domain" {
					return worktrees.RetireResult{}, want
				}
				phase := "complete"
				if kind == "plan" {
					phase = "plan"
				}
				return worktrees.RetireResult{Task: "task", Phase: phase}, nil
			}}
			args := []string{"task", "--apply", "--message=message", "--preserve=tag"}
			out := io.Writer(io.Discard)
			if kind == "json" || kind == "jsonwriter" {
				args = append(args, "--json", "--format=invalid")
			}
			if kind == "format" {
				args = append(args, "--format=invalid")
			}
			if kind == "writer" || kind == "jsonwriter" {
				out = &cwWtFailWriter{}
			}
			err := retirementExecute(NewRetire(retirementRuntime(&root), deps), args, out, io.Discard)
			success := kind == "plan" || kind == "complete" || kind == "json"
			if success != (err == nil) {
				t.Fatalf("kind %s error %v", kind, err)
			}
			if (kind == "admit" || kind == "domain") && err != want {
				t.Fatalf("original effect error %v", err)
			}
			if kind == "format" {
				if len(order) != 0 {
					t.Fatal("admission before format")
				}
				return
			}
			if kind == "admit" {
				if !reflect.DeepEqual(order, []string{"admit"}) {
					t.Fatal(order)
				}
				return
			}
			if order[len(order)-1] != "finish" {
				t.Fatal("admission release lost")
			}
			if kind != "domain" && kind != "plan" && !reflect.DeepEqual(order, []string{"admit", "run", "ownership", "release", "finish"}) {
				t.Fatal(order)
			}
		})
	}
}
