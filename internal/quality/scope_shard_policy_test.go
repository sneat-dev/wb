package quality

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestScopeShardPolicyToPackagesKeepsOnlyShardsTheScopeCovers(t *testing.T) {
	t.Parallel()
	policy := RunOptions{GoTestShards: 4, GoShardPackages: []string{"./internal/worktrees", "./internal/orchestrate"}}
	cases := []struct {
		name       string
		scope      []string
		wantShards int
		wantKept   []string
	}{
		{"no scope keeps the whole policy", nil, 4, []string{"./internal/worktrees", "./internal/orchestrate"}},
		{"scope covering one shard package keeps just it", []string{"./cmd/wb", "./internal/worktrees"}, 4, []string{"./internal/worktrees"}},
		{"subtree pattern covers nested shard packages", []string{"./internal/..."}, 4, []string{"./internal/worktrees", "./internal/orchestrate"}},
		{"root subtree covers everything", []string{"./..."}, 4, []string{"./internal/worktrees", "./internal/orchestrate"}},
		{"bare ellipsis covers everything", []string{"..."}, 4, []string{"./internal/worktrees", "./internal/orchestrate"}},
		{"module root subtree is not the root package", []string{"."}, 1, nil},
		{"scope covering none runs unsharded", []string{"./cmd/wb"}, 1, nil},
		{"sibling prefix is not a subtree", []string{"./internal/work/..."}, 1, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			options := policy
			options.GoTestPackages = tc.scope
			got := ScopeShardPolicyToPackages(options)
			if got.GoTestShards != tc.wantShards || !reflect.DeepEqual(got.GoShardPackages, tc.wantKept) {
				t.Fatalf("shards=%d packages=%v, want shards=%d packages=%v", got.GoTestShards, got.GoShardPackages, tc.wantShards, tc.wantKept)
			}
		})
	}
	if got := ScopeShardPolicyToPackages(RunOptions{GoTestPackages: []string{"./x"}}); got.GoTestShards != 0 || got.GoShardPackages != nil {
		t.Fatalf("a run with no shard policy changed: %+v", got)
	}
}

func TestExistingPackagePatternsDropsDirectoriesTheRefDoesNotHave(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, directory := range []string{"present", "tree/deep"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got := existingPackagePatterns(root, []string{"./present", "./added", ".", "./tree/...", "./...", "./file"})
	want := []string{"./present", ".", "./tree/...", "./..."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("existing patterns = %v, want %v", got, want)
	}
	if got := existingPackagePatterns(root, []string{"...", "./nope"}); !reflect.DeepEqual(got, []string{"..."}) {
		t.Fatalf("bare ellipsis = %v", got)
	}
}

// A merge-base measurement scoped to the changed packages measures only the
// packages that existed at the merge base: the baseline carries no entry for
// the one the change adds, and none for a package outside the scope.
func TestComputeBaselineAtRefScopedToChangedPackagesMeasuresOnlyThoseThatExist(t *testing.T) {
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

func TestComputeBaselineAtRefScopedToOnlyNewPackagesYieldsAnEmptyBaseline(t *testing.T) {
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
