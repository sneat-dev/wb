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
)

//nolint:paralleltest // the Git fixture configures process-wide environment
func TestE2ELogRecoverDiagnosesMissingManifestAndInvalidHome(t *testing.T) {
	worktree := newJournalWorktree(t)
	projectsRoot := t.TempDir()
	result, err := LogRecover(context.Background(), LogRecoverOptions{
		ProjectsRoot: projectsRoot, Worktree: worktree,
	})
	if err != nil || result.Applied {
		t.Fatalf("read-only diagnosis = %#v, %v", result, err)
	}
	manifestDiagnosed := false
	for _, diagnosis := range result.Diagnosis {
		manifestDiagnosed = manifestDiagnosed || strings.HasPrefix(diagnosis, "manifest:")
	}
	if !manifestDiagnosed {
		t.Fatalf("missing manifest diagnosis: %#v", result.Diagnosis)
	}
	if _, err := LogRecover(context.Background(), LogRecoverOptions{
		ProjectsRoot: projectsRoot, Worktree: worktree, EstablishClaim: true,
	}); err == nil || !strings.Contains(err.Error(), "readable immutable manifest") {
		t.Fatalf("missing manifest established claim: %v", err)
	}
	blockedRoot := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(blockedRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LogRecover(context.Background(), LogRecoverOptions{
		ProjectsRoot: filepath.Join(blockedRoot, "projects"), Worktree: worktree,
	}); err == nil {
		t.Fatal("invalid projects root accepted")
	}
}

func newLogRecoverClaimFixture(t *testing.T, operation string) (*gitFixture, string) {
	t.Helper()
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: operation, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, created[0].WorktreeDir
}

//nolint:paralleltest // each subtest creates a Git fixture with process-wide environment
func TestE2ELogRecoverApplyReportsClaimAndJournalFaults(t *testing.T) {
	for _, stage := range []string{"existing claim", "claim fence", "journal open", "projection write", "takeover append"} {
		//nolint:paralleltest // the fixture calls t.Setenv and runs native Git
		t.Run(stage, func(t *testing.T) {
			fixture, worktree := newLogRecoverClaimFixture(t, "recover-"+strings.ReplaceAll(stage, " ", "-"))
			ctx := context.Background()
			options := LogRecoverOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, Apply: true}
			journal := filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, worklogDirectory)
			switch stage {
			case "existing claim":
				options.EstablishClaim = true
			case "claim fence":
				path := filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)
				if err := os.WriteFile(path, []byte("invalid hybrid projection"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "journal open":
				if err := os.Rename(journal, journal+".kept"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(journal, []byte("occupied"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "projection write":
				path := filepath.Join(journal, localWorkLogProjectionName)
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "takeover append":
				options.Takeover, options.Actor = true, "operator"
				options.appendRecoveryEvent = func(string, LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
					return LocalWorkLogEvent{}, LocalWorkLogProjection{}, errors.New("recovery append refused")
				}
			}
			result, err := LogRecover(ctx, options)
			if err == nil || result.Applied {
				t.Fatalf("%s fault accepted: result=%#v err=%v", stage, result, err)
			}
		})
	}
}

//nolint:paralleltest // the Git fixture configures process-wide environment
func TestE2ELogRecoverBlankClaimPublicationAndEventFaults(t *testing.T) {
	fixture := newGitFixture(t)
	const operation = "recover-blank-faults"
	worktree := filepath.Join(fixture.home, "worktrees", operation, "acme", "app")
	branch := "wb/" + operation
	gitTest(t, fixture.canonical, "worktree", "add", "-b", branch, worktree, "origin/main")
	baseSHA := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
	effort := operation + ".acme-app"
	if err := WriteManifest(worktree, Manifest{
		Version: 1, EffortID: effort, ParentEffort: operation, EffortKind: EffortKindTask,
		Repository: "acme/app", Worktree: worktree, Branch: branch, Base: "main", BaseSHA: baseSHA,
		CreatedAt: time.Now().UTC(), DependencyCampaign: true, Provenance: ProvenanceCreated,
	}); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrompt(worktree, PromptHeader{Source: PromptSourceAgent, Slug: "campaign"}, []byte("dependency campaign")); err != nil {
		t.Fatal(err)
	}
	if _, err := LogInit(context.Background(), LogInitOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree}); err != nil {
		t.Fatal(err)
	}
	options := LogRecoverOptions{ProjectsRoot: fixture.projectsRoot, Worktree: worktree, EstablishClaim: true, Apply: true}
	worklogs := filepath.Join(fixture.home, "worklogs")
	if err := os.MkdirAll(worklogs, 0o700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(worklogs, effort)
	if err := os.WriteFile(blocker, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, err := LogRecover(context.Background(), options); err == nil || result.Applied {
		t.Fatalf("blocked private claim publication = %#v, %v", result, err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	options.appendRecoveryEvent = func(string, LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, errors.New("recovery append refused")
	}
	if result, err := LogRecover(context.Background(), options); err == nil || result.Applied || !strings.Contains(err.Error(), "record claim recovery event") {
		t.Fatalf("unrecordable claim recovery = %#v, %v", result, err)
	}
	options.appendRecoveryEvent = nil
	if result, err := LogRecover(context.Background(), options); err != nil || !result.Applied || result.Event == nil {
		t.Fatalf("retry after recorded claim without event = %#v, %v", result, err)
	}
}
