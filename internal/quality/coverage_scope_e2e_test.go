//go:build e2e

package quality

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestE2ECoverageScopeRevisionUnion(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("gone/gone.go", "package gone\nfunc Value() int { return 1 }\n")
	repo.runGit("add", "-A")
	repo.runGit("commit", "-qm", "base")
	base := strings.TrimSpace(repo.runGit("rev-parse", "HEAD"))
	repo.runGit("rm", "gone/gone.go")
	repo.writeFile("new/new.go", "package new\nfunc Value() int { return 2 }\n")
	repo.writeFile("new/new_test.go", "package new\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value()!=2 { t.Fatal(Value()) } }\n")
	scope, err := AffectedCoverageScope(t.Context(), repo.dir, base, map[string]bool{"gone/gone.go": true, "new/new.go": true}, true)
	if err != nil || !reflect.DeepEqual(scope.Packages, []string{"./gone", "./new"}) {
		t.Fatalf("scope=%+v err=%v", scope, err)
	}
	baseline, err := ComputeBaselineAtRef(t.Context(), repo.dir, base, time.Minute, RunOptions{GoTestPackages: []string{"./new"}, IncludeE2E: true})
	if err != nil || len(baseline.Packages) != 0 || !baseline.IncludeE2E {
		t.Fatalf("new package baseline=%+v err=%v", baseline, err)
	}
	baseline, err = ComputeBaselineAtRef(t.Context(), repo.dir, base, time.Minute, RunOptions{GoTestPackages: scope.Packages})
	if err != nil || baseline.Packages["gone"] != 1 {
		t.Fatalf("mixed deleted/new baseline=%+v err=%v", baseline, err)
	}
}

func TestE2ECoverageScopeNativeOnlyPackageBroadensSafely(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("ordinary/ordinary.go", "package ordinary\nfunc Value() int { return 1 }\n")
	repo.runGit("add", "-A")
	repo.runGit("commit", "-qm", "base")
	base := strings.TrimSpace(repo.runGit("rev-parse", "HEAD"))
	repo.writeFile("native/native.go", "//go:build e2e\n\npackage native\nfunc Value() int { return 2 }\n")
	scope, err := AffectedCoverageScope(t.Context(), repo.dir, base, map[string]bool{"native/native.go": true}, true)
	if err != nil || !scope.Full || !reflect.DeepEqual(scope.Packages, []string{"./..."}) || !reflect.DeepEqual(scope.ChangedPackages, []string{"./native"}) {
		t.Fatalf("native-only scope=%+v error=%v", scope, err)
	}
	profile := repo.dir + "/profile.cov"
	report := CoverWithOptions(t.Context(), "native-only", repo.dir, RunOptions{GoTestPackages: scope.Packages, IncludeE2E: true, CoverageProfile: profile, Timeout: time.Minute})
	if report.Status == StatusFailed {
		t.Fatal(report.Error)
	}
}

func TestE2ECoverageScopeGoListEmbeddingConsumers(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("parent/parent.go", "package parent\nimport _ \"embed\"\n//go:embed child/data.txt\nvar data string\n")
	repo.writeFile("parent/parent_test.go", "package parent\nimport _ \"embed\"\n//go:embed child/data.txt\nvar testData string\n")
	repo.writeFile("parent/external_test.go", "package parent_test\nimport _ \"embed\"\n//go:embed child/data.txt\nvar externalData string\n")
	repo.writeFile("parent/child/child.go", "package child\n")
	repo.writeFile("parent/child/data.txt", "base")
	repo.writeFile("root_test.go", "package app\nimport _ \"embed\"\n//go:embed parent/child/data.txt\nvar rootTestData string\n")
	repo.runGit("add", "-A")
	repo.runGit("commit", "-qm", "base")
	base := strings.TrimSpace(repo.runGit("rev-parse", "HEAD"))
	output, err := runStdout(t.Context(), repo.dir, "go", "list", "-test", "-json", "./...")
	if err != nil {
		t.Fatal(err)
	}
	graph, err := parseCoverageScopeGraph(repo.dir, output)
	if err != nil {
		t.Fatal(err)
	}
	var parent CoverageScopePackage
	for _, pkg := range graph {
		if pkg.Pattern == "./parent" {
			parent = pkg
		}
	}
	for _, files := range [][]string{parent.EmbedFiles, parent.TestEmbedFiles, parent.XTestEmbedFiles} {
		if !reflect.DeepEqual(files, []string{"child/data.txt"}) {
			t.Fatalf("native go list embed metadata=%+v", parent)
		}
	}
	repo.writeFile("parent/child/data.txt", "head")
	scope, err := AffectedCoverageScope(t.Context(), repo.dir, base, map[string]bool{"parent/child/data.txt": true}, true)
	if err != nil || scope.Full || !reflect.DeepEqual(scope.Packages, []string{".", "./parent", "./parent/child"}) || !reflect.DeepEqual(scope.ChangedPackages, []string{".", "./parent", "./parent/child"}) {
		t.Fatalf("native embedding scope=%+v error=%v", scope, err)
	}
}
