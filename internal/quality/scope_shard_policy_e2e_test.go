//go:build e2e

package quality

import (
	"context"
	"testing"
	"time"
)

// A merge-base measurement scoped to the changed packages measures only the
// packages that existed at the merge base: the baseline carries no entry for
// the one the change adds, and none for a package outside the scope.
func TestE2EComputeBaselineAtRefScopedToChangedPackagesMeasuresOnlyThoseThatExist(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	repo.writeFile("other/other.go", "package other\n\nfunc Value() int { return 1 }\n")
	repo.writeFile("other/other_test.go", "package other\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"out of scope package was measured\") }\n")
	repo.commitAll("base")

	baseline, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute, RunOptions{GoTestPackages: []string{".", "./added"}})
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Packages["."] != 3 {
		t.Fatalf("baseline.Packages[.] = %d, want 3", baseline.Packages["."])
	}
	if _, measured := baseline.Packages["added"]; measured {
		t.Fatalf("a package absent at the merge base has a baseline entry: %v", baseline.Packages)
	}
}

func TestE2EComputeBaselineAtRefScopedToOnlyNewPackagesYieldsAnEmptyBaseline(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	repo.commitAll("base")

	baseline, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute, RunOptions{GoTestPackages: []string{"./added"}, IncludeE2E: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.Packages) != 0 || baseline.SHA == "" || !baseline.IncludeE2E {
		t.Fatalf("baseline = %+v, want an empty baseline stamped with the merge-base SHA and the E2E flag", baseline)
	}
}
