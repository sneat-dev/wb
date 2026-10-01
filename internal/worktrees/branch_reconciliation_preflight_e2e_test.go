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
func TestE2EBranchReconciliationPreflightRefusesUnprovenAuthority(t *testing.T) {
	fixture, created, liveBranch, head, _, _ := prepareBranchReconciliationFixture(t)
	options := reconcileOptions(fixture, created, liveBranch, head)
	ctx := context.Background()
	marker := errors.New("operation port failure")
	reject := func(name string, request LogRecoverOptions, ports reconciliationPorts, want string) {
		t.Helper()
		_, err := reconcileClaimBranchWithPorts(ctx, request, ports)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s error = %v, want %q", name, err, want)
		}
	}
	request := options
	request.Worktree = filepath.Join(t.TempDir(), "missing")
	reject("missing worktree", request, reconciliationPorts{}, "")
	request = options
	fileRoot := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(fileRoot, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.ProjectsRoot = filepath.Join(fileRoot, "child")
	reject("invalid projects root", request, reconciliationPorts{}, "")
	request = options
	request.ProjectsRoot = t.TempDir()
	reject("unrelated WB home", request, reconciliationPorts{}, "")
	request = options
	request.ReconcileBranch = created.Branch
	reject("claim branch is live branch", request, reconciliationPorts{}, "different from immutable claim")
	request = options
	request.ReconcileBranch = "codex/another-live-branch"
	reject("wrong live branch", request, reconciliationPorts{}, "does not match --reconcile-branch")
	reject("lifecycle read", options, reconciliationPorts{lifecycle: func(context.Context, string, string, workLogClaim) (ListResult, error) { return ListResult{}, marker }}, "operation port failure")
	reject("canonical open", options, reconciliationPorts{openCanonical: func(string) (*canonicalRepository, error) { return nil, marker }}, "operation port failure")
	reject("canonical descriptor validation", options, reconciliationPorts{openCanonical: func(path string) (*canonicalRepository, error) {
		canonical, err := openCanonicalRepository(path)
		if err == nil {
			canonical.close()
		}
		return canonical, err
	}}, "")
	reject("remote observation", options, reconciliationPorts{remoteHead: func(context.Context, string, string) (string, error) { return "", marker }}, "resolve exact remote")
	reject("malformed remote observation", options, reconciliationPorts{remoteHead: func(context.Context, string, string) (string, error) { return "invalid", nil }}, "resolve exact remote")
	localHead := gitTestOutput(t, fixture.canonical, "rev-parse", "refs/heads/"+created.Branch)
	gitTest(t, fixture.canonical, "update-ref", "-d", "refs/heads/"+created.Branch, localHead)
	reject("missing exact local head", options, reconciliationPorts{}, "resolve exact local")
	gitTest(t, fixture.canonical, "update-ref", "refs/heads/"+created.Branch, localHead)

	_, claim, err := reconciliationClaimPorts().ReadClaim(fixture.home, created.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	run, runPath, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = run.Close()
	request = options
	request.Apply = true
	locksPath := filepath.Join(runPath, "locks", claim.ClaimID+".lock")
	if err := os.MkdirAll(filepath.Dir(locksPath), 0o700); err != nil {
		t.Fatal(err)
	}
	lockPresent := false
	if _, err := os.Lstat(locksPath); err == nil {
		lockPresent = true
		if err := os.Rename(locksPath, locksPath+".held"); err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.Mkdir(locksPath, 0o700); err != nil {
		t.Fatal(err)
	}
	reject("claim lock descriptor", request, reconciliationPorts{}, "open claim-lock file")
	if err := os.Remove(locksPath); err != nil {
		t.Fatal(err)
	}
	if lockPresent {
		if err := os.Rename(locksPath+".held", locksPath); err != nil {
			t.Fatal(err)
		}
	}

	request = options
	request.EventID = "corrupt-record"
	directory, err := reconciliationClaimPorts().OpenEvent(fixture.home, claim, request.EventID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBytesImmutableAt(directory, "record.json", []byte("not JSON"), 0o600, false); err != nil {
		t.Fatal(err)
	}
	_ = directory.Close()
	reject("corrupt private record", request, reconciliationPorts{}, "")

	request = options
	request.EventID = "blocked-record"
	path := filepath.Join(runPath, "branch-reconciliations", request.EventID)
	if err := os.WriteFile(path, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	request.Apply = true
	reject("record creation", request, reconciliationPorts{}, "")
}
