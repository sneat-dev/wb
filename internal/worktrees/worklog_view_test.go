package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWorkLogPromptFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "original-prompt.txt")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadWorkLogViewIncludesInitialPromptAndClaim(t *testing.T) {
	fixture := newGitFixture(t)
	promptBody := "implement wb worktree log for agent bootstrap\n"
	promptPath := writeWorkLogPromptFile(t, promptBody)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "agent-worklog",
		WorkLog: WorkLogOptions{
			RunID: "agent-worklog-run", Model: "unknown",
			OriginalPrompt: promptPath, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 {
		t.Fatalf("created = %#v", created)
	}
	worktree := created[0].WorktreeDir
	if _, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceHuman}, []byte("also record this steering note")); err != nil {
		t.Fatal(err)
	}

	view, err := LoadWorkLogView(context.Background(), LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, IncludePromptBodies: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Manifest == nil || view.Manifest.EffortID != "agent-worklog" {
		t.Fatalf("manifest = %#v", view.Manifest)
	}
	if view.Claim == nil || view.Claim.RunID != "agent-worklog-run" || view.Claim.Lifecycle != "active" {
		t.Fatalf("claim = %#v", view.Claim)
	}
	if view.OriginalPrompt == nil || view.OriginalPrompt.Body != promptBody {
		t.Fatalf("original prompt = %#v", view.OriginalPrompt)
	}
	if len(view.Prompts) != 2 {
		t.Fatalf("prompts = %#v", view.Prompts)
	}
	if view.Prompts[0].Body != promptBody {
		t.Fatalf("prompt 0 body = %q", view.Prompts[0].Body)
	}
	if view.Prompts[1].Body != "also record this steering note\n" && view.Prompts[1].Body != "also record this steering note" {
		t.Fatalf("prompt 1 body = %q", view.Prompts[1].Body)
	}
	digest := sha256.Sum256([]byte(promptBody))
	if view.OriginalPrompt.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("original digest = %s", view.OriginalPrompt.SHA256)
	}

	text := FormatWorkLogViewText(view)
	for _, want := range []string{
		"# WB work log",
		"## Original prompt",
		promptBody,
		"also record this steering note",
		"## Claim",
		"agent-worklog-run",
		"## Git",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("text dump missing %q:\n%s", want, text)
		}
	}
}

func TestLoadWorkLogViewFallsBackToArchiveWhenJournalPromptsMissing(t *testing.T) {
	fixture := newGitFixture(t)
	promptBody := "recover me from the private archive\n"
	promptPath := writeWorkLogPromptFile(t, promptBody)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot,
		Operation:    "archive-fallback",
		WorkLog: WorkLogOptions{
			RunID: "archive-fallback-run", Model: "unknown",
			OriginalPrompt: promptPath, RequireOriginalPrompt: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if err := os.RemoveAll(filepath.Join(worktree, journalRootDirectory, journalLocalDirectory, promptsDirectory)); err != nil {
		t.Fatal(err)
	}

	view, err := LoadWorkLogView(context.Background(), LoadWorkLogOptions{
		ProjectsRoot: fixture.projectsRoot, Worktree: worktree, IncludePromptBodies: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.OriginalPrompt == nil || view.OriginalPrompt.Source != "archive" || view.OriginalPrompt.Body != promptBody {
		t.Fatalf("original prompt = %#v", view.OriginalPrompt)
	}
	if len(view.Prompts) != 0 {
		t.Fatalf("expected empty journal prompts, got %#v", view.Prompts)
	}
}

func TestLoadWorkLogViewWithoutBodiesKeepsHeadersOnly(t *testing.T) {
	worktree := newJournalWorktree(t)
	if err := WriteManifest(worktree, newCreatedManifest("headers-only")); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendPrompt(worktree, PromptHeader{Source: PromptSourceHuman}, []byte("secret instruction")); err != nil {
		t.Fatal(err)
	}
	view, err := LoadWorkLogView(context.Background(), LoadWorkLogOptions{
		Worktree: worktree, IncludePromptBodies: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.OriginalPrompt != nil {
		t.Fatalf("original prompt must stay redacted: %#v", view.OriginalPrompt)
	}
	if len(view.Prompts) != 1 || view.Prompts[0].Body != "" || view.Prompts[0].SHA256 == "" {
		t.Fatalf("prompts = %#v", view.Prompts)
	}
	text := FormatWorktreeInfoText(view)
	for _, want := range []string{
		"# WB worktree info",
		"headers-only",
		"sha256=",
		"wb worktree log",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("info text missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "secret instruction") {
		t.Fatalf("info text leaked prompt body:\n%s", text)
	}
}
