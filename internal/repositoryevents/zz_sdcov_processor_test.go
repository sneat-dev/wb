package repositoryevents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/fleetsync"
	"github.com/sneat-dev/wb/internal/lifecyclehooks"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestSdCovProcessorRejectsInvalidEventAndIncompleteCleanupState(t *testing.T) {
	processor := SyncProcessor{ProjectsRoot: t.TempDir()}
	if _, err := processor.Process(context.Background(), repositoryevent.Event{}, ProcessState{}); err == nil {
		t.Fatal("Process accepted an invalid event")
	}
	if _, err := processor.Process(context.Background(), receiverEvent("event-incomplete"), ProcessState{CleanupReceipt: "receipt.json"}); err == nil || !strings.Contains(err.Error(), "cleanup state is incomplete") {
		t.Fatalf("incomplete cleanup state error = %v", err)
	}
}

func TestSdCovProcessorRecoversPendingCleanupBeforeSyncing(t *testing.T) {
	t.Run("default recovery seam rejects a foreign receipt", func(t *testing.T) {
		projects := t.TempDir()
		receipt := filepath.Join(t.TempDir(), "pending-cleanup.json")
		command := "wb repo transfer cleanup --receipt " + receipt + " --apply"
		processor := SyncProcessor{ProjectsRoot: projects}
		result, err := processor.Process(context.Background(), receiverEvent("event-default-recovery"), ProcessState{CleanupReceipt: receipt, RecoveryCommand: command})
		if err == nil || !strings.Contains(err.Error(), "recover repository transfer cleanup") {
			t.Fatalf("default recovery error = %v", err)
		}
		if !result.CleanupStateSet || result.CleanupReceipt != receipt || result.RecoveryCommand != command {
			t.Fatalf("default recovery result = %+v", result)
		}
	})

	t.Run("recovery failure is surfaced", func(t *testing.T) {
		projects := t.TempDir()
		receipt := filepath.Join(projects, ".wb", "reports", "repository-transfers", "pending-cleanup.json")
		command := "wb repo transfer cleanup --receipt " + receipt + " --apply"
		forced := errors.New("cleanup store unavailable")
		processor := SyncProcessor{
			ProjectsRoot: projects,
			recoverCleanup: func(_ context.Context, options worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error) {
				if options.ProjectsRoot != projects || options.ReceiptPath != receipt || !options.Apply {
					t.Fatalf("cleanup options = %+v", options)
				}
				return worktrees.RepositoryTransferCleanupResult{}, forced
			},
		}
		result, err := processor.Process(context.Background(), receiverEvent("event-recovery-error"), ProcessState{CleanupReceipt: receipt, RecoveryCommand: command})
		if !errors.Is(err, forced) {
			t.Fatalf("recovery error = %v", err)
		}
		if !result.CleanupStateSet || result.CleanupReceipt != receipt {
			t.Fatalf("recovery failure result = %+v", result)
		}
	})

	t.Run("an unapplied recovery is surfaced", func(t *testing.T) {
		projects := t.TempDir()
		receipt := filepath.Join(projects, ".wb", "reports", "repository-transfers", "pending-cleanup.json")
		command := "wb repo transfer cleanup --receipt " + receipt + " --apply"
		processor := SyncProcessor{
			ProjectsRoot: projects,
			recoverCleanup: func(context.Context, worktrees.RepositoryTransferCleanupOptions) (worktrees.RepositoryTransferCleanupResult, error) {
				return worktrees.RepositoryTransferCleanupResult{Reason: "evidence pending"}, nil
			},
		}
		result, err := processor.Process(context.Background(), receiverEvent("event-recovery-unapplied"), ProcessState{CleanupReceipt: receipt, RecoveryCommand: command})
		if err == nil || !strings.Contains(err.Error(), "evidence pending") {
			t.Fatalf("unapplied recovery error = %v", err)
		}
		if !result.CleanupStateSet || result.RecoveryCommand != command {
			t.Fatalf("unapplied recovery result = %+v", result)
		}
	})
}

func TestSdCovProcessorUsesDefaultRelocationSeamForExistingRename(t *testing.T) {
	projects := t.TempDir()
	oldPath := filepath.Join(projects, "acme", "old-app")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	processor := SyncProcessor{ProjectsRoot: projects}
	event := repositoryevent.Event{
		Version: repositoryevent.ContractVersion, ID: "event-default-relocate",
		Repository: "github.com/acme/new-app", PreviousRepository: "github.com/acme/old-app",
		Ref: "refs/heads/main", Reason: repositoryevent.ReasonRepositoryRenamed,
	}
	if _, err := processor.Process(context.Background(), event, ProcessState{}); err == nil || !strings.Contains(err.Error(), "relocate renamed repository") {
		t.Fatalf("default relocation error = %v", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("non-repository source moved during default relocation: %v", err)
	}
}

func TestSdCovProcessorRejectsIncompleteRelocationCleanupRecovery(t *testing.T) {
	projects := t.TempDir()
	oldPath := filepath.Join(projects, "acme", "old-app")
	if err := os.MkdirAll(oldPath, 0o755); err != nil {
		t.Fatal(err)
	}
	processor := SyncProcessor{
		ProjectsRoot: projects,
		relocate: func(context.Context, worktrees.RepositoryRelocateOptions) (worktrees.RepositoryRelocateResult, error) {
			return worktrees.RepositoryRelocateResult{Applied: true, CleanupPending: true, Reason: "evidence pending"}, nil
		},
	}
	event := repositoryevent.Event{
		Version: repositoryevent.ContractVersion, ID: "event-incomplete-relocation",
		Repository: "github.com/acme/new-app", PreviousRepository: "github.com/acme/old-app",
		Ref: "refs/heads/main", Reason: repositoryevent.ReasonRepositoryRenamed,
	}
	if _, err := processor.Process(context.Background(), event, ProcessState{}); err == nil || !strings.Contains(err.Error(), "incomplete cleanup recovery state") {
		t.Fatalf("incomplete relocation cleanup error = %v", err)
	}
}

func TestSdCovProcessorRenameStatFailures(t *testing.T) {
	t.Run("source stat failure", func(t *testing.T) {
		projects := t.TempDir()
		if err := os.WriteFile(filepath.Join(projects, "acme"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		relocated := false
		processor := SyncProcessor{
			ProjectsRoot: projects,
			relocate: func(context.Context, worktrees.RepositoryRelocateOptions) (worktrees.RepositoryRelocateResult, error) {
				relocated = true
				return worktrees.RepositoryRelocateResult{}, nil
			},
		}
		event := repositoryevent.Event{
			Version: repositoryevent.ContractVersion, ID: "event-source-stat",
			Repository: "github.com/acme/new-app", PreviousRepository: "github.com/acme/old-app",
			Ref: "refs/heads/main", Reason: repositoryevent.ReasonRepositoryRenamed,
		}
		if _, err := processor.Process(context.Background(), event, ProcessState{}); err == nil {
			t.Fatal("Process ignored an inspection failure for the rename source")
		}
		if relocated {
			t.Fatal("relocation ran despite an unreadable source path")
		}
	})

	t.Run("destination stat failure", func(t *testing.T) {
		projects := t.TempDir()
		if err := os.WriteFile(filepath.Join(projects, "newco"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		processor := SyncProcessor{ProjectsRoot: projects}
		event := repositoryevent.Event{
			Version: repositoryevent.ContractVersion, ID: "event-destination-stat",
			Repository: "github.com/newco/new-app", PreviousRepository: "github.com/oldco/old-app",
			Ref: "refs/heads/main", Reason: repositoryevent.ReasonRepositoryRenamed,
		}
		if _, err := processor.Process(context.Background(), event, ProcessState{}); err == nil {
			t.Fatal("Process ignored an inspection failure for the rename destination")
		}
	})
}

func TestSdCovProcessorRejectsNonCanonicalCheckouts(t *testing.T) {
	t.Run("path is not a directory", func(t *testing.T) {
		projects := t.TempDir()
		if err := os.MkdirAll(filepath.Join(projects, "acme"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(projects, "acme", "app"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		processor := SyncProcessor{ProjectsRoot: projects}
		if _, err := processor.Process(context.Background(), receiverEvent("event-not-a-directory"), ProcessState{}); err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("non-directory canonical path error = %v", err)
		}
	})

	t.Run("missing git directory", func(t *testing.T) {
		projects := t.TempDir()
		if err := os.MkdirAll(filepath.Join(projects, "acme", "app"), 0o755); err != nil {
			t.Fatal(err)
		}
		processor := SyncProcessor{ProjectsRoot: projects}
		if _, err := processor.Process(context.Background(), receiverEvent("event-no-git"), ProcessState{}); err == nil || !strings.Contains(err.Error(), "not a canonical Git checkout") {
			t.Fatalf("missing .git error = %v", err)
		}
	})
}

func TestSdCovProcessorSkipsCheckoutOnADifferentBranch(t *testing.T) {
	projects := t.TempDir()
	path := filepath.Join(projects, "acme", "app")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "init", "-b", "release")
	runGit(t, path, "config", "user.email", "test@example.com")
	runGit(t, path, "config", "user.name", "Test")
	writeCommit(t, path, "one")
	processor := SyncProcessor{
		ProjectsRoot: projects,
		verifyOrigin: func(string, string) error { return nil },
		sync: func(context.Context, discover.Repo, string, bool, bool) fleetsync.Result {
			t.Fatal("sync ran for a checkout on an unrelated branch")
			return fleetsync.Result{}
		},
	}
	result, err := processor.Process(context.Background(), receiverEvent("event-branch-mismatch"), ProcessState{})
	if err != nil || result.Detail != "canonical checkout is on release; skipped main" {
		t.Fatalf("branch mismatch result = %+v, %v", result, err)
	}
}

func TestSdCovProcessorSyncsAbsentCheckoutAndPropagatesFailure(t *testing.T) {
	t.Run("absent checkout syncs with an empty path", func(t *testing.T) {
		projects := t.TempDir()
		var synced discover.Repo
		var root string
		processor := SyncProcessor{
			ProjectsRoot: projects,
			sync: func(_ context.Context, repo discover.Repo, projectsRoot string, _, _ bool) fleetsync.Result {
				synced = repo
				root = projectsRoot
				return fleetsync.Result{Status: fleetsync.Pulled}
			},
		}
		result, err := processor.Process(context.Background(), receiverEvent("event-absent"), ProcessState{})
		if err != nil || result.Detail != "pulled" {
			t.Fatalf("absent checkout result = %+v, %v", result, err)
		}
		if synced.Path != "" || synced.Org != "acme" || synced.Name != "app" || synced.CloneURL != "git@github.com:acme/app.git" || !synced.Remote || root != projects {
			t.Fatalf("sync input = %+v, root = %q", synced, root)
		}
	})

	t.Run("failed sync is propagated", func(t *testing.T) {
		projects := t.TempDir()
		forced := errors.New("sync exploded")
		processor := SyncProcessor{
			ProjectsRoot: projects,
			sync: func(context.Context, discover.Repo, string, bool, bool) fleetsync.Result {
				return fleetsync.Result{Status: fleetsync.Failed, Err: forced}
			},
		}
		if _, err := processor.Process(context.Background(), receiverEvent("event-failed-sync"), ProcessState{}); !errors.Is(err, forced) {
			t.Fatalf("failed sync error = %v", err)
		}
	})
}

func TestSdCovProcessorDispatchesLifecycleEventsThroughBothSeams(t *testing.T) {
	cloned := func(context.Context, discover.Repo, string, bool, bool) fleetsync.Result {
		return fleetsync.Result{Repo: discover.Repo{Path: "/projects/acme/app"}, Status: fleetsync.Cloned, HeadSHA: strings.Repeat("a", 40)}
	}

	t.Run("default dispatch seam ignores an empty hook configuration", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		t.Setenv("XDG_STATE_HOME", t.TempDir())
		testenv.Isolate(t)
		processor := SyncProcessor{ProjectsRoot: t.TempDir(), sync: cloned}
		result, err := processor.Process(context.Background(), receiverEvent("event-default-dispatch"), ProcessState{})
		if err != nil {
			t.Fatalf("default dispatch error = %v", err)
		}
		if !strings.HasPrefix(result.Detail, "cloned") {
			t.Fatalf("default dispatch detail = %q", result.Detail)
		}
	})

	t.Run("dispatch failure becomes a hook warning", func(t *testing.T) {
		processor := SyncProcessor{
			ProjectsRoot: t.TempDir(),
			sync:         cloned,
			dispatchLifecycle: func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
				return lifecyclehooks.Report{}, errors.New("hook boom")
			},
		}
		result, err := processor.Process(context.Background(), receiverEvent("event-dispatch-failure"), ProcessState{})
		if err != nil {
			t.Fatalf("dispatch failure error = %v", err)
		}
		if !strings.Contains(result.Detail, "hook warning") || !strings.Contains(result.Detail, "hook boom") {
			t.Fatalf("dispatch failure detail = %q", result.Detail)
		}
	})

	t.Run("reported warnings become a hook warning", func(t *testing.T) {
		processor := SyncProcessor{
			ProjectsRoot: t.TempDir(),
			sync:         cloned,
			dispatchLifecycle: func(context.Context, []lifecyclehooks.Event) (lifecyclehooks.Report, error) {
				return lifecyclehooks.Report{Warnings: []string{"hook w1"}}, nil
			},
		}
		result, err := processor.Process(context.Background(), receiverEvent("event-dispatch-warning"), ProcessState{})
		if err != nil {
			t.Fatalf("dispatch warning error = %v", err)
		}
		if !strings.Contains(result.Detail, "hook warning") || !strings.Contains(result.Detail, "hook w1") {
			t.Fatalf("dispatch warning detail = %q", result.Detail)
		}
	})
}

func TestSdCovVerifyGitHubOrigin(t *testing.T) {
	if err := verifyGitHubOrigin(t.TempDir(), "github.com/acme/app"); err == nil {
		t.Fatal("verifyGitHubOrigin accepted a non-repository path")
	}

	path := filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, path, "init", "-b", "main")
	runGit(t, path, "remote", "add", "origin", "git@github.com:acme/app.git")
	if err := verifyGitHubOrigin(path, "github.com/acme/app"); err != nil {
		t.Fatalf("matching origin error = %v", err)
	}
}

func TestSdCovProcessorReportsCanonicalPathInspectionFailure(t *testing.T) {
	projects := t.TempDir()
	if err := os.WriteFile(filepath.Join(projects, "acme"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	processor := SyncProcessor{ProjectsRoot: projects}
	if _, err := processor.Process(context.Background(), receiverEvent("event-canonical-stat-failure"), ProcessState{}); err == nil {
		t.Fatal("Process ignored a canonical path inspection failure")
	}
}

// TestSdCovProcessorReportsTrackingFailure drives gitops.Tracking to its
// error branch with a fake git on PATH: symbolic-ref and rev-parse succeed but
// rev-list reports a single field instead of the expected ahead/behind pair.
func TestSdCovProcessorReportsTrackingFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake git shim requires a POSIX shell")
	}
	projects := t.TempDir()
	path := filepath.Join(projects, "acme", "app")
	if err := os.MkdirAll(filepath.Join(path, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	shim := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n  symbolic-ref) echo main; exit 0 ;;\n  config) echo refs/heads/main; exit 0 ;;\n  rev-parse) echo origin/main; exit 0 ;;\n  rev-list) echo only-one-field; exit 0 ;;\nesac\nexit 0\n"
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shim+string(os.PathListSeparator)+os.Getenv("PATH"))

	processor := SyncProcessor{ProjectsRoot: projects, verifyOrigin: func(string, string) error { return nil }}
	if _, err := processor.Process(context.Background(), receiverEvent("event-tracking-failure"), ProcessState{}); err == nil || !strings.Contains(err.Error(), "unexpected rev-list output") {
		t.Fatalf("tracking failure error = %v", err)
	}
}
