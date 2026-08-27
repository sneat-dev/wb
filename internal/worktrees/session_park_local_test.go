package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionpark"
)

func TestAttachParkedLocalSuccessorRequiresExactLatestSourceOwner(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-local-exact-owner")
	useIdentityRemote(t, fixture, worktree)
	branch := gitTestOutput(t, worktree, "branch", "--show-current")
	gitTest(t, worktree, "push", "origin", branch)
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	member, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, ListResult{
		Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree,
		WorktreesRoot: guard.WorktreesRoot, Branch: branch,
	}, source)
	if err != nil {
		t.Fatal(err)
	}
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-local-owner",
		Source: source, Continuation: "private continuation", Worktrees: []sessionpark.Worktree{member}, ParkedAt: time.Now().UTC()}
	successor := session.Record{PID: os.Getpid(), WBSessionID: "wbs-local-successor", PredecessorWBSessionID: source.WBSessionID,
		Machine: source.Machine, Runtime: source.Runtime, Model: source.Model, StartedAt: time.Now().UTC()}
	options := ParkedLocalSuccessorOptions{ProjectsRoot: fixture.projectsRoot, Bundle: bundle, Successor: successor,
		AttemptID: "000001-11111111111111111111111111111111", AttemptIndex: 1}
	if err := AttachParkedLocalSuccessor(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	if err := AttachParkedLocalSuccessor(context.Background(), options); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	events, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	ownerCount := 0
	for _, event := range events {
		if event.Type == LocalEventOwner && event.Owner != nil && event.Owner.Agent == successor.Runtime+"/"+successor.WBSessionID {
			ownerCount++
		}
	}
	if ownerCount != 1 {
		t.Fatalf("successor owner events = %d, want one: %#v", ownerCount, events)
	}
}

func TestAttachParkedLocalSuccessorPreservesDirtyUnpushedBytes(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-local-dirty-unpushed")
	useIdentityRemote(t, fixture, worktree)
	branch := gitTestOutput(t, worktree, "branch", "--show-current")
	gitTest(t, worktree, "push", "origin", branch)
	if err := os.WriteFile(filepath.Join(worktree, "unpushed.txt"), []byte("committed locally\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "unpushed.txt")
	gitTest(t, worktree, "commit", "-m", "local unpushed commit")
	dirtyPath := filepath.Join(worktree, "dirty.txt")
	if err := os.WriteFile(dirtyPath, []byte("exact dirty bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	member, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, ListResult{
		Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree,
		WorktreesRoot: guard.WorktreesRoot, Branch: branch,
	}, source)
	if err != nil {
		t.Fatal(err)
	}
	if !member.Dirty || member.Head == member.RemoteHead {
		t.Fatalf("parked member did not capture dirty/unpushed evidence: %#v", member)
	}
	headBefore := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	statusBefore := gitTestOutput(t, worktree, "status", "--porcelain=v1", "--untracked-files=all")
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-local-dirty-unpushed",
		Source: source, Continuation: "private continuation", Worktrees: []sessionpark.Worktree{member}, ParkedAt: time.Now().UTC()}
	successor := session.Record{PID: os.Getpid(), WBSessionID: "wbs-local-dirty-successor", PredecessorWBSessionID: source.WBSessionID,
		Machine: source.Machine, Runtime: source.Runtime, Model: source.Model, StartedAt: time.Now().UTC()}
	if err := AttachParkedLocalSuccessor(context.Background(), ParkedLocalSuccessorOptions{ProjectsRoot: fixture.projectsRoot,
		Bundle: bundle, Successor: successor, AttemptID: "000001-33333333333333333333333333333333", AttemptIndex: 1}); err != nil {
		t.Fatal(err)
	}
	if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("local resume changed unpushed HEAD %s -> %s", headBefore, got)
	}
	if got := gitTestOutput(t, worktree, "status", "--porcelain=v1", "--untracked-files=all"); got != statusBefore {
		t.Fatalf("local resume changed dirty status %q -> %q", statusBefore, got)
	}
	if raw, err := os.ReadFile(dirtyPath); err != nil || string(raw) != "exact dirty bytes\n" {
		t.Fatalf("dirty bytes=%q err=%v", raw, err)
	}
}

func TestAttachParkedLocalSuccessorRefusesNewerCustodyWithoutAppending(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-local-newer-owner")
	useIdentityRemote(t, fixture, worktree)
	branch := gitTestOutput(t, worktree, "branch", "--show-current")
	gitTest(t, worktree, "push", "origin", branch)
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	member, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, ListResult{
		Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree,
		WorktreesRoot: guard.WorktreesRoot, Branch: branch,
	}, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordCustody(worktree, "", "newer sequential session", AgentIdentity{Runtime: "codex", AgentID: "newer", Model: "gpt-5", PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	before, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-local-conflict",
		Source: source, Continuation: "private continuation", Worktrees: []sessionpark.Worktree{member}, ParkedAt: time.Now().UTC()}
	successor := session.Record{PID: os.Getpid(), WBSessionID: "wbs-refused-successor", PredecessorWBSessionID: source.WBSessionID,
		Machine: source.Machine, Runtime: source.Runtime, Model: source.Model, StartedAt: time.Now().UTC()}
	err = AttachParkedLocalSuccessor(context.Background(), ParkedLocalSuccessorOptions{
		ProjectsRoot: fixture.projectsRoot, Bundle: bundle, Successor: successor,
		AttemptID: "000001-22222222222222222222222222222222", AttemptIndex: 1,
	})
	if err == nil {
		t.Fatal("newer-session custody was stolen")
	}
	after, readErr := readLocalEvents(worktree)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(after) != len(before) {
		t.Fatalf("refusal mutated journal: before=%d after=%d", len(before), len(after))
	}
}

func TestAttachParkedLocalSuccessorConcurrentCandidatesHaveOneOwner(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-local-concurrent-owner")
	useIdentityRemote(t, fixture, worktree)
	branch := gitTestOutput(t, worktree, "branch", "--show-current")
	gitTest(t, worktree, "push", "origin", branch)
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	member, err := CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, ListResult{
		Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree,
		WorktreesRoot: guard.WorktreesRoot, Branch: branch,
	}, source)
	if err != nil {
		t.Fatal(err)
	}
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: "park-local-race",
		Source: source, Continuation: "private continuation", Worktrees: []sessionpark.Worktree{member}, ParkedAt: time.Now().UTC()}
	candidates := []session.Record{
		{PID: os.Getpid(), WBSessionID: "wbs-local-race-a", PredecessorWBSessionID: source.WBSessionID,
			Machine: source.Machine, Runtime: source.Runtime, Model: source.Model, StartedAt: time.Now().UTC()},
		{PID: os.Getpid(), WBSessionID: "wbs-local-race-b", PredecessorWBSessionID: source.WBSessionID,
			Machine: source.Machine, Runtime: source.Runtime, Model: source.Model, StartedAt: time.Now().UTC()},
	}
	errs := make(chan error, len(candidates))
	var group sync.WaitGroup
	for index, successor := range candidates {
		group.Add(1)
		go func(index int, successor session.Record) {
			defer group.Done()
			errs <- AttachParkedLocalSuccessor(context.Background(), ParkedLocalSuccessorOptions{
				ProjectsRoot: fixture.projectsRoot, Bundle: bundle, Successor: successor,
				AttemptID: []string{"000001-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "000001-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}[index], AttemptIndex: 1,
			})
		}(index, successor)
	}
	group.Wait()
	close(errs)
	succeeded, refused := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else {
			refused++
		}
	}
	if succeeded != 1 || refused != 1 {
		t.Fatalf("concurrent candidates: succeeded=%d refused=%d", succeeded, refused)
	}
	events, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	owners := 0
	for _, event := range events {
		if event.Type == LocalEventOwner && event.Owner != nil &&
			(event.Owner.Agent == source.Runtime+"/wbs-local-race-a" || event.Owner.Agent == source.Runtime+"/wbs-local-race-b") {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("concurrent candidates published %d successor owners", owners)
	}
}

func TestCaptureParkedSessionWorktreeRejectsCredentialRemoteWithoutDisclosureOrMutation(t *testing.T) {
	fixture, worktree, source := newSessionCheckpointFixture(t, "park-capture-credential-remote")
	const secret = "parked-source-secret"
	gitTest(t, worktree, "remote", "set-url", "origin", "https://user:"+secret+"@example.invalid/acme/app.git")
	branch := gitTestOutput(t, worktree, "branch", "--show-current")
	headBefore := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	statusBefore := gitTestOutput(t, worktree, "status", "--porcelain=v1")
	eventsBefore, err := readLocalEvents(worktree)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := Guard(context.Background(), worktree, GuardOptions{ProjectsRoot: fixture.projectsRoot, Admission: AdmissionEnforce})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CaptureParkedSessionWorktree(context.Background(), fixture.projectsRoot, ListResult{
		Repository: "acme/app", CanonicalDir: guard.CanonicalDir, WorktreeDir: worktree,
		WorktreesRoot: guard.WorktreesRoot, Branch: branch,
	}, source)
	if err == nil || !strings.Contains(err.Error(), "origin fetch remote is unsafe") {
		t.Fatalf("credential-bearing source remote error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("credential-bearing source remote was disclosed: %v", err)
	}
	if got := gitTestOutput(t, worktree, "rev-parse", "HEAD"); got != headBefore {
		t.Fatalf("park refusal changed HEAD from %s to %s", headBefore, got)
	}
	if got := gitTestOutput(t, worktree, "status", "--porcelain=v1"); got != statusBefore {
		t.Fatalf("park refusal changed status from %q to %q", statusBefore, got)
	}
	eventsAfter, eventErr := readLocalEvents(worktree)
	if eventErr != nil {
		t.Fatal(eventErr)
	}
	if len(eventsAfter) != len(eventsBefore) {
		t.Fatalf("park refusal changed Work Log events from %d to %d", len(eventsBefore), len(eventsAfter))
	}
}

func useIdentityRemote(t *testing.T, fixture *gitFixture, worktree string) {
	t.Helper()
	remote := filepath.Join(filepath.Dir(fixture.remote), "acme", "app.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(fixture.remote), "clone", "--bare", fixture.remote, remote)
	gitTest(t, worktree, "remote", "set-url", "origin", remote)
}
