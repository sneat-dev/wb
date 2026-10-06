package depsrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/policy"
)

func TestPolicyServicePreservesResolutionAndEngineErrors(t *testing.T) {
	t.Parallel()
	boom := errors.New("selected policy stage")
	for _, stage := range []string{"abs", "config", "load", "scan", "check", "explain", "describe"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			service := newPolicyTestService()
			request := PolicyRequest{Directory: violatingModule(t), Policy: writeTestPolicy(t), DeclaredType: "extension-implementation"}
			switch stage {
			case "abs":
				service.deps.Abs = func(string) (string, error) { return "", boom }
			case "config":
				service.deps.LoadConfig = func(string) (policy.RepoConfig, error) { return policy.RepoConfig{}, boom }
			case "load":
				service.deps.Load = func(string) (policy.Policy, error) { return policy.Policy{}, boom }
			case "scan":
				service.deps.Scan = func(string) (policy.Module, error) { return policy.Module{}, boom }
			case "check":
				service.deps.Check = func(policy.Policy, policy.Module, string) (policy.Result, error) { return policy.Result{}, boom }
			case "explain":
				service.deps.Explain = func(policy.Policy, string, string, string) (policy.Explanation, error) {
					return policy.Explanation{}, boom
				}
			case "describe":
				service.deps.Describe = func(policy.Policy, string, string, string, bool) (policy.Effective, error) {
					return policy.Effective{}, boom
				}
			}
			var err error
			switch stage {
			case "explain":
				_, err = service.Explain(context.Background(), request, "example.com/import")
			case "describe":
				_, err = service.Describe(context.Background(), request)
			default:
				_, err = service.Check(context.Background(), request)
			}
			if exitCodeOfSafe(err) != 2 || err.Error() != boom.Error() {
				t.Fatalf("%s: %v", stage, err)
			}
		})
	}
	for _, call := range []func(*PolicyService, PolicyRequest) error{
		func(s *PolicyService, r PolicyRequest) error {
			_, e := s.Explain(context.Background(), r, "")
			return e
		}, func(s *PolicyService, r PolicyRequest) error { _, e := s.Describe(context.Background(), r); return e }, func(s *PolicyService, r PolicyRequest) error {
			_, e := s.Init(context.Background(), r, func(PolicyInitNotice) { t.Fatal("notice after failed resolution") })
			return e
		}} {
		s := newPolicyTestService()
		s.deps.Abs = func(string) (string, error) { return "", boom }
		if e := call(s, PolicyRequest{Policy: "present"}); e == nil || e.Error() != boom.Error() {
			t.Fatalf("initial resolution: %v", e)
		}
	}
}
func TestPolicyInitRetainsRealWriteThenNoticeThenCheck(t *testing.T) {
	t.Parallel()
	boom := errors.New("selected storage refusal")
	for _, stage := range []string{"missing-reference", "type", "write", "check", "clean"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			s := newPolicyTestService()
			root := violatingModule(t)
			r := PolicyRequest{Directory: root, Policy: writeTestPolicy(t)}
			target := filepath.Join(root, policy.ConfigFileName)
			var order []string
			if stage == "missing-reference" {
				r.Policy = ""
			}
			if stage == "type" {
				if e := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module unknown.example/module\n\ngo 1.26\n"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			actualWrite := s.deps.WriteFile
			s.deps.WriteFile = func(path string, b []byte, m os.FileMode) error {
				order = append(order, "write")
				if path != target || m != 0600 {
					t.Fatalf("write custody %s %o", path, m)
				}
				if stage == "write" {
					return boom
				}
				return actualWrite(path, b, m)
			}
			actualCheck := s.deps.Check
			s.deps.Check = func(p policy.Policy, m policy.Module, d string) (policy.Result, error) {
				order = append(order, "check")
				if d != "" {
					t.Fatal("init unexpectedly supplied declared type")
				}
				if stage == "check" {
					return policy.Result{}, boom
				}
				if stage == "clean" {
					return policy.Result{}, nil
				}
				return actualCheck(p, m, d)
			}
			_, e := s.Init(context.Background(), r, func(n PolicyInitNotice) {
				order = append(order, "notice")
				b, err := os.ReadFile(n.Path)
				if err != nil || string(b) != "policy: "+r.Policy+"\n" || n.DetectedType != "extension-implementation" {
					t.Fatalf("actual notice/write: %q %+v %v", b, n, err)
				}
			})
			switch stage {
			case "missing-reference":
				if e == nil || !strings.Contains(e.Error(), "--policy is required") {
					t.Fatal(e)
				}
			case "type":
				if e == nil || !strings.Contains(e.Error(), "Add a \"type:\" line") {
					t.Fatal(e)
				}
			case "write":
				if e != boom || !reflect.DeepEqual(order, []string{"write"}) {
					t.Fatalf("write refusal %v %v", order, e)
				}
			case "check":
				if e == nil || e.Error() != boom.Error() || !reflect.DeepEqual(order, []string{"write", "notice", "check"}) {
					t.Fatalf("durable check refusal %v %v", order, e)
				}
			case "clean":
				if e != nil || !reflect.DeepEqual(order, []string{"write", "notice", "check"}) {
					t.Fatalf("clean %v %v", order, e)
				}
			}
		})
	}
}
func TestPolicyFleetAndDocumentErrorsKeepPreflightOrdering(t *testing.T) {
	t.Parallel()
	boom := errors.New("selected stage refusal")
	ctx := context.Background()
	for _, name := range []string{"report", "drift", "impact", "validate", "expectations"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newPolicyTestService()
			s.deps.Select = func(context.Context, Selection) ([]deps.Repository, error) { return nil, boom }
			s.deps.Load = func(string) (policy.Policy, error) { return policy.Policy{}, boom }
			var e error
			switch name {
			case "report":
				_, e = s.Report(ctx, PolicyFleetRequest{})
			case "drift":
				_, e = s.Drift(ctx, PolicyFleetRequest{})
			case "impact":
				_, e = s.Impact(ctx, PolicyFleetRequest{}, "candidate")
			case "validate":
				_, e = s.Validate(ctx, "candidate")
			case "expectations":
				_, e = s.Expectations(ctx, "candidate")
			}
			if e == nil || e.Error() != boom.Error() {
				t.Fatalf("%s: %v", name, e)
			}
		})
	}
	s := newPolicyTestService()
	s.deps.Abs = func(string) (string, error) { return "", boom }
	s.deps.Select = func(context.Context, Selection) ([]deps.Repository, error) {
		t.Fatal("selection before candidate preflight")
		return nil, nil
	}
	if _, e := s.Impact(ctx, PolicyFleetRequest{}, "candidate"); e == nil || e.Error() != boom.Error() {
		t.Fatal(e)
	}
	s = newPolicyTestService()
	candidate := writeTestPolicy(t)
	s.deps.Select = func(context.Context, Selection) ([]deps.Repository, error) { return nil, boom }
	if _, e := s.Impact(ctx, PolicyFleetRequest{}, candidate); e == nil || e.Error() != boom.Error() {
		t.Fatal(e)
	}
}
func TestPolicySweepKeepsRealConfigurationAndSelectedEngineRefusal(t *testing.T) {
	t.Parallel()
	s := newPolicyTestService()
	root := violatingModule(t)
	ref := writeTestPolicy(t)
	s.deps.Check = func(policy.Policy, policy.Module, string) (policy.Result, error) {
		return policy.Result{}, errors.New("selected engine refusal")
	}
	rows := s.sweep("", []deps.Repository{{Slug: "private/module", Path: root}}, ref)
	if len(rows) != 1 || rows[0].Governed || rows[0].Skipped != "selected engine refusal" {
		t.Fatalf("%+v", rows)
	}
}

func TestPolicyResolutionAndDriftKeepMissingModuleAndTypeFindings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newPolicyTestService()
	if _, e := s.Check(ctx, PolicyRequest{Directory: t.TempDir()}); e == nil || exitCodeOfSafe(e) != 2 || !strings.Contains(e.Error(), "no go.mod found") {
		t.Fatalf("actual missing module: %v", e)
	}
	root := violatingModule(t)
	s.deps.Select = func(context.Context, Selection) ([]deps.Repository, error) {
		return []deps.Repository{{Slug: "private/module", Path: root}}, nil
	}
	s.deps.Scan = func(string) (policy.Module, error) {
		return policy.Module{}, errors.New("selected lexical read refusal")
	}
	got, e := s.Drift(ctx, PolicyFleetRequest{})
	if e != nil || got.Issues != 1 || len(got.Rows) != 1 || got.Rows[0].Module != filepath.Base(root) {
		t.Fatalf("fallback %+v %v", got, e)
	}
	s = newPolicyTestService()
	s.deps.Select = func(context.Context, Selection) ([]deps.Repository, error) {
		return []deps.Repository{{Slug: "private/module", Path: root}}, nil
	}
	if e := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module unknown.example/module\n\ngo 1.26\n"), 0600); e != nil {
		t.Fatal(e)
	}
	got, e = s.Drift(ctx, PolicyFleetRequest{Policy: writeTestPolicy(t)})
	if e != nil || got.Issues != 1 || got.Rows[0].Issue != "no type declared and none detected" {
		t.Fatalf("missing type %+v %v", got, e)
	}
}
func TestPolicyImpactUsesActualBaselineAndCandidateVerdicts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := newPolicyTestService()
	baseline := writeTestPolicy(t)
	candidate := filepath.Join(t.TempDir(), "candidate.yaml")
	raw := strings.Replace(testPolicyDocument, "source: {allow: [own-repo, extension-contract, dalgo-core, third-party]}", "source: {allow: [own-repo, extension-contract, extension-implementation, dalgo-core, third-party]}", 1)
	if e := os.WriteFile(candidate, []byte(raw), 0600); e != nil {
		t.Fatal(e)
	}
	failing := violatingModule(t)
	clean, ungoverned := t.TempDir(), t.TempDir()
	for _, root := range []string{failing, clean, ungoverned} {
		if root != failing {
			cwCovWriteFile(t, filepath.Join(root, "go.mod"), "module github.com/acme/clean/backend\n\ngo 1.26\n")
			cwCovWriteFile(t, filepath.Join(root, "app.go"), "package clean\n")
		}
		if root != ungoverned {
			cwCovWriteFile(t, filepath.Join(root, policy.ConfigFileName), "policy: "+baseline+"\n")
		}
	}
	s.deps.Select = func(context.Context, Selection) ([]deps.Repository, error) {
		return []deps.Repository{{Slug: "private/failing", Path: failing}, {Slug: "private/clean", Path: clean}, {Slug: "private/ungoverned", Path: ungoverned}}, nil
	}
	got, e := s.Impact(ctx, PolicyFleetRequest{}, candidate)
	if e != nil || len(got.NewlyPassing) != 1 || got.NewlyPassing[0].Repository != "private/failing" || got.NewlyPassing[0].Before != 1 || got.NewlyPassing[0].After != 0 || got.Unchanged != 1 || len(got.NewlyFailing) != 0 || got.Candidate != candidate {
		t.Fatalf("actual verdict comparison %+v %v", got, e)
	}
}
