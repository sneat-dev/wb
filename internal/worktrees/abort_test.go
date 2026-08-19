package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAbortDiscardedResumesExactBranchAfterWorktreeRemoval(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "discard-resume-after-remove", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected abort crash after worktree removal")
	first, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "discard-resume-after-remove",
		Disposition: AbortDiscarded, DeleteRemote: true, Apply: true,
		afterAbortWorktreeRemoval: func(string) error { return injected },
	})
	if !errors.Is(err, injected) {
		t.Fatalf("abort interruption = %v, want %v", err, injected)
	}
	if len(first) != 1 || !first[0].WorktreeGone || first[0].BranchDeleted || first[0].BacklogID == "" {
		t.Fatalf("interrupted abort = %#v", first)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created[0].Branch); branchErr != nil || !exists {
		t.Fatalf("interrupted abort branch exists=%t err=%v", exists, branchErr)
	}

	resumed, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "discard-resume-after-remove",
		Disposition: AbortDiscarded, DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed) != 1 || !resumed[0].Applied || !resumed[0].WorktreeGone || !resumed[0].BranchDeleted || resumed[0].BacklogID == "" {
		t.Fatalf("resumed abort = %#v", resumed)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created[0].Branch); branchErr != nil || exists {
		t.Fatalf("resumed abort branch exists=%t err=%v", exists, branchErr)
	}
}

// TestAbortDiscardedUnusedWorktreesIsAudited covers the common storage-agent
// failure shape: two untouched worktrees were claimed but never started, so
// they cannot have merged PR evidence and must not become abandoned branches.
func TestAbortDiscardedUnusedWorktreesIsAudited(t *testing.T) {
	fixture := newGitFixture(t)
	otherCanonical := filepath.Join(fixture.projectsRoot, "acme", "storage")
	gitTest(t, fixture.projectsRoot, "clone", fixture.remote, otherCanonical)
	created, err := Create(context.Background(), []string{"acme/app", "acme/storage"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "unused-storage", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 2 {
		t.Fatalf("created = %#v", created)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "unused-storage", Disposition: AbortDiscarded,
		DeleteRemote: true, Apply: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("abort = %#v", results)
	}
	for _, result := range results {
		if !result.Applied || !result.WorktreeGone || !result.BranchDeleted {
			t.Fatalf("abort result = %#v", result)
		}
	}
	for _, create := range created {
		if _, err := os.Stat(create.WorktreeDir); !os.IsNotExist(err) {
			t.Fatalf("discarded worktree remains: %v", err)
		}
		canonical := fixture.canonical
		if create.Repository == "acme/storage" {
			canonical = otherCanonical
		}
		if exists, err := localBranchExists(context.Background(), canonical, create.Branch); err != nil || exists {
			t.Fatalf("discarded branch exists=%t err=%v", exists, err)
		}
	}
	terminal := filepath.Join(fixture.home, "worklogs", "unused-storage", "runs")
	entries, err := os.ReadDir(terminal)
	if err != nil || len(entries) != 1 {
		t.Fatalf("terminal archive directory = %#v err=%v", entries, err)
	}
	terminals, err := os.ReadDir(filepath.Join(terminal, entries[0].Name(), "terminals"))
	if err != nil || len(terminals) != 2 {
		t.Fatalf("sealed terminal cardinality = %d err=%v, want 2", len(terminals), err)
	}
	for _, terminal := range terminals {
		if !validClaimID(strings.TrimSuffix(terminal.Name(), ".json")) {
			t.Fatalf("invalid terminal claim name: %s", terminal.Name())
		}
	}
}

func TestAbortDiscardedRetiresExactRemoteBranchOnlyWithExplicitAuthorization(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "pushed-discard", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	gitTest(t, created[0].WorktreeDir, "push", "-u", "origin", created[0].Branch)

	_, err = Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "pushed-discard",
		Disposition:  AbortDiscarded,
		Apply:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "--remote") {
		t.Fatalf("discard without remote authorization error = %v", err)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("refused discard changed worktree: %v", statErr)
	}
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created[0].Branch); remoteErr != nil || remoteHead == "" {
		t.Fatalf("refused discard changed remote branch: head=%q err=%v", remoteHead, remoteErr)
	}

	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "pushed-discard",
		Disposition:  AbortDiscarded,
		DeleteRemote: true,
		Apply:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || !results[0].RemoteDeleted || !results[0].WorktreeGone || !results[0].BranchDeleted {
		t.Fatalf("discard result = %#v", results)
	}
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created[0].Branch); remoteErr != nil || remoteHead != "" {
		t.Fatalf("discard left remote branch at %q: %v", remoteHead, remoteErr)
	}
}

func TestAbortDiscardedRechecksDirtyStateAtRemovalBoundary(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "raced-discard", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	var writeErr error
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "raced-discard",
		Disposition:  AbortDiscarded,
		DeleteRemote: true,
		Apply:        true,
		beforeAbortRemoval: func(worktree string) {
			writeErr = os.WriteFile(filepath.Join(worktree, "README.md"), []byte("concurrent writer\n"), 0o644)
		},
	})
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if err == nil || !strings.Contains(err.Error(), "immediately before removal") {
		t.Fatalf("raced discard error = %v, results=%#v", err, results)
	}
	if _, statErr := os.Stat(created[0].WorktreeDir); statErr != nil {
		t.Fatalf("raced discard removed worktree: %v", statErr)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, created[0].Branch); branchErr != nil || !exists {
		t.Fatalf("raced discard removed branch: exists=%t err=%v", exists, branchErr)
	}
	after, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before || after.Lifecycle != "active" {
		t.Fatalf("raced discard changed active projection: before=%#v after=%#v", before, after)
	}
}

func TestAbortNotLandedSealsButRetainsResumableWorktree(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "resume-storage", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "resume-storage", Disposition: AbortNotLanded,
		Successor: "codex-resume-2", Apply: true,
		SuccessorIdentity: ClaimExecutionIdentity{Model: "gpt-5.6-terra", CLI: "opencode", Provider: "opencode-go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || results[0].WorktreeGone || results[0].BranchDeleted {
		t.Fatalf("abort = %#v", results)
	}
	if _, err := os.Stat(created[0].WorktreeDir); err != nil {
		t.Fatalf("resumable worktree missing: %v", err)
	}
	after, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Lifecycle != "active" || after.ClaimID == before.ClaimID {
		t.Fatalf("successor projection = %#v, prior = %#v", after, before)
	}
	runDir := filepath.Join(fixture.home, "worklogs", after.EffortID, "runs", after.RunID)
	claims, err := os.ReadDir(filepath.Join(runDir, "claims"))
	if err != nil || len(claims) != 2 {
		t.Fatalf("claim transfer cardinality = %d err=%v, want 2", len(claims), err)
	}
	claimBytes, err := os.ReadFile(filepath.Join(runDir, "claims", after.ClaimID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var successor workLogClaim
	if err := json.Unmarshal(claimBytes, &successor); err != nil {
		t.Fatal(err)
	}
	if successor.Version != 2 || successor.Model != "gpt-5.6-terra" || successor.ModelProvenance != modelProvenanceCallerDeclared ||
		successor.ModelDeclaredBy != "codex-resume-2" || successor.CLI != "opencode" || successor.Provider != "opencode-go" || successor.AgentRuntime != "" {
		t.Fatalf("successor execution identity = %#v", successor)
	}
	terminals, err := os.ReadDir(filepath.Join(runDir, "terminals"))
	if err != nil || len(terminals) != 1 {
		t.Fatalf("terminal transfer cardinality = %d err=%v, want 1", len(terminals), err)
	}
}

// TestAbortHandoffAutoChecksPointsDirtyStateForSuccessor proves the
// composition between abort and checkpoint: handoff/not_landed explicitly
// allow a dirty worktree, so the automatic checkpoint immediately before the
// claim transfer is the primary mechanism that preserves it for the
// successor, not a defense-in-depth backstop. The worktree itself must stay
// exactly as the outgoing agent left it — dirty and unpushed — because
// abort's whole point is to keep it resumable.
func TestAbortHandoffAutoChecksPointsDirtyStateForSuccessor(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "handoff-checkpoint", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if err := os.WriteFile(filepath.Join(worktree, "in-flight.go"), []byte("package main\n\nfunc broken( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	results, err := Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "handoff-checkpoint", Disposition: AbortHandoff,
		Successor: "codex-handoff-2", Apply: true,
		SuccessorIdentity: ClaimExecutionIdentity{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied {
		t.Fatalf("abort = %#v", results)
	}
	if results[0].CheckpointRef == "" || results[0].CheckpointError != "" {
		t.Fatalf("expected a recorded checkpoint, got %#v", results[0])
	}
	if !strings.HasPrefix(results[0].CheckpointRef, checkpointRefPrefix) {
		t.Fatalf("checkpoint ref %q outside dedicated namespace", results[0].CheckpointRef)
	}

	// The worktree itself must be untouched: still dirty, still holding the
	// exact in-flight file, ready for the successor to resume.
	status := gitTestOutput(t, worktree, "status", "--porcelain")
	if !strings.Contains(status, "in-flight.go") {
		t.Fatalf("handoff must not clean the worktree it is handing off: status=%q", status)
	}
	if _, err := os.Stat(filepath.Join(worktree, "in-flight.go")); err != nil {
		t.Fatalf("in-flight file missing after handoff: %v", err)
	}
}

func TestAbortHandoffCrashBindsSuccessorExecutionIdentity(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "handoff-crash-binding",
		WorkLog: WorkLogOptions{RunID: "handoff-crash-run", Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := os.ReadFile(created[0].WorkLogPath)
	if err != nil {
		t.Fatal(err)
	}
	var claim workLogClaim
	if err := json.Unmarshal(claimBytes, &claim); err != nil {
		t.Fatal(err)
	}
	head := gitTestOutput(t, created[0].WorktreeDir, "rev-parse", "HEAD")
	first := ClaimExecutionIdentity{Model: "gpt-5.6-terra", CLI: "opencode", Provider: "opencode-go"}
	drifted := ClaimExecutionIdentity{Model: "gpt-5.6-terra", CLI: "opencode", Provider: "openai-codex"}
	firstID := declaredSuccessorWorkLogClaimID(claim.ClaimID, "next-run", "handoff", first)
	driftedID := declaredSuccessorWorkLogClaimID(claim.ClaimID, "next-run", "handoff", drifted)
	if firstID == driftedID {
		t.Fatal("same model with different commercial routes produced one successor identity")
	}
	runDir, _, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeWorkLogTerminal(fixture.home, runDir, claim, head, "handoff", firstID, "next-run"); err != nil {
		_ = runDir.Close()
		t.Fatal(err)
	}
	_ = runDir.Close() // simulate a crash before successor claim publication

	err = transferWorkLogClaim(fixture.home, created[0].WorktreeDir, head, "handoff", "next-run", drifted)
	if err == nil || !strings.Contains(err.Error(), "immutable terminal conflicts") {
		t.Fatalf("identity drift after sealed crash = %v", err)
	}
	afterDrift, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil || afterDrift != projection {
		t.Fatalf("drifted retry changed projection: before=%#v after=%#v err=%v", projection, afterDrift, err)
	}
	claimsDir := filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims")
	if _, err := os.Stat(filepath.Join(claimsDir, driftedID+".json")); !os.IsNotExist(err) {
		t.Fatalf("drifted retry published a successor claim: %v", err)
	}
	if err := transferWorkLogClaim(fixture.home, created[0].WorktreeDir, head, "handoff", "next-run", first); err != nil {
		t.Fatalf("same-identity crash retry: %v", err)
	}
	afterRetry, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil || afterRetry.ClaimID != firstID || afterRetry.Lifecycle != "active" {
		t.Fatalf("same-identity retry projection = %#v err=%v", afterRetry, err)
	}
}

func TestAbortResumableRequiresSuccessorAndExplicitModel(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "needs-successor", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	before, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: "needs-successor", Disposition: AbortHandoff, Apply: true})
	if err == nil || !strings.Contains(err.Error(), "--successor") {
		t.Fatalf("missing successor error = %v", err)
	}
	_, err = Abort(context.Background(), AbortOptions{ProjectsRoot: fixture.projectsRoot, Task: "needs-successor", Disposition: AbortHandoff, Successor: "next-run", Apply: true})
	if err == nil || !strings.Contains(err.Error(), "--model is required") {
		t.Fatalf("missing successor model error = %v", err)
	}
	_, err = Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "needs-successor", Disposition: AbortHandoff,
		Successor: "next-run", Apply: true,
		SuccessorIdentity: ClaimExecutionIdentity{Model: "gpt-5.6-sol", Provider: "xoxb-synthetic-credential"},
	})
	if err == nil || !strings.Contains(err.Error(), "provider") {
		t.Fatalf("credential-shaped successor provider error = %v", err)
	}
	after, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("missing model changed claim projection: before=%#v after=%#v", before, after)
	}
	runDir := filepath.Join(fixture.home, "worklogs", before.EffortID, "runs", before.RunID)
	if terminals, readErr := os.ReadDir(filepath.Join(runDir, "terminals")); !os.IsNotExist(readErr) || len(terminals) != 0 {
		t.Fatalf("missing model published terminal records: entries=%#v err=%v", terminals, readErr)
	}
}
