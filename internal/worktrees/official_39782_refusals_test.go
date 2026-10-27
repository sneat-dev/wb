//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/wbhome"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // native Git fixture changes process-wide isolated environment.
func TestE2EOfficialClaimProjectionCorruptionRefusesEveryRelocationAdmission(t *testing.T) {
	f := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: f.projectsRoot, Operation: "projection-refusal", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	path := created[0].WorktreeDir
	projection, err := readWorkLogProjectionForReadOnlyClaim(path)
	if err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(path, workLogProjectionDirectory, workLogProjectionName)
	original, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(projectionPath, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	homes := wbhome.Resolution{Write: wbhome.Layout{Home: f.home}}
	if _, _, _, err = claimForRelocationAcrossHomes(homes, path); err == nil || errors.Is(err, errRelocationClaimAbsent) {
		t.Fatalf("corruption treated as absence: %v", err)
	}
	if err = corroborateRecordedRepositoryTransfer(homes, path); err == nil {
		t.Fatal("corrupt projection accepted by transfer guard")
	}
	if planned, e := preflightAbortWorkLog(f.home, AbortOptions{}, ListResult{WorktreeDir: path}); e == nil || planned {
		t.Fatalf("corrupt projection permits abort: %v %v", planned, e)
	}
	if err = os.WriteFile(projectionPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = locateGuardedAdoptedWorktree(context.Background(), t.TempDir(), wbhome.Layout{Home: f.home}, path); err == nil {
		t.Fatal("managed active claim substituted for adopted registration")
	}
	claimPath := filepath.Join(f.home, "worklogs", projection.EffortID, "runs", projection.RunID, "claims", projection.ClaimID+".json")
	raw, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	var claim workLogClaim
	if err = json.Unmarshal(raw, &claim); err != nil {
		t.Fatal(err)
	}
	claim.Branch = "different-valid-branch"
	claim.ClaimID, err = expectedWorkLogClaimID(claim)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(claimPath, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, found, e := projectedClaimInHome(f.home, projection); e == nil || found || !strings.Contains(e.Error(), "claim ID does not match") {
		t.Fatalf("misbound valid claim: found=%v err=%v", found, e)
	}
	after, e := os.ReadFile(claimPath)
	if e != nil || string(after) != string(changed) {
		t.Fatal("refusal altered evidence", e)
	}
}

//nolint:paralleltest // native Git fixture configures isolated process environment.
func TestE2EOfficialCloneMoveConflictingPendingIntentDoesNotPublishSecondIntent(t *testing.T) {
	f := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: f.projectsRoot, Operation: "clone-conflict", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	path := created[0].WorktreeDir
	claim, _, _, err := activeWorkLogClaim(f.home, path)
	if err != nil {
		t.Fatal(err)
	}
	head := gitTestOutput(t, path, "rev-parse", "HEAD")
	intent, intentPath, err := appendRelocationIntent(f.home, claim, path, filepath.Join(t.TempDir(), "prior"), "local", head, relocationPlacementRecord{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := *intent
	duplicate.OperationID = strings.Repeat("e", 32)
	duplicatePath := filepath.Join(filepath.Dir(intentPath), relocationIntentName(claim.ClaimID, duplicate.OperationID))
	encoded, e := json.Marshal(duplicate)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(duplicatePath, encoded, 0600); e != nil {
		t.Fatal(e)
	}
	entries, err := RecordCloneMoveRelocationIntents(f.projectsRoot, []CloneMoveWorktree{{Source: path, Destination: intent.Destination, Head: head}}, time.Now().UTC())
	if err == nil || len(entries) != 0 || !strings.Contains(err.Error(), "record clone-move relocation intent") {
		t.Fatalf("conflicting move: %+v %v", entries, err)
	}
	after, e := os.ReadFile(intentPath)
	if e != nil || string(after) != string(before) {
		t.Fatal("pending intent changed", e)
	}
	if got := gitTestOutput(t, path, "rev-parse", "HEAD"); got != head {
		t.Fatal("source moved")
	}
}

//nolint:paralleltest // transfer uses native isolated Git fixture environment.
func TestE2EOfficialCompletedTransferCorruptionAndCopiedProjectionNeverAuthorizeOtherPath(t *testing.T) {
	f := newRepositoryTransferFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: f.projectsRoot, Operation: "transfer-proof", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	old := created[0].WorktreeDir
	f.moveRemote(t)
	result, err := RelocateRepository(context.Background(), RepositoryRelocateOptions{ProjectsRoot: f.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: f.newRemote, DefaultBranch: "main", Apply: true})
	if err != nil || !result.Applied {
		t.Fatalf("transfer: %+v %v", result, err)
	}
	path := strings.Replace(old, f.canonical, f.destination, 1)
	homes := wbhome.Resolution{Write: wbhome.Layout{Home: f.home}}
	if err = corroborateRecordedRepositoryTransfer(homes, path); err != nil {
		t.Fatal("baseline", err)
	}
	copied := t.TempDir()
	if err = os.MkdirAll(filepath.Join(copied, workLogProjectionDirectory), 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(path, workLogProjectionDirectory, workLogProjectionName))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(copied, workLogProjectionDirectory, workLogProjectionName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = corroborateRecordedRepositoryTransfer(homes, copied); err == nil || !strings.Contains(err.Error(), "does not bind this checkout path") {
		t.Fatalf("copied projection: %v", err)
	}
	projection, err := readWorkLogProjectionForReadOnlyClaim(path)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(f.home, "worklogs", projection.EffortID, "runs", projection.RunID, "relocations")
	files, e := os.ReadDir(journal)
	if e != nil {
		t.Fatal(e)
	}
	blocker := ""
	for _, entry := range files {
		if strings.HasPrefix(entry.Name(), projection.ClaimID) {
			blocker = filepath.Join(journal, entry.Name())
			break
		}
	}
	if blocker == "" {
		t.Fatal("no native relocation evidence")
	}
	originalProof, e := os.ReadFile(blocker)
	if e != nil {
		t.Fatal(e)
	}
	if err = os.WriteFile(blocker, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = corroborateRecordedRepositoryTransfer(homes, path); err == nil {
		t.Fatal("corrupt relocation journal accepted")
	}
	if err = os.WriteFile(blocker, originalProof, 0600); err != nil {
		t.Fatal(err)
	}
	if err = corroborateRecordedRepositoryTransfer(homes, path); err != nil {
		t.Fatal("restored original proof", err)
	}
}

//nolint:paralleltest // native transfer fixture configures process-wide Git environment.
func TestE2EOfficialRepositoryTransferIntentRaceRefusesBeforeMove(t *testing.T) {
	f := newRepositoryTransferFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: f.projectsRoot, Operation: "transfer-intent-race", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	path := created[0].WorktreeDir
	projection, err := readWorkLogProjectionForReadOnlyClaim(path)
	if err != nil {
		t.Fatal(err)
	}
	f.moveRemote(t)
	options := RepositoryRelocateOptions{ProjectsRoot: f.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: f.newRemote, DefaultBranch: "main", Apply: true, Now: time.Now}
	result := RepositoryRelocateResult{SourceRepository: options.SourceRepository, DestinationRepository: options.DestinationRepository, SourceDir: f.canonical, DestinationDir: f.destination, RemoteURL: f.newRemote, DefaultBranch: "main"}
	plan, err := prepareRepositoryTransfer(context.Background(), options, &result, gitRawOutput)
	if err != nil || !result.Eligible {
		t.Fatalf("plan: %+v %v", result, err)
	}
	run := filepath.Join(f.home, "worklogs", projection.EffortID, "runs", projection.RunID)
	journal := filepath.Join(run, "relocations")
	if err = os.WriteFile(journal, []byte("journal obstruction"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = applyRepositoryTransfer(context.Background(), options, result, "newco", plan, gitRawOutput)
	if err == nil || !strings.Contains(err.Error(), "record repository relocation intent") {
		t.Fatalf("intent race: %+v %v", result, err)
	}
	if _, e := os.Stat(path); e != nil {
		t.Fatal("source changed", e)
	}
	if _, e := os.Stat(f.destination); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("destination mutated", e)
	}
	raw, e := os.ReadFile(journal)
	if e != nil || string(raw) != "journal obstruction" {
		t.Fatal("blocker changed", e)
	}
}

//nolint:paralleltest // native shared-to-local lifecycle configures process-wide fixture settings.
func TestE2EOfficialScopedRecoveryDoesNotRediscoverAlreadyKnownOriginalAfterRelocation(t *testing.T) {
	f := newGitFixture(t)
	configureFixtureSharedWorktrees(t, f)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: f.projectsRoot, Operation: "known-original", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	old := created[0].WorktreeDir
	result, err := Relocate(context.Background(), RelocateOptions{ProjectsRoot: f.projectsRoot, Task: "known-original", To: "local", Apply: true})
	if err != nil || len(result.Results) != 1 || !result.Results[0].Applied {
		t.Fatalf("relocate: %+v %v", result, err)
	}
	values, diagnostics := listTaskScopedClaimedRegistryWorktrees(context.Background(), f.projectsRoot, f.home, map[string]bool{old: true}, map[string]bool{"known-original": true}, "main", "", "", false, 1, nil, inspectPolicy{})
	if len(values) != 0 || len(diagnostics) != 0 {
		t.Fatalf("known original rediscovered: %+v %+v", values, diagnostics)
	}
	if _, err = os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old path present", err)
	}
}
