package worktrees

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkLogRecordsOneImmutableClaimPerRepositoryInSharedRun(t *testing.T) {
	homeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(homeRoot, ".wb")
	worktreeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktrees := []string{filepath.Join(worktreeRoot, "first"), filepath.Join(worktreeRoot, "second"), filepath.Join(worktreeRoot, "third")}
	for _, worktree := range worktrees {
		if err := os.MkdirAll(worktree, 0o755); err != nil {
			t.Fatal(err)
		}
		gitTest(t, worktree, "init")
	}
	options := WorkLogOptions{EffortID: "fair-split", RunID: "codex-run-1", AgentRuntime: "codex", Model: "unknown"}
	for _, result := range []CreateResult{
		{Repository: "acme/a-b", WorktreeDir: worktrees[0], Branch: "feature/fair-split", Base: "main", BaseSHA: "aabbcc"},
		{Repository: "acme-a/b", WorktreeDir: worktrees[1], Branch: "feature/fair-split", Base: "main", BaseSHA: "ddeeff"},
		{Repository: "acme/third", WorktreeDir: worktrees[2], Branch: "feature/fair-split", Base: "main", BaseSHA: "ffeeaa"},
	} {
		if _, err := recordWorkLog(home, "fair-split", result, options); err != nil {
			t.Fatal(err)
		}
	}
	claims, err := os.ReadDir(filepath.Join(home, "worklogs", "fair-split", "runs", "codex-run-1", "claims"))
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 3 {
		t.Fatalf("claims = %#v, want one immutable entry per repository", claims)
	}
	for _, claim := range claims {
		if !validClaimID(strings.TrimSuffix(claim.Name(), ".json")) {
			t.Fatalf("claim uses non-injective repository filename: %s", claim.Name())
		}
	}
	outbox, err := os.ReadDir(filepath.Join(home, "worklogs", "fair-split", "outbox"))
	if err != nil || len(outbox) != 3 {
		t.Fatalf("outbox cardinality = %d err=%v, want 3", len(outbox), err)
	}
	for _, worktree := range worktrees {
		if _, err := git(context.Background(), worktree, "check-ignore", ".wb-worklog/recovery.json"); err != nil {
			t.Fatalf("projection at %s is not locally ignored: %v", worktree, err)
		}
		contents, err := os.ReadFile(filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(contents, []byte(worktree)) || bytes.Contains(contents, []byte("repository")) || bytes.Contains(contents, []byte("prompt")) {
			t.Fatalf("projection leaked private/path metadata: %s", contents)
		}
	}
}

func TestWorkLogClaimIdentitySurvivesRunAndWorktreeRelocation(t *testing.T) {
	original := CreateResult{
		Repository:  "acme/app",
		WorktreeDir: "/machine-a/worktrees/task/acme/app",
		Branch:      "feature/fair-split",
		Base:        "main",
		BaseSHA:     strings.Repeat("a", 40),
	}
	relocated := original
	relocated.WorktreeDir = "/machine-b/recovered/acme/app"
	first := workLogClaimID("fair-split", original)
	second := workLogClaimID("fair-split", relocated)
	if first != second {
		t.Fatalf("claim ID changed across relocation: %s != %s", first, second)
	}
	changedBase := relocated
	changedBase.BaseSHA = strings.Repeat("b", 40)
	if workLogClaimID("fair-split", changedBase) == first {
		t.Fatal("claim ID did not change with immutable base")
	}
	changedRepository := relocated
	changedRepository.Repository = "acme/other"
	if workLogClaimID("fair-split", changedRepository) == first {
		t.Fatal("claim ID did not change with canonical repository")
	}
}

func TestNewClaimsRequireExplicitExecutionIdentityBeforePublication(t *testing.T) {
	fixture := newGitFixture(t)
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "missing-model", WorkLog: WorkLogOptions{RunID: "missing-model-run"},
	})
	if err == nil || !strings.Contains(err.Error(), "--model is required") {
		t.Fatalf("missing model error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.home, "worktrees", "missing-model")); !os.IsNotExist(statErr) {
		t.Fatalf("missing-model create published a worktree: %v", statErr)
	}
	for _, model := range []string{"gpt-5.6-sol", "unknown"} {
		prepared, prepareErr := PrepareWorkLogOptions(fixture.projectsRoot, "identity-"+model, WorkLogOptions{Model: model, CLI: "codex", Provider: "openai-codex"})
		if prepareErr != nil || prepared.Model != model {
			t.Fatalf("explicit model %q prepared=%#v err=%v", model, prepared, prepareErr)
		}
	}
}

func TestExecutionIdentityCorrectionIsAppendOnlyIdempotentAndProjectsChain(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "identity-chain",
		WorkLog: WorkLogOptions{RunID: "identity-chain-run", Initiator: "dispatcher", Model: "gpt-5.6-sol", CLI: "codex", Provider: "openai-codex"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var claim workLogClaim
	contents, err := os.ReadFile(created[0].WorkLogPath)
	if err != nil || json.Unmarshal(contents, &claim) != nil {
		t.Fatalf("claim = %v", err)
	}
	if claim.Version != 2 || claim.ModelProvenance != modelProvenanceCallerDeclared || claim.ModelDeclaredBy != "dispatcher" || claim.CLI != "codex" || claim.Provider != "openai-codex" {
		t.Fatalf("new claim execution identity = %#v", claim)
	}
	modelUnknown := "unknown"
	first, err := CorrectExecutionIdentity(CorrectExecutionIdentityOptions{ProjectsRoot: fixture.projectsRoot, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, EventID: "identity-fix-1", Actor: "reviewer", Reason: "observed route differs", Model: &modelUnknown})
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity.Model != "unknown" || first.Identity.ModelProvenance != modelProvenanceUnknown || first.Identity.CLI != "codex" || first.Identity.Provider != "openai-codex" {
		t.Fatalf("first projection = %#v", first.Identity)
	}
	clearCLI, newProvider := "", "opencode-go"
	second, err := CorrectExecutionIdentity(CorrectExecutionIdentityOptions{ProjectsRoot: fixture.projectsRoot, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, EventID: "identity-fix-2", Actor: "reviewer", Reason: "correct independent route", CLI: &clearCLI, Provider: &newProvider})
	if err != nil {
		t.Fatal(err)
	}
	if second.Identity.Model != "unknown" || second.Identity.CLI != "" || second.Identity.Provider != "opencode-go" || len(second.Identity.CorrectionIDs) != 2 {
		t.Fatalf("second projection = %#v", second.Identity)
	}
	// Same stable event is the crash-safe retry path, and must not append twice.
	retried, err := CorrectExecutionIdentity(CorrectExecutionIdentityOptions{ProjectsRoot: fixture.projectsRoot, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, EventID: "identity-fix-2", Actor: "reviewer", Reason: "correct independent route", CLI: &clearCLI, Provider: &newProvider})
	if err != nil || len(retried.Identity.CorrectionIDs) != 2 {
		t.Fatalf("idempotent retry = %#v err=%v", retried, err)
	}
	if _, err := CorrectExecutionIdentity(CorrectExecutionIdentityOptions{ProjectsRoot: fixture.projectsRoot, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, EventID: "identity-fix-2", Actor: "reviewer", Reason: "different", CLI: &clearCLI}); err == nil {
		t.Fatal("same correction event ID accepted a mutation")
	}
	if _, err := os.Stat(first.OutboxPath); err != nil {
		t.Fatalf("offline outbox receipt = %v", err)
	}
	if err := os.RemoveAll(created[0].WorktreeDir); err != nil {
		t.Fatal(err)
	}
	provider := "direct-api"
	if _, err := CorrectExecutionIdentity(CorrectExecutionIdentityOptions{ProjectsRoot: fixture.projectsRoot, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, EventID: "identity-fix-3", Actor: "reviewer", Reason: "post-cleanup correction", Provider: &provider}); err != nil {
		t.Fatalf("correction after worktree removal = %v", err)
	}
}

func TestExecutionIdentityCorrectionRejectsMalformedAndCrossClaimHistory(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "identity-reject", WorkLog: WorkLogOptions{RunID: "identity-reject-run", Model: "gpt-5.6-sol"}})
	if err != nil {
		t.Fatal(err)
	}
	claims := make([]workLogClaim, 2)
	bytes, readErr := os.ReadFile(created[0].WorkLogPath)
	if readErr != nil || json.Unmarshal(bytes, &claims[0]) != nil {
		t.Fatalf("read first claim: %v", readErr)
	}
	secondRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	secondWorktree := filepath.Join(secondRoot, "second")
	if err := os.MkdirAll(secondWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, secondWorktree, "init")
	secondPath, err := recordWorkLog(fixture.home, "identity-reject", CreateResult{Repository: "acme/second", WorktreeDir: secondWorktree, Branch: "identity-reject", Base: "main", BaseSHA: "abcdef"}, WorkLogOptions{RunID: claims[0].RunID, Model: "gpt-5.6-sol"})
	if err != nil {
		t.Fatal(err)
	}
	bytes, readErr = os.ReadFile(secondPath)
	if readErr != nil || json.Unmarshal(bytes, &claims[1]) != nil {
		t.Fatalf("read second claim: %v", readErr)
	}
	badProvider := "sk-secret-route"
	if _, err := CorrectExecutionIdentity(CorrectExecutionIdentityOptions{ProjectsRoot: fixture.projectsRoot, EffortID: claims[0].EffortID, RunID: claims[0].RunID, ClaimID: claims[0].ClaimID, EventID: "bad-route", Actor: "reviewer", Reason: "test", Provider: &badProvider}); err == nil {
		t.Fatal("credential-shaped provider was accepted")
	}
	runDir, _, err := openWorkLogRun(fixture.home, claims[1].EffortID, claims[1].RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runDir.Close() }()
	directory, err := openWorkLogCorrections(runDir, claims[1].ClaimID, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	model := "unknown"
	forged := workLogIdentityCorrection{Version: 1, Type: "worktree.execution_identity_corrected", CorrectionID: "forged", ClaimID: claims[0].ClaimID, Sequence: 1, At: time.Now().UTC(), Actor: "attacker", Reason: "cross claim", Model: &model}
	if err := writeJSONImmutableAt(directory, "forged.json", forged, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := projectExecutionIdentity(runDir, claims[1]); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("cross-claim correction error = %v", err)
	}
}

func TestExecutionIdentityCorrectionConcurrentRetryPublishesOneEvent(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "identity-concurrent", WorkLog: WorkLogOptions{RunID: "identity-concurrent-run", Model: "gpt-5.6-sol"}})
	if err != nil {
		t.Fatal(err)
	}
	var claim workLogClaim
	contents, err := os.ReadFile(created[0].WorkLogPath)
	if err != nil || json.Unmarshal(contents, &claim) != nil {
		t.Fatalf("claim = %v", err)
	}
	cli := "opencode"
	request := CorrectExecutionIdentityOptions{ProjectsRoot: fixture.projectsRoot, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, EventID: "same-retry", Actor: "dispatcher", Reason: "concurrent retry", CLI: &cli}
	errors := make(chan error, 2)
	for range 2 {
		go func() { _, correctErr := CorrectExecutionIdentity(request); errors <- correctErr }()
	}
	for range 2 {
		if correctErr := <-errors; correctErr != nil {
			t.Fatalf("concurrent correction = %v", correctErr)
		}
	}
	runDir, _, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runDir.Close() }()
	identity, corrections, err := projectExecutionIdentity(runDir, claim)
	if err != nil || identity.CLI != "opencode" || len(corrections) != 1 {
		t.Fatalf("concurrent projection=%#v corrections=%#v err=%v", identity, corrections, err)
	}
}

func TestWorkLogBindsRunToExactPromptBeforeWorktreeCreation(t *testing.T) {
	fixture := newGitFixture(t)
	firstPrompt := filepath.Join(t.TempDir(), "first-prompt.txt")
	secondPrompt := filepath.Join(t.TempDir(), "second-prompt.txt")
	if err := os.WriteFile(firstPrompt, []byte("implement the audited lifecycle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPrompt, []byte("a conflicting originating request\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const effort = "prompt-bound-effort"
	const run = "prompt-bound-run"
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "prompt-source",
		WorkLog: WorkLogOptions{EffortID: effort, RunID: run, Model: "unknown",
			OriginalPrompt: firstPrompt, RequireOriginalPrompt: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimPath := created[0].WorkLogPath
	var claim workLogClaim
	claimBytes, err := os.ReadFile(claimPath)
	if err != nil || json.Unmarshal(claimBytes, &claim) != nil {
		t.Fatalf("read private claim: %v", err)
	}
	wantDigest := sha256.Sum256([]byte("implement the audited lifecycle\n"))
	if claim.PromptArchive != "original-prompt.txt" || claim.PromptDigest != hex.EncodeToString(wantDigest[:]) {
		t.Fatalf("claim prompt evidence = archive %q digest %q", claim.PromptArchive, claim.PromptDigest)
	}
	runDir := filepath.Join(fixture.home, "worklogs", effort, "runs", run)
	archived, err := os.ReadFile(filepath.Join(runDir, "original-prompt.txt"))
	if err != nil || string(archived) != "implement the audited lifecycle\n" {
		t.Fatalf("private prompt archive = %q err=%v", archived, err)
	}
	var metadata workLogPromptMetadata
	metadataBytes, err := os.ReadFile(filepath.Join(runDir, "original-prompt.json"))
	if err != nil || json.Unmarshal(metadataBytes, &metadata) != nil {
		t.Fatalf("read prompt metadata: %v", err)
	}
	if metadata.Version != 1 || metadata.SHA256 != claim.PromptDigest || metadata.SourceReference != firstPrompt || metadata.CapturedAt.IsZero() {
		t.Fatalf("prompt metadata = %#v", metadata)
	}

	_, err = Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "prompt-conflict",
		WorkLog: WorkLogOptions{EffortID: effort, RunID: run, Model: "unknown",
			OriginalPrompt: secondPrompt, RequireOriginalPrompt: true},
	})
	if err == nil || !strings.Contains(err.Error(), "different original prompt bytes") {
		t.Fatalf("conflicting same-run prompt error = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixture.home, "worktrees", "prompt-conflict")); !os.IsNotExist(statErr) {
		t.Fatalf("conflicting prompt created task state before rejection: %v", statErr)
	}

	// A relocated source file with identical exact bytes is the same request.
	samePrompt := filepath.Join(t.TempDir(), "same-prompt.txt")
	if err := os.WriteFile(samePrompt, archived, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareWorkLogOptions(fixture.projectsRoot, "same-prompt", WorkLogOptions{
		EffortID: effort, RunID: run, Model: "unknown", OriginalPrompt: samePrompt, RequireOriginalPrompt: true,
	}); err != nil {
		t.Fatalf("identical same-run prompt was rejected: %v", err)
	}
}

func TestWorkLogMigratesLegacyProjectionOnlyAfterPrivateClaimCorroboration(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "projection-migration",
		WorkLog:      WorkLogOptions{RunID: "legacy-projection-run", Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	projection, err := readWorkLogProjection(worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeWorkLogProjection(worktree); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(worktree, legacyWorkLogProjectionName), projection, 0o600); err != nil {
		t.Fatal(err)
	}
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	if err := preflightWorkLogSeal(fixture.home, worktree, head); err != nil {
		t.Fatalf("migrate corroborated legacy projection: %v", err)
	}
	migrated, err := readWorkLogProjection(worktree)
	if err != nil || migrated != projection {
		t.Fatalf("migrated projection = %#v err=%v, want %#v", migrated, err, projection)
	}
	if _, err := os.Stat(filepath.Join(worktree, legacyWorkLogProjectionName)); !os.IsNotExist(err) {
		t.Fatalf("legacy projection remains after migration: %v", err)
	}
	if _, err := git(context.Background(), worktree, "check-ignore", ".wb-worklog/recovery.json"); err != nil {
		t.Fatalf("migrated projection is not excluded: %v", err)
	}

	// Recreate a legacy pointer with a valid-looking but mismatching claim ID.
	if err := removeWorkLogProjection(worktree); err != nil {
		t.Fatal(err)
	}
	malicious := projection
	malicious.ClaimID = strings.Repeat("0", 64)
	if err := writeJSONAtomic(filepath.Join(worktree, legacyWorkLogProjectionName), malicious, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preflightWorkLogSeal(fixture.home, worktree, head); err == nil {
		t.Fatal("mismatching legacy projection migrated without private claim")
	}
	if _, err := os.Stat(filepath.Join(worktree, legacyWorkLogProjectionName)); err != nil {
		t.Fatalf("rejected legacy projection was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)); !os.IsNotExist(err) {
		t.Fatalf("rejected legacy projection created current pointer: %v", err)
	}
}

func TestWorkLogProjectionCannotEscapePrivateArchiveOrAuthorizeCleanup(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "projection-trust",
	})
	if err != nil {
		t.Fatal(err)
	}
	projection, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	projection.EffortID = "../escaped-effort"
	contents, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, workLogProjectionDirectory, workLogProjectionName), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = Abort(context.Background(), AbortOptions{
		ProjectsRoot: fixture.projectsRoot,
		Task:         "projection-trust",
		Disposition:  AbortDiscarded,
		DeleteRemote: true,
		Apply:        true,
	})
	if err == nil || !strings.Contains(err.Error(), "invalid work-log projection identity") {
		t.Fatalf("malicious projection error = %v", err)
	}
	if _, err := os.Stat(created[0].WorktreeDir); err != nil {
		t.Fatalf("untrusted projection authorized cleanup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fixture.home, "escaped-effort")); !os.IsNotExist(err) {
		t.Fatalf("projection escaped private worklogs root: %v", err)
	}
}

func TestWorkLogTerminalCardinalityAndRestartForThreeRepositories(t *testing.T) {
	fixture := newGitFixture(t)
	repositories := []string{"acme/app", "acme/storage", "acme/third"}
	for _, repository := range repositories[1:] {
		_, name, err := splitRepository(repository)
		if err != nil {
			t.Fatal(err)
		}
		gitTest(t, fixture.projectsRoot, "clone", fixture.remote, filepath.Join(fixture.projectsRoot, "acme", name))
	}
	created, err := Create(context.Background(), repositories, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "terminal-cardinality",
		WorkLog:      WorkLogOptions{RunID: "shared-three", Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		for _, result := range created {
			head := gitTestOutput(t, result.WorktreeDir, "rev-parse", "HEAD")
			if err := sealWorkLogForRecycle(fixture.home, result.WorktreeDir, head, "discarded"); err != nil {
				t.Fatalf("attempt %d seal %s: %v", attempt, result.Repository, err)
			}
		}
	}
	runDir := filepath.Join(fixture.home, "worklogs", "terminal-cardinality", "runs", "shared-three")
	terminals, err := os.ReadDir(filepath.Join(runDir, "terminals"))
	if err != nil || len(terminals) != 3 {
		t.Fatalf("terminal cardinality = %d err=%v, want 3", len(terminals), err)
	}
	outbox, err := os.ReadDir(filepath.Join(fixture.home, "worklogs", "terminal-cardinality", "outbox"))
	if err != nil || len(outbox) != 6 {
		t.Fatalf("claimed+sealed outbox cardinality = %d err=%v, want 6", len(outbox), err)
	}
	for _, result := range created {
		projection, err := readWorkLogProjection(result.WorktreeDir)
		if err != nil || projection.Lifecycle != "terminal" {
			t.Fatalf("terminal projection for %s = %#v err=%v", result.Repository, projection, err)
		}
	}
}

func TestCreateResumeRejectsSilentWorkLogReclaim(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "claim-owner",
		WorkLog:      WorkLogOptions{RunID: "original-run", AgentID: "agent-one", Model: "unknown"},
	})
	if err != nil || len(created) != 1 {
		t.Fatalf("create original claim = %#v err=%v", created, err)
	}
	before, err := readWorkLogProjection(created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []WorkLogOptions{
		{RunID: "replacement-run", AgentID: "agent-one", Model: "unknown"},
		{RunID: "original-run", AgentID: "agent-two", Model: "unknown"},
	} {
		_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
			ProjectsRoot: fixture.projectsRoot,
			Operation:    "claim-owner",
			Resume:       true,
			WorkLog:      request,
		})
		if err == nil || !strings.Contains(err.Error(), "audited handoff") {
			t.Fatalf("silent reclaim request %#v error = %v", request, err)
		}
		after, readErr := readWorkLogProjection(created[0].WorktreeDir)
		if readErr != nil || after != before {
			t.Fatalf("rejected reclaim changed projection: before=%#v after=%#v err=%v", before, after, readErr)
		}
	}
}

func TestCreateResumePreservesIndependentActiveClaimsAcrossRepositories(t *testing.T) {
	fixture := newGitFixture(t)
	storageCanonical := filepath.Join(fixture.projectsRoot, "acme", "storage")
	gitTest(t, fixture.projectsRoot, "clone", fixture.remote, storageCanonical)
	operation := "independent-resume-runs"
	created := make([]CreateResult, 0, 2)
	for _, request := range []struct {
		repository string
		run        string
	}{
		{repository: "acme/app", run: "app-run"},
		{repository: "acme/storage", run: "storage-run"},
	} {
		results, err := Create(context.Background(), []string{request.repository}, CreateOptions{
			ProjectsRoot: fixture.projectsRoot,
			Operation:    operation,
			WorkLog:      WorkLogOptions{RunID: request.run, Model: "unknown"},
		})
		if err != nil || len(results) != 1 {
			t.Fatalf("create %s = %#v err=%v", request.repository, results, err)
		}
		created = append(created, results[0])
	}
	before := make(map[string]workLogProjection, len(created))
	for _, result := range created {
		projection, err := readWorkLogProjection(result.WorktreeDir)
		if err != nil {
			t.Fatal(err)
		}
		before[result.Repository] = projection
	}

	resumed, err := Create(context.Background(), []string{"acme/app", "acme/storage"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    operation,
		Resume:       true,
	})
	if err != nil || len(resumed) != 2 {
		t.Fatalf("coordinated resume = %#v err=%v", resumed, err)
	}
	for _, result := range resumed {
		if result.Action != "resumed" {
			t.Fatalf("resume action for %s = %q", result.Repository, result.Action)
		}
		after, readErr := readWorkLogProjection(result.WorktreeDir)
		if readErr != nil || after != before[result.Repository] {
			t.Fatalf("resume replaced %s active claim: before=%#v after=%#v err=%v", result.Repository, before[result.Repository], after, readErr)
		}
	}
}

func TestCreateRollsBackPublishedGitAfterWorkLogStageFailure(t *testing.T) {
	tests := []struct {
		name           string
		configure      func(*CreateOptions)
		wantProjection bool
	}{
		{
			name: "after immutable claim",
			configure: func(options *CreateOptions) {
				options.afterWorkLogClaim = func(CreateResult) error { return errors.New("injected claim-stage failure") }
			},
		},
		{
			name:           "after recovery projection",
			wantProjection: true,
			configure: func(options *CreateOptions) {
				options.afterWorkLogProjection = func(CreateResult) error { return errors.New("injected projection-stage failure") }
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newGitFixture(t)
			options := CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "worklog-stage-failure", WorkLog: WorkLogOptions{RunID: "stage-failure-run", Model: "unknown"}}
			test.configure(&options)
			_, err := Create(context.Background(), []string{"acme/app"}, options)
			var publicationErr *CreatePublicationError
			if !errors.As(err, &publicationErr) || len(publicationErr.Outcomes) != 1 {
				t.Fatalf("typed publication error = %#v err=%v", publicationErr, err)
			}
			outcome := publicationErr.Outcomes[0]
			if !outcome.WorkLog.ClaimWritten || outcome.WorkLog.ProjectionWritten != test.wantProjection || !outcome.RollbackCompleted || !outcome.BacklogPersisted {
				t.Fatalf("publication recovery outcome = %#v", outcome)
			}
			if outcome.Result.Repository != "acme/app" || outcome.Result.WorktreeDir == "" || outcome.Result.Branch != "worklog-stage-failure" || !isGitObjectID(outcome.HeadSHA) {
				t.Fatalf("publication recovery lost exact Git identity: %#v", outcome)
			}
			assertFailedCreateRolledBack(t, fixture, "worklog-stage-failure")
			var backlog lifecycleBacklogRecord
			contents, err := os.ReadFile(outcome.CleanupBacklogPath)
			if err == nil {
				err = json.Unmarshal(contents, &backlog)
			}
			if err != nil || backlog.Stage != lifecycleStageComplete || backlog.RecoveryKind != "create_work_log_failed" {
				t.Fatalf("durable create recovery backlog = %#v err=%v", backlog, err)
			}
			terminal := filepath.Join(fixture.home, "worklogs", outcome.WorkLog.EffortID, "runs", outcome.WorkLog.RunID, "terminals", outcome.WorkLog.ClaimID+".json")
			if _, err := os.Stat(terminal); err != nil {
				t.Fatalf("failed-create claim was not terminalized append-only: %v", err)
			}
		})
	}
}

func TestCreatePublicationErrorRetainsExactRecoveryWhenBacklogStorageAndRollbackFail(t *testing.T) {
	fixture := newGitFixture(t)
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "receipt-unavailable",
		WorkLog:      WorkLogOptions{RunID: "receipt-unavailable-run", Model: "unknown"},
		afterWorkLogClaim: func(CreateResult) error {
			return errors.New("injected Work Log failure")
		},
		beforeCreateBacklogPersist: func(CreateResult) error {
			return errors.New("injected durable receipt failure")
		},
		beforeWorkLogRollback: func(CreateResult) error {
			return errors.New("injected uncertain rollback")
		},
	})
	var publicationErr *CreatePublicationError
	if !errors.As(err, &publicationErr) || len(publicationErr.Outcomes) != 1 {
		t.Fatalf("typed uncertain publication error = %#v err=%v", publicationErr, err)
	}
	outcome := publicationErr.Outcomes[0]
	if outcome.BacklogPersisted || outcome.RollbackCompleted || outcome.CleanupBacklogID == "" || outcome.CleanupBacklogPath == "" || outcome.RecoveryError == "" ||
		outcome.Result.Repository != "acme/app" || outcome.Result.WorktreeDir == "" || outcome.Result.Branch != "receipt-unavailable" ||
		!isGitObjectID(outcome.HeadSHA) || !outcome.WorkLog.ClaimWritten || outcome.WorkLog.ClaimPath == "" {
		t.Fatalf("uncertain recovery outcome omitted exact evidence: %#v", outcome)
	}
	if _, statErr := os.Stat(outcome.CleanupBacklogPath); !os.IsNotExist(statErr) {
		t.Fatalf("injected backlog storage failure unexpectedly wrote receipt: %v", statErr)
	}
	for _, exact := range []string{outcome.Result.Repository, outcome.Result.WorktreeDir, outcome.Result.Branch, outcome.HeadSHA, outcome.CleanupBacklogID} {
		if !strings.Contains(err.Error(), exact) {
			t.Fatalf("CLI-visible publication error omitted exact recovery coordinate %q: %v", exact, err)
		}
	}
	listed, listErr := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "receipt-unavailable"})
	if listErr != nil || len(listed.Results) != 1 || listed.Results[0].WorktreeDir != outcome.Result.WorktreeDir || listed.Results[0].Branch != outcome.Result.Branch {
		t.Fatalf("uncertain live asset is not visibly inventoryable: listed=%#v err=%v", listed, listErr)
	}
}

func TestCreateUncertainRollbackPersistsExactVisibleCleanupBacklog(t *testing.T) {
	fixture := newGitFixture(t)
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "uncertain-create-rollback",
		WorkLog:      WorkLogOptions{RunID: "uncertain-create-run", Model: "unknown"},
		afterWorkLogClaim: func(CreateResult) error {
			return errors.New("injected Work Log failure")
		},
		beforeWorkLogRollback: func(CreateResult) error {
			return errors.New("injected uncertain rollback")
		},
	})
	var publicationErr *CreatePublicationError
	if !errors.As(err, &publicationErr) || len(publicationErr.Outcomes) != 1 {
		t.Fatalf("typed uncertain publication error = %#v err=%v", publicationErr, err)
	}
	outcome := publicationErr.Outcomes[0]
	if !outcome.BacklogPersisted || outcome.RollbackCompleted || outcome.CleanupBacklogPath == "" {
		t.Fatalf("uncertain rollback outcome = %#v", outcome)
	}
	contents, readErr := os.ReadFile(outcome.CleanupBacklogPath)
	var backlog lifecycleBacklogRecord
	if readErr == nil {
		readErr = json.Unmarshal(contents, &backlog)
	}
	if readErr != nil || backlog.ID != outcome.CleanupBacklogID || backlog.Stage != lifecycleStageRemovingWorktree ||
		backlog.Repository != outcome.Result.Repository || backlog.WorktreeDir != outcome.Result.WorktreeDir ||
		backlog.Branch != outcome.Result.Branch || backlog.HeadSHA != outcome.HeadSHA || backlog.WorkLogClaim != outcome.WorkLog.ClaimID {
		t.Fatalf("durable uncertain cleanup receipt = %#v err=%v", backlog, readErr)
	}
	listed, listErr := ListWithDiagnostics(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "uncertain-create-rollback"})
	if listErr != nil || len(listed.Results) != 1 || listed.Results[0].WorktreeDir != outcome.Result.WorktreeDir {
		t.Fatalf("uncertain published worktree is not visible: listed=%#v err=%v", listed, listErr)
	}
}

func TestCreateRecoveryBacklogPreservesPreexistingLocalAndRemoteBranch(t *testing.T) {
	fixture := newGitFixture(t)
	const branch = "existing-publication-branch"
	gitTest(t, fixture.canonical, "branch", branch, "main")
	gitTest(t, fixture.canonical, "push", "origin", branch)
	_, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "preserve-existing-publication",
		Resume:       true,
		Branch:       branch,
		BranchChosen: true,
		WorkLog:      WorkLogOptions{RunID: "preserve-existing-run", Model: "unknown"},
		afterWorkLogClaim: func(CreateResult) error {
			return errors.New("injected Work Log failure")
		},
	})
	var publicationErr *CreatePublicationError
	if !errors.As(err, &publicationErr) || len(publicationErr.Outcomes) != 1 {
		t.Fatalf("typed publication error = %#v err=%v", publicationErr, err)
	}
	outcome := publicationErr.Outcomes[0]
	contents, err := os.ReadFile(outcome.CleanupBacklogPath)
	var backlog lifecycleBacklogRecord
	if err == nil {
		err = json.Unmarshal(contents, &backlog)
	}
	if err != nil || !backlog.PreserveLocalBranch || backlog.Stage != lifecycleStageComplete {
		t.Fatalf("preexisting-branch recovery backlog = %#v err=%v", backlog, err)
	}
	if err := persistLifecycleBacklog(fixture.home, &backlog, lifecycleStageRemovingWorktree); err != nil {
		t.Fatal(err)
	}
	if err := resumeLifecycleBacklog(context.Background(), fixture.home, &backlog); err != nil {
		t.Fatalf("resume completed create recovery with preserved remote branch: %v", err)
	}
	if exists, branchErr := localBranchExists(context.Background(), fixture.canonical, branch); branchErr != nil || !exists {
		t.Fatalf("preexisting local branch preserved=%t err=%v", exists, branchErr)
	}
	if remoteHead, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, branch); remoteErr != nil || remoteHead == "" {
		t.Fatalf("preexisting remote branch head=%q err=%v", remoteHead, remoteErr)
	}
}

func TestCreateWorkLogFailureRollsBackEveryRepositoryPublishedByInvocation(t *testing.T) {
	fixture := newGitFixture(t)
	storageCanonical := filepath.Join(fixture.projectsRoot, "acme", "storage")
	gitTest(t, fixture.projectsRoot, "clone", fixture.remote, storageCanonical)
	_, err := Create(context.Background(), []string{"acme/app", "acme/storage"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "coordinated-worklog-failure",
		WorkLog:      WorkLogOptions{RunID: "coordinated-failure-run", Model: "unknown"},
		afterWorkLogProjection: func(result CreateResult) error {
			if result.Repository == "acme/storage" {
				return errors.New("injected second-repository Work Log failure")
			}
			return nil
		},
	})
	var publicationErr *CreatePublicationError
	if !errors.As(err, &publicationErr) || len(publicationErr.Outcomes) != 2 {
		t.Fatalf("coordinated publication error = %#v err=%v", publicationErr, err)
	}
	for _, outcome := range publicationErr.Outcomes {
		if !outcome.RollbackCompleted || !outcome.BacklogPersisted || !outcome.WorkLog.ClaimWritten {
			t.Fatalf("coordinated recovery outcome = %#v", outcome)
		}
		if _, statErr := os.Stat(outcome.Result.WorktreeDir); !os.IsNotExist(statErr) {
			t.Fatalf("coordinated rollback left worktree %s: %v", outcome.Result.WorktreeDir, statErr)
		}
		canonical := fixture.canonical
		if outcome.Result.Repository == "acme/storage" {
			canonical = storageCanonical
		}
		if exists, branchErr := localBranchExists(context.Background(), canonical, outcome.Result.Branch); branchErr != nil || exists {
			t.Fatalf("coordinated rollback left branch %s in %s: exists=%t err=%v", outcome.Result.Branch, outcome.Result.Repository, exists, branchErr)
		}
	}
	terminals, err := os.ReadDir(filepath.Join(fixture.home, "worklogs", "coordinated-worklog-failure", "runs", "coordinated-failure-run", "terminals"))
	if err != nil || len(terminals) != 2 {
		t.Fatalf("coordinated failed claims terminalized = %d err=%v, want 2", len(terminals), err)
	}
}

func TestWorkLogMigratesLegacySingletonAndReportsLostCardinality(t *testing.T) {
	homeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(homeRoot, ".wb")
	effort, run := "legacy-effort", "legacy-run"
	runDir := filepath.Join(home, "worklogs", effort, "runs", run)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := legacyWorkLogClaim{Version: 1, EffortID: effort, RunID: run,
		Task: effort, Repository: "acme/last-survivor", Worktree: filepath.Join(home, "worktrees", effort, "acme", "last-survivor"),
		Branch: "feature/legacy", Base: "main", BaseSHA: "deadbeef", Lifecycle: "active"}
	if err := writeJSONAtomic(filepath.Join(runDir, "claim.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(filepath.Join(runDir, "run.json"), struct {
		Version int       `json:"version"`
		Effort  string    `json:"effort_id"`
		Run     string    `json:"run_id"`
		Created time.Time `json:"created_at"`
	}{1, effort, run, time.Unix(1, 0).UTC()}, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"one", "two", "last-survivor"} {
		worktree := filepath.Join(home, "worktrees", effort, "acme", repository)
		if err := os.MkdirAll(worktree, 0o755); err != nil {
			t.Fatal(err)
		}
		projection, err := json.Marshal(map[string]any{"version": 1, "effort_id": effort, "run_id": run, "repository": "acme/" + repository})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree, legacyWorkLogProjectionName), projection, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	newWorktreeRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newWorktree := filepath.Join(newWorktreeRoot, "new-worktree")
	if err := os.MkdirAll(newWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, newWorktree, "init")
	if _, err := recordWorkLog(home, effort, CreateResult{Repository: "acme/new", WorktreeDir: newWorktree,
		Branch: "feature/legacy", Base: "main", BaseSHA: "c0ffee"}, WorkLogOptions{EffortID: effort, RunID: run, Model: "unknown"}); err != nil {
		t.Fatal(err)
	}
	claims, err := os.ReadDir(filepath.Join(runDir, "claims"))
	if err != nil || len(claims) != 2 {
		t.Fatalf("migrated+new claims = %d err=%v, want 2", len(claims), err)
	}
	var migration legacyClaimMigration
	contents, err := os.ReadFile(filepath.Join(runDir, "legacy-claim-migration.json"))
	if err != nil || json.Unmarshal(contents, &migration) != nil {
		t.Fatalf("read migration report: %v", err)
	}
	if migration.RecoveredClaims != 1 || migration.ObservedProjections != 3 || !migration.LostCardinality {
		t.Fatalf("migration report = %#v", migration)
	}
}
