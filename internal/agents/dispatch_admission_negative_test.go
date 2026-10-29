package agents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestManagedWorktreeEntryRefusesEachUnusableInventoryShape(t *testing.T) {
	t.Parallel()
	const (
		worktreeDir = "/fleet/repo/.worktrees/task-one"
		canonical   = "/fleet/repo"
	)
	target := Target{Repository: "acme/app", Dir: worktreeDir, WorktreeName: "task-one"}
	cases := map[string]struct {
		deps   DispatchDeps
		target Target
		want   string
	}{
		"no inventory reader": {
			deps: DispatchDeps{}, target: target, want: "no inventory reader is available",
		},
		"entry without a worktree directory": {
			deps: DispatchDeps{ListWorktrees: func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
				return []worktrees.ListResult{{Repository: "acme/app", CanonicalDir: canonical}}, nil
			}},
			target: target, want: "not an entry in WB's managed worktree inventory",
		},
		"entry for a different worktree path": {
			deps: DispatchDeps{ListWorktrees: func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
				return []worktrees.ListResult{{Repository: "acme/app", WorktreeDir: worktreeDir + "-other", CanonicalDir: canonical}}, nil
			}},
			target: target, want: "not an entry in WB's managed worktree inventory",
		},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := managedWorktreeEntry(context.Background(), testCase.target, testCase.deps)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestSnapshotExecutionWorkspaceRefusesUnsafeAdmissionFacts(t *testing.T) {
	t.Parallel()
	const (
		worktree  = "/fleet/repo/.worktrees/task-one"
		canonical = "/fleet/repo"
	)
	cases := map[string]struct {
		target worktreeResolution
		want   string
	}{
		"no worktree directory": {
			target: worktreeResolution{CanonicalDir: canonical},
			want:   "none was resolved",
		},
		"relative worktree": {
			target: worktreeResolution{Dir: "repo/.worktrees/task-one", CanonicalDir: canonical},
			want:   "clean absolute path",
		},
		"unclean worktree": {
			target: worktreeResolution{Dir: worktree + "/.", CanonicalDir: canonical},
			want:   "clean absolute path",
		},
		"relative canonical": {
			target: worktreeResolution{Dir: worktree, CanonicalDir: "repo"},
			want:   "clean absolute path",
		},
		"unclean canonical": {
			target: worktreeResolution{Dir: worktree, CanonicalDir: canonical + "/."},
			want:   "clean absolute path",
		},
		"canonical git directory overlaps the worktree": {
			target: worktreeResolution{Dir: canonical + "/.git", CanonicalDir: canonical},
			want:   "two exact disjoint roots",
		},
	}
	for name, testCase := range cases {
		name, testCase := name, testCase
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := snapshotExecutionWorkspace(testCase.target); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

//nolint:paralleltest // newDispatchFixture pins process-wide environment variables (WB_PROJECTS_ROOT and the provider credential), so the test cannot run in parallel.
func TestDispatchRefusesContainedAdmissionWhenTheCreatedWorktreeHasNoCanonicalClone(t *testing.T) {
	// Not parallel: the dispatch fixture pins environment variables.
	fixture := newDispatchFixture(t)
	body := executionYAML(ExecutionModeLinuxContainer, executionTestImage, 8589934592, 8589934592, 2, 128, 1) + `  providers:
    deepseek-official:
      base_url: https://api.deepseek.com
      credential_env: WB_TEST_DEEPSEEK_OFFICIAL_KEY
      wire_api: chat
  profiles:
    ds: { harness: dsh, model: deepseek-flash }
`
	fixture.deps.LoadConfig = func() (Config, error) {
		return LoadConfigFile(writeConfig(t, body))
	}
	fixture.deps.CreateWorktree = func(_ context.Context, _ []string, _ worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
		fixture.createCalls++
		// The checkout exists but the inventory facts never name the canonical
		// clone, so no trusted mount can be snapshotted.
		return []worktrees.CreateResult{{
			Repository: "acme/app", WorktreeDir: "/fleet/.worktrees/task-one",
			Branch: "task-one", Base: "main", BaseSHA: "abc123", Action: "created",
		}}, nil
	}
	request := newRequest()
	request.Profile = "ds"
	request.Timeout = time.Minute
	_, err := fixture.dispatch(t, request)
	if err == nil || !strings.Contains(err.Error(), "canonical clone") {
		t.Fatalf("error = %v, want the missing canonical-clone refusal", err)
	}
	if fixture.spawnedAgentID != "" {
		t.Fatalf("a refused contained admission started an owner: %q", fixture.spawnedAgentID)
	}
	records, err := NewStore(fixture.home).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("a refused contained admission persisted a run: %#v", records)
	}
}
