package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLogVerbsCoverageBatch exercises the twenty related Work Log and cleanup
// entry points as one package batch. Most happy paths already have journey
// tests; these cases concentrate on the remaining validation, filesystem, and
// degraded-state branches.
//
//nolint:paralleltest // newGitFixture and WB home resolution use process environment variables.
func TestLogVerbsCoverageBatch(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	created, err := Create(ctx, []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "log-coverage-batch",
		WorkLog:      WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir

	//nolint:paralleltest // shares the parent fixture's process environment and Git checkout.
	t.Run("claim fence", func(t *testing.T) {
		fence, err := withOptionalClaimFence(fixture.projectsRoot, worktree, true)
		if err != nil {
			t.Fatal(err)
		}
		if fence.unlock == nil || fence.claim.ClaimID == "" {
			t.Fatalf("fence = %#v", fence)
		}
		fence.unlock()

		unclaimed := newJournalWorktree(t)
		if _, err := withOptionalClaimFence(fixture.projectsRoot, unclaimed, true); err == nil || !strings.Contains(err.Error(), "claim required") {
			t.Fatalf("required missing claim error = %v", err)
		}
	})

	//nolint:paralleltest // shares the parent fixture's process environment and Git checkout.
	t.Run("init steer show", func(t *testing.T) {
		first, err := LogInit(ctx, LogInitOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
			Prompt:       []byte("first prompt"),
		})
		if err != nil || first.Prompt == "" {
			t.Fatalf("first init = %#v, %v", first, err)
		}
		second, err := LogInit(ctx, LogInitOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
			Prompt:       []byte("must not replace first prompt"),
		})
		if err != nil || len(second.Notes) == 0 {
			t.Fatalf("second init = %#v, %v", second, err)
		}
		steered, err := LogSteer(ctx, LogSteerOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
			Body:         []byte("continue"),
		})
		if err != nil || steered.Prompt == "" {
			t.Fatalf("steer = %#v, %v", steered, err)
		}
		view, projection, err := LogShow(ctx, fixture.projectsRoot, worktree)
		if err != nil || view.Claim == nil || projection.LastSeq < 0 {
			t.Fatalf("show = view %#v projection %#v err %v", view, projection, err)
		}
	})

	//nolint:paralleltest // shares the parent fixture's process environment and Git checkout.
	t.Run("checkpoint helpers", func(t *testing.T) {
		input := int64(1)
		if _, err := LogCheckpoint(ctx, LogCheckpointOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
			InputTokens:  &input,
		}); err == nil || !strings.Contains(err.Error(), "usage-discriminator") {
			t.Fatalf("checkpoint missing usage provenance = %v", err)
		}
		checkpoint, err := LogCheckpoint(ctx, LogCheckpointOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
			Message:      "batch checkpoint",
			SkipRemote:   true,
		})
		if err != nil || len(checkpoint.Notes) != 1 || !strings.Contains(checkpoint.Notes[0], "skipped") {
			t.Fatalf("checkpoint = %#v, %v", checkpoint, err)
		}

		var result LogVerbResult
		if notes := remoteCheckpointFromLog(ctx, worktree, "", LocalGitEvidence{}, false, &result); !strings.Contains(notes[0], "no task") {
			t.Fatalf("missing task notes = %#v", notes)
		}
		if notes := remoteCheckpointFromLog(ctx, worktree, "task", LocalGitEvidence{}, false, &result); !strings.Contains(notes[0], "HEAD") {
			t.Fatalf("missing head notes = %#v", notes)
		}
		if notes := remoteCheckpointFromLog(ctx, t.TempDir(), "task", LocalGitEvidence{Head: strings.Repeat("a", 40)}, false, &result); !strings.Contains(notes[0], "not pushed") {
			t.Fatalf("failed push notes = %#v", notes)
		}

		ahead, behind, err := aheadBehind(ctx, worktree, created[0].BaseSHA)
		if err != nil || ahead < 0 || behind < 0 {
			t.Fatalf("ahead/behind = %d/%d, %v", ahead, behind, err)
		}
		if _, _, err := aheadBehind(ctx, t.TempDir(), strings.Repeat("a", 40)); err == nil {
			t.Fatal("aheadBehind accepted a non-repository")
		}
	})

	//nolint:paralleltest // shares the parent fixture's process environment and Git checkout.
	t.Run("refresh integrate handoff", func(t *testing.T) {
		if base := resolveLogBase(worktree, " release "); base != "release" {
			t.Fatalf("explicit log base = %q", base)
		}
		if base := resolveLogBase(worktree, ""); base != "main" {
			t.Fatalf("manifest log base = %q", base)
		}
		if base := resolveLogBase(t.TempDir(), ""); base != "main" {
			t.Fatalf("fallback log base = %q", base)
		}
		dirty := filepath.Join(worktree, "dirty.txt")
		if err := os.WriteFile(dirty, []byte("dirty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		refreshed, err := LogRefresh(ctx, LogRefreshOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
		})
		if err != nil || refreshed.Event == nil || refreshed.Event.Type != LocalEventRefreshNeed {
			t.Fatalf("dirty refresh = %#v, %v", refreshed, err)
		}
		if _, err := LogIntegrate(ctx, LogIntegrateOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
		}); err == nil || !strings.Contains(err.Error(), "clean worktree") {
			t.Fatalf("dirty integrate error = %v", err)
		}
		if err := os.Remove(dirty); err != nil {
			t.Fatal(err)
		}
		if _, err := LogHandoff(ctx, LogHandoffOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
			Summary:      "ready",
		}); err == nil || !strings.Contains(err.Error(), "successor") {
			t.Fatalf("handoff missing successor error = %v", err)
		}
	})

	//nolint:paralleltest // shares the parent fixture's process environment and Git checkout.
	t.Run("recover helpers", func(t *testing.T) {
		if _, err := LogRecover(ctx, LogRecoverOptions{
			ProjectsRoot:   fixture.projectsRoot,
			Worktree:       worktree,
			EstablishClaim: true,
			Takeover:       true,
		}); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("incompatible recover error = %v", err)
		}

		manifest, err := ReadManifest(worktree)
		if err != nil {
			t.Fatal(err)
		}
		manifest.DependencyCampaign = false
		manifest.ClaimID = ""
		if _, err := recoverableBlankManifestClaimID(ctx, worktree, manifest); err == nil || !strings.Contains(err.Error(), "dependency-campaign") {
			t.Fatalf("non-campaign recovery error = %v", err)
		}
		manifest.DependencyCampaign = true
		manifest.ClaimID = "already-present"
		if _, err := recoverableBlankManifestClaimID(ctx, worktree, manifest); err == nil || !strings.Contains(err.Error(), "already records") {
			t.Fatalf("existing claim recovery error = %v", err)
		}
		manifest.ClaimID = ""
		manifest.BaseSHA = ""
		if _, err := recoverBlankManifestClaim(ctx, fixture.home, worktree, manifest); err == nil || !strings.Contains(err.Error(), "lacks complete") {
			t.Fatalf("blank claim recovery error = %v", err)
		}
	})

	//nolint:paralleltest // shares the parent fixture's process environment and Git checkout.
	t.Run("finalize sync and view", func(t *testing.T) {
		if _, err := LogFinalize(ctx, LogFinalizeOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
			Result:       "maybe",
		}); err == nil || !strings.Contains(err.Error(), "success or failure") {
			t.Fatalf("invalid finalize result = %v", err)
		}
		dirty := filepath.Join(worktree, "finalize-dirty.txt")
		if err := os.WriteFile(dirty, []byte("dirty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LogFinalize(ctx, LogFinalizeOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
			Result:       "success",
		}); err == nil || !strings.Contains(err.Error(), "dirty worktree") {
			t.Fatalf("dirty finalize error = %v", err)
		}
		if err := os.Remove(dirty); err != nil {
			t.Fatal(err)
		}
		synced, err := LogSync(ctx, LogSyncOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     worktree,
			Apply:        true,
		})
		if err != nil || !synced.Offline || len(synced.Notes) != 2 {
			t.Fatalf("sync = %#v, %v", synced, err)
		}
		view, err := LoadWorkLogView(ctx, LoadWorkLogOptions{
			ProjectsRoot:        fixture.projectsRoot,
			Worktree:            worktree,
			IncludePromptBodies: true,
		})
		if err != nil || view.Claim == nil || view.OriginalPrompt == nil {
			t.Fatalf("view = %#v, %v", view, err)
		}
	})

	//nolint:paralleltest // shares the parent fixture's process environment and Git checkout.
	t.Run("archive and copy", func(t *testing.T) {
		archiveRoot := newJournalWorktree(t)
		manifest := newCreatedManifest("archive-batch")
		manifest.Worktree = archiveRoot
		if err := WriteManifest(archiveRoot, manifest); err != nil {
			t.Fatal(err)
		}
		if _, _, err := appendLocalEvent(archiveRoot, LocalWorkLogEvent{Type: LocalEventInit, Message: "archive fixture"}); err != nil {
			t.Fatal(err)
		}
		if _, err := LogArchive(ctx, LogArchiveOptions{Worktree: archiveRoot}); err == nil || !strings.Contains(err.Error(), "terminal") {
			t.Fatalf("nonterminal archive error = %v", err)
		}
		archived, err := LogArchive(ctx, LogArchiveOptions{
			ProjectsRoot: fixture.projectsRoot,
			Worktree:     archiveRoot,
			Apply:        true,
			Force:        true,
		})
		if err != nil || !archived.Applied || archived.Event == nil {
			t.Fatalf("archive = %#v, %v", archived, err)
		}

		src, dst := filepath.Join(t.TempDir(), "src"), filepath.Join(t.TempDir(), "dst")
		if err := os.MkdirAll(filepath.Join(src, "nested"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "nested", "value"), []byte("value"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := copyDir(src, dst); err != nil {
			t.Fatal(err)
		}
	})

	//nolint:paralleltest // shares the parent fixture's process environment and Git checkout.
	t.Run("cleanup receipt readers", func(t *testing.T) {
		root := t.TempDir()
		validPath := filepath.Join(root, "cleanup.json")
		contents, err := json.Marshal(cleanupReport{Phase: "applied", Apply: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(validPath, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		if report, err := readCleanupReportFile(validPath); err != nil || report.Phase != "applied" {
			t.Fatalf("cleanup report = %#v, %v", report, err)
		}
		if _, err := readCleanupReportFile(root); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("directory cleanup report error = %v", err)
		}
		if _, err := readCleanupReportFile(filepath.Join(root, "missing")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing cleanup report error = %v", err)
		}
		if _, err := FindTerminalCleanupProof(root, "invalid", "main", "task", filepath.Join(root, "worktree"), "branch"); err == nil || !strings.Contains(err.Error(), "invalid terminal cleanup repository") {
			t.Fatalf("invalid cleanup lookup error = %v", err)
		}
		if _, err := FindTerminalCleanupProof(root, "acme/app", "", "task", filepath.Join(root, "worktree"), "branch"); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("incomplete cleanup lookup error = %v", err)
		}
	})
}

func TestLogVerbEntryPointsRejectNonRepositories(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "missing")
	checks := []struct {
		name string
		run  func() error
	}{
		{"init", func() error { _, err := LogInit(ctx, LogInitOptions{Worktree: missing}); return err }},
		{"steer", func() error { _, err := LogSteer(ctx, LogSteerOptions{Worktree: missing}); return err }},
		{"show", func() error { _, _, err := LogShow(ctx, "", missing); return err }},
		{"checkpoint", func() error { _, err := LogCheckpoint(ctx, LogCheckpointOptions{Worktree: missing}); return err }},
		{"refresh", func() error { _, err := LogRefresh(ctx, LogRefreshOptions{Worktree: missing}); return err }},
		{"integrate", func() error { _, err := LogIntegrate(ctx, LogIntegrateOptions{Worktree: missing}); return err }},
		{"handoff", func() error { _, err := LogHandoff(ctx, LogHandoffOptions{Worktree: missing}); return err }},
		{"recover", func() error { _, err := LogRecover(ctx, LogRecoverOptions{Worktree: missing}); return err }},
		{"finalize", func() error { _, err := LogFinalize(ctx, LogFinalizeOptions{Worktree: missing}); return err }},
		{"sync", func() error { _, err := LogSync(ctx, LogSyncOptions{Worktree: missing}); return err }},
		{"archive", func() error { _, err := LogArchive(ctx, LogArchiveOptions{Worktree: missing}); return err }},
		{"view", func() error { _, err := LoadWorkLogView(ctx, LoadWorkLogOptions{Worktree: missing}); return err }},
	}
	for _, check := range checks {
		check := check
		t.Run(check.name, func(t *testing.T) {
			t.Parallel()
			if err := check.run(); err == nil {
				t.Fatal("accepted non-repository")
			}
		})
	}
}

func TestNewWorkLogClaimViewMapsEveryField(t *testing.T) {
	t.Parallel()
	recordedAt := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	claim := workLogClaim{
		EffortID: "effort", RunID: "run", ClaimID: "claim", Task: "task",
		Repository: "old/repository", Worktree: "/old/worktree", Branch: "branch",
		Base: "main", BaseSHA: strings.Repeat("a", 40), Lifecycle: "active", RecordedAt: recordedAt,
		Initiator: "initiator", AgentID: "agent", AgentRuntime: "runtime", Model: "model",
		ModelProvenance: "declared", CLI: "cli", Provider: "provider", TaskSummary: "summary",
		PromptDigest: "digest", PromptArchive: "/archive",
	}
	view := newWorkLogClaimView(claim, "new/repository", "/new/worktree", "/claim/path")
	if view.EffortID != claim.EffortID || view.RunID != claim.RunID || view.ClaimID != claim.ClaimID || view.Task != claim.Task ||
		view.Repository != "new/repository" || view.Worktree != "/new/worktree" || view.Branch != claim.Branch ||
		view.Base != claim.Base || view.BaseSHA != claim.BaseSHA || view.Lifecycle != claim.Lifecycle || view.RecordedAt != recordedAt ||
		view.Initiator != claim.Initiator || view.AgentID != claim.AgentID || view.AgentRuntime != claim.AgentRuntime ||
		view.Model != claim.Model || view.ModelProvenance != claim.ModelProvenance || view.CLI != claim.CLI ||
		view.Provider != claim.Provider || view.TaskSummary != claim.TaskSummary || view.PromptDigest != claim.PromptDigest ||
		view.PromptArchive != claim.PromptArchive || view.ClaimPath != "/claim/path" {
		t.Fatalf("claim view lost fields: %#v", view)
	}
}
