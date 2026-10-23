//go:build e2e

package quality

import (
	"reflect"
	"testing"
	"time"
)

func TestE2EComputeBaselineAtRefMeasuresARedBaseAndNamesItsFailedTests(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource+"\nfunc TestBrokenAtBase(t *testing.T) { t.Fatal(\"broken at base\") }\n")
	sha := repo.commitAll("red base")

	baseline, err := ComputeBaselineAtRef(t.Context(), repo.dir, "HEAD", 2*time.Minute, RunOptions{})
	if err != nil {
		t.Fatalf("a base with a failing test could not be measured: %v", err)
	}
	want := &RedBaseline{SHA: sha, FailedTests: []string{repo.modulePath + ".TestBrokenAtBase"}}
	if baseline.SHA != sha || !reflect.DeepEqual(baseline.RedBase, want) {
		t.Fatalf("baseline sha %q red base = %+v, want %+v", baseline.SHA, baseline.RedBase, want)
	}
	uncovered := 0
	for _, count := range baseline.Packages {
		uncovered += count
	}
	if uncovered == 0 {
		t.Fatalf("packages = %v, want the uncovered statements the failing run still measured", baseline.Packages)
	}
}
