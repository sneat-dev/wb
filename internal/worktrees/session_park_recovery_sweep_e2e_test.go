//go:build e2e

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
	"github.com/sneat-dev/wb/internal/wbhome"
)

//nolint:paralleltest // the checkpoint fixture configures process-wide Git and agent environment
func TestE2EParkedReasonSelectsOnlyUnpickedExactBundleMembers(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-reason-sweep")
	branch := preparePushedParkedWorktree(t, fixture, worktree)
	guard, member := captureParkedWorktreeMember(t, fixture, worktree, source, branch)
	const id = "park-ffffffffffffffffffffffffffffffff"
	home, err := wbhome.Root(fixture.projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	storeRoot := filepath.Join(home, sessionpark.SourceDirName)
	if reason := ParkedSessionReason(fixture.projectsRoot, []string{worktree}); reason != "" {
		t.Fatalf("unparked checkout blocked: %q", reason)
	}
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: id,
		Source: source, Continuation: "private continuation", ParkedAt: time.Now().UTC(),
		Worktrees: []sessionpark.Worktree{member}}
	if _, err := sessionpark.NewStore(storeRoot).Create(bundle); err != nil {
		t.Fatal(err)
	}
	if reason := ParkedSessionReason(fixture.projectsRoot, []string{t.TempDir()}); reason != "" {
		t.Fatalf("unrelated checkout blocked: %q", reason)
	}
	if reason := ParkedSessionReason(fixture.projectsRoot, []string{guard.CanonicalDir}); !strings.Contains(reason, id) {
		t.Fatalf("canonical member reason = %q", reason)
	}
	if reason := ParkedSessionReason(fixture.projectsRoot, []string{worktree}); !strings.Contains(reason, id) {
		t.Fatalf("worktree member reason = %q", reason)
	}
	if err := os.WriteFile(filepath.Join(storeRoot, "ordinary-file"), []byte("noise"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(storeRoot, "park-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"), 0o700); err != nil {
		t.Fatal(err)
	}
	if reason := ParkedSessionReason(fixture.projectsRoot, []string{worktree}); !strings.Contains(reason, id) {
		t.Fatalf("unreadable and non-directory store entries obscured member: %q", reason)
	}
	if reason := ParkedSessionReason("relative-projects-root", []string{worktree}); reason != "" {
		t.Fatalf("invalid root reported parked authority: %q", reason)
	}
}

//nolint:paralleltest // the checkpoint fixture configures process-wide Git and agent environment
func TestE2EParkedRemoteRefusesChangedMemberBeforeDelivery(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-remote-sweep")
	branch := preparePushedParkedWorktree(t, fixture, worktree)
	_, member := captureParkedWorktreeMember(t, fixture, worktree, source, branch)
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion,
		ParkedSessionID: "park-99999999999999999999999999999999", Source: source,
		Continuation: "private continuation", ParkedAt: time.Now().UTC(),
		Worktrees: []sessionpark.Worktree{member}}
	called := false
	if err := WithParkedRemoteResumeCustody(context.Background(), fixture.projectsRoot, bundle, nil); err == nil {
		t.Fatal("nil delivery callback accepted")
	}
	mutated := bundle
	mutated.Worktrees = append([]sessionpark.Worktree(nil), bundle.Worktrees...)
	mutated.Worktrees[0].RemoteHead = strings.Repeat("a", 40)
	err := WithParkedRemoteResumeCustody(context.Background(), fixture.projectsRoot, mutated, func() error { called = true; return nil })
	if err == nil || called || !strings.Contains(err.Error(), "clean pushed member") {
		t.Fatalf("changed remote head = (%v, called=%t)", err, called)
	}
	mutated = bundle
	mutated.Worktrees = append([]sessionpark.Worktree(nil), bundle.Worktrees...)
	mutated.Worktrees[0].OwnerEventID = "older-owner"
	err = WithParkedRemoteResumeCustody(context.Background(), fixture.projectsRoot, mutated, func() error { called = true; return nil })
	if err == nil || called || !strings.Contains(err.Error(), "custody") {
		t.Fatalf("changed owner = (%v, called=%t)", err, called)
	}
	mutated = bundle
	mutated.Worktrees = append(append([]sessionpark.Worktree(nil), bundle.Worktrees...), bundle.Worktrees[0])
	err = WithParkedRemoteResumeCustody(context.Background(), fixture.projectsRoot, mutated, func() error { called = true; return nil })
	if err == nil || called || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate member = (%v, called=%t)", err, called)
	}
	fault := errors.New("delivery refused")
	err = WithParkedRemoteResumeCustody(context.Background(), fixture.projectsRoot, bundle, func() error { called = true; return fault })
	if !called || !errors.Is(err, fault) {
		t.Fatalf("valid delivery fault = (%v, called=%t)", err, called)
	}
	if err := os.WriteFile(filepath.Join(worktree, "untracked-after-park"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}
	called = false
	err = WithParkedRemoteResumeCustody(context.Background(), fixture.projectsRoot, bundle, func() error { called = true; return nil })
	if err == nil || called || !strings.Contains(err.Error(), "clean status changed") {
		t.Fatalf("dirty member = (%v, called=%t)", err, called)
	}
}

//nolint:paralleltest // the checkpoint fixture configures process-wide Git and agent environment
func TestE2EParkedLocalAcquisitionRejectsMissingAndReplacedJournal(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-local-sweep")
	branch := preparePushedParkedWorktree(t, fixture, worktree)
	_, member := captureParkedWorktreeMember(t, fixture, worktree, source, branch)
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion,
		ParkedSessionID: "park-88888888888888888888888888888888", Source: source,
		Continuation: "private continuation", ParkedAt: time.Now().UTC(),
		Worktrees: []sessionpark.Worktree{member}}
	bad := bundle
	bad.Worktrees = append([]sessionpark.Worktree(nil), bundle.Worktrees...)
	bad.Worktrees[0].Repository = "invalid"
	if err := WithParkedLocalResumeCustody(context.Background(), fixture.projectsRoot, bad, func(*ParkedLocalCustody) error {
		t.Fatal("callback reached with invalid repository")
		return nil
	}); err == nil || !strings.Contains(err.Error(), "owner/repository") {
		t.Fatalf("invalid repository = %v", err)
	}
	bad = bundle
	bad.Worktrees = append([]sessionpark.Worktree(nil), bundle.Worktrees...)
	bad.Worktrees[0].RepositoryRemote = "https://github.com/acme/other.git"
	if err := WithParkedLocalResumeCustody(context.Background(), fixture.projectsRoot, bad, func(*ParkedLocalCustody) error {
		t.Fatal("callback reached with changed origin")
		return nil
	}); err == nil || !strings.Contains(err.Error(), "identity changed since park") {
		t.Fatalf("changed origin = %v", err)
	}
	journal := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
	if err := os.Rename(journal, journal+".missing"); err != nil {
		t.Fatal(err)
	}
	if err := WithParkedLocalResumeCustody(context.Background(), fixture.projectsRoot, bundle, func(*ParkedLocalCustody) error {
		t.Fatal("callback reached without journal")
		return nil
	}); err == nil || !strings.Contains(err.Error(), "journal") {
		t.Fatalf("missing journal = %v", err)
	}
}

//nolint:paralleltest // the target fixture configures process-wide Git and agent environment
func TestE2EParkedTargetPublicationRetriesAfterOutboxOpenRefusal(t *testing.T) {
	fixture := newParkedTargetCompletionFixture(t, "park-target-sweep")
	successor := fixture.successor
	options := ParkedSessionWorkLogPrepareOptions{
		ProjectsRoot: fixture.base.projectsRoot, Request: fixture.request, RequestDigest: fixture.digest,
		Member: fixture.member, ReceivedAt: fixture.request.CreatedAt, WorktreeDir: fixture.worktree,
		PinnedCommit: fixture.member.Commit, AttemptID: successor.AttemptID, AttemptIndex: successor.AttemptIndex,
		Session: session.Record{PID: successor.PID, WBSessionID: successor.WBSessionID,
			PredecessorWBSessionID: successor.PredecessorWBSessionID, Machine: successor.TargetMachine,
			Runtime: successor.Runtime, Model: successor.Model, TmuxName: successor.TmuxName,
			HandoffID: successor.HandoffID, StartedAt: successor.StartedAt},
	}
	value, err := sessionpark.TargetWorkLogReference(options.Request, options.RequestDigest, options.Member)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := sessionmove.ParseWorkLogReference(value)
	if err != nil {
		t.Fatal(err)
	}
	outbox := filepath.Join(fixture.base.home, "worklogs", reference.EffortID, "outbox")
	if err := os.Rename(outbox, outbox+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outbox, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareParkedSessionWorkLog(context.Background(), options); err == nil || !strings.Contains(err.Error(), "outbox") {
		t.Fatalf("target publication did not report occupied outbox: %v", err)
	}
	if err := os.Remove(outbox); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(outbox+".moved", outbox); err != nil {
		t.Fatal(err)
	}
	replayed, err := PrepareParkedSessionWorkLog(context.Background(), options)
	if err != nil || !replayed.Replayed || replayed.WorkLogReference != value || replayed.ClaimID != reference.ClaimID {
		t.Fatalf("publication retry = %#v, %v", replayed, err)
	}
}
