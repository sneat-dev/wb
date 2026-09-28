package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func sessionReceiveStateSpec(fixture *sessionReceiveFixture) SessionReceiveSpec {
	request := fixture.request
	return SessionReceiveSpec{
		AuthorityID: request.HandoffID, OperationID: request.HandoffID, MemberKey: "primary",
		RepositoryRemote: request.RepositoryRemote, Branch: request.Branch, Commit: request.BundleCommit,
		PinBranch: "wb-session/" + request.HandoffID, SourceWorkCommit: request.SourceWorkCommit,
		HandoverPath: request.HandoverPath, HandoverDigest: request.HandoverDigest,
	}
}

//nolint:paralleltest // The Git fixture sets process-wide WB configuration and signing environment.
func TestSessionReceiveStateClosesCanonicalAfterSourceProofFailure(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	spec := sessionReceiveStateSpec(fixture)
	spec.HandoverDigest = sessionmove.DigestBytes([]byte("different handover"))
	state := &sessionReceiveState{}
	closed := false
	t.Cleanup(func() {
		if !closed {
			state.close()
		}
	})
	err := state.prepareSource(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}, nil)
	if err == nil || !strings.Contains(err.Error(), "handover digest mismatch") {
		t.Fatalf("source proof error = %v, want digest mismatch", err)
	}
	if state.canonical == nil || state.canonical.root == nil || state.operation.Directory != nil {
		t.Fatalf("source failure ownership = canonical %v, operation %v", state.canonical, state.operation.Directory)
	}
	canonicalRoot := state.canonical.root
	state.close()
	closed = true
	if _, err := canonicalRoot.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("canonical root after close: %v, want closed", err)
	}
	if _, err := os.Lstat(fixture.operationLockRoot()); !os.IsNotExist(err) {
		t.Fatalf("source failure created target operation: %v", err)
	}
}

//nolint:paralleltest // The Git fixture sets process-wide WB configuration and signing environment.
func TestSessionReceiveStateClosesOperationAfterLockFailure(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	state := &sessionReceiveState{}
	closed := false
	t.Cleanup(func() {
		if !closed {
			state.close()
		}
	})
	if err := state.prepareSource(context.Background(), SessionMemberReceiveOptions{
		ProjectsRoot: fixture.projectsRoot, Spec: sessionReceiveStateSpec(fixture),
	}, nil); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(fixture.operationLockRoot(), ".lock")
	if err := os.MkdirAll(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := state.prepareOperation(); err == nil {
		t.Fatal("expected obstructed operation lock to fail")
	}
	if state.operation.Directory == nil {
		t.Fatal("operation directory was not acquired before lock failure")
	}
	operationDirectory := state.operation.Directory
	canonicalRoot := state.canonical.root
	state.close()
	closed = true
	if _, err := operationDirectory.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("operation directory after lock failure close: %v, want closed", err)
	}
	if _, err := canonicalRoot.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("canonical root after lock failure close: %v, want closed", err)
	}
	if info, err := os.Stat(lockPath); err != nil || !info.IsDir() {
		t.Fatalf("obstructing lock path changed: info=%v err=%v", info, err)
	}
}

//nolint:paralleltest // The Git fixture sets process-wide WB configuration and signing environment.
func TestSessionReceiveStateReleasesTargetHandlesAndLock(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	state := &sessionReceiveState{}
	closed := false
	t.Cleanup(func() {
		if !closed {
			state.close()
		}
	})
	if err := state.prepareSource(context.Background(), SessionMemberReceiveOptions{
		ProjectsRoot: fixture.projectsRoot, Spec: sessionReceiveStateSpec(fixture),
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := state.prepareOperation(); err != nil {
		t.Fatal(err)
	}
	if _, done, err := state.reuseRegistered(); err != nil || done {
		t.Fatalf("registered reuse before placement: done=%t err=%v", done, err)
	}
	result, err := state.placeTarget()
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused || !sameSessionReceivePath(result.WorktreeDir, fixture.targetWorktree()) || string(result.HandoverBytes) != string(fixture.handover) {
		t.Fatalf("placed target = %#v", result)
	}
	if state.localRootDirectory == nil || state.publication == nil || state.publication.worktreeDirectory == nil {
		t.Fatal("local target did not acquire expected publication and root handles")
	}
	canonicalRoot, operationDirectory, lockFile := state.canonical.root, state.operation.Directory, state.lock.file
	localRoot, publishedWorktree := state.localRootDirectory, state.publication.worktreeDirectory
	state.close()
	closed = true
	for name, file := range map[string]*os.File{
		"canonical": canonicalRoot, "operation": operationDirectory, "lock": lockFile,
		"local root": localRoot, "publication": publishedWorktree,
	} {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Errorf("%s handle after close: %v, want closed", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(fixture.operationLockRoot(), ".lock")); !os.IsNotExist(err) {
		t.Fatalf("completed operation lock still named: %v", err)
	}
}
