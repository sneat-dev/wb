package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestSessionPartialClosureBatchPurePathPolicy(t *testing.T) {
	t.Parallel()
	injected := errors.New("injected relative path failure")
	if _, _, err := sessionReceiveCanonicalParentUsing(
		func(string, string) (string, error) { return "", injected }, "/projects", "/projects/acme/app",
	); !errors.Is(err, injected) {
		t.Fatalf("canonical parent relative-path error = %v", err)
	}
	if parent, name, err := sessionReceiveCanonicalParentUsing(
		func(string, string) (string, error) { return "acme/app", nil }, "/projects", "/projects/acme/app",
	); err != nil || parent != "acme" || name != "app" {
		t.Fatalf("canonical parent = %q/%q/%v", parent, name, err)
	}
	if _, _, err := sessionReceiveCanonicalParentUsing(
		func(string, string) (string, error) { return "../outside", nil }, "/projects", "/outside",
	); err == nil {
		t.Fatal("canonical parent accepted an escaping relative path")
	}
	if parent, name, err := sessionReceiveCanonicalParentUsing(
		func(string, string) (string, error) { return "app", nil }, "/projects", "/projects/app",
	); err != nil || parent != "" || name != "app" {
		t.Fatalf("direct canonical parent = %q/%q/%v", parent, name, err)
	}

	stage := ".wb-stage-" + strings.Repeat("a", 32)
	if _, ok := exactInterruptedSessionStageUsing(
		func(string, string) (string, error) { return "", injected }, "/operation", "/operation/stage/checkout",
	); ok {
		t.Fatal("relative-path failure produced an interrupted stage")
	}
	if _, ok := exactInterruptedSessionStageUsing(
		func(string, string) (string, error) { return "ordinary/checkout", nil }, "/operation", "/operation/ordinary/checkout",
	); ok {
		t.Fatal("ordinary directory was accepted as an interrupted stage")
	}
	if got, ok := exactInterruptedSessionStageUsing(
		func(string, string) (string, error) { return stage + "/checkout", nil }, "/operation", "/operation/"+stage+"/checkout",
	); !ok || got != stage {
		t.Fatalf("exact interrupted stage = %q/%t", got, ok)
	}

	spec := SessionReceiveSpec{OperationID: "handoff-123"}
	local := worktreePlacement{Root: "/worktrees", Local: true}
	gotPlacement, operation, parent, name, checkout, err := sessionReceiveCoordinatesForPlacement(local, spec, "acme/app")
	if err != nil || gotPlacement != local || operation != local.Root || parent != "" || name != "session-handoff-123" || checkout != filepath.Join(local.Root, name) {
		t.Fatalf("local receive coordinates = %#v %q %q %q %q %v", gotPlacement, operation, parent, name, checkout, err)
	}
	if _, _, _, _, _, err := sessionReceiveCoordinatesForPlacement(local, spec, "not-a-repository"); err == nil {
		t.Fatal("local receive coordinates accepted an invalid repository")
	}
	shared := worktreePlacement{Root: "/shared", Relative: "github.com/acme/app"}
	_, operation, parent, name, checkout, err = sessionReceiveCoordinatesForPlacement(shared, spec, "acme/app")
	if err != nil || operation != filepath.Join(shared.Root, "session-"+spec.OperationID) || parent != "github.com/acme" || name != "app" || checkout != filepath.Join(operation, "github.com", "acme", "app") {
		t.Fatalf("shared receive coordinates = %q %q %q %q %v", operation, parent, name, checkout, err)
	}
	invalidShared := shared
	invalidShared.Relative = "../escape"
	if _, _, _, _, _, err := sessionReceiveCoordinatesForPlacement(invalidShared, spec, "acme/app"); err == nil {
		t.Fatal("shared receive coordinates accepted an unsafe clone address")
	}
	mismatch := shared
	mismatch.Relative = "github.com/acme/other"
	if _, _, _, _, _, err := sessionReceiveCoordinatesForPlacement(mismatch, spec, "acme/app"); err == nil {
		t.Fatal("shared receive coordinates accepted a different repository")
	}
}

//nolint:paralleltest // session receive fixtures intentionally control WB home and XDG configuration.
func TestSessionPartialClosureBatchEntryPointErrors(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	unresolvableRoot := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink("loop", unresolvableRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := sessionMoveReceiveSpec(unresolvableRoot, SessionReceiveOptions{Request: fixture.request}); err == nil {
		t.Fatal("session move receive spec accepted an unresolvable projects root")
	}
	if _, err := SessionReceiveWorktreePath(unresolvableRoot, fixture.request); err == nil {
		t.Fatal("session receive path accepted an unresolvable projects root")
	}
	if _, err := plannedSessionReceiveWorktreePath(fixture.projectsRoot, "relative", SessionReceiveSpec{OperationID: "handoff"}, "acme/app"); err == nil {
		t.Fatal("planned session path accepted a relative canonical path")
	}

	invalid := fixture.request
	invalid.SchemaVersion = 0
	if _, err := VerifyReceivedSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: invalid}); err == nil {
		t.Fatal("received bundle verification accepted an invalid request")
	}
	if _, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: invalid}); err == nil {
		t.Fatal("bundle receive accepted an invalid request")
	}
	if _, err := VerifyReceivedSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: unresolvableRoot, Request: fixture.request}); err == nil {
		t.Fatal("received bundle verification accepted an unresolvable projects root")
	}
	if _, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: unresolvableRoot, Request: fixture.request}); err == nil {
		t.Fatal("bundle receive accepted an unresolvable projects root")
	}
	if _, err := ReceiveSessionMember(context.Background(), SessionMemberReceiveOptions{}); err == nil {
		t.Fatal("member receive accepted an empty specification")
	}
	if _, _, _, _, _, err := sessionReceivePhysicalCoordinates(
		context.Background(), fixture.projectsRoot, nil,
		SessionReceiveSpec{Commit: strings.Repeat("a", 40)}, "acme/app",
	); err == nil {
		t.Fatal("physical coordinate resolver accepted a missing canonical repository")
	}

	state := sessionReceiveState{projectsRoot: unresolvableRoot, spec: SessionReceiveSpec{OperationID: "bad-root"}}
	if err := state.prepareOperation(); err == nil {
		t.Fatal("receive operation accepted an unresolvable projects root")
	}
	blockedRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(blockedRoot, ".wb"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	state = sessionReceiveState{ctx: context.Background(), projectsRoot: blockedRoot, spec: SessionReceiveSpec{OperationID: "blocked-home"}}
	if err := state.prepareOperation(); err == nil {
		t.Fatal("receive operation was prepared through a non-directory WB home")
	}

	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := requireOnlyInterruptedSessionStage(directory, ".wb-stage-"+strings.Repeat("a", 32)); err == nil {
		t.Fatal("closed operation directory was accepted")
	}
}

func TestSessionPartialClosureBatchParkedResolutionErrors(t *testing.T) {
	t.Parallel()
	blocking := filepath.Join(t.TempDir(), "projects-file")
	if err := os.WriteFile(blocking, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveParkedMemberCanonicalDir(blocking, "acme/app"); err == nil {
		t.Fatal("canonical resolver ignored a projects-root read failure")
	}
	if parkedMemberOwnsWorktree(filepath.Join(t.TempDir(), "missing"), "worklog:effort/run/"+strings.Repeat("a", 64)) {
		t.Fatal("missing checkout was reported as owning a Work Log")
	}
	reference, err := sessionmove.ParseWorkLogReference("worklog:effort/run/" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkLogClaimByReference(t.TempDir(), reference); err == nil {
		t.Fatal("missing Work Log run returned a claim")
	}
	member := sessionpark.Worktree{WorktreeDir: filepath.Join(t.TempDir(), "missing"), WorkLogReference: reference.String()}
	unresolvableRoot := filepath.Join(t.TempDir(), "loop")
	if err := os.Symlink("loop", unresolvableRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveParkedMemberWorktreeDir(unresolvableRoot, member); err == nil {
		t.Fatal("parked worktree resolver accepted an unresolvable projects root")
	}
	if _, err := resolveParkedMemberWorktreeDir(t.TempDir(), member); err == nil || !strings.Contains(err.Error(), "no relocation receipt") {
		t.Fatalf("missing relocation receipt error = %v", err)
	}
	resolved := relocatedParkedMemberWorktree(
		[]string{"read-fails", "resolve-fails", "empty", "found"}, reference,
		func(home string, _ sessionmove.WorkLogReference) (workLogClaim, error) {
			if home == "read-fails" {
				return workLogClaim{}, errors.New("read failed")
			}
			return workLogClaim{Worktree: "/recorded/" + home}, nil
		},
		func(home string, _ workLogClaim) (workLogRelocationResolution, error) {
			switch home {
			case "resolve-fails":
				return workLogRelocationResolution{}, errors.New("resolve failed")
			case "empty":
				return workLogRelocationResolution{}, nil
			default:
				return workLogRelocationResolution{worktree: "/current/found"}, nil
			}
		},
	)
	if resolved != "/current/found" {
		t.Fatalf("relocated parked worktree = %q", resolved)
	}
	if resolved := relocatedParkedMemberWorktree(nil, reference,
		func(string, sessionmove.WorkLogReference) (workLogClaim, error) { return workLogClaim{}, nil },
		func(string, workLogClaim) (workLogRelocationResolution, error) {
			return workLogRelocationResolution{}, nil
		},
	); resolved != "" {
		t.Fatalf("empty relocation homes resolved to %q", resolved)
	}

	if err := withParkedLocalResumeCustody(context.Background(), t.TempDir(), sessionpark.Bundle{}, "", nil); err == nil {
		t.Fatal("local parked custody accepted a nil callback")
	}
	duplicatePath := filepath.Join(t.TempDir(), "same")
	duplicate := sessionpark.Bundle{Worktrees: []sessionpark.Worktree{{WorktreeDir: duplicatePath}, {WorktreeDir: duplicatePath}}}
	if err := withParkedLocalResumeCustody(context.Background(), t.TempDir(), duplicate, "", func(*ParkedLocalCustody) error { return nil }); err == nil {
		t.Fatal("local parked custody accepted duplicate member paths")
	}
	missing := sessionpark.Bundle{Worktrees: []sessionpark.Worktree{{WorktreeDir: filepath.Join(t.TempDir(), "missing"), Repository: "invalid"}}}
	if err := withParkedLocalResumeCustody(context.Background(), t.TempDir(), missing, "", func(*ParkedLocalCustody) error { return nil }); err == nil {
		t.Fatal("local parked custody accepted an unresolvable member")
	}
}

//nolint:paralleltest // Git fixtures and canonical Git interception are process-scoped test infrastructure.
func TestSessionPartialClosureBatchGitBoundaries(t *testing.T) {
	const repository = "/fixture/repository"
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"git", "-C", repository, "remote", "get-url", "origin"}, runner.Result{CombinedOutput: "://\n"}, nil)
	if reason := verifyParkedMemberOriginRemote(withGitRunner(context.Background(), fake), repository, "https://example.com/acme/app.git"); !strings.Contains(reason, "is unusable") {
		t.Fatalf("invalid current origin reason = %q", reason)
	}

	source := newExternalSourceFixture(t)
	if _, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
		Store: source.store, ExecutionLock: source.lock(t), ProjectsRoot: source.base.projectsRoot,
		Request: source.base.request, RequestDigest: source.digest, SourceSession: source.source,
	}); err != nil {
		t.Fatal(err)
	}
	if err := RecordCustody(source.worktree, "", "later owner", AgentIdentity{
		Runtime: "codex", AgentID: "later-owner", Model: "gpt-test", PID: os.Getpid(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateLiveExternalSourceOwner(source.worktree, source.source, source.base.request, source.digest, source.claim); err == nil || !strings.Contains(err.Error(), "not the current") {
		t.Fatalf("superseded source owner error = %v", err)
	}
}

//nolint:paralleltest // target custody and receive fixtures control shared process configuration.
func TestSessionPartialClosureBatchAttemptAndReuse(t *testing.T) {
	target := newExternalTargetFixture(t)
	if _, err := PrepareExternalSessionWorkLog(context.Background(), target.options); err != nil {
		t.Fatal(err)
	}
	claim, _, unlock, err := loadExternalTargetClaim(target.base.projectsRoot, target.base.request, target.digest, target.worktree)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := validateExternalAttemptOwner(
		target.worktree, target.base.request, target.digest, claim,
		target.options.AttemptID, target.options.AttemptIndex, target.session.PID, target.session.StartedAt, false,
	); err == nil || !strings.Contains(err.Error(), "not proven gone") {
		t.Fatalf("live PID accepted as an orphaned attempt: %v", err)
	}

	canonical := mustOpenCanonical(t, target.base.physicalCanonical())
	operationRoot := filepath.Dir(target.worktree)
	injected := errors.New("injected pin inspection failure")
	ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
		if len(args) > 0 && args[0] == "worktree" {
			return nil, injected
		}
		return next()
	})
	if err := verifySessionReceiveReuse(ctx, canonical, operationRoot, target.worktree,
		"wb-session/"+target.base.request.HandoffID, target.base.request.BundleCommit,
	); !errors.Is(err, injected) {
		t.Fatalf("pin inspection error = %v", err)
	}
	ctx = withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, next func() ([]byte, error)) ([]byte, error) {
		if len(args) > 0 && args[0] == "worktree" {
			return []byte("worktree /different/path\nbranch refs/heads/wb-session/" + target.base.request.HandoffID + "\n"), nil
		}
		return next()
	})
	if err := verifySessionReceiveReuse(ctx, canonical, operationRoot, target.worktree,
		"wb-session/"+target.base.request.HandoffID, target.base.request.BundleCommit,
	); err == nil || !strings.Contains(err.Error(), "not registered at exact deterministic worktree") {
		t.Fatalf("mismatched pin registration error = %v", err)
	}
}
