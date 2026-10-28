package agents

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestMandatoryTargetsRequireTheExactManagedInventoryEntry(t *testing.T) {
	t.Parallel()
	const (
		worktreeDir = "/fleet/repo/.worktrees/task-one"
		canonical   = "/fleet/repo"
	)
	entry := worktrees.ListResult{
		Task: "task-one", Repository: "acme/app", WorktreeDir: worktreeDir, CanonicalDir: canonical,
	}
	target := Target{
		Repository: "acme/app", Dir: worktreeDir, WorktreeName: "task-one",
		Branch: "task-one", Base: "main", BaseSHA: "abc123",
	}
	deps := func(results []worktrees.ListResult, fail error) DispatchDeps {
		return DispatchDeps{ListWorktrees: func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
			return results, fail
		}}
	}

	resolution, err := resolveMandatoryTarget(context.Background(), target, deps([]worktrees.ListResult{entry}, nil))
	if err != nil {
		t.Fatalf("a managed entry must resolve: %v", err)
	}
	if resolution.Dir != worktreeDir || resolution.CanonicalDir != canonical || resolution.Repository != "acme/app" {
		t.Fatalf("resolution = %#v", resolution)
	}

	cases := map[string]struct {
		target  Target
		results []worktrees.ListResult
		fail    error
		want    string
	}{
		"unmanaged checkout": {
			target: target, want: "not an entry in WB's managed worktree inventory",
		},
		"inventory failure": {
			target: target, fail: os.ErrPermission, want: "resolve the managed worktree inventory",
		},
		"repository mismatch": {
			target: target,
			results: []worktrees.ListResult{{
				Repository: "other/app", WorktreeDir: worktreeDir, CanonicalDir: canonical,
			}},
			want: "not an entry in WB's managed worktree inventory",
		},
		"ambiguous entries": {
			target:  target,
			results: []worktrees.ListResult{entry, entry},
			want:    "refuses to guess",
		},
		"whole clone entry": {
			target: Target{Repository: "acme/app", Dir: canonical},
			results: []worktrees.ListResult{{
				Repository: "acme/app", WorktreeDir: canonical, CanonicalDir: canonical,
			}},
			want: "canonical whole clone",
		},
		"missing canonical": {
			target:  target,
			results: []worktrees.ListResult{{Repository: "acme/app", WorktreeDir: worktreeDir}},
			want:    "carries no canonical directory",
		},
		"canonical target": {
			target:  Target{Repository: "acme/app", Dir: canonical, Canonical: true},
			results: []worktrees.ListResult{entry},
			want:    "is a canonical clone",
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := resolveMandatoryTarget(context.Background(), testCase.target, deps(testCase.results, testCase.fail))
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestMandatoryPathTargetRefusesCanonicalEvenWithTheLegacyOverride(t *testing.T) {
	t.Parallel()
	canonical := t.TempDir()
	runGit(t, canonical, "init", "-b", "main")
	runGit(t, canonical, "config", "user.email", "test@example.com")
	runGit(t, canonical, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(canonical, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, canonical, "add", "-A")
	runGit(t, canonical, "commit", "-qm", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, canonical, "worktree", "add", "-b", "linked", linked)

	var listed bool
	deps := DispatchDeps{
		OriginSlug: func(context.Context, string) (string, error) { return "acme/app", nil },
		ListWorktrees: func(context.Context, worktrees.ListOptions) ([]worktrees.ListResult, error) {
			listed = true
			return []worktrees.ListResult{{
				Repository: "acme/app", WorktreeDir: linked, CanonicalDir: canonical,
			}}, nil
		},
	}

	request := DispatchRequest{Mode: ModePath, Worktree: linked}
	resolved, err := resolveExistingTargetWithPolicy(context.Background(), request, deps, true)
	if err != nil {
		t.Fatalf("a managed linked worktree must resolve: %v", err)
	}
	if resolved.CanonicalDir != canonical {
		t.Fatalf("canonical directory = %q, want %q", resolved.CanonicalDir, canonical)
	}

	// The legacy --allow-canonical option must not turn a contained execution
	// into a whole-clone mount.
	listed = false
	request = DispatchRequest{Mode: ModePath, Worktree: canonical, AllowCanonical: true}
	if _, err := resolveExistingTargetWithPolicy(context.Background(), request, deps, true); err == nil || !strings.Contains(err.Error(), "canonical clone") {
		t.Fatalf("a canonical clone must be refused: %v", err)
	}
	if listed {
		t.Fatal("a canonical target must be refused before the inventory is trusted")
	}

	// The absent-policy path keeps the legacy behaviour exactly.
	request = DispatchRequest{Mode: ModePath, Worktree: canonical, AllowCanonical: true}
	legacy, err := resolveExistingTargetWithPolicy(context.Background(), request, deps, false)
	if err != nil {
		t.Fatalf("legacy current/path resolution must still allow a canonical checkout: %v", err)
	}
	if legacy.CanonicalDir != "" {
		t.Fatalf("legacy resolution must not claim inventory metadata: %#v", legacy)
	}
}

func TestExecutionWorkspaceSnapshotUsesOnlyTheTrustedCanonicalGitDirectory(t *testing.T) {
	t.Parallel()
	target := worktreeResolution{Dir: "/fleet/repo/.worktrees/task-one", CanonicalDir: "/fleet/repo"}
	workspace, err := snapshotExecutionWorkspace(target)
	if err != nil {
		t.Fatalf("snapshotExecutionWorkspace: %v", err)
	}
	if workspace.WorktreeDir != target.Dir || workspace.GitCommonDir != "/fleet/repo/.git" {
		t.Fatalf("workspace = %#v", workspace)
	}
	if workspace.InputDir != "" {
		t.Fatalf("admission must never invent a scoped input directory: %q", workspace.InputDir)
	}

	source := &ContainerWorkspace{WorktreeDir: target.Dir, GitCommonDir: "/fleet/repo/.git"}
	snapshot := SnapshotExecutionWorkspace(source)
	snapshot.WorktreeDir = "/mutated"
	if source.WorktreeDir != target.Dir {
		t.Fatal("the workspace snapshot aliases its source value")
	}
	if SnapshotExecutionWorkspace(nil) != nil {
		t.Fatal("a nil workspace must stay nil")
	}

	unmanaged := target
	unmanaged.CanonicalDir = ""
	if _, err := snapshotExecutionWorkspace(unmanaged); err == nil || !strings.Contains(err.Error(), "canonical clone") {
		t.Fatalf("a workspace without trusted inventory metadata must be refused: %v", err)
	}
}

func TestRecordAndResultSnapshotWorkspaceAndRuntimeFactsWithoutAliasing(t *testing.T) {
	t.Parallel()
	policy := executionTestPolicy()
	workspace := &ContainerWorkspace{
		WorktreeDir: "/fleet/repo/.worktrees/task-one", GitCommonDir: "/fleet/repo/.git",
	}
	limits := ContainerVerifiedLimits{MemoryBytes: 8589934592, MemorySwapBytes: 0, CPUCount: 2, PIDsLimit: 128}
	facts := &ExecutionRuntimeFacts{
		ContainerID:     strings.Repeat("a", 64),
		ResolvedImageID: "sha256:" + strings.Repeat("b", 64),
		VerifiedLimits:  &limits,
		CleanupPending:  true,
	}
	store := NewStore(t.TempDir())
	agentID, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	const credentialEnvName = "DEEPSEEK_API_KEY"
	record := Record{
		AgentID: agentID, State: StateRunning, ExecutionStatus: StatusRunning,
		Execution: &policy, ExecutionWorkspace: workspace, ExecutionRuntime: facts,
		Resolved: Resolved{
			Profile: "flash", Harness: HarnessDSH, Provider: ProviderDshDefault, Model: "deepseek-v4-flash",
			Routing: Provider{BaseURL: "https://api.deepseek.com", CredentialEnv: credentialEnvName, WireAPI: "chat"},
		},
		HarnessCapabilities: HarnessCapabilities{
			Harness: HarnessDSH, TaskDelivery: TaskDeliveryStdin, TokenUsage: UsageFromEvents,
			Cost: CostUnavailable, Effort: EffortUnsupported,
			Credential: CredentialWBInjected, Isolation: IsolationLinuxContainer,
		},
		StartedAt: time.Unix(1700000000, 0).UTC(),
	}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(agentID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ExecutionWorkspace == nil || *loaded.ExecutionWorkspace != *workspace {
		t.Fatalf("loaded workspace = %#v", loaded.ExecutionWorkspace)
	}
	if loaded.ExecutionRuntime == nil || loaded.ExecutionRuntime.ContainerID != facts.ContainerID ||
		loaded.ExecutionRuntime.ResolvedImageID != facts.ResolvedImageID ||
		loaded.ExecutionRuntime.VerifiedLimits == nil || *loaded.ExecutionRuntime.VerifiedLimits != limits ||
		!loaded.ExecutionRuntime.CleanupPending {
		t.Fatalf("loaded runtime facts = %#v", loaded.ExecutionRuntime)
	}

	raw, err := os.ReadFile(store.RecordPath(agentID))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}

	// The record legitimately carries credential-source metadata: the routing
	// credential *name* and the adapter's declared credential capability. Those
	// are what the runtime and workspace subobjects must not duplicate.
	var recordedResolved Resolved
	if err := json.Unmarshal(document["resolved"], &recordedResolved); err != nil {
		t.Fatal(err)
	}
	if recordedResolved.Routing.CredentialEnv != credentialEnvName || recordedResolved.Routing.CredentialFile != "" {
		t.Fatalf("recorded routing must keep the credential name and no value or file: %#v", recordedResolved.Routing)
	}
	var recordedCapabilities HarnessCapabilities
	if err := json.Unmarshal(document["harness_capabilities"], &recordedCapabilities); err != nil {
		t.Fatal(err)
	}
	if recordedCapabilities.Credential != CredentialWBInjected {
		t.Fatalf("recorded credential source = %q", recordedCapabilities.Credential)
	}

	// The public facts are the closed field sets of their own subobjects. Assert
	// privacy there rather than over the whole record: legitimate credential
	// metadata elsewhere must not make the runtime or workspace facts leak.
	publicSubobjects := map[string]map[string]bool{
		"execution_runtime": {
			"container_id": true, "resolved_image_id": true, "verified_limits": true,
			"cleanup_pending": true, "cleanup_verified": true, "cleanup_failed": true, "cleanup_failure": true,
		},
		"execution_workspace": {"worktree_dir": true, "git_common_dir": true, "input_dir": true},
	}
	for name, allowed := range publicSubobjects {
		subobject, ok := document[name]
		if !ok {
			t.Fatalf("record %s is missing the public subobject %q", raw, name)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(subobject, &fields); err != nil {
			t.Fatalf("%s is not a JSON object: %v", name, err)
		}
		for field := range fields {
			if !allowed[field] {
				t.Fatalf("%s carries the unexpected field %q: %s", name, field, subobject)
			}
		}
		for _, forbidden := range []string{"credential", credentialEnvName, "argv"} {
			if strings.Contains(string(subobject), forbidden) {
				t.Fatalf("%s must not carry %q: %s", name, forbidden, subobject)
			}
		}
	}
	var recordedRuntime ExecutionRuntimeFacts
	if err := json.Unmarshal(document["execution_runtime"], &recordedRuntime); err != nil {
		t.Fatal(err)
	}
	if recordedRuntime.ContainerID != facts.ContainerID || recordedRuntime.ResolvedImageID != facts.ResolvedImageID ||
		recordedRuntime.VerifiedLimits == nil || *recordedRuntime.VerifiedLimits != limits || !recordedRuntime.CleanupPending {
		t.Fatalf("recorded public runtime facts = %#v", recordedRuntime)
	}
	var recordedWorkspace ContainerWorkspace
	if err := json.Unmarshal(document["execution_workspace"], &recordedWorkspace); err != nil {
		t.Fatal(err)
	}
	if recordedWorkspace != *workspace {
		t.Fatalf("recorded public workspace = %#v", recordedWorkspace)
	}

	rendered := store.Render(loaded)
	if rendered.ExecutionWorkspace == nil || rendered.ExecutionWorkspace == loaded.ExecutionWorkspace {
		t.Fatal("the rendered workspace must be a value snapshot, not the record's pointer")
	}
	if rendered.ExecutionRuntime == nil || rendered.ExecutionRuntime == loaded.ExecutionRuntime ||
		rendered.ExecutionRuntime.VerifiedLimits == loaded.ExecutionRuntime.VerifiedLimits {
		t.Fatal("the rendered runtime facts must not alias the record")
	}
	rendered.ExecutionWorkspace.WorktreeDir = "/mutated"
	rendered.ExecutionRuntime.VerifiedLimits.MemoryBytes = 1
	if loaded.ExecutionWorkspace.WorktreeDir != workspace.WorktreeDir || loaded.ExecutionRuntime.VerifiedLimits.MemoryBytes != limits.MemoryBytes {
		t.Fatal("mutating the rendered result rewrote the durable record")
	}
}

func TestLegacyRecordJSONCarriesNoRuntimeOrWorkspaceFacts(t *testing.T) {
	t.Parallel()
	store := NewStore(t.TempDir())
	agentID, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Create(Record{
		AgentID: agentID, State: StateCompleted, ExecutionStatus: StatusCompleted,
		StartedAt: time.Unix(1700000000, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.RecordPath(agentID))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"execution", "execution_workspace", "execution_runtime"} {
		if _, ok := document[absent]; ok {
			t.Fatalf("legacy JSON must omit %q: %s", absent, raw)
		}
	}
}
