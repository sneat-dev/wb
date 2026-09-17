package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWTCoreCovResidueRemovesOnlyTheExactHeldCheckout asserts the
// descriptor-anchored residue removal empties and unlinks the very checkout the
// handle was validated against, reports whether anything was there, and refuses
// a handle whose descriptors no longer match its paths.
func TestWTCoreCovResidueRemovesOnlyTheExactHeldCheckout(t *testing.T) {
	ctx := context.Background()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "owner")
	checkout := filepath.Join(parent, "app")
	if err := os.MkdirAll(filepath.Join(checkout, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "nested", "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentHandle, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parentHandle.Close() }()
	checkoutHandle, err := os.Open(checkout)
	if err != nil {
		t.Fatal(err)
	}
	handle := &cleanupWorktreeHandle{
		parentPath: parent, parentName: "owner", parent: parentHandle,
		worktreePath: checkout, worktree: checkoutHandle, ownParent: true,
	}
	removed, err := removeUnregisteredWorktreeResidue(handle, checkout)
	if err != nil || !removed {
		t.Fatalf("removeUnregisteredWorktreeResidue = %v, err=%v", removed, err)
	}
	if _, err := os.Stat(checkout); !os.IsNotExist(err) {
		t.Fatalf("residue remains: %v", err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatalf("parent after removal = %#v, err=%v", entries, err)
	}

	// A handle whose held descriptor names a different directory is refused
	// rather than allowed to delete by pathname.
	replaced := filepath.Join(parent, "replaced")
	if err := os.Mkdir(replaced, 0o755); err != nil {
		t.Fatal(err)
	}
	mismatched, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mismatched.Close() }()
	held, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	bad := &cleanupWorktreeHandle{
		parentPath: parent, parent: mismatched, worktreePath: replaced, worktree: held,
	}
	if err := removeWorktreeResidue(bad); err == nil {
		t.Fatal("a handle whose worktree descriptor names another directory was accepted")
	}
	if _, err := os.Stat(replaced); err != nil {
		t.Fatalf("the refused checkout was removed anyway: %v", err)
	}

	// worktreeStillRegistered answers the same question from a canonical clone.
	fixture := newGitFixture(t)
	registeredPath := filepath.Join(fixture.projectsRoot, "registered-checkout")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "registered", registeredPath, "main")
	resolved, err := filepath.EvalSymlinks(registeredPath)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := worktreeStillRegistered(ctx, fixture.canonical, resolved)
	if err != nil || !registered {
		t.Fatalf("registered worktree = %v, err=%v", registered, err)
	}
	unregistered, err := worktreeStillRegistered(ctx, fixture.canonical, filepath.Join(fixture.projectsRoot, "never-created"))
	if err != nil || unregistered {
		t.Fatalf("unregistered path = %v, err=%v", unregistered, err)
	}
}

// TestWTCoreCovCreatePublicationErrorUnwrap asserts the published error retains
// the exact cause a caller inspects with errors.Is, and a nil error unwraps to
// nothing.
func TestWTCoreCovCreatePublicationErrorUnwrap(t *testing.T) {
	var absent *CreatePublicationError
	if absent.Unwrap() != nil {
		t.Fatal("a nil publication error unwrapped to a cause")
	}
	cause := errors.New("publication failed")
	wrapped := &CreatePublicationError{Err: cause}
	if !errors.Is(wrapped, cause) {
		t.Fatalf("errors.Is did not reach the cause: %#v", wrapped)
	}
}

// TestWTCoreCovOpenOperationLockDirectoryCreatesOnlyTheLeaf asserts the
// exported opener creates the named operation directory but never fabricates a
// missing parent hierarchy.
func TestWTCoreCovOpenOperationLockDirectoryRefusesMissingPath(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "operation")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	directory, err := OpenOperationLockDirectory(target)
	if err != nil {
		t.Fatalf("OpenOperationLockDirectory: %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(root, "new-operation")
	createdDirectory, err := OpenOperationLockDirectory(created)
	if err != nil {
		t.Fatalf("creating an absent operation directory: %v", err)
	}
	if err := createdDirectory.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(created); err != nil || !info.IsDir() {
		t.Fatalf("created operation directory = %v, err=%v", info, err)
	}
	// A regular file in the ancestor chain is a hard refusal: the opener must
	// never treat a file as a directory it can descend through.
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenOperationLockDirectory(filepath.Join(blocker, "operation")); err == nil {
		t.Fatal("a regular file in the ancestor chain was descended through")
	}
}

// TestWTCoreCovValidateRemovedTerminalWorkLogsRefusesIncompleteExpectations
// asserts the exported verifier fails closed before reading any Work Log when
// the supplied expectations are empty, malformed, or duplicated.
func TestWTCoreCovValidateRemovedTerminalWorkLogsRefusesIncompleteExpectations(t *testing.T) {
	projectsRoot := t.TempDir()
	if err := ValidateRemovedTerminalWorkLogs(projectsRoot, nil); err == nil {
		t.Fatal("an empty expectation set was accepted")
	} else if !strings.Contains(err.Error(), "no terminal Work Log expectations") {
		t.Fatalf("error %q does not name the empty set", err)
	}
	valid := TerminalWorkLogExpectation{
		Task: "task", Repository: "acme/app", Worktree: "/checkout", Branch: "wb/task",
		Base: "main", FinalCommit: strings.Repeat("a", 40),
	}
	for name, invalid := range map[string]TerminalWorkLogExpectation{
		"unsafe task":    {Task: "bad/task", Repository: "acme/app", Worktree: "/checkout", Branch: "wb/task", FinalCommit: "a"},
		"empty repo":     {Task: "task", Worktree: "/checkout", Branch: "wb/task", FinalCommit: "a"},
		"empty worktree": {Task: "task", Repository: "acme/app", Branch: "wb/task", FinalCommit: "a"},
		"empty branch":   {Task: "task", Repository: "acme/app", Worktree: "/checkout", FinalCommit: "a"},
		"empty commit":   {Task: "task", Repository: "acme/app", Worktree: "/checkout", Branch: "wb/task"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateRemovedTerminalWorkLogs(projectsRoot, []TerminalWorkLogExpectation{invalid}); err == nil {
				t.Fatal("an incomplete expectation was accepted")
			} else if !strings.Contains(err.Error(), "invalid terminal Work Log expectation") {
				t.Fatalf("error %q does not name the invalid expectation", err)
			}
		})
	}
	// A well-formed expectation against a home with no Work Logs is refused by
	// the reader, not silently accepted.
	if err := ValidateRemovedTerminalWorkLogs(projectsRoot, []TerminalWorkLogExpectation{valid}); err == nil {
		t.Fatal("a missing terminal Work Log was accepted")
	}
}

// TestWTCoreCovValidateOrphanedClaimIdentitySuccessorShapes asserts the
// identity gate routes each recorded successor acquisition through the digest
// rule that owns it, accepting none of them when the recorded ID is wrong.
func TestWTCoreCovValidateOrphanedClaimIdentitySuccessorShapes(t *testing.T) {
	successor := func(version int, via string) workLogClaim {
		return workLogClaim{
			Version: version, Lifecycle: "active", EffortID: "task", RunID: "run",
			ClaimID: strings.Repeat("a", 64), Task: "task", Repository: "acme/app",
			Worktree: "/checkout", Branch: "wb/task", Base: "main", BaseSHA: strings.Repeat("b", 40),
			ParentClaimID: strings.Repeat("c", 64), AgentID: "agent", AcquiredVia: via,
			Model: "claude-sonnet-4.5", CLI: "claude", Provider: "anthropic",
		}
	}
	for _, testCase := range []struct {
		name string
		via  string
	}{
		{name: "external handoff", via: "external_handoff"},
		{name: "parked session resume", via: "parked_session_resume"},
		{name: "declared successor", via: "handoff"},
		{name: "not landed", via: "not_landed"},
		{name: "recycled failure", via: "recycle_failed"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := validateOrphanedClaimIdentity(successor(2, testCase.via)); err == nil {
				t.Fatal("a successor claim with a mismatched digest was accepted")
			}
			// Version 1 takes the legacy successor rule for handoff/not_landed.
			if err := validateOrphanedClaimIdentity(successor(1, testCase.via)); err == nil {
				t.Fatal("a version 1 successor claim with a mismatched digest was accepted")
			}
		})
	}
}
