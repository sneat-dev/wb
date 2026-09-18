package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func TestWtLogCovSessionMoveReceiveSpec(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	digest := sessionmove.DigestBytes([]byte("digest"))
	spec, err := sessionMoveReceiveSpec(fixture.projectsRoot, SessionReceiveOptions{Request: fixture.request, RequestDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if spec.AuthorityID != fixture.request.HandoffID || spec.OperationID != fixture.request.HandoffID ||
		spec.MemberKey != "primary" || spec.AuthorityDigest != digest ||
		spec.PinBranch != "wb-session/"+fixture.request.HandoffID ||
		spec.RepositoryRemote != fixture.request.RepositoryRemote || spec.Branch != fixture.request.Branch ||
		spec.Commit != fixture.request.BundleCommit {
		t.Fatalf("spec = %#v", spec)
	}
	if spec.AuthorityStore != filepath.Join(fixture.home, sessionmove.DirName) {
		t.Fatalf("authority store = %q", spec.AuthorityStore)
	}
	invalid := fixture.request
	invalid.SchemaVersion = 0
	if _, err := sessionMoveReceiveSpec(fixture.projectsRoot, SessionReceiveOptions{Request: invalid}); err == nil {
		t.Fatal("invalid request was accepted")
	}
}

func TestWtLogCovValidateSessionReceiveSpec(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	digest := sessionmove.DigestBytes([]byte("digest"))
	spec, err := sessionMoveReceiveSpec(fixture.projectsRoot, SessionReceiveOptions{Request: fixture.request, RequestDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	// Path derivation admits an intentionally absent store/fence.
	spec.AuthorityStore = ""
	if err := validateSessionReceiveSpec(context.Background(), spec); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	for name, mutate := range map[string]func(*SessionReceiveSpec){
		"authority id":    func(s *SessionReceiveSpec) { s.AuthorityID = "" },
		"operation id":    func(s *SessionReceiveSpec) { s.OperationID = "../escape" },
		"member key":      func(s *SessionReceiveSpec) { s.MemberKey = "" },
		"remote":          func(s *SessionReceiveSpec) { s.RepositoryRemote = "not-a-remote" },
		"branch":          func(s *SessionReceiveSpec) { s.Branch = "bad branch name" },
		"pin branch":      func(s *SessionReceiveSpec) { s.PinBranch = "" },
		"commit":          func(s *SessionReceiveSpec) { s.Commit = "short" },
		"source commit":   func(s *SessionReceiveSpec) { s.SourceWorkCommit = "short" },
		"path no digest":  func(s *SessionReceiveSpec) { s.HandoverPath = "path.md"; s.HandoverDigest = "" },
		"store no fence":  func(s *SessionReceiveSpec) { s.AuthorityStore = "/tmp/store"; s.Fence = nil },
		"store not abs":   func(s *SessionReceiveSpec) { s.AuthorityStore = "relative" },
		"store not clean": func(s *SessionReceiveSpec) { s.AuthorityStore = "/tmp/store/../store" },
	} {
		mutated := spec
		mutate(&mutated)
		if err := validateSessionReceiveSpec(context.Background(), mutated); err == nil {
			t.Errorf("invalid session receive spec %q was accepted", name)
		}
	}
}

func TestWtLogCovSessionReceivePathHelpers(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	path, err := SessionReceiveWorktreePath(fixture.projectsRoot, fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join("session-"+fixture.request.HandoffID)) {
		t.Fatalf("worktree path = %q", path)
	}
	invalid := fixture.request
	invalid.SchemaVersion = 0
	if _, err := SessionReceiveWorktreePath(fixture.projectsRoot, invalid); err == nil {
		t.Fatal("invalid request produced a path")
	}

	digest := sessionmove.DigestBytes([]byte("digest"))
	spec, err := sessionMoveReceiveSpec(fixture.projectsRoot, SessionReceiveOptions{Request: fixture.request, RequestDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	// A fence-less spec with a store is repaired for read-only path derivation.
	path, err = SessionReceiveMemberPath(fixture.projectsRoot, spec)
	if err != nil {
		t.Fatalf("member path derivation failed: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("session-"+fixture.request.HandoffID)) {
		t.Fatalf("member path = %q", path)
	}
	invalidSpec := spec
	invalidSpec.Branch = "bad branch name"
	if _, err := SessionReceiveMemberPath(fixture.projectsRoot, invalidSpec); err == nil {
		t.Fatal("invalid spec produced a member path")
	}
}

func TestWtLogCovSessionReceivePhysicalCoordinates(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	canonical := mustOpenCanonical(t, fixture.physicalCanonical())
	spec := SessionReceiveSpec{OperationID: fixture.request.HandoffID, Commit: fixture.request.BundleCommit}
	placement, operationPath, owner, name, worktreePath, err := sessionReceivePhysicalCoordinates(context.Background(), fixture.projectsRoot, canonical, spec, "acme/app")
	if err != nil {
		t.Fatal(err)
	}
	if !placement.Local {
		t.Fatalf("default placement = %#v", placement)
	}
	if owner != "" || name != "session-"+fixture.request.HandoffID {
		t.Fatalf("local coordinates owner=%q name=%q", owner, name)
	}
	if operationPath != placement.Root || worktreePath != filepath.Join(operationPath, name) {
		t.Fatalf("local coordinates op=%q worktree=%q root=%q", operationPath, worktreePath, placement.Root)
	}
	if _, _, _, _, _, err := sessionReceivePhysicalCoordinates(context.Background(), fixture.projectsRoot, canonical, spec, "not-a-slug"); err == nil {
		t.Fatal("invalid repository slug was accepted")
	}
}

func TestWtLogCovReceivedSessionWorktreePath(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	canonical := mustOpenCanonical(t, fixture.physicalCanonical())
	spec := SessionReceiveSpec{OperationID: fixture.request.HandoffID, PinBranch: "wb-session/" + fixture.request.HandoffID}
	if _, _, err := receivedSessionWorktreePath(context.Background(), fixture.projectsRoot, canonical, spec, "acme/app"); err == nil || !strings.Contains(err.Error(), "pin branch") {
		t.Fatalf("unregistered pin error = %v", err)
	}

	localRoot := filepath.Join(fixture.physicalCanonical(), ".worktrees")
	registered := filepath.Join(localRoot, "session-"+spec.OperationID)
	if err := os.MkdirAll(filepath.Dir(registered), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", "-b", spec.PinBranch, registered, "HEAD")
	operationPath, worktreePath, err := receivedSessionWorktreePath(context.Background(), fixture.projectsRoot, canonical, spec, "acme/app")
	if err != nil {
		t.Fatalf("registered pin rejected: %v", err)
	}
	if operationPath != localRoot || worktreePath != registered {
		t.Fatalf("local registration = %q/%q", operationPath, worktreePath)
	}

	// A registration outside the deterministic repository hierarchy is refused.
	divergent := SessionReceiveSpec{OperationID: "other-op", PinBranch: "wb-session/other-op"}
	divergentPath := filepath.Join(t.TempDir(), "not-the-layout")
	gitTest(t, fixture.canonical, "worktree", "add", "-b", divergent.PinBranch, divergentPath, "HEAD")
	if _, _, err := receivedSessionWorktreePath(context.Background(), fixture.projectsRoot, canonical, divergent, "acme/app"); err == nil {
		t.Fatal("out-of-layout pin registration was accepted")
	}
	if _, _, err := receivedSessionWorktreePath(context.Background(), fixture.projectsRoot, canonical, divergent, "not-a-slug"); err == nil {
		t.Fatal("invalid repository slug was accepted")
	}
}

func TestWtLogCovExactInterruptedSessionStage(t *testing.T) {
	operationRoot := t.TempDir()
	stage := ".wb-stage-" + strings.Repeat("a", 32)
	if err := os.MkdirAll(filepath.Join(operationRoot, stage, "checkout"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got, ok := exactInterruptedSessionStage(operationRoot, filepath.Join(operationRoot, stage, "checkout")); !ok || got != stage {
		t.Fatalf("exact stage = %q/%t", got, ok)
	}
	for name, registered := range map[string]string{
		"wrong leaf":   filepath.Join(operationRoot, stage, "elsewhere"),
		"too shallow":  filepath.Join(operationRoot, stage),
		"too deep":     filepath.Join(operationRoot, stage, "checkout", "nested"),
		"bad stage":    filepath.Join(operationRoot, ".wb-stage-short", "checkout"),
		"outside root": filepath.Join(t.TempDir(), stage, "checkout"),
	} {
		if _, ok := exactInterruptedSessionStage(operationRoot, registered); ok {
			t.Errorf("non-stage path %q was accepted", name)
		}
	}
}

func TestWtLogCovInterruptedSessionStageNames(t *testing.T) {
	operationRoot := t.TempDir()
	stage := ".wb-stage-" + strings.Repeat("b", 32)
	if err := os.MkdirAll(filepath.Join(operationRoot, stage), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(operationRoot, "other-directory"), 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(operationRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	names, err := interruptedSessionStageNames(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != stage {
		t.Fatalf("stage names = %#v", names)
	}
	if err := requireOnlyInterruptedSessionStage(directory, stage); err != nil {
		t.Fatalf("single matching stage rejected: %v", err)
	}
	if err := requireOnlyInterruptedSessionStage(directory, ".wb-stage-"+strings.Repeat("c", 32)); err == nil {
		t.Fatal("mismatched wanted stage was accepted")
	}
	second := ".wb-stage-" + strings.Repeat("d", 32)
	if err := os.MkdirAll(filepath.Join(operationRoot, second), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := requireOnlyInterruptedSessionStage(directory, stage); err == nil {
		t.Fatal("ambiguous stages were accepted")
	}
}

func TestWtLogCovVerifyReceivedSessionMemberRefusals(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	digest := sessionmove.DigestBytes([]byte("digest"))
	spec, err := sessionMoveReceiveSpec(fixture.projectsRoot, SessionReceiveOptions{Request: fixture.request, RequestDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: spec}); err == nil || !strings.Contains(err.Error(), "requires exact admitted handoff authority") {
		t.Fatalf("fence-less replay error = %v", err)
	}
	invalidSpec := spec
	invalidSpec.Branch = "bad branch name"
	if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: fixture.projectsRoot, Spec: invalidSpec}); err == nil {
		t.Fatal("invalid spec replay was accepted")
	}
	if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: "relative", Spec: spec}); err == nil {
		t.Fatal("relative projects root was accepted")
	}
	if _, err := VerifyReceivedSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot: fixture.projectsRoot, Request: fixture.request, RequestDigest: digest,
	}); err == nil {
		t.Fatal("bundle replay without a fence was accepted")
	}
}
