//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func assertSessionReceiveEvidenceUnchanged(t *testing.T, fixture *sessionReceiveFixture, checkout string) {
	t.Helper()
	if got := gitTestOutput(t, fixture.remote, "rev-parse", "refs/heads/"+fixture.request.Branch); got != fixture.request.BundleCommit {
		t.Fatalf("remote source ref = %s, want %s", got, fixture.request.BundleCommit)
	}
	if _, err := os.Lstat(filepath.Join(fixture.operationLockRoot(), ".lock")); !os.IsNotExist(err) {
		t.Fatalf("receive operation lock changed: %v", err)
	}
	if checkout == "" {
		fixture.requireNoTargetWorktree(t)
		return
	}
	if got := gitTestOutput(t, checkout, "rev-parse", "HEAD"); got != fixture.request.BundleCommit {
		t.Fatalf("received checkout HEAD = %s, want %s", got, fixture.request.BundleCommit)
	}
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide WB and Git environment.
func TestE2ESessionReceiveMemberPathPreservesRegisteredIdentity(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	spec := sessionReceiveStateSpec(fixture)
	ref := gitTestOutput(t, fixture.remote, "rev-parse", "refs/heads/"+fixture.request.Branch)
	invalid := spec
	invalid.AuthorityStore = filepath.Join(fixture.home, sessionmove.DirName)
	invalid.Branch = "bad branch name"
	if _, err := SessionReceiveMemberPath(fixture.projectsRoot, invalid); err == nil || !strings.Contains(err.Error(), "branch or pin is invalid") {
		t.Fatalf("invalid fence-less path spec = %v", err)
	}
	if _, err := SessionReceiveMemberPath("", spec); err == nil || !strings.Contains(err.Error(), "projects root is required") {
		t.Fatalf("missing projects root admitted: %v", err)
	}
	if got := gitTestOutput(t, fixture.remote, "rev-parse", "refs/heads/"+fixture.request.Branch); got != ref {
		t.Fatalf("path refusals changed remote ref: %s", got)
	}
	assertSessionReceiveEvidenceUnchanged(t, fixture, "")

	created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request})
	if err != nil {
		t.Fatal(err)
	}
	if path, err := SessionReceiveMemberPath(fixture.projectsRoot, spec); err != nil || path != created.WorktreeDir {
		t.Fatalf("registered path = %q, %v, want %q", path, err, created.WorktreeDir)
	}
	badPin := spec
	badPin.PinBranch = "wb-session/other-pin"
	if path, err := SessionReceiveMemberPath(fixture.projectsRoot, badPin); err != nil || path != created.WorktreeDir {
		t.Fatalf("unregistered pin fallback plan = %q, %v; want policy path %q", path, err, created.WorktreeDir)
	}
	assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide WB and Git environment.
func TestE2ESessionReceivePathRejectsWrongRegisteredTask(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	spec := sessionReceiveStateSpec(fixture)
	outside := filepath.Join(fixture.root, "other-task", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(outside), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", "--quiet", "-b", spec.PinBranch, outside, spec.Commit)
	if _, err := SessionReceiveMemberPath(fixture.projectsRoot, spec); err == nil || !strings.Contains(err.Error(), "outside its deterministic repository path") {
		t.Fatalf("wrong task's registered pin accepted: %v", err)
	}
	if got := gitTestOutput(t, outside, "rev-parse", "HEAD"); got != spec.Commit {
		t.Fatalf("wrong task checkout changed: %s", got)
	}
	assertSessionReceiveEvidenceUnchanged(t, fixture, "")
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide WB and Git environment.
func TestE2ESessionReceiveRegisteredPathRetainsOnDiskLegacySuffix(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	shared := filepath.Join(fixture.root, "shared")
	config := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wb", "worktrees.yaml")
	mustWriteBranchConfig(t, config, "version: 1\nworktrees:\n  root: "+shared+"\n")
	created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request})
	if err != nil {
		t.Fatal(err)
	}
	canonical := mustOpenCanonical(t, fixture.canonical)
	spec := sessionReceiveStateSpec(fixture)
	if _, _, err := receivedSessionWorktreePath(context.Background(), filepath.Join(fixture.root, "wrong-projects"), canonical, spec, "acme/app"); err == nil || !strings.Contains(err.Error(), "must be at <projects-root>") {
		t.Fatalf("registered path accepted a different projects root: %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", "https://github.com/acme/app.git")
	operation, path, err := receivedSessionWorktreePath(context.Background(), fixture.projectsRoot, canonical, spec, "acme/app")
	if err != nil || path != created.WorktreeDir || operation != filepath.Join(shared, "session-"+fixture.request.HandoffID) {
		t.Fatalf("legacy registered path after host-level origin = %q, %q, %v", operation, path, err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", fixture.remote)
	assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide WB and Git environment.
func TestE2EVerifyReceivedSessionMemberRequiresHeldExactEvidence(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request})
	if err != nil {
		t.Fatal(err)
	}
	fence, digest := acquireSessionReceiveFence(t, fixture, filepath.Join(fixture.home, sessionmove.DirName))
	spec := sessionReceiveStateSpec(fixture)
	spec.AuthorityStore = filepath.Join(fixture.home, sessionmove.DirName)
	spec.AuthorityDigest = digest
	spec.Fence = fence
	verified, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec})
	if err != nil || !verified.Reused || verified.WorktreeDir != created.WorktreeDir || string(verified.HandoverBytes) != string(fixture.handover) {
		t.Fatalf("exact held replay = %#v, %v", verified, err)
	}

	for _, tc := range []struct {
		name, want string
		change     func(*SessionReceiveSpec)
	}{
		{"wrong pin", "pin branch", func(s *SessionReceiveSpec) { s.PinBranch = "wb-session/not-registered" }},
		{"missing handover blob", "read accepted handover blob locally", func(s *SessionReceiveSpec) { s.HandoverPath = ".wb/handoffs/missing.md" }},
		{"wrong handover digest", "accepted handover blob digest changed", func(s *SessionReceiveSpec) { s.HandoverDigest = sessionmove.DigestBytes([]byte("different")) }},
	} {
		//nolint:paralleltest // the cases share one published checkout and held execution fence.
		t.Run(tc.name, func(t *testing.T) {
			changed := spec
			tc.change(&changed)
			if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: changed}); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("replay refusal = %v, want %q", err, tc.want)
			}
			assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)
		})
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", "https://github.com/other/app.git")
	if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}); err == nil || !strings.Contains(err.Error(), "logical identity does not match") {
		t.Fatalf("changed canonical origin admitted: %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", fixture.remote)
	assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)

	dirtyPath := filepath.Join(created.WorktreeDir, "untracked-replay.txt")
	if err := os.WriteFile(dirtyPath, []byte("must survive refusal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}); err == nil || !strings.Contains(err.Error(), "existing target worktree is dirty") {
		t.Fatalf("dirty exact replay admitted: %v", err)
	}
	if raw, err := os.ReadFile(dirtyPath); err != nil || string(raw) != "must survive refusal\n" {
		t.Fatalf("replay changed dirty checkout evidence: %q, %v", raw, err)
	}
	assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide WB and Git environment.
func TestE2ESessionReceiveCanonicalAndHeldCheckoutRefusals(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request})
	if err != nil {
		t.Fatal(err)
	}
	canonical := mustOpenCanonical(t, fixture.canonical)
	identity, err := sessionReceiveRepositoryFromRemote(fixture.remote)
	if err != nil || identity != "acme/app" {
		t.Fatalf("fixture remote identity = %q, %v", identity, err)
	}
	// A missing origin is a canonical-identity refusal; the registered checkout
	// and remote source ref must remain intact while origin is restored.
	gitTest(t, fixture.canonical, "remote", "remove", "origin")
	declared, err := gitremote.Parse(fixture.remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySessionReceiveCanonical(context.Background(), canonical, declared.Identity); err == nil || !strings.Contains(err.Error(), "read canonical origin") {
		t.Fatalf("missing canonical origin = %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "add", "origin", fixture.remote)
	assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", "https://github.com/other/app.git")
	if err := verifySessionReceiveCanonical(context.Background(), canonical, declared.Identity); err == nil || !strings.Contains(err.Error(), "logical identity does not match") {
		t.Fatalf("changed canonical identity = %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", fixture.remote)
	assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)

	operationRoot := fixture.physicalWorktreesRoot()
	handle, err := openAdoptedCleanupWorktree(created.WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.close()
	gitTest(t, created.WorktreeDir, "checkout", "--detach", fixture.request.BundleCommit)
	if err := verifyHeldSessionReceiveCheckout(context.Background(), fixture.physicalCanonical(), operationRoot, created.WorktreeDir,
		handle.worktree, "wb-session/"+fixture.request.HandoffID, fixture.request.BundleCommit); err == nil || !strings.Contains(err.Error(), "not attached to exact pin branch") {
		t.Fatalf("detached target refusal = %v", err)
	}
	if got := gitTestOutput(t, created.WorktreeDir, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
		t.Fatalf("refusal reattached detached target: %q", got)
	}
	assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)
	gitTest(t, created.WorktreeDir, "checkout", "wb-session/"+fixture.request.HandoffID)
	pinRef := "refs/heads/wb-session/" + fixture.request.HandoffID
	gitTest(t, fixture.canonical, "update-ref", pinRef, fixture.request.SourceWorkCommit)
	if err := verifyHeldSessionReceiveCheckout(context.Background(), fixture.physicalCanonical(), operationRoot, created.WorktreeDir,
		handle.worktree, "wb-session/"+fixture.request.HandoffID, fixture.request.BundleCommit); err == nil || !strings.Contains(err.Error(), "does not identify exact bundle commit") {
		t.Fatalf("changed exact pin tip admitted: %v", err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", pinRef); got != fixture.request.SourceWorkCommit {
		t.Fatalf("refusal changed unexpected pin tip: %s", got)
	}
	gitTest(t, fixture.canonical, "update-ref", pinRef, fixture.request.BundleCommit)
	assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)
	if err := os.WriteFile(filepath.Join(created.WorktreeDir, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyHeldSessionReceiveCheckout(context.Background(), fixture.physicalCanonical(), operationRoot, created.WorktreeDir,
		handle.worktree, "wb-session/"+fixture.request.HandoffID, fixture.request.BundleCommit); err == nil || !strings.Contains(err.Error(), "worktree is dirty") {
		t.Fatalf("dirty target refusal = %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(created.WorktreeDir, "untracked.txt")); err != nil || string(raw) != "dirty\n" {
		t.Fatalf("held-checkout refusal changed untracked bytes: %q, %v", raw, err)
	}
	assertSessionReceiveEvidenceUnchanged(t, fixture, created.WorktreeDir)
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide WB and Git environment.
func TestE2ESessionReceiveCanonicalRejectsUnreadableGitEvidence(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	canonical := mustOpenCanonical(t, fixture.canonical)
	declared, err := gitremote.Parse(fixture.remote)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, arg string
	}{
		{"root query", "--show-toplevel"},
		{"Git directory query", "--absolute-git-dir"},
	} {
		//nolint:paralleltest // each case uses the same descriptor-authorized canonical clone.
		t.Run(tc.name, func(t *testing.T) {
			seen := false
			ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
				if len(args) == 2 && args[0] == "rev-parse" && args[1] == tc.arg {
					seen = true
					return nil, os.ErrPermission
				}
				return run()
			})
			if err := verifySessionReceiveCanonical(ctx, canonical, declared.Identity); !seen || !errors.Is(err, os.ErrPermission) {
				t.Fatalf("%s failure = %v; query reached = %t", tc.name, err, seen)
			}
			assertSessionReceiveEvidenceUnchanged(t, fixture, "")
		})
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", "not-a-remote")
	if err := verifySessionReceiveCanonical(context.Background(), canonical, declared.Identity); err == nil || !strings.Contains(err.Error(), "logical identity") {
		t.Fatalf("malformed origin admitted: %v", err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", fixture.remote)
	assertSessionReceiveEvidenceUnchanged(t, fixture, "")
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide WB and Git environment.
func TestE2ESessionReceiveSourceAdmissionRefusesBeforeTargetMutation(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	spec := sessionReceiveStateSpec(fixture)
	state := &sessionReceiveState{}
	defer state.close()
	if err := state.prepareSource(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: "", Spec: spec}, nil); err == nil || !strings.Contains(err.Error(), "projects root is required") {
		t.Fatalf("missing target projects root admitted: %v", err)
	}
	assertSessionReceiveEvidenceUnchanged(t, fixture, "")

	// A canonical path replaced by a symlink may not be opened or cloned.
	outside := filepath.Join(fixture.root, "outside")
	if err := os.Rename(fixture.canonical, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, fixture.canonical); err != nil {
		t.Fatal(err)
	}
	state = &sessionReceiveState{}
	defer state.close()
	if err := state.prepareSource(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}, nil); err == nil || !strings.Contains(err.Error(), "inspect canonical clone for acme/app") {
		t.Fatalf("symlinked canonical source admission = %v", err)
	}
	if _, err := SessionReceiveMemberPath(fixture.projectsRoot, spec); err == nil {
		t.Fatal("path helper accepted symlinked canonical clone")
	}
	if info, err := os.Lstat(fixture.canonical); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("source refusal changed canonical symlink: %v, %v", info, err)
	}
	if got := gitTestOutput(t, outside, "rev-parse", "HEAD"); got != fixture.request.SourceWorkCommit {
		t.Fatalf("source refusal changed canonical checkout HEAD: %s", got)
	}
	assertSessionReceiveEvidenceUnchanged(t, fixture, "")
}

//nolint:paralleltest // newSessionReceiveFixture sets process-wide WB and Git environment.
func TestE2EVerifyReceivedSessionMemberRefusesInvalidCanonicalBeforeReplay(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	fence, digest := acquireSessionReceiveFence(t, fixture, filepath.Join(fixture.home, sessionmove.DirName))
	spec := sessionReceiveStateSpec(fixture)
	spec.AuthorityStore = filepath.Join(fixture.home, sessionmove.DirName)
	spec.AuthorityDigest = digest
	spec.Fence = fence
	if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: "", Spec: spec}); err == nil || !strings.Contains(err.Error(), "projects root is required") {
		t.Fatalf("missing projects root = %v", err)
	}
	outside := filepath.Join(fixture.root, "original-canonical")
	if err := os.Rename(fixture.canonical, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.canonical, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}); err == nil || !strings.Contains(err.Error(), "open canonical Git directory") {
		t.Fatalf("non-Git canonical root admitted for local replay: %v", err)
	}
	if err := os.Remove(fixture.canonical); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, fixture.canonical); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}); err == nil || !strings.Contains(err.Error(), "open accepted canonical repository") {
		t.Fatalf("symlinked canonical root admitted for local replay: %v", err)
	}
	if got := gitTestOutput(t, outside, "rev-parse", "HEAD"); got != fixture.request.SourceWorkCommit {
		t.Fatalf("canonical source checkout changed: %s", got)
	}
	assertSessionReceiveEvidenceUnchanged(t, fixture, "")
}
