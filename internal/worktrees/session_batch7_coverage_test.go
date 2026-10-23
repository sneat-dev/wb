package worktrees

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/sessionpark"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
)

// TestBatch7BoundedRelativeRegularFailureBoundaries proves the descriptor
// validation failures that cannot be scheduled reliably with filesystem races.
//
//nolint:paralleltest // mutates the package-level readBoundedRelativeRegular Fstat and ReadAll seams.
func TestBatch7BoundedRelativeRegularFailureBoundaries(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "handover.md"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	originalFstat, originalReadAll := readBoundedRelativeRegularFstat, readBoundedRelativeRegularReadAll
	t.Cleanup(func() {
		readBoundedRelativeRegularFstat = originalFstat
		readBoundedRelativeRegularReadAll = originalReadAll
	})

	readBoundedRelativeRegularFstat = func(int, *unix.Stat_t) error { return errors.New("injected fstat failure") }
	if _, err := readBoundedRelativeRegular(root, "handover.md", 64); err == nil || !strings.Contains(err.Error(), "injected fstat failure") {
		t.Fatalf("fstat failure = %v", err)
	}
	readBoundedRelativeRegularFstat = originalFstat
	readBoundedRelativeRegularReadAll = func(io.Reader) ([]byte, error) { return nil, errors.New("injected read failure") }
	if _, err := readBoundedRelativeRegular(root, "handover.md", 64); err == nil || !strings.Contains(err.Error(), "injected read failure") {
		t.Fatalf("read failure = %v", err)
	}
	readBoundedRelativeRegularReadAll = func(io.Reader) ([]byte, error) { return []byte("changed"), nil }
	if _, err := readBoundedRelativeRegular(root, "handover.md", 64); err == nil || !strings.Contains(err.Error(), "changed while being read") {
		t.Fatalf("drift failure = %v", err)
	}
}

func TestBatch7ExternalHandoverPromptRejectsUnusableSequence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	prompts := filepath.Join(root, journalRootDirectory, journalLocalDirectory, promptsDirectory)
	if err := os.MkdirAll(prompts, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"0001-second.md": "---\nseq: 1\nsource: agent_observed\n---\n\nbody\n",
		"notes.md":       "not a prompt",
	} {
		if err := os.WriteFile(filepath.Join(prompts, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := validateExternalHandoverPrompt(root, time.Time{}, "", "", "", nil); err == nil {
		t.Fatal("non-contiguous one-prompt sequence was accepted")
	}
}

//nolint:paralleltest // newSessionReceiveFixture calls t.Setenv for WB home and XDG configuration.
func TestBatch7SessionReceiveCanonicalAndHeldRootBoundaries(t *testing.T) {
	if _, err := openSessionReceiveCanonicalFromHeldRoot(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "descriptor is unavailable") {
		t.Fatalf("nil held root error = %v", err)
	}
	fixture := newSessionReceiveFixture(t)
	canonical := mustOpenCanonical(t, fixture.canonical)
	defer canonical.close()
	declared, err := gitremote.Parse(fixture.remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifySessionReceiveCanonical(context.Background(), canonical, declared.Identity); err != nil {
		t.Fatalf("valid canonical rejected: %v", err)
	}
	ctx := withCanonicalGitInterceptor(context.Background(), func(_ context.Context, args []string, run func() ([]byte, error)) ([]byte, error) {
		if len(args) >= 1 && args[0] == "rev-parse" {
			return []byte(filepath.Join(fixture.root, "replacement")), nil
		}
		return run()
	})
	if err := verifySessionReceiveCanonical(ctx, canonical, declared.Identity); err == nil || !strings.Contains(err.Error(), "not the root") {
		t.Fatalf("canonical replacement error = %v", err)
	}
}

func TestBatch7RetireAndRemoteCustodyGuards(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if err := retireCompletedInterruptedSessionStage(context.Background(), root, directory); err != nil {
		t.Fatalf("empty interrupted stage retirement: %v", err)
	}
	for _, suffix := range []string{"a", "b"} {
		if err := os.Mkdir(filepath.Join(root, ".wb-stage-"+strings.Repeat(suffix, 32)), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := retireCompletedInterruptedSessionStage(context.Background(), root, directory); err == nil || !strings.Contains(err.Error(), "multiple active") {
		t.Fatalf("ambiguous stage retirement = %v", err)
	}
	if err := WithParkedRemoteResumeCustody(context.Background(), t.TempDir(), sessionpark.Bundle{}, nil); err == nil {
		t.Fatal("nil remote custody callback was accepted")
	}
	if err := WithParkedRemoteResumeCustody(context.Background(), t.TempDir(), sessionpark.Bundle{Worktrees: make([]sessionpark.Worktree, sessionpark.MaxMembers+1)}, func() error { return nil }); err == nil {
		t.Fatal("oversized remote custody bundle was accepted")
	}
}

//nolint:paralleltest // mutates the package-level invoked command recording.
func TestBatch7MicroValueCoverage(t *testing.T) {
	if got := AbortDisposition("  discard ").String(); got != "discard" {
		t.Fatalf("abort disposition = %q", got)
	}
	if !abortRepositoryExcludedByFilter("acme/app", "other/app", "/worktree") || abortRepositoryExcludedByFilter("acme/app", "acme/app", "/worktree") {
		t.Fatal("repository filter inversion is incorrect")
	}

	now := time.Date(2026, time.September, 29, 10, 11, 12, 13, time.FixedZone("offset", 3600))
	if got, want := DefaultBranchCleanupReportDir("/home/wb", now), "/home/wb/reports/branch-cleanup/20260929T091112.000000013Z"; got != want {
		t.Fatalf("branch cleanup report directory = %q, want %q", got, want)
	}
	if got, want := DefaultCleanupReportDir("/home/wb", now), "/home/wb/reports/worktree-cleanup/20260929T091112.000000013Z"; got != want {
		t.Fatalf("cleanup report directory = %q, want %q", got, want)
	}
	if !IsAncestorEffort("parent", "parent.child") || IsAncestorEffort("", "child") || IsAncestorEffort("parent", "parent") {
		t.Fatal("effort ancestry is incorrect")
	}

	for name, publication := range map[string]*CanonicalFreshness{
		"nil":       nil,
		"published": {Status: PublicationPublished},
		"other":     {Status: PublicationUnpublished},
	} {
		want := name == "published"
		if got := PublicationVerified(publication); got != want {
			t.Fatalf("publication %s verified=%t, want %t", name, got, want)
		}
	}

	SetInvokedCommand("worktree create")
	if got := InvokedCommand(); got != "worktree create" {
		t.Fatalf("invoked command = %q", got)
	}
	warning := UndeclaredOwnerWarning("/projects/acme/app")
	for _, want := range []string{"/projects/acme/app", "wb worktree own", EnvAgentPID, EnvAgentRuntime, EnvAgentModel, EnvAgentID} {
		if !strings.Contains(warning, want) {
			t.Fatalf("undeclared warning %q lacks %q", warning, want)
		}
	}

	mismatch := &RepositoryRenameMismatchError{Worktree: "/worktree", PathRepository: "acme/old", CanonicalRepository: "acme/new"}
	if got := mismatch.Error(); !strings.Contains(got, "acme/old") || !strings.Contains(got, "acme/new") {
		t.Fatalf("rename mismatch = %q", got)
	}
	if got := (&pullRequestHeadMismatchError{Message: "head moved"}).Error(); got != "head moved" {
		t.Fatalf("head mismatch = %q", got)
	}

	root := t.TempDir()
	if got, err := ExpectedRemoteURL(root, filepath.Join(root, "github.com", "acme", "app")); err != nil || got != "https://github.com/acme/app" {
		t.Fatalf("expected remote = %q, %v", got, err)
	}
	if _, err := ExpectedRemoteURL("relative", filepath.Join(root, "github.com", "acme", "app")); err == nil {
		t.Fatal("relative projects root was accepted for expected remote")
	}
}

func TestBatch7MicroFormattingAndLockCoverage(t *testing.T) {
	t.Parallel()

	wrapped := errors.New("publication interrupted")
	if got := (*CreatePublicationError)(nil).Error(); got != "worktree publication failed" {
		t.Fatalf("nil publication error = %q", got)
	}
	if got := (*CreatePublicationError)(nil).Unwrap(); got != nil {
		t.Fatalf("nil publication unwrap = %v", got)
	}
	publication := &CreatePublicationError{Err: wrapped, Outcomes: []CreateRecoveryOutcome{{
		Result: CreateResult{Repository: "acme/app", WorktreeDir: "/worktree", Branch: "feature"}, HeadSHA: "abcdef",
		CleanupBacklogID: "backlog", BacklogPersisted: true, RollbackCompleted: true, RecoveryError: "cleanup failed",
	}}}
	if got := publication.Error(); !strings.Contains(got, wrapped.Error()) || !strings.Contains(got, "recovery_error=\"cleanup failed\"") {
		t.Fatalf("publication error = %q", got)
	}
	if got := publication.Unwrap(); !errors.Is(got, wrapped) {
		t.Fatalf("publication unwrap = %v", got)
	}

	outcome := GCOutcome{Totals: map[string]int{"refused": 3}}
	if got := outcome.Refused(); got != 3 {
		t.Fatalf("refused total = %d", got)
	}
	for name, entry := range map[string]GCEntry{
		"kept":     {Task: "task", Repository: "acme/app", Branch: "feature", Class: "clean", Owner: "agent", AgeSeconds: 1},
		"eligible": {Task: "task", Repository: "acme/app", HeadSHA: "abcdef123456", Class: "clean", Eligible: true, Owner: "agent", AgeSeconds: 61},
		"applied":  {Task: "task", Repository: "acme/app", HeadSHA: "abcdef123456", Class: "clean", Applied: true, Owner: "agent", AgeSeconds: 3661},
	} {
		got := entry.String()
		if !strings.Contains(got, "task") || !strings.Contains(got, entry.Repository) {
			t.Fatalf("GC entry %s = %q", name, got)
		}
	}

	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if (&HeldOperationLock{}).ReclaimedInterrupted() || (&HeldOperationLock{}).Release() != nil {
		t.Fatal("empty held lock was not inert")
	}
	(&HeldOperationLock{}).Preserve()
	lock, err := AcquireOperationLock(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	if lock.ReclaimedInterrupted() {
		t.Fatal("new lock was marked interrupted")
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("repeated release = %v", err)
	}
	lock, err = AcquireOperationLock(directory, false)
	if err != nil {
		t.Fatal(err)
	}
	lock.Preserve()
}

func TestBatch7PublicationFindingCoverage(t *testing.T) {
	t.Parallel()

	if got := PublicationFinding(nil, "feature"); got != "" {
		t.Fatalf("nil publication finding = %q", got)
	}
	for name, publication := range map[string]CanonicalFreshness{
		"published":   {Status: PublicationPublished},
		"unpublished": {Status: PublicationUnpublished, LocalSHA: "abcdef123456", Ahead: 2, RemoteRef: "origin/feature"},
		"unborn":      {Status: PublicationUnborn, RemoteRef: "origin/feature"},
		"behind":      {Status: PublicationBehind, RemoteRef: "origin/feature", Behind: 3},
		"diverged":    {Status: PublicationDiverged, RemoteRef: "origin/feature", Ahead: 2, Behind: 3},
		"unknown":     {Status: "offline", RemoteRef: "origin/feature"},
		"error":       {Status: "offline", RemoteRef: "origin/feature", Error: "fetch failed"},
	} {
		got := PublicationFinding(&publication, "feature")
		if name == "published" {
			if got != "" {
				t.Fatalf("published finding = %q", got)
			}
			continue
		}
		if got == "" || !strings.Contains(got, "origin/feature") {
			t.Fatalf("publication finding %s = %q", name, got)
		}
	}
}
