package worktrees

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	options := WorkLogOptions{EffortID: "fair-split", RunID: "codex-run-1", AgentRuntime: "codex"}
	for _, result := range []CreateResult{
		{Repository: "acme/a-b", WorktreeDir: worktrees[0], Branch: "codex/fair-split", Base: "main", BaseSHA: "aabbcc"},
		{Repository: "acme-a/b", WorktreeDir: worktrees[1], Branch: "codex/fair-split", Base: "main", BaseSHA: "ddeeff"},
		{Repository: "acme/third", WorktreeDir: worktrees[2], Branch: "codex/fair-split", Base: "main", BaseSHA: "ffeeaa"},
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
		Branch:      "codex/fair-split",
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
		WorkLog: WorkLogOptions{EffortID: effort, RunID: run,
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
		WorkLog: WorkLogOptions{EffortID: effort, RunID: run,
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
		EffortID: effort, RunID: run, OriginalPrompt: samePrompt, RequireOriginalPrompt: true,
	}); err != nil {
		t.Fatalf("identical same-run prompt was rejected: %v", err)
	}
}

func TestWorkLogMigratesLegacyProjectionOnlyAfterPrivateClaimCorroboration(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "projection-migration",
		WorkLog:      WorkLogOptions{RunID: "legacy-projection-run"},
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
		WorkLog:      WorkLogOptions{RunID: "shared-three"},
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
		Branch: "codex/legacy", Base: "main", BaseSHA: "deadbeef", Lifecycle: "active"}
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
		Branch: "codex/legacy", Base: "main", BaseSHA: "c0ffee"}, WorkLogOptions{EffortID: effort, RunID: run}); err != nil {
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
