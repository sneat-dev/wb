package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestSessionPartialCoverageBatchValueHelpers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)
	valid := session.Record{PID: 42, WBSessionID: "wbs-source", Machine: "laptop", Runtime: "codex", StartedAt: now}
	if err := validateSourceSession(valid); err != nil {
		t.Fatalf("valid source session rejected: %v", err)
	}
	for name, record := range map[string]session.Record{
		"identity": {},
		"metadata": {PID: 42, WBSessionID: "wbs-source"},
	} {
		if err := validateSourceSession(record); err == nil {
			t.Errorf("invalid source session %q was accepted", name)
		}
	}

	for name, testCase := range map[string]struct {
		handover SessionHandover
		want     string
	}{
		"summary":   {handover: SessionHandover{Summary: " explicit "}, want: "explicit"},
		"body line": {handover: SessionHandover{Body: []byte(" first line \nsecond line")}, want: "first line"},
		"body":      {handover: SessionHandover{Body: []byte(" body only ")}, want: "body only"},
		"default":   {handover: SessionHandover{}, want: "Session handoff offered"},
	} {
		if got := normalizedHandoverSummary(testCase.handover); got != testCase.want {
			t.Errorf("normalized summary %q = %q, want %q", name, got, testCase.want)
		}
	}

	root := t.TempDir()
	parent, name, err := sessionReceiveCanonicalParent(root, filepath.Join(root, "github.com", "acme", "app"))
	if err != nil || parent != "github.com/acme" || name != "app" {
		t.Fatalf("nested canonical parent = %q/%q/%v", parent, name, err)
	}
	parent, name, err = sessionReceiveCanonicalParent(root, filepath.Join(root, "app"))
	if err != nil || parent != "" || name != "app" {
		t.Fatalf("direct canonical parent = %q/%q/%v", parent, name, err)
	}
	if _, _, err := sessionReceiveCanonicalParent(root, filepath.Join(filepath.Dir(root), "outside")); err == nil {
		t.Fatal("canonical clone outside projects root was accepted")
	}

	if _, err := resolveParkedMemberCanonicalDir(root, "invalid"); err == nil {
		t.Fatal("invalid parked repository was accepted")
	}
	if _, err := resolveParkedMemberWorktreeDir(root, sessionpark.Worktree{
		WorktreeDir: filepath.Join(root, "missing"), WorkLogReference: "invalid",
	}); err == nil {
		t.Fatal("missing parked worktree with invalid Work Log reference was accepted")
	}
	if err := validateParkedLocalMember(context.Background(), root, sessionpark.Bundle{}, nil, parkedLocalMember{}); err == nil {
		t.Fatal("parked local member without custody evidence was accepted")
	}
	if err := validateParkedLocalMember(context.Background(), root, sessionpark.Bundle{}, nil, parkedLocalMember{
		member: sessionpark.Worktree{OwnerEventID: "owner", WorkLogReference: "worklog:effort/run/" + strings.Repeat("a", 64)},
	}); err == nil {
		t.Fatal("parked local member without retained descriptor was accepted")
	}
	if err := validateRemoteParkedMember(context.Background(), root, &parkedSessionCaptureMember{}); err == nil {
		t.Fatal("remote parked member without exact pushed evidence was accepted")
	}
	if _, err := VerifyReceivedSessionMember(context.Background(), SessionMemberReceiveOptions{ProjectsRoot: "relative"}); err == nil {
		t.Fatal("received session verification accepted a relative projects root")
	}
}

//nolint:paralleltest // the session receive fixture mutates WB home and XDG environment variables.
func TestSessionPartialCoverageBatchRemoteAndReceiveHelpers(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	canonical := mustOpenCanonical(t, fixture.physicalCanonical())
	preflight := &sessionCheckpointPreflight{canonical: canonical, repositoryRemote: fixture.remote}
	const head = "0123456789abcdef0123456789abcdef01234567"
	injected := errors.New("injected ls-remote failure")
	for name, testCase := range map[string]struct {
		output  string
		err     error
		want    string
		wantErr bool
	}{
		"failure":   {err: injected, wantErr: true},
		"missing":   {},
		"malformed": {output: "not-a-sha refs/heads/feature", wantErr: true},
		"present":   {output: head + "\trefs/heads/feature\n", want: head},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, _ func() ([]byte, error)) ([]byte, error) {
				if len(args) == 0 || args[0] != "ls-remote" {
					t.Fatalf("unexpected canonical Git arguments: %#v", args)
				}
				return []byte(testCase.output), testCase.err
			})
			got, err := sessionRemoteBranchTip(ctx, preflight, "feature")
			if (err != nil) != testCase.wantErr || got != testCase.want {
				t.Fatalf("remote branch tip = %q/%v, want %q/error=%t", got, err, testCase.want, testCase.wantErr)
			}
		})
	}

	missing := filepath.Join(t.TempDir(), "missing-worktree")
	if err := verifySessionReceiveReuse(context.Background(), canonical, filepath.Dir(missing), missing, "wb-session/missing", head); err == nil ||
		!strings.Contains(err.Error(), "open existing target linked worktree") {
		t.Fatalf("missing receive reuse error = %v", err)
	}
	ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, _ func() ([]byte, error)) ([]byte, error) {
		if len(args) == 0 || args[0] != "worktree" {
			t.Fatalf("unexpected canonical Git arguments: %#v", args)
		}
		return nil, injected
	})
	if _, _, err := receivedSessionWorktreePath(ctx, fixture.projectsRoot, canonical, SessionReceiveSpec{
		OperationID: fixture.request.HandoffID, PinBranch: "wb-session/" + fixture.request.HandoffID,
	}, "acme/app"); !errors.Is(err, injected) {
		t.Fatalf("received worktree inspection error = %v, want injected failure", err)
	}
}

//nolint:paralleltest // the session receive fixture mutates WB home and XDG environment variables.
func TestSessionPartialCoverageBatchSharedPlacementAndOrigin(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	canonical := mustOpenCanonical(t, fixture.physicalCanonical())
	sharedRoot := filepath.Join(fixture.root, "shared-worktrees")
	mustWriteBranchConfig(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wb", "worktrees.yaml"),
		"version: 1\nworktrees:\n  root: "+sharedRoot+"\n")
	spec := SessionReceiveSpec{OperationID: fixture.request.HandoffID, Commit: fixture.request.BundleCommit}
	placement, operationPath, parent, name, worktreePath, err := sessionReceivePhysicalCoordinates(
		context.Background(), fixture.projectsRoot, canonical, spec, "acme/app",
	)
	if err != nil {
		t.Fatal(err)
	}
	if placement.Local || operationPath != filepath.Join(sharedRoot, "session-"+spec.OperationID) ||
		parent != "acme" || name != "app" || worktreePath != filepath.Join(operationPath, "acme", "app") {
		t.Fatalf("shared receive coordinates = %#v %q %q %q %q", placement, operationPath, parent, name, worktreePath)
	}
	if _, _, _, _, _, err := sessionReceivePhysicalCoordinates(
		context.Background(), fixture.projectsRoot, canonical, spec, "acme/other",
	); err == nil || !strings.Contains(err.Error(), "does not match canonical clone") {
		t.Fatalf("mismatched shared repository error = %v", err)
	}

	canonicalDir, err := resolveParkedMemberCanonicalDir(fixture.projectsRoot, "acme/app")
	if err != nil || filepath.Clean(canonicalDir) != filepath.Clean(fixture.canonical) {
		t.Fatalf("resolved parked canonical = %q/%v, want %q", canonicalDir, err, fixture.canonical)
	}
	if reason := verifyParkedMemberOriginRemote(context.Background(), fixture.canonical, fixture.remote); reason != "" {
		t.Fatalf("matching parked origin rejected: %s", reason)
	}
	if reason := verifyParkedMemberOriginRemote(context.Background(), filepath.Join(fixture.root, "missing"), fixture.remote); !strings.Contains(reason, "cannot read origin remote") {
		t.Fatalf("missing parked origin reason = %q", reason)
	}
	otherRemote := filepath.Join(fixture.root, "remotes", "acme", "other.git")
	if reason := verifyParkedMemberOriginRemote(context.Background(), fixture.canonical, otherRemote); !strings.Contains(reason, "does not match") {
		t.Fatalf("mismatched parked origin reason = %q", reason)
	}
	if reason := verifyParkedMemberOriginRemote(context.Background(), fixture.canonical, "not-a-remote"); reason != "" {
		t.Fatalf("invalid recorded remote should be ignored, got %q", reason)
	}
}

func TestSessionPartialCoverageBatchInterruptedStageErrors(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := interruptedSessionStageNames(file); err == nil || !strings.Contains(err.Error(), "inspect interrupted receive stages") {
		t.Fatalf("ordinary-file stage inspection error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := interruptedSessionStageNames(file); err == nil || !strings.Contains(err.Error(), "rewind interrupted receive operation directory") {
		t.Fatalf("closed-directory stage rewind error = %v", err)
	}

	runDir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runDir.Close() })
	found, sealedAt, err := validateExistingExternalTerminal(
		runDir, workLogClaim{ClaimID: strings.Repeat("a", 64)}, sessionmove.Request{}, sessionmove.WorkLogReference{}, nil,
	)
	if err != nil || found || !sealedAt.IsZero() {
		t.Fatalf("missing external terminal = %t/%s/%v", found, sealedAt, err)
	}
}

//nolint:paralleltest // the custody fixtures mutate WB home and XDG environment variables.
func TestSessionPartialCoverageBatchCustodyValidation(t *testing.T) {
	source := newExternalSourceFixture(t)
	lock := source.lock(t)
	if _, err := EnsureExternalSourceOfferEvidence(ExternalSourceOfferOptions{
		Store: source.store, ExecutionLock: lock, ProjectsRoot: source.base.projectsRoot,
		Request: source.base.request, RequestDigest: source.digest, SourceSession: source.source,
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateExternalSourceOffer(source.worktree, source.base.request, source.digest); err != nil {
		t.Fatalf("valid external source offer rejected: %v", err)
	}
	if err := validateLiveExternalSourceOwner(source.worktree, source.source, source.base.request, source.digest, source.claim); err != nil {
		t.Fatalf("valid live external source owner rejected: %v", err)
	}

	targetFixture := newExternalTargetFixture(t)
	prepared, err := PrepareExternalSessionWorkLog(context.Background(), targetFixture.options)
	if err != nil {
		t.Fatal(err)
	}
	receipt := targetFixture.receipt(t, prepared)
	if err := validateExternalReceipt(targetFixture.base.request, targetFixture.digest, receipt); err != nil {
		t.Fatalf("valid external receipt rejected: %v", err)
	}
	claim, _, unlock, err := loadExternalTargetClaim(
		targetFixture.base.projectsRoot, targetFixture.base.request, targetFixture.digest, targetFixture.worktree,
	)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := validateExternalAttemptOwner(
		targetFixture.worktree, targetFixture.base.request, targetFixture.digest, claim,
		targetFixture.options.AttemptID, targetFixture.options.AttemptIndex,
		targetFixture.session.PID, targetFixture.session.StartedAt, true,
	); err != nil {
		t.Fatalf("valid external attempt owner rejected: %v", err)
	}
	if err := validateExternalHandoverPrompt(
		targetFixture.worktree, claim.RecordedAt, claim.AgentRuntime, targetFixture.session.Model,
		targetFixture.base.request.HandoverDigest, targetFixture.base.handover,
	); err != nil {
		t.Fatalf("valid external handover prompt rejected: %v", err)
	}
}
