//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

//nolint:paralleltest // newGitFixture and related real Git commands configure process-wide test environment.
func TestTransferAbsentDestinationProofs(t *testing.T) {
	for _, scenario := range []string{"origin changed", "remote head changed", "fetched branch missing"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			fixture := newRepositoryTransferFixture(t)
			fixture.moveRemote(t)
			fixture.cloneDestination(t)
			head := gitTestOutput(t, fixture.destination, "rev-parse", "HEAD")
			receipt := repositoryTransferCleanupReceipt{DestinationDir: fixture.destination,
				RemoteURL: fixture.newRemote, DefaultBranch: "main", RemoteHead: head}
			switch scenario {
			case "origin changed":
				receipt.RemoteURL = fixture.oldRemote
			case "remote head changed":
				receipt.RemoteHead = strings.Repeat("0", 40)
			case "fetched branch missing":
				gitTest(t, fixture.destination, "update-ref", "-d", "refs/remotes/origin/main")
			}
			if _, err := absentRepositoryTransferCleanupOutcome(context.Background(), receipt); err == nil || !strings.Contains(err.Error(), "quarantine is absent") {
				t.Fatalf("%s: accepted proof, %v", scenario, err)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture and related real Git commands configure process-wide test environment.
func TestRepositoryTransferPlanningRefusals(t *testing.T) {
	for _, scenario := range []string{"fetch ambiguous", "push ambiguous", "source identity changed", "remote absent", "quarantine occupied", "destination stat denied"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			fixture := newRepositoryTransferFixture(t)
			fixture.moveRemote(t)
			options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main"}
			switch scenario {
			case "fetch ambiguous":
				gitTest(t, fixture.canonical, "remote", "set-url", "--add", "origin", fixture.newRemote)
			case "push ambiguous":
				gitTest(t, fixture.canonical, "remote", "set-url", "--push", "--add", "origin", fixture.newRemote)
				gitTest(t, fixture.canonical, "remote", "set-url", "--push", "--add", "origin", fixture.oldRemote)
			case "source identity changed":
				gitTest(t, fixture.canonical, "remote", "set-url", "origin", fixture.newRemote)
			case "remote absent":
				if err := os.Rename(fixture.newRemote, fixture.newRemote+"-hidden"); err != nil {
					t.Fatal(err)
				}
			case "quarantine occupied":
				fixture.cloneDestination(t)
				head := gitTestOutput(t, fixture.destination, "rev-parse", "HEAD")
				quarantine := filepath.Join(filepath.Dir(fixture.destination), ".wb-replaced-renamed-"+head[:12])
				if err := os.Mkdir(quarantine, 0o700); err != nil {
					t.Fatal(err)
				}
			case "destination stat denied":
				parent := filepath.Dir(fixture.destination)
				if err := os.MkdirAll(parent, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(parent, 0); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
			}
			got, err := RelocateRepository(context.Background(), options)
			if scenario == "quarantine occupied" {
				if err != nil || got.Eligible || !strings.Contains(got.Reason, "quarantine already exists") {
					t.Fatalf("%s = %#v, %v", scenario, got, err)
				}
			} else if err == nil {
				t.Fatalf("%s was accepted: %#v", scenario, got)
			}
			if _, statErr := os.Stat(fixture.canonical); statErr != nil {
				t.Fatalf("source changed during refused transfer: %v", statErr)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture and related real Git commands configure process-wide test environment.
func TestRepositoryTransferRestoresAfterCheckpointRefusal(t *testing.T) {
	fixture := newRepositoryTransferFixture(t)
	fixture.moveRemote(t)
	fixture.cloneDestination(t)
	got, err := RelocateRepository(context.Background(), RepositoryRelocateOptions{
		ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed",
		RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true,
		OnCleanupPending: func(path, command string) error {
			if path == "" || !strings.Contains(command, "repo transfer cleanup") {
				t.Fatalf("cleanup checkpoint = %q, %q", path, command)
			}
			return errors.New("checkpoint refused")
		},
	})
	if err == nil || !strings.Contains(err.Error(), "checkpoint replacement cleanup") || got.Applied {
		t.Fatalf("checkpoint refusal = %#v, %v", got, err)
	}
	if _, err := os.Stat(fixture.canonical); err != nil {
		t.Fatalf("source canonical lost: %v", err)
	}
	if _, err := os.Stat(fixture.destination); err != nil {
		t.Fatalf("disposable destination was not restored: %v", err)
	}
}

//nolint:paralleltest // newGitFixture and related real Git commands configure process-wide test environment.
func TestDisposableDestinationRefusals(t *testing.T) {
	for _, scenario := range []string{"fetch ambiguous", "push ambiguous", "push identity", "linked worktree", "wrong branch", "wrong head", "remote refs unavailable"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			fixture := newRepositoryTransferFixture(t)
			fixture.moveRemote(t)
			fixture.cloneDestination(t)
			head := gitTestOutput(t, fixture.destination, "rev-parse", "HEAD")
			options := RepositoryRelocateOptions{DestinationRepository: "newco/renamed", DefaultBranch: "main"}
			switch scenario {
			case "fetch ambiguous":
				gitTest(t, fixture.destination, "remote", "set-url", "--add", "origin", fixture.oldRemote)
			case "push identity":
				gitTest(t, fixture.destination, "remote", "set-url", "--push", "origin", fixture.oldRemote)
			case "push ambiguous":
				gitTest(t, fixture.destination, "remote", "set-url", "--push", "--add", "origin", fixture.newRemote)
				gitTest(t, fixture.destination, "remote", "set-url", "--push", "--add", "origin", fixture.oldRemote)
			case "linked worktree":
				gitTest(t, fixture.destination, "worktree", "add", filepath.Join(t.TempDir(), "linked"), "-b", "linked")
			case "wrong branch":
				gitTest(t, fixture.destination, "checkout", "-b", "other")
			case "wrong head":
				head = strings.Repeat("0", 40)
			case "remote refs unavailable":
				if err := os.Rename(fixture.newRemote, fixture.newRemote+"-hidden"); err != nil {
					t.Fatal(err)
				}
			}
			if reason := disposableDestinationReason(context.Background(), fixture.destination, options, head); reason == "" {
				t.Fatalf("%s was considered disposable", scenario)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture and related real Git commands configure process-wide test environment.
func TestRepositoryTransferCleanupInterruptions(t *testing.T) {
	for _, scenario := range []string{"intent publication denied", "retirement paused", "terminal publication paused", "restored terminal denied"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			fixture := newRepositoryTransferFixture(t)
			fixture.moveRemote(t)
			fixture.cloneDestination(t)
			options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
			reportDir := filepath.Join(fixture.projectsRoot, ".wb", "reports", "repository-transfers")
			switch scenario {
			case "intent publication denied":
				if err := os.MkdirAll(reportDir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(reportDir, 0o500); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(reportDir, 0o700) })
			case "retirement paused":
				options.beforeReplacementRetirement = func() error { return errors.New("retirement paused") }
			case "terminal publication paused":
				options.beforeReplacementCleanupCompleted = func() error { return errors.New("terminal publication paused") }
			case "restored terminal denied":
				options.OnCleanupPending = func(_, _ string) error {
					if err := os.Chmod(reportDir, 0o500); err != nil {
						t.Fatal(err)
					}
					return errors.New("checkpoint refused")
				}
				t.Cleanup(func() { _ = os.Chmod(reportDir, 0o700) })
			}
			got, err := RelocateRepository(context.Background(), options)
			switch scenario {
			case "intent publication denied", "restored terminal denied":
				if err == nil || got.Applied {
					t.Fatalf("%s = %#v, %v", scenario, got, err)
				}
				if _, statErr := os.Stat(fixture.canonical); statErr != nil {
					t.Fatalf("source not preserved: %v", statErr)
				}
				if _, statErr := os.Stat(fixture.destination); statErr != nil {
					t.Fatalf("replacement not restored: %v", statErr)
				}
			case "retirement paused", "terminal publication paused":
				if err != nil || !got.Applied || !got.CleanupPending || got.ReplacementCleanupReceipt == "" {
					t.Fatalf("%s = %#v, %v", scenario, got, err)
				}
				if _, statErr := os.Stat(got.ReplacementCleanupReceipt); statErr != nil {
					t.Fatalf("pending receipt lost: %v", statErr)
				}
			}
		})
	}
}

//nolint:paralleltest // newGitFixture and related real Git commands configure process-wide test environment.
func TestDiscardedBacklogProofRefusals(t *testing.T) {
	for _, scenario := range []string{"backlog absent", "invalid record", "nonterminal record", "local branch remains", "malformed neighbor"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			fixture := newGitFixture(t)
			head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
			worktreesRoot := filepath.Join(t.TempDir(), "worktrees")
			worktree := filepath.Join(worktreesRoot, "discard-task", "acme", "app")
			entry := ListResult{Task: "discard-task", Repository: "acme/app", CanonicalDir: fixture.canonical,
				WorktreesRoot: worktreesRoot, WorktreeDir: worktree, Branch: "wb/discard", Base: "main", HeadSHA: head}
			record := newLifecycleBacklogRecord(fixture.projectsRoot, entry, string(AbortDiscarded))
			if scenario != "backlog absent" {
				if scenario == "invalid record" {
					directory, err := openLifecycleBacklogDirectory(fixture.home, true)
					if err != nil {
						t.Fatal(err)
					}
					record.Version++
					if err := writeJSONImmutableAt(directory, record.ID+".json", record, true); err != nil {
						t.Fatal(err)
					}
					_ = directory.Close()
				} else {
					stage := lifecycleStageComplete
					if scenario == "nonterminal record" {
						stage = lifecycleStageSealed
					}
					if err := persistLifecycleBacklog(fixture.home, &record, stage); err != nil {
						t.Fatal(err)
					}
				}
			}
			if scenario == "local branch remains" {
				gitTest(t, fixture.canonical, "branch", "wb/discard")
			}
			if scenario == "malformed neighbor" {
				if err := os.WriteFile(filepath.Join(lifecycleBacklogDirectory(fixture.home), "neighbor.json"), []byte("{bad"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			proof, err := FindDiscardedLifecycleBacklogProof(context.Background(), fixture.projectsRoot, "acme/app", "main", "discard-task", worktree, "wb/discard", head)
			if scenario == "malformed neighbor" {
				if err != nil || proof == nil {
					t.Fatalf("valid proof lost among malformed neighbor: %#v, %v", proof, err)
				}
			} else if err == nil {
				t.Fatalf("%s accepted: %#v", scenario, proof)
			}
		})
	}
}

//nolint:paralleltest // newGitFixture and related real Git commands configure process-wide test environment.
func TestTaskBoundLocalStageRecovery(t *testing.T) {
	for _, scenario := range []string{"empty unregistered", "registered checkout", "destination occupied"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			fixture := newGitFixture(t)
			canonical, err := openCanonicalRepository(fixture.canonical)
			if err != nil {
				t.Fatal(err)
			}
			defer canonical.close()
			root := filepath.Join(t.TempDir(), "local")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			task := "recover-local"
			stage := filepath.Join(root, taskBoundLocalStagePrefix(task)+"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
			if err := os.Mkdir(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			if scenario != "empty unregistered" {
				gitTest(t, fixture.canonical, "worktree", "add", filepath.Join(stage, "checkout"), "-b", "wb/recover-local")
			}
			if scenario == "destination occupied" {
				if err := os.Mkdir(filepath.Join(root, task), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			recovered, err := recoverTaskBoundLocalStage(context.Background(), canonical, root, task, "wb/recover-local")
			switch scenario {
			case "empty unregistered":
				if err != nil || recovered {
					t.Fatalf("empty stage recovery = %v, %v", recovered, err)
				}
			case "registered checkout":
				if err != nil || !recovered {
					t.Fatalf("registered stage recovery = %v, %v", recovered, err)
				}
				if _, err := os.Stat(filepath.Join(root, task, ".git")); err != nil {
					t.Fatalf("published checkout missing: %v", err)
				}
			case "destination occupied":
				if err == nil || recovered {
					t.Fatalf("occupied destination recovery = %v, %v", recovered, err)
				}
				if _, err := os.Stat(filepath.Join(stage, "checkout")); err != nil {
					t.Fatalf("staged checkout lost: %v", err)
				}
			}
		})
	}
}

//nolint:paralleltest // newGitFixture and related real Git commands configure process-wide test environment.
func TestRepositoryTransferAfterMoveFailures(t *testing.T) {
	for _, scenario := range []string{"quarantine contents unreadable", "terminal receipt unwritable", "quarantine moved before restore", "source moved before publish"} {
		//nolint:paralleltest // the case configures WB environment or mutates a shared filesystem fixture.
		t.Run(scenario, func(t *testing.T) {
			fixture := newRepositoryTransferFixture(t)
			fixture.moveRemote(t)
			fixture.cloneDestination(t)
			head := gitTestOutput(t, fixture.destination, "rev-parse", "HEAD")
			quarantine := filepath.Join(filepath.Dir(fixture.destination), ".wb-replaced-renamed-"+head[:12])
			reportDir := filepath.Join(fixture.projectsRoot, ".wb", "reports", "repository-transfers")
			options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed", RemoteURL: fixture.newRemote, DefaultBranch: "main", Apply: true}
			switch scenario {
			case "quarantine contents unreadable":
				options.beforeReplacementRetirement = func() error {
					child := filepath.Join(quarantine, "unreadable")
					if err := os.Mkdir(child, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(child, "evidence"), []byte("keep"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(child, 0); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(child, 0o700) })
					return nil
				}
			case "terminal receipt unwritable":
				options.beforeReplacementCleanupCompleted = func() error {
					if err := os.Chmod(reportDir, 0o500); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(reportDir, 0o700) })
					return nil
				}
			case "quarantine moved before restore":
				options.OnCleanupPending = func(_, _ string) error {
					if err := os.Rename(quarantine, quarantine+"-saved"); err != nil {
						t.Fatal(err)
					}
					return errors.New("checkpoint refused")
				}
			case "source moved before publish":
				options.OnCleanupPending = func(_, _ string) error {
					if err := os.Rename(fixture.canonical, fixture.canonical+"-saved"); err != nil {
						t.Fatal(err)
					}
					return nil
				}
			}
			got, err := RelocateRepository(context.Background(), options)
			switch scenario {
			case "quarantine contents unreadable", "terminal receipt unwritable":
				if err != nil || !got.Applied || !got.CleanupPending {
					t.Fatalf("%s = %#v, %v", scenario, got, err)
				}
			case "quarantine moved before restore":
				if err == nil || got.Applied || !strings.Contains(err.Error(), "restore quarantined destination") {
					t.Fatalf("restore drift = %#v, %v", got, err)
				}
				if _, statErr := os.Stat(quarantine + "-saved"); statErr != nil {
					t.Fatalf("moved quarantine lost: %v", statErr)
				}
			case "source moved before publish":
				if err == nil || got.Applied || !strings.Contains(err.Error(), "move canonical repository") {
					t.Fatalf("source drift = %#v, %v", got, err)
				}
				if _, statErr := os.Stat(fixture.canonical + "-saved"); statErr != nil {
					t.Fatalf("moved source lost: %v", statErr)
				}
				if _, statErr := os.Stat(fixture.destination); statErr != nil {
					t.Fatalf("replacement not restored: %v", statErr)
				}
			}
		})
	}
}
