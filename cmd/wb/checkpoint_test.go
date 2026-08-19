package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// TestWorktreeCheckpointCLICreateListRestore drives checkpoint, list, and
// restore through run(), the same entry point main() uses, proving the cobra
// wiring reaches the checkpoint engine end to end: a checkpoint of code that
// does not compile succeeds, a second identical call is a no-op, list finds
// it, and restore recovers it into a new branch without disturbing the
// worktree it came from.
func TestWorktreeCheckpointCLICreateListRestore(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	prompt := writeOriginalPromptFixture(t, "checkpoint CLI journey")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "cli-checkpoint", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	worktree := filepath.Join(os.Getenv(wbhome.EnvOverride), "worktrees", "cli-checkpoint", "acme", "app")

	if err := os.WriteFile(filepath.Join(worktree, "broken.go"), []byte("package main\n\nfunc broken( {\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"worktree", "checkpoint", worktree, "--no-push", "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("checkpoint on broken code failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var created worktrees.CheckpointResult
	if err := json.Unmarshal(stdout.Bytes(), &created); err != nil {
		t.Fatalf("decode checkpoint result: %v\n%s", err, stdout.String())
	}
	if !created.Created || !created.Dirty || created.Ref == "" {
		t.Fatalf("checkpoint result = %#v", created)
	}

	// A second, unchanged call must be a cheap no-op.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"worktree", "checkpoint", worktree, "--no-push", "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("no-op checkpoint failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var reused worktrees.CheckpointResult
	if err := json.Unmarshal(stdout.Bytes(), &reused); err != nil {
		t.Fatal(err)
	}
	if reused.Created || reused.Ref != created.Ref {
		t.Fatalf("expected reused checkpoint, got %#v", reused)
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"worktree", "checkpoint", "list", worktree, "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("list failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var refs []worktrees.CheckpointRef
	if err := json.Unmarshal(stdout.Bytes(), &refs); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Ref != created.Ref {
		t.Fatalf("list result = %#v", refs)
	}

	startHead := ""
	{
		var out bytes.Buffer
		var errOut bytes.Buffer
		if code := run([]string{"worktree", "info", worktree, "--format", "json"}, &out, &errOut); code != exitOK {
			t.Fatalf("info failed: %d %s %s", code, out.String(), errOut.String())
		}
		startHead = out.String()
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"worktree", "checkpoint", "restore", created.Ref, worktree, "--apply", "--branch", "recovered/broken", "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("restore failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var restored worktrees.CheckpointRestoreResult
	if err := json.Unmarshal(stdout.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Applied || restored.Branch != "recovered/broken" || restored.Commit != created.Commit {
		t.Fatalf("restore result = %#v", restored)
	}

	// Restore must not have moved the worktree's own current branch/HEAD.
	var infoAfter bytes.Buffer
	var infoErr bytes.Buffer
	if code := run([]string{"worktree", "info", worktree, "--format", "json"}, &infoAfter, &infoErr); code != exitOK {
		t.Fatalf("info after restore failed: %d %s", code, infoErr.String())
	}
	if infoAfter.String() != startHead {
		t.Fatalf("restore disturbed the worktree it recovered from:\nbefore: %s\nafter:  %s", startHead, infoAfter.String())
	}
}

func TestWorktreeCheckpointSweepCLICoversEveryKnownWorktree(t *testing.T) {
	projects := setUpRenameCLIFixture(t)
	prompt := writeOriginalPromptFixture(t, "sweep CLI journey")
	previousProjectsRoot := projectsRoot
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", projects, "worktree", "create", "cli-sweep", "acme/app", "--model", "unknown", "--original-prompt-file", prompt}, &stdout, &stderr); code != exitOK {
		t.Fatalf("create failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--projects-root", projects, "worktree", "checkpoint", "sweep", "--no-push", "--format", "json"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("sweep failed: code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var results []worktrees.CheckpointAllResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Task != "cli-sweep" || results[0].Error != "" {
		t.Fatalf("sweep results = %#v", results)
	}

	if strings.TrimSpace(stderr.String()) != "" {
		t.Fatalf("sweep stderr = %q", stderr.String())
	}
}
