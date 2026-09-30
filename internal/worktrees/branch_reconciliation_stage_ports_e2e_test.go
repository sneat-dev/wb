//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // the native Git fixture configures process-wide Git and WB environment.
func TestE2EBranchReconciliationStagePortsPreserveRecordedAuthority(t *testing.T) {
	fixture, created, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	options := reconcileOptions(fixture, created, liveBranch, head)
	options.Apply = true
	ctx := context.Background()
	marker := errors.New("stage action failed")
	reject := func(name string, ports reconciliationPorts, want string) {
		t.Helper()
		_, err := reconcileClaimBranchWithPorts(ctx, options, ports)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s error = %v, want %q", name, err, want)
		}
	}
	stageWrite := func(stage string) {
		t.Helper()
		stopped := options
		stopped.testStopAfterStage = stage
		if _, err := LogRecover(ctx, stopped); err == nil || !strings.Contains(err.Error(), "injected interruption") {
			t.Fatalf("stage %s stop = %v", stage, err)
		}
	}
	claimPorts := reconciliationClaimPorts()
	_, claim, err := claimPorts.ReadClaim(fixture.home, created.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	reject("record creation port", reconciliationPorts{createRecord: func(string, workLogClaim, branchReconciliationRecord) (*os.File, error) { return nil, marker }}, "stage action failed")
	bundleGit := func(ctx context.Context, canonical *canonicalRepository, args ...string) error {
		return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", args...)
	}
	reject("local bundle creation", reconciliationPorts{bundle: reconciliationBundlePorts{secureGit: func(ctx context.Context, canonical *canonicalRepository, args ...string) error {
		if len(args) > 1 && args[0] == "bundle" && args[1] == "create" {
			return marker
		}
		return bundleGit(ctx, canonical, args...)
	}}}, "stage action failed")
	reject("remote fetch", reconciliationPorts{bundle: reconciliationBundlePorts{secureGit: func(ctx context.Context, canonical *canonicalRepository, args ...string) error {
		if len(args) > 0 && args[0] == "fetch" {
			return marker
		}
		return bundleGit(ctx, canonical, args...)
	}}}, "stage action failed")
	reject("fetched head changed", reconciliationPorts{bundle: reconciliationBundlePorts{fetched: func(context.Context, *canonicalRepository, string) (string, error) {
		return strings.Repeat("f", 40), nil
	}}}, "fetched remote immutable-claim head changed")
	reject("remote bundle creation", reconciliationPorts{bundle: reconciliationBundlePorts{secureGit: func(ctx context.Context, canonical *canonicalRepository, args ...string) error {
		if len(args) > 3 && args[0] == "bundle" && args[1] == "create" && strings.Contains(args[3], "refs/remotes/origin/") {
			return marker
		}
		return bundleGit(ctx, canonical, args...)
	}}}, "stage action failed")

	// Creation writes the immutable request and verified bundles before any
	// remote effect. Each refusal below leaves that durable stage reusable.
	reject("remote effect", reconciliationPorts{retireRemote: func(context.Context, *canonicalRepository, branchReconciliationRecord) error { return marker }}, "stage action failed")
	dry := options
	dry.Apply = false
	if result, err := reconcileClaimBranchWithPorts(ctx, dry, reconciliationPorts{}); err != nil || result.Applied || !strings.Contains(strings.Join(result.Diagnosis, " "), reconciliationStageBundles) {
		t.Fatalf("recorded dry-run = %+v, %v", result, err)
	}
	different := options
	different.Actor = "another actor"
	if _, err := reconcileClaimBranchWithPorts(ctx, different, reconciliationPorts{}); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("changed request accepted: %v", err)
	}
	reject("recorded canonical open", reconciliationPorts{openCanonical: func(string) (*canonicalRepository, error) { return nil, marker }}, "stage action failed")
	reject("recorded canonical descriptor", reconciliationPorts{openCanonical: func(path string) (*canonicalRepository, error) {
		canonical, err := openCanonicalRepository(path)
		if err == nil {
			canonical.close()
		}
		return canonical, err
	}}, "")
	reject("recorded live branch", reconciliationPorts{lifecycle: func(ctx context.Context, projectsRoot, root string, claim workLogClaim) (ListResult, error) {
		entry, err := reconciliationLifecycleEvidence(ctx, projectsRoot, root, claim)
		entry.Branch = "codex/unrecorded"
		return entry, err
	}}, "neither recorded recovery branch")
	directory, err := claimPorts.OpenEvent(fixture.home, claim, options.EventID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	run, runPath, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Close()
	proofPath := filepath.Join(runPath, "branch-reconciliations", options.EventID, "local.json")
	missingPath := proofPath + ".held"
	proofMissing := func(stage string) {
		t.Helper()
		if err := os.Rename(proofPath, missingPath); err != nil {
			t.Fatal(err)
		}
		reject(stage+" bundle", reconciliationPorts{}, "read durable local reconciliation bundle evidence")
		if err := os.Rename(missingPath, proofPath); err != nil {
			t.Fatal(err)
		}
	}
	proofMissing("remote")
	reject("remote read", reconciliationPorts{remoteHead: func(context.Context, string, string) (string, error) { return "", marker }}, "stage action failed")
	reject("remote drift", reconciliationPorts{remoteHead: func(context.Context, string, string) (string, error) { return strings.Repeat("f", 40), nil }}, "moved from expected")
	lifecycleFail := func(context.Context, string, string, workLogClaim) (ListResult, error) { return ListResult{}, marker }
	for _, stage := range []string{"remote", "local", "rebind", "event"} {
		calls := 0
		reject(stage+" lifecycle", reconciliationPorts{lifecycle: func(ctx context.Context, projectsRoot, root string, claim workLogClaim) (ListResult, error) {
			calls++
			if calls == 2 {
				return lifecycleFail(ctx, projectsRoot, root, claim)
			}
			return reconciliationLifecycleEvidence(ctx, projectsRoot, root, claim)
		}}, "stage action failed")
		if calls != 2 {
			t.Fatalf("%s lifecycle calls = %d", stage, calls)
		}
		if stage == "remote" {
			calls = 0
			reject("changed branch during stage", reconciliationPorts{lifecycle: func(ctx context.Context, projectsRoot, root string, claim workLogClaim) (ListResult, error) {
				calls++
				entry, err := reconciliationLifecycleEvidence(ctx, projectsRoot, root, claim)
				if calls == 2 {
					entry.Branch = "codex/unrecorded"
				}
				return entry, err
			}}, "changed outside the recorded reconciliation")
		}
		switch stage {
		case "remote":
			stageWrite("remote")
			gitTest(t, fixture.canonical, "push", "origin", gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+created.Branch)+":refs/heads/"+created.Branch)
			reject("remote ref reappeared", reconciliationPorts{}, "reappeared")
			gitTest(t, fixture.canonical, "push", "origin", ":refs/heads/"+created.Branch)
			proofMissing("local")
			reject("local read", reconciliationPorts{localHead: func(context.Context, *canonicalRepository, string) (string, error) { return "", marker }}, "stage action failed")
			reject("local drift", reconciliationPorts{localHead: func(context.Context, *canonicalRepository, string) (string, error) {
				return strings.Repeat("f", 40), nil
			}}, "moved from expected")
			reject("local effect", reconciliationPorts{retireLocal: func(context.Context, *canonicalRepository, branchReconciliationRecord) error { return marker }}, "stage action failed")
		case "local":
			stageWrite("local")
			proofMissing("rebind")
			reject("live head guard", reconciliationPorts{requireHead: func(context.Context, *canonicalRepository, string, string) error { return marker }}, "stage action failed")
			reject("rebind read", reconciliationPorts{localHead: func(context.Context, *canonicalRepository, string) (string, error) { return "", marker }}, "stage action failed")
			reject("rebind drift", reconciliationPorts{localHead: func(context.Context, *canonicalRepository, string) (string, error) {
				return strings.Repeat("f", 40), nil
			}}, "reappeared")
			reject("rebind already moved without checkout", reconciliationPorts{localHead: func(context.Context, *canonicalRepository, string) (string, error) { return head, nil }}, "not checked out")
			reject("rebind effect", reconciliationPorts{rebind: func(context.Context, *canonicalRepository, branchReconciliationRecord) error { return marker }}, "stage action failed")
		case "rebind":
			stageWrite("rebound")
			reject("private projection corroboration", reconciliationPorts{corroborate: func(string, string, workLogProjection) error { return marker }}, "stage action failed")
			reject("event append", reconciliationPorts{appendEvent: func(string, LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
				return LocalWorkLogEvent{}, LocalWorkLogProjection{}, marker
			}}, "stage action failed")
		case "event":
			stageWrite("event")
			reject("event replay append", reconciliationPorts{appendEvent: func(string, LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
				return LocalWorkLogEvent{}, LocalWorkLogProjection{}, marker
			}}, "stage action failed")
		}
	}
	if result, err := LogRecover(ctx, options); err != nil || !result.Applied {
		t.Fatalf("complete = %+v, %v", result, err)
	}
	record, replay, err := claimPorts.ReadRecord(fixture.home, claim, options.EventID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = replay.Close() }()
	record.Stage = "unknown-future-stage"
	if err := claimPorts.WriteRecord(directory, record); err != nil {
		t.Fatal(err)
	}
	reject("unknown stage", reconciliationPorts{}, "unknown branch reconciliation stage")
}
