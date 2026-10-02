package quality

import (
	"reflect"
	"strings"
	"testing"
)

func TestPlanCoverageScope(t *testing.T) {
	t.Parallel()
	base := []CoverageScopePackage{
		{Pattern: "./core", ImportPath: "m/core", Imports: []string{"m/top"}},
		{Pattern: "./top", ImportPath: "m/top", TestImports: []string{"m/core", "m/core"}},
		{Pattern: "./external", ImportPath: "m/external", XTestImports: []string{"m/top"}},
		{Pattern: "./other", ImportPath: "m/other"},
		{Pattern: "./gone", ImportPath: "m/gone"},
	}
	head := []CoverageScopePackage{
		{Pattern: "./native", ImportPath: "m/native", Imports: []string{"m/external"}},
		{Pattern: "./new", ImportPath: "m/new"},
		{Pattern: "./core/nested", ImportPath: "m/core/nested"},
	}
	tests := []struct {
		name   string
		files  []string
		want   []string
		full   bool
		reason string
	}{
		{name: "reverse closure with cycles and test imports", files: []string{"core/core.go"}, want: []string{"./core", "./external", "./native", "./top"}},
		{name: "fixture ancestor", files: []string{"core/testdata/input.json"}, want: []string{"./core", "./external", "./native", "./top"}},
		{name: "nearest package owns embedded asset", files: []string{"core/nested/assets/logo.svg"}, want: []string{"./core/nested"}},
		{name: "deleted and new packages", files: []string{"gone/gone.go", "new/new.go"}, want: []string{"./gone", "./new"}},
		{name: "deduplicate and sort", files: []string{"other/b.go", "new/a.go", "other/a.go"}, want: []string{"./new", "./other"}},
		{name: "docs only", files: []string{"README.md", "spec/plans/x.yaml", "docs/example.txt"}, want: []string{}},
		{name: "empty", want: []string{}},
		{name: "unowned source", files: []string{"missing/a.go"}, want: []string{"./..."}, full: true, reason: "unowned input"},
		{name: "unowned fixture", files: []string{"testdata/shared.json"}, want: []string{"./..."}, full: true, reason: "unowned input"},
	}
	for _, file := range []string{"go.mod", "go.sum", "go.work", "go.work.sum", "tools/go.mod", "Makefile", "tools/Makefile", "Dockerfile", ".github/workflows/go-ci.yml", ".github/scripts/go-scope.sh", ".wb/quality.yaml", "vendor/x/x.go", ".goreleaser.yml", ".golangci.yaml"} {
		tests = append(tests, struct {
			name   string
			files  []string
			want   []string
			full   bool
			reason string
		}{name: file, files: []string{file}, want: []string{"./..."}, full: true, reason: "shared build input"})
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := PlanCoverageScope(test.files, base, head)
			if !reflect.DeepEqual(got.Packages, test.want) || got.Full != test.full || !strings.Contains(got.Reason, test.reason) {
				t.Fatalf("PlanCoverageScope = %+v, want packages=%v full=%v reason=%s", got, test.want, test.full, test.reason)
			}
		})
	}
}

func TestPlanCoverageScopeRootPackage(t *testing.T) {
	t.Parallel()
	got := PlanCoverageScope([]string{"assets/input.txt"}, []CoverageScopePackage{{Pattern: ".", ImportPath: "m"}, {Pattern: "./child", ImportPath: "m/child"}})
	if !reflect.DeepEqual(got.Packages, []string{"."}) || got.Full {
		t.Fatalf("root asset scope = %+v", got)
	}
}

func TestPlanCoverageScopeSelectsEveryEmbeddingConsumer(t *testing.T) {
	t.Parallel()
	base := []CoverageScopePackage{
		{Pattern: "./parent", ImportPath: "m/parent", EmbedFiles: []string{"child/data.txt"}},
		{Pattern: "./parent/child", ImportPath: "m/parent/child"},
		{Pattern: ".", ImportPath: "m", TestEmbedFiles: []string{"parent/child/data.txt"}},
		{Pattern: "./dependent", ImportPath: "m/dependent", Imports: []string{"m/parent"}},
	}
	head := []CoverageScopePackage{
		{Pattern: "./parent", ImportPath: "m/parent", XTestEmbedFiles: []string{"child/data.txt", "child/head.txt"}},
	}
	got := PlanCoverageScope([]string{"parent/child/data.txt"}, base, head)
	if got.Full || !reflect.DeepEqual(got.Packages, []string{".", "./dependent", "./parent", "./parent/child"}) ||
		!reflect.DeepEqual(got.ChangedPackages, []string{".", "./parent", "./parent/child"}) {
		t.Fatalf("embedding scope=%+v", got)
	}
	got = PlanCoverageScope([]string{"parent/child/head.txt"}, base, head)
	if got.Full || !reflect.DeepEqual(got.ChangedPackages, []string{"./parent", "./parent/child"}) {
		t.Fatalf("new embedding relationship scope=%+v", got)
	}
}
