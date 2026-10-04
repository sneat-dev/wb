package cmdworktree

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
	"github.com/sneat-dev/wb/internal/hostload"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

type mergeContextKey struct{}
type mergeCodedError struct {
	code    int
	message string
}

func (e *mergeCodedError) Error() string { return e.message }

type mergeRefusingWriter struct{ err error }

func (w mergeRefusingWriter) Write([]byte) (int, error) { return 0, w.err }

func mergeRecordingInvocation(flags *shared.Flags) *mergeInvocation {
	inv := mergeTestInvocation(*flags)
	inv.runtime.Flags = func() shared.Flags { return *flags }
	inv.runtime.ExitError = func(code int, message string) error { return &mergeCodedError{code, message} }
	inv.hostLoad = mergeHostLoad{resolve: func() (float64, string) { return 4, "" }, check: func(float64, bool) error { return nil }, reader: func() hostload.Reader { return func() (float64, error) { return 3, nil } }, now: func() time.Time { return time.Unix(9, 0) }}
	inv.bindings.RefusePaths = func(string, []string) error { return nil }
	inv.bindings.RefuseReceipt = func(string, string) error { return nil }
	inv.bindings.ReleaseLane = func(string, orchestrate.WorktreeMergeReceipt) {}
	inv.bindings.LaneRequest = func(root, command, reason string, takeOver bool) orchestrate.LaneGuardRequest {
		return orchestrate.LaneGuardRequest{}
	}
	inv.bindings.CheckoutUpdated = func(io.Writer) func(context.Context, orchestrate.CheckoutUpdate) { return nil }
	return inv
}
func mergeExecute(c *cobra.Command, args ...string) error {
	c.SilenceErrors = true
	c.SilenceUsage = true
	c.SetArgs(args)
	return c.Execute()
}

func TestMergeConstructorsBindTheActualAnnotationPolicy(t *testing.T) {
	t.Parallel()
	var calls []string
	flags := shared.Flags{}
	inv := mergeRecordingInvocation(&flags)
	inv.bindings.Discovery = func(c *cobra.Command, terms string) { calls = append(calls, c.Name()+":discovery:"+terms) }
	inv.bindings.Quiet = func(c *cobra.Command) { calls = append(calls, c.Name()+":quiet") }
	inv.bindings.Landing = func(c *cobra.Command, mode string) *cobra.Command {
		calls = append(calls, c.Name()+":landing:"+mode)
		return c
	}
	cmd := NewMerge(inv.runtime, inv.operations, inv.bindings)
	want := []string{
		"merge:discovery:finish work merge land deliver ship integrate complete cleanup agent worktree branch pull request main",
		"merge:quiet", "merge:landing:worktree", "prepare:quiet", "prepare:landing:worktree", "land:quiet", "land:landing:receipt", "resume:quiet", "resume:landing:receipt", "revert:quiet",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("construction annotations=%v want%v", calls, want)
	}
	if len(cmd.Commands()) != 18 {
		t.Fatalf("child count=%d", len(cmd.Commands()))
	}
	calls = nil
	land := NewLand(inv.runtime, inv.operations, inv.bindings)
	want = []string{"land:discovery:finish work land deliver ship integrate complete cleanup agent worktree branch pull request merge main multi repo repository", "land:quiet", "land:landing:worktree"}
	if !reflect.DeepEqual(calls, want) || land.Flags().Lookup("cleanup").DefValue != "true" {
		t.Fatalf("land annotations=%v flags=%v", calls, land.Flags())
	}
}

func TestMergeCombinedKeepsLazyInputsAdmissionAndOutputCustody(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"peek error", "admission refusal", "linked refusal", "engine error", "writer error", "success", "lazy admission"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "before", Quiet: true}
			inv := mergeRecordingInvocation(&flags)
			sentinel := errors.New(stage)
			ctx := context.WithValue(context.Background(), mergeContextKey{}, stage)
			var order []string
			inv.operations.PeekWorktreeMergeValidationDeferral = func(got context.Context, root string, sources []string, target string, route orchestrate.WorktreeMergeRoute, local, unfenced bool, pr ...string) (bool, error) {
				if got != ctx || root != "after" || !reflect.DeepEqual(sources, []string{"private/source"}) {
					t.Fatalf("peek inputs=%v %q %v", got, root, sources)
				}
				order = append(order, "peek")
				if stage == "peek error" {
					return false, sentinel
				}
				return stage == "lazy admission", nil
			}
			inv.bindings.RefusePaths = func(root string, paths []string) error {
				order = append(order, "linked")
				if root != "after" {
					t.Fatal(root)
				}
				if stage == "linked refusal" {
					return sentinel
				}
				return nil
			}
			inv.hostLoad.check = func(float64, bool) error {
				order = append(order, "admission")
				if stage == "admission refusal" {
					return sentinel
				}
				return nil
			}
			receipt := orchestrate.WorktreeMergeReceipt{Status: orchestrate.WorktreeMergeComplete, Repository: "acme/app", Target: "main"}
			inv.operations.RunWorktreeMerge = func(got context.Context, p orchestrate.WorktreeMergePrepareOptions, l orchestrate.WorktreeMergeLandOptions) (orchestrate.WorktreeMergeReceipt, error) {
				order = append(order, "engine")
				if got != ctx || p.ProjectsRoot != "after" || l.ProjectsRoot != "after" || p.Model != "model" || l.CheckPollInterval != 2*time.Second || !l.Cleanup {
					t.Fatalf("options=%+v %+v", p, l)
				}
				if stage == "lazy admission" {
					if p.HostLoadAdmission != nil || p.RequireHostLoadAdmission == nil {
						t.Fatal("optimistic admission lost")
					}
					if _, err := p.RequireHostLoadAdmission(); err != nil {
						t.Fatal(err)
					}
				}
				if stage == "engine error" {
					return receipt, sentinel
				}
				return receipt, nil
			}
			inv.bindings.ReleaseLane = func(root string, r orchestrate.WorktreeMergeReceipt) {
				order = append(order, "release")
				if root != "after" || r.Repository != "acme/app" {
					t.Fatal(root, r)
				}
			}
			command := newWorktreeMergeCmd(inv)
			flags.ProjectsRoot = "after"
			var out, diagnostic bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&diagnostic)
			command.SetContext(ctx)
			if stage == "writer error" || stage == "engine error" {
				command.SetOut(mergeRefusingWriter{sentinel})
			}
			err := mergeExecute(command, "private/source", "--model", "model", "--cleanup", "--check-interval", "2s")
			if stage == "admission refusal" {
				if !errors.Is(err, sentinel) || err.Error() != "wb worktree merge: "+sentinel.Error() {
					t.Fatalf("wrapped admission refusal=%v", err)
				}
			} else if stage == "linked refusal" || stage == "writer error" || stage == "engine error" {
				if err != sentinel {
					t.Fatalf("error priority=%v want%v", err, sentinel)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if stage == "linked refusal" && len(order) != 1 {
				t.Fatal(order)
			}
			if stage == "admission refusal" && strings.Contains(strings.Join(order, ","), "engine") {
				t.Fatal(order)
			}
			if stage == "success" && (out.Len() == 0 || order[len(order)-1] != "release") {
				t.Fatalf("output/order=%q %v", out.String(), order)
			}
		})
	}
}

func TestMergeRecoveryContractsKeepAdmissionIdentityAndEachTypedOperation(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"operation error", "admission error", "json writer", "text writer", "json success", "text success", "dry-run"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			flags := shared.Flags{ProjectsRoot: "private/root", Quiet: true, NonInteractive: true}
			inv := mergeRecordingInvocation(&flags)
			sentinel := errors.New(mode)
			called := 0
			released := 0
			inv.bindings.Admission = func(_ *cobra.Command, apply bool) (worktrees.AgentIdentity, func(), error) {
				if apply != (mode != "dry-run") {
					t.Fatal("apply not projected")
				}
				if mode == "admission error" {
					return worktrees.AgentIdentity{}, func() {}, sentinel
				}
				return worktrees.AgentIdentity{Model: "registered model", Runtime: "registered runtime", AgentID: "registered agent", Registered: true}, func() { released++ }, nil
			}
			inv.bindings.Initiator = func(*cobra.Command) string { return "real initiator" }
			ops := reflect.ValueOf(&inv.operations).Elem()
			for i := 0; i < ops.NumField(); i++ {
				field := ops.Field(i)
				typ := field.Type()
				name := ops.Type().Field(i).Name
				if strings.HasPrefix(name, "Peek") || name == "RunWorktreeMerge" || name == "LandWorktreeMerge" || name == "PrepareWorktreeMerge" || name == "PrepareWorktreeMergeRevert" || name == "ResumeWorktreeMerge" {
					continue
				}
				field.Set(reflect.MakeFunc(typ, func(args []reflect.Value) []reflect.Value {
					called++
					options := args[1]
					if options.FieldByName("ProjectsRoot").String() != "private/root" || options.FieldByName("Apply").Bool() != (mode != "dry-run") {
						t.Fatalf("%s inputs=%v", name, options.Interface())
					}
					for _, key := range []string{"Model", "AgentRuntime", "AgentID"} {
						v := options.FieldByName(key)
						if v.IsValid() && v.Kind() == reflect.String && v.String() == "" {
							t.Fatalf("missing admission identity %s", key)
						}
					}
					v := options.FieldByName("Initiator")
					if v.IsValid() && v.String() != "real initiator" {
						t.Fatal(v.String())
					}
					result := reflect.New(typ.Out(0)).Elem()
					status := result.FieldByName("Status")
					if status.IsValid() && status.Kind() == reflect.String {
						status.SetString("done")
					}
					for _, key := range []string{"CandidateLandingTreeSHA", "PullRequestHeadSHA"} {
						v := result.FieldByName(key)
						if v.IsValid() {
							v.SetString("actual typed evidence")
						}
					}
					if v := result.FieldByName("ExcusedDerivedPaths"); v.IsValid() {
						v.Set(reflect.ValueOf([]string{"generated/file"}))
					}
					e := reflect.Zero(typ.Out(1))
					if mode == "operation error" {
						e = reflect.ValueOf(sentinel)
					}
					return []reflect.Value{result, e}
				}))
			}
			for _, child := range newWorktreeMergeCmd(inv).Commands() {
				name := child.Name()
				if name == "prepare" || name == "land" || name == "resume" || name == "revert" {
					continue
				}
				var out bytes.Buffer
				child.SetOut(&out)
				child.SetErr(io.Discard)
				if strings.Contains(mode, "writer") {
					child.SetOut(mergeRefusingWriter{sentinel})
				}
				child.Parent().RemoveCommand(child)
				args := []string{"receipt"}
				if strings.Contains(name, "supersede") || strings.Contains(name, "self-supersession") || strings.Contains(name, "forward-repair") || strings.Contains(name, "conflict-replacement") || name == "adopt-published-candidate" {
					args = append(args, "replacement")
				}
				args = append(args, "--actor", "actor", "--reason", "reason")
				if mode != "dry-run" {
					args = append(args, "--apply")
				}
				if strings.HasPrefix(mode, "json") {
					args = append(args, "--format", "json")
				}
				// Pinning flag validation belongs to orchestrate; callbacks verify the exact native typed DTO.
				err := mergeExecute(child, args...)
				if strings.Contains(mode, "error") || strings.Contains(mode, "writer") {
					if err != sentinel {
						t.Fatalf("%s/%s error=%v want%v", name, mode, err, sentinel)
					}
				} else if err != nil || out.Len() == 0 {
					t.Fatalf("%s/%s output=%q err=%v", name, mode, out.String(), err)
				}
			}
			if mode == "admission error" {
				if called != 0 || released != 0 {
					t.Fatalf("premature backend=%d release=%d", called, released)
				}
			} else if called != 14 || released != 14 {
				t.Fatalf("operations=%d release=%d", called, released)
			}
		})
	}
}

func TestMergeConflictReplacementPreservesQuietProgressException(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{Quiet: true, NonInteractive: true}
	inv := mergeRecordingInvocation(&flags)
	inv.bindings.Admission = func(*cobra.Command, bool) (worktrees.AgentIdentity, func(), error) {
		return worktrees.AgentIdentity{}, func() {}, nil
	}
	inv.operations.PrepareConflictWorktreeMergeReplacement = func(_ context.Context, o orchestrate.WorktreeMergeConflictCandidateRefreshOptions) (orchestrate.WorktreeMergeConflictCandidateRefresh, error) {
		if o.Progress == nil {
			t.Fatal("quiet must not erase the current explicit conflict-replacement progress policy")
		}
		return orchestrate.WorktreeMergeConflictCandidateRefresh{Status: "prepared"}, nil
	}
	cmd := newWorktreeMergePrepareConflictReplacementCmd(inv)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := mergeExecute(cmd, "receipt", "source", "--progress"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "conflict replacement") || !strings.Contains(stdout.String(), "prepared") {
		t.Fatalf("progress=%q stdout=%q", stderr.String(), stdout.String())
	}
}
