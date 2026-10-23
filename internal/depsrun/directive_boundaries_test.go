package depsrun

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sneat-dev/wb/internal/deps"
)

type directiveContextKey struct{}

func TestDirectiveChecksKeepSynchronousMutationAndResultAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.WithValue(context.Background(), directiveContextKey{}, "current")
	refused := errors.New("module refused")
	for _, apply := range []bool{false, true} {
		t.Run(map[bool]string{false: "assess", true: "apply"}[apply], func(t *testing.T) {
			t.Parallel()
			var order []string
			inspect := func(got context.Context, path string, p deps.DirectivePolicy, o deps.Options) (deps.DirectiveAssessment, error) {
				if got != ctx || p.GoVersion != "1.26.0" || o.Retry != 7 {
					t.Fatalf("request=%v/%+v/%+v", got, p, o)
				}
				order = append(order, "engine:"+path)
				if path == "/repo/broken" {
					return deps.DirectiveAssessment{}, refused
				}
				return deps.DirectiveAssessment{Verdict: deps.DirectiveWouldChange, Detail: "before", CurrentGoVersion: "1.27.0"}, nil
			}
			d := Dependencies{Abs: func(path string) (string, error) {
				if path != "relative" {
					t.Fatal(path)
				}
				return "/repo", nil
			}, DiscoverModules: func(path string) []string {
				if path != "/repo" {
					t.Fatal(path)
				}
				return []string{"/repo/good", "/repo/broken"}
			}, AssessDirective: inspect, ApplyDirective: inspect}
			result, err := New(d).CheckDirectives(ctx, DirectiveCheckRequest{Directory: "relative", Policy: deps.DirectivePolicy{GoVersion: "1.26.0"}, Options: deps.Options{Retry: 7}, Apply: apply, CodeQLCeiling: "1.26.7"}, func(row DirectiveCheckRow) {
				order = append(order, "output:"+row.Label)
				if row.Label == "good" && (row.Error != nil || row.Detail == "before") {
					t.Fatalf("risk/detail=%+v", row)
				}
				if row.Label == "broken" && !errors.Is(row.Error, refused) {
					t.Fatalf("error=%v", row.Error)
				}
			})
			want := 2
			if apply {
				want = 1
			}
			if err != nil || result.ModuleCount != 2 || result.Attention != want || !reflect.DeepEqual(order, []string{"engine:/repo/good", "output:good", "engine:/repo/broken", "output:broken"}) {
				t.Fatalf("result=%+v/%v order=%v", result, err, order)
			}
		})
	}
}
func TestDirectiveCheckInitialPathErrorPrecedesDiscovery(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("abs refused")
	service := New(Dependencies{Abs: func(string) (string, error) { return "", sentinel }, DiscoverModules: func(string) []string { t.Fatal("discovery after refusal"); return nil }})
	result, err := service.CheckDirectives(context.Background(), DirectiveCheckRequest{}, func(DirectiveCheckRow) { t.Fatal("output after refusal") })
	if !errors.Is(err, sentinel) || result != (DirectiveCheckResult{}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
func TestDiscoverModulesPreservesPrivateReadOnlyExclusionsAndErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, path := range []string{"go.mod", "backend/go.mod", "vendor/skip/go.mod", "node_modules/skip/go.mod", "testdata/skip/go.mod", ".hidden/go.mod"} {
		name := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(name), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte("module example.test/module\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got := DiscoverModules(root)
	want := []string{root, filepath.Join(root, "backend")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("modules=%v", got)
	}
	if got := DiscoverModules(filepath.Join(root, "missing")); len(got) != 0 {
		t.Fatalf("missing=%v", got)
	}
	if got := moduleLabel("/absolute", "relative"); got != "absolute" {
		t.Fatalf("incompatible root label=%q", got)
	}
}
func TestDirectiveSweepPreservesAssessmentErrorAndRiskOrdering(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sentinel := errors.New("assessment refused")
	service := New(Dependencies{DiscoverModules: func(string) []string { return []string{"/repo/broken", "/repo/risky"} }, AssessDirective: func(got context.Context, path string, _ deps.DirectivePolicy, _ deps.Options) (deps.DirectiveAssessment, error) {
		if got != ctx {
			t.Fatal("context changed")
		}
		if path == "/repo/broken" {
			return deps.DirectiveAssessment{}, sentinel
		}
		return deps.DirectiveAssessment{ModulePath: "example.test/risky", Verdict: deps.DirectiveCannotComply, Detail: "cannot", CurrentGoVersion: "1.27.0"}, nil
	}})
	rows := service.ReportDirectives(ctx, []deps.Repository{{Slug: "repo", Path: "/repo"}}, deps.DirectivePolicy{}, deps.Options{}, "1.26.7")
	if len(rows) != 2 || rows[0].Verdict != string(deps.DirectiveError) || rows[0].Detail != sentinel.Error() || !rows[1].CodeQLAtRisk || rows[1].Module != "example.test/risky" || rows[1].Detail == "cannot" {
		t.Fatalf("rows=%+v", rows)
	}
}
func TestDirectiveDefaultStagesUseCanonicalReadOnlyModuleDiscovery(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	service := New(DefaultDependencies(io.Discard))
	result, err := service.CheckDirectives(context.Background(), DirectiveCheckRequest{Directory: root}, func(DirectiveCheckRow) { t.Fatal("empty root emitted a module") })
	if err != nil || result.ModuleCount != 0 {
		t.Fatalf("result=%+v/%v", result, err)
	}
}
