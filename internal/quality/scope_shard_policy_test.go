package quality

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
