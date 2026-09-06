package repositoryevents

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestSyncProcessorFastForwardsCanonicalAndPreservesDirtyState(t *testing.T) {
	projects := t.TempDir()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	seed := filepath.Join(t.TempDir(), "seed")
	runGit(t, "", "clone", remote, seed)
	runGit(t, seed, "config", "user.email", "test@example.com")
	runGit(t, seed, "config", "user.name", "Test")
	runGit(t, seed, "switch", "-c", "main")
	writeCommit(t, seed, "one")
	runGit(t, seed, "push", "-u", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	canonical := filepath.Join(projects, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "clone", remote, canonical)
	first := strings.TrimSpace(runGit(t, canonical, "rev-parse", "HEAD"))
	writeCommit(t, seed, "two")
	runGit(t, seed, "push", "origin", "main")

	event := repositoryevent.Event{Version: repositoryevent.ContractVersion, ID: "event-sync", Repository: "github.com/acme/app", Ref: "refs/heads/main", Reason: repositoryevent.ReasonDefaultBranchUpdated}
	detail, err := (SyncProcessor{ProjectsRoot: projects}).Process(context.Background(), event)
	if err != nil || detail != "pulled" {
		t.Fatalf("sync = %q, %v", detail, err)
	}
	second := strings.TrimSpace(runGit(t, canonical, "rev-parse", "HEAD"))
	if first == second {
		t.Fatal("canonical checkout did not fast-forward")
	}
	if err := os.WriteFile(filepath.Join(canonical, "dirty.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeCommit(t, seed, "three")
	runGit(t, seed, "push", "origin", "main")
	detail, err = (SyncProcessor{ProjectsRoot: projects}).Process(context.Background(), event)
	if err != nil || detail != "skipped (dirty)" {
		t.Fatalf("dirty sync = %q, %v", detail, err)
	}
	if got := strings.TrimSpace(runGit(t, canonical, "rev-parse", "HEAD")); got != second {
		t.Fatalf("dirty canonical moved from %s to %s", second, got)
	}
	if _, err := os.Stat(filepath.Join(canonical, "dirty.txt")); err != nil {
		t.Fatal("dirty file was discarded")
	}
}

func TestReceiverQueueAndProcessorFastForwardEndToEnd(t *testing.T) {
	projects := t.TempDir()
	remote := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", remote)
	seed := filepath.Join(t.TempDir(), "seed")
	runGit(t, "", "clone", remote, seed)
	runGit(t, seed, "config", "user.email", "test@example.com")
	runGit(t, seed, "config", "user.name", "Test")
	runGit(t, seed, "switch", "-c", "main")
	writeCommit(t, seed, "one")
	runGit(t, seed, "push", "-u", "origin", "main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	canonical := filepath.Join(projects, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, "", "clone", remote, canonical)
	before := strings.TrimSpace(runGit(t, canonical, "rev-parse", "HEAD"))
	writeCommit(t, seed, "two")
	runGit(t, seed, "push", "origin", "main")

	event := receiverEvent("event-end-to-end")
	source := &sourceFake{response: repositoryevent.PollResponse{Version: repositoryevent.ContractVersion, Cursor: "", NextCursor: "cursor-1", Events: []repositoryevent.Event{event}}}
	queue, err := NewQueue(projects)
	if err != nil {
		t.Fatal(err)
	}
	receiver := Receiver{Source: source, Queue: queue, Cursor: CursorStore{Path: filepath.Join(projects, ".wb", "runtime", "daemon", "repository-events", "cursor.json")}}
	if err := receiver.ReceiveOnce(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if len(source.acks) != 1 || queue.jobs[event.ID].State != "queued" {
		t.Fatalf("delivery ack/jobs = %+v / %+v", source.acks, queue.jobs[event.ID])
	}
	queue.workers = 1
	queue.acquire = func(context.Context) (func(), error) { return func() {}, nil }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go queue.Run(ctx, SyncProcessor{ProjectsRoot: projects}, nil)
	deadline := time.Now().Add(3 * time.Second)
	for {
		queue.mu.Lock()
		state := queue.jobs[event.ID].State
		queue.mu.Unlock()
		if state == "succeeded" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job state = %q", state)
		}
		time.Sleep(10 * time.Millisecond)
	}
	after := strings.TrimSpace(runGit(t, canonical, "rev-parse", "HEAD"))
	if before == after {
		t.Fatal("canonical checkout did not fast-forward after provider delivery")
	}
}

func TestSyncProcessorLeavesUnsafeRenameQueuedFromSharedGuard(t *testing.T) {
	projects := t.TempDir()
	oldPath := filepath.Join(projects, "acme", "old-app")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, oldPath, "init", "-b", "main")
	runGit(t, oldPath, "config", "user.email", "test@example.com")
	runGit(t, oldPath, "config", "user.name", "Test")
	writeCommit(t, oldPath, "one")
	event := repositoryevent.Event{Version: repositoryevent.ContractVersion, ID: "event-rename", Repository: "github.com/acme/new-app", PreviousRepository: "github.com/acme/old-app", Ref: "refs/heads/main", Reason: repositoryevent.ReasonRepositoryRenamed}
	processor := SyncProcessor{ProjectsRoot: projects, relocate: func(_ context.Context, options worktrees.RepositoryRelocateOptions) (worktrees.RepositoryRelocateResult, error) {
		if options.SourceRepository != "acme/old-app" || options.DestinationRepository != "acme/new-app" || options.RemoteURL != "git@github.com:acme/new-app.git" || options.DefaultBranch != "main" || !options.Apply {
			t.Fatalf("relocation options = %+v", options)
		}
		return worktrees.RepositoryRelocateResult{Reason: "worktree has local changes"}, nil
	}}
	if _, err := processor.Process(context.Background(), event); err == nil || !strings.Contains(err.Error(), "worktree has local changes") {
		t.Fatalf("rename error = %v", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatal("old canonical path was moved")
	}
}

func TestSyncProcessorUsesSharedRelocationThenSafeSync(t *testing.T) {
	projects := t.TempDir()
	oldPath := filepath.Join(projects, "acme", "old-app")
	newPath := filepath.Join(projects, "acme", "new-app")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, oldPath, "init", "-b", "main")
	runGit(t, oldPath, "config", "user.email", "test@example.com")
	runGit(t, oldPath, "config", "user.name", "Test")
	writeCommit(t, oldPath, "one")
	processor := SyncProcessor{
		ProjectsRoot: projects,
		relocate: func(_ context.Context, _ worktrees.RepositoryRelocateOptions) (worktrees.RepositoryRelocateResult, error) {
			if err := os.Rename(oldPath, newPath); err != nil {
				t.Fatal(err)
			}
			return worktrees.RepositoryRelocateResult{Eligible: true, Applied: true}, nil
		},
		sync: func(_ context.Context, repo discover.Repo, root string, _, _ bool) fleetsync.Result {
			if repo.Path != newPath || repo.CloneURL != "git@github.com:acme/new-app.git" || root != projects {
				t.Fatalf("sync input = %+v, root=%q", repo, root)
			}
			return fleetsync.Result{Status: fleetsync.Pulled}
		},
	}
	event := repositoryevent.Event{Version: repositoryevent.ContractVersion, ID: "event-rename", Repository: "github.com/acme/new-app", PreviousRepository: "github.com/acme/old-app", Ref: "refs/heads/main", Reason: repositoryevent.ReasonRepositoryRenamed}
	if detail, err := processor.Process(context.Background(), event); err != nil || detail != "relocated; pulled" {
		t.Fatalf("rename process = %q, %v", detail, err)
	}
}

func writeCommit(t *testing.T, directory, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "value.txt"), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, directory, "add", "value.txt")
	runGit(t, directory, "commit", "-m", value)
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	if directory != "" {
		command.Dir = directory
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}
