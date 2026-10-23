package cmddeps

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/policy"
	"github.com/spf13/cobra"
)

func policyChild(t *testing.T, name string, runtime shared.Runtime, ops PolicyDependencies) *cobra.Command {
	t.Helper()
	family := NewPolicy(runtime, ops)
	child, _, err := family.Find([]string{name})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(child)
	child.SilenceUsage = true
	child.SilenceErrors = true
	return child
}
func policyRecordingOperations(err error) PolicyDependencies {
	return PolicyDependencies{
		Check: func(context.Context, depsrun.PolicyRequest) (policy.Result, error) { return policy.Result{}, err },
		Explain: func(context.Context, depsrun.PolicyRequest, string) (policy.Explanation, error) {
			return policy.Explanation{}, err
		},
		Describe: func(context.Context, depsrun.PolicyRequest) (policy.Effective, error) { return policy.Effective{}, err },
		Validate: func(context.Context, string) (depsrun.PolicyValidation, error) {
			return depsrun.PolicyValidation{}, err
		},
		Expectations: func(context.Context, string) ([]policy.ExpectationResult, error) { return nil, err },
		Init: func(context.Context, depsrun.PolicyRequest, func(depsrun.PolicyInitNotice)) (policy.Result, error) {
			return policy.Result{}, err
		},
		Report: func(context.Context, depsrun.PolicyFleetRequest) ([]depsrun.PolicyModuleOutcome, error) {
			return nil, err
		},
		Drift: func(context.Context, depsrun.PolicyFleetRequest) (depsrun.PolicyDriftResult, error) {
			return depsrun.PolicyDriftResult{}, err
		},
		Impact: func(context.Context, depsrun.PolicyFleetRequest, string) (depsrun.PolicyImpactResult, error) {
			return depsrun.PolicyImpactResult{}, err
		},
	}
}
func TestPolicyCommandsPreserveOperationRefusalsAndArgumentCustody(t *testing.T) {
	t.Parallel()
	boom := errors.New("operation refused")
	for name, args := range map[string][]string{"check": {}, "explain": {"import"}, "show": {}, "validate": {"policy"}, "test": {"policy"}, "init": {"--policy", "policy"}, "report": {}, "drift": {}, "impact": {"policy"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cmd := policyChild(t, name, testRuntime(), policyRecordingOperations(boom))
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(io.Discard)
			if e := executeArgs(cmd, args...); e != boom || out.Len() != 0 {
				t.Fatalf("%s: %v %q", name, e, out.String())
			}
		})
	}
	c := policyChild(t, "check", testRuntime(), PolicyDependencies{Check: func(context.Context, depsrun.PolicyRequest) (policy.Result, error) {
		t.Fatal("operation after invalid format")
		return policy.Result{}, nil
	}})
	if e := executeArgs(c, "--format", "unknown"); e == nil {
		t.Fatal("invalid format accepted")
	}
}
func TestPolicyCurrentFlagsContextAndWritersAreReadAtExecution(t *testing.T) {
	t.Parallel()
	flags := shared.Flags{ProjectsRoot: "first", Filter: "repo", ExtraOrgs: []string{"org"}}
	runtime := directiveRecordingRuntime(func() shared.Flags { return flags })
	ctx := context.WithValue(context.Background(), directiveKey{}, "policy")
	calls := 0
	ops := policyRecordingOperations(nil)
	ops.Report = func(got context.Context, r depsrun.PolicyFleetRequest) ([]depsrun.PolicyModuleOutcome, error) {
		calls++
		expected := depsrun.Selection{ProjectsRoot: flags.ProjectsRoot, Filter: flags.Filter, ExtraOrgs: flags.ExtraOrgs, Fleet: true, Parallel: 1, Match: "acme/*", Regex: "repo"}
		if got != ctx || !reflect.DeepEqual(r.Selection, expected) || r.Policy != "override" {
			t.Fatalf("request=%+v ctx=%v", r, got)
		}
		return nil, nil
	}
	cmd := policyChild(t, "report", runtime, ops)
	cmd.SetContext(ctx)
	var first, second bytes.Buffer
	cmd.SetOut(&first)
	if e := executeArgs(cmd, "--match", "acme/*", "--regex", "repo", "--policy", "override"); e != nil {
		t.Fatal(e)
	}
	flags.ProjectsRoot = "second"
	cmd.SetOut(&second)
	if e := cmd.Execute(); e != nil {
		t.Fatal(e)
	}
	if calls != 2 || first.String() != second.String() || !strings.Contains(second.String(), "0 module(s)") {
		t.Fatalf("calls=%d first=%q second=%q", calls, first.String(), second.String())
	}
	ops.Check = func(got context.Context, r depsrun.PolicyRequest) (policy.Result, error) {
		if got != ctx || r.Directory != "." || r.ProjectsRoot != "second" || r.Policy != "override" || r.DeclaredType != "named" || !r.Strict {
			t.Fatalf("%+v", r)
		}
		return policy.Result{}, nil
	}
	cmd = policyChild(t, "check", runtime, ops)
	cmd.SetContext(ctx)
	cmd.SetOut(io.Discard)
	if e := executeArgs(cmd, "", "--policy", "override", "--type", "named", "--strict"); e != nil {
		t.Fatal(e)
	}
}
func TestPolicyWritersPrecedeFindingsAndTextFallbackIsBestEffort(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"check", "init", "report", "drift", "impact"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ops := policyRecordingOperations(nil)
			ops.Check = func(context.Context, depsrun.PolicyRequest) (policy.Result, error) {
				return policy.Result{Findings: []policy.Finding{{Mode: policy.ModeEnforce}}}, nil
			}
			ops.Init = func(_ context.Context, _ depsrun.PolicyRequest, emit func(depsrun.PolicyInitNotice)) (policy.Result, error) {
				emit(depsrun.PolicyInitNotice{Path: "private", DetectedType: "service"})
				return policy.Result{Findings: []policy.Finding{{Mode: policy.ModeEnforce}}}, nil
			}
			ops.Report = func(context.Context, depsrun.PolicyFleetRequest) ([]depsrun.PolicyModuleOutcome, error) {
				return []depsrun.PolicyModuleOutcome{{Blocking: 1}}, nil
			}
			ops.Drift = func(context.Context, depsrun.PolicyFleetRequest) (depsrun.PolicyDriftResult, error) {
				return depsrun.PolicyDriftResult{Issues: 1}, nil
			}
			ops.Impact = func(context.Context, depsrun.PolicyFleetRequest, string) (depsrun.PolicyImpactResult, error) {
				return depsrun.PolicyImpactResult{NewlyFailing: []depsrun.PolicyChange{{Repository: "private"}}}, nil
			}
			cmd := policyChild(t, name, testRuntime(), ops)
			cmd.SetOut(cwDepsFailingWriter{})
			args := []string{}
			if name == "init" {
				args = []string{"--policy", "policy"}
			}
			if name == "impact" {
				args = []string{"policy"}
			}
			if name != "init" {
				args = append(args, "--format", "json")
			}
			e := executeArgs(cmd, args...)
			if e == nil || !strings.Contains(e.Error(), "write refused") {
				t.Fatalf("writer custody %s: %v", name, e)
			}
			if name == "report" || name == "drift" || name == "impact" {
				cmd.SetOut(io.Discard)
				args = append(args[:len(args)-2], "--format", "other")
				e = executeArgs(cmd, args...)
				if e == nil {
					t.Fatal("unknown format lost findings")
				}
			}
		})
	}
}
func TestPolicyOperationsUseTheActualServiceMethods(t *testing.T) {
	t.Parallel()
	boom := errors.New("bound service")
	dependencies := depsrun.DefaultPolicyDependencies(io.Discard)
	dependencies.Abs = func(string) (string, error) { return "", boom }
	dependencies.Load = func(string) (policy.Policy, error) { return policy.Policy{}, boom }
	dependencies.Select = func(context.Context, depsrun.Selection) ([]deps.Repository, error) { return nil, boom }
	service := depsrun.NewPolicy(dependencies, func(message string) error { return errors.New(message) })
	ops := PolicyOperations(service)
	ctx := context.Background()
	req := depsrun.PolicyRequest{Policy: "present"}
	checks := []func() error{func() error { _, e := ops.Check(ctx, req); return e }, func() error { _, e := ops.Explain(ctx, req, "import"); return e }, func() error { _, e := ops.Describe(ctx, req); return e }, func() error { _, e := ops.Validate(ctx, "policy"); return e }, func() error { _, e := ops.Expectations(ctx, "policy"); return e }, func() error {
		_, e := ops.Init(ctx, req, func(depsrun.PolicyInitNotice) { t.Fatal("unexpected write") })
		return e
	}, func() error { _, e := ops.Report(ctx, depsrun.PolicyFleetRequest{}); return e }, func() error { _, e := ops.Drift(ctx, depsrun.PolicyFleetRequest{}); return e }, func() error { _, e := ops.Impact(ctx, depsrun.PolicyFleetRequest{}, "policy"); return e }}
	for _, check := range checks {
		if e := check(); e == nil || e.Error() != boom.Error() {
			t.Fatal(e)
		}
	}
}

func TestPolicySuccessfulPresentationUsesStdlibAndPassingRows(t *testing.T) {
	t.Parallel()
	ops := policyRecordingOperations(nil)
	ops.Explain = func(context.Context, depsrun.PolicyRequest, string) (policy.Explanation, error) {
		return policy.Explanation{Import: "fmt", RepoType: "service", Classification: policy.Classification{Group: policy.GroupStdlib}, Scopes: []policy.ScopeVerdict{{Scope: policy.ScopeSource, Allowed: true}}}, nil
	}
	ops.Init = func(_ context.Context, _ depsrun.PolicyRequest, emit func(depsrun.PolicyInitNotice)) (policy.Result, error) {
		emit(depsrun.PolicyInitNotice{Path: "private/config", DetectedType: "service"})
		return policy.Result{}, nil
	}
	ops.Impact = func(context.Context, depsrun.PolicyFleetRequest, string) (depsrun.PolicyImpactResult, error) {
		return depsrun.PolicyImpactResult{NewlyPassing: []depsrun.PolicyChange{{Repository: "private/passing", Before: 1, After: 0}}, Unchanged: 1}, nil
	}
	for name, args := range map[string][]string{"explain": {"fmt"}, "init": {"--policy", "private"}, "drift": {}, "impact": {"candidate"}} {
		cmd := policyChild(t, name, testRuntime(), ops)
		var out bytes.Buffer
		cmd.SetOut(&out)
		if e := executeArgs(cmd, args...); e != nil {
			t.Fatal(e)
		}
		if name == "explain" && !strings.Contains(out.String(), "the standard library is always permitted") {
			t.Fatal(out.String())
		}
		if name == "impact" && !strings.Contains(out.String(), "private/passing") {
			t.Fatal(out.String())
		}
	}
}
