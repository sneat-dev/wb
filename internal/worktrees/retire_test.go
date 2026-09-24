package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func retireAllowRemoteOwner(context.Context, string) error { return nil }

func TestRetireBareRemotePreservesSourceAndPlainWorkLog(t *testing.T) {
	fixture := newGitFixture(t)
	prompt := filepath.Join(t.TempDir(), "prompt.txt")
	if err := os.WriteFile(prompt, []byte("private instruction\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retire-demo", WorkLog: WorkLogOptions{Model: "unknown", OriginalPrompt: prompt},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if err := os.WriteFile(filepath.Join(worktree, "retained.txt"), []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "push", "-u", "origin", "retire-demo")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	options := RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-demo", ArchiveRemote: archive,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	}
	planned, err := Retire(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if planned.Phase != "planned" {
		t.Fatalf("plan: %#v", planned)
	}
	if head := gitTestOutput(t, worktree, "rev-parse", "HEAD"); head != planned.SourceSHA {
		t.Fatalf("dry run moved HEAD")
	}
	options.Apply = true
	applied, err := Retire(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Phase != "complete" {
		t.Fatalf("result: %#v", applied)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree remains: %v", err)
	}
	if original := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/retire-demo"); original != "" {
		t.Fatalf("original ref remains: %s", original)
	}
	retired := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/"+applied.RetiredRef)
	if !strings.HasPrefix(retired, applied.SourceSHA+"\t") {
		t.Fatalf("retired ref: %q", retired)
	}
	if content := gitTestOutput(t, fixture.canonical, "show", applied.SourceSHA+":retained.txt"); content != "keep me" {
		t.Fatalf("source content: %q", content)
	}
	archived := gitTestOutput(t, fixture.canonical, "ls-remote", archive, "refs/heads/"+applied.ArchiveRef)
	if !strings.HasPrefix(archived, applied.ArchiveSHA+"\t") {
		t.Fatalf("archive ref: %q", archived)
	}
	gitTest(t, fixture.canonical, "fetch", archive, "refs/heads/"+applied.ArchiveRef)
	if body := gitTestOutput(t, fixture.canonical, "show", "FETCH_HEAD:worklog/run/claims/"+applied.ClaimID+".json"); !strings.Contains(body, applied.ClaimID) {
		t.Fatalf("archive claim: %q", body)
	}
	if body := gitTestOutput(t, fixture.canonical, "show", "FETCH_HEAD:worklog/run/terminals/"+applied.ClaimID+".json"); !strings.Contains(body, `"worktree_disposition": "retired"`) {
		t.Fatalf("archive terminal: %q", body)
	}
	if body := gitTestOutput(t, fixture.canonical, "show", "FETCH_HEAD:worklog/run/original-prompt.txt"); body != "private instruction" {
		t.Fatalf("private prompt not readable in archive")
	}
	if _, err := gitTestRun(fixture.canonical, "show", applied.SourceSHA+":original-prompt.txt"); err == nil {
		t.Fatal("private prompt entered source commit")
	}
	if body := gitTestOutput(t, fixture.canonical, "show", "FETCH_HEAD:retirement.json"); strings.Contains(body, "keep me") {
		t.Fatalf("archive manifest contains source")
	}
}

func TestRetireBareRemotePreservesSourceTagAndDeletesOriginalBranch(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-tag", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retire-tag")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	result, err := Retire(context.Background(), RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-tag", Preserve: "tag", ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Preserve != "tag" || result.Phase != "complete" {
		t.Fatalf("result = %#v", result)
	}
	if got := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/tags/"+result.RetiredRef); !strings.HasPrefix(got, result.SourceSHA+"\t") {
		t.Fatalf("retired tag = %q", got)
	}
	if got := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/"+result.RetiredRef); got != "" {
		t.Fatalf("retired branch = %q", got)
	}
	if got := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/retire-tag"); got != "" {
		t.Fatalf("original branch remains = %q", got)
	}
}

func TestRetireTagModeRejectsResumeAsBranch(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-mode", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retire-mode")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	options := RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-mode", Preserve: "tag", ArchiveRemote: archive, Apply: true, RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
		afterPhase: func(phase string) error {
			if phase == "source_published" {
				return errors.New("stop")
			}
			return nil
		},
	}
	if _, err := Retire(context.Background(), options); err == nil {
		t.Fatal("expected interruption")
	}
	options.Preserve, options.afterPhase = "branch", nil
	if _, err := Retire(context.Background(), options); err == nil || !strings.Contains(err.Error(), "receipt conflicts") {
		t.Fatalf("mode switch error = %v", err)
	}
}

func TestRetireResumesAfterRemotePhases(t *testing.T) {
	for _, phase := range []string{"source_committed", "source_published", "archive_published", "original_delete_intent", "original_delete_pushed", "original_deleted", "worktree_removed"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newGitFixture(t)
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-resume", WorkLog: WorkLogOptions{Model: "unknown"}})
			if err != nil {
				t.Fatal(err)
			}
			worktree := created[0].WorktreeDir
			if err := os.WriteFile(filepath.Join(worktree, "resume.txt"), []byte("resume\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			gitTest(t, worktree, "push", "-u", "origin", "retire-resume")
			archive := filepath.Join(t.TempDir(), "archive.git")
			gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
			options := RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-resume", ArchiveRemote: archive, Apply: true,
				RemoteOwnership: retireAllowRemoteOwner,
				Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
					return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
				},
				OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
				afterPhase: func(got string) error {
					if got == phase {
						return errors.New("simulated interruption")
					}
					return nil
				},
			}
			partial, err := Retire(context.Background(), options)
			wantPartialPhase := phase
			if phase == "source_committed" {
				wantPartialPhase = "commit_intent"
			}
			if phase == "worktree_removed" {
				wantPartialPhase = "original_deleted"
			}
			if phase == "original_delete_intent" || phase == "original_delete_pushed" {
				wantPartialPhase = "archive_published"
				if partial.DeleteIntentSHA != partial.OriginalRemoteSHA || partial.DeleteIntentSHA == "" {
					t.Fatalf("missing durable deletion intent: %#v", partial)
				}
			}
			if err == nil || !strings.Contains(err.Error(), "simulated interruption") || partial.Phase != wantPartialPhase {
				t.Fatalf("partial phase=%s err=%v", partial.Phase, err)
			}
			if phase == "original_delete_pushed" {
				proof := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", retireDeletionProofRef(partial))
				if !strings.HasPrefix(proof, partial.SourceSHA+"\t") {
					t.Fatalf("atomic deletion proof missing after push: %s", proof)
				}
			}
			options.afterPhase = nil
			finished, err := Retire(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if finished.Phase != "complete" {
				t.Fatalf("resume changed identity: partial=%#v finished=%#v", partial, finished)
			}
			if phase == "source_committed" {
				if finished.IntentParentSHA != partial.SourceSHA || finished.RetiredRef != retiredBranchDestination(partial.IntentAt, partial.Branch, finished.SourceSHA) {
					t.Fatalf("resume did not honor durable commit intent: partial=%#v finished=%#v", partial, finished)
				}
			} else if finished.SourceSHA != partial.SourceSHA || finished.RetiredRef != partial.RetiredRef {
				t.Fatalf("resume changed identity: partial=%#v finished=%#v", partial, finished)
			}
		})
	}
}

func TestRetirePreservesUnpushedCommits(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-ahead", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retire-ahead")
	remoteBefore := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(worktree, "ahead.txt"), []byte("unpublished\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "ahead.txt")
	gitTest(t, worktree, "commit", "-m", "local source commit")
	want := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	result, err := Retire(context.Background(), RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-ahead", ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSHA != want || result.OriginalRemoteSHA != remoteBefore {
		t.Fatalf("unpushed commit not preserved: %#v", result)
	}
	if body := gitTestOutput(t, fixture.canonical, "show", result.SourceSHA+":ahead.txt"); body != "unpublished" {
		t.Fatalf("retired source lost unpushed commit: %q", body)
	}
}

func TestRetireRefusesExternallyDeletedOriginalWithoutAtomicProof(t *testing.T) {
	for _, interruptedAt := range []string{"archive_published", "original_delete_intent"} {
		t.Run(interruptedAt, func(t *testing.T) {
			fixture := newGitFixture(t)
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-external", WorkLog: WorkLogOptions{Model: "unknown"}})
			if err != nil {
				t.Fatal(err)
			}
			worktree := created[0].WorktreeDir
			gitTest(t, worktree, "push", "-u", "origin", "retire-external")
			archive := filepath.Join(t.TempDir(), "archive.git")
			gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
			options := RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-external", ArchiveRemote: archive, Apply: true,
				RemoteOwnership: retireAllowRemoteOwner,
				Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
					return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
				},
				OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
				afterPhase: func(phase string) error {
					if phase == interruptedAt {
						return errors.New("pause before WB deletion")
					}
					return nil
				},
			}
			partial, err := Retire(context.Background(), options)
			if err == nil || !strings.Contains(err.Error(), "pause before WB deletion") {
				t.Fatalf("interruption: %#v %v", partial, err)
			}
			gitTest(t, worktree, "push", "origin", ":refs/heads/retire-external")
			options.afterPhase = nil
			_, err = Retire(context.Background(), options)
			if err == nil {
				t.Fatal("external deletion without atomic WB proof was accepted")
			}
			if proof := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", retireDeletionProofRef(partial)); proof != "" {
				t.Fatalf("external deletion produced WB proof: %s", proof)
			}
			if _, err := os.Stat(worktree); err != nil {
				t.Fatalf("external deletion removed local checkout: %v", err)
			}
		})
	}
}

func TestRetireRefusesRemoteWithoutAtomicPush(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-atomic", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retire-atomic")
	originalSHA := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	gitTest(t, fixture.remote, "config", "receive.advertiseAtomic", "false")
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	partial, err := Retire(context.Background(), RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-atomic", ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "atomic") {
		t.Fatalf("non-atomic remote accepted: partial=%#v err=%v", partial, err)
	}
	if original := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/retire-atomic"); !strings.HasPrefix(original, originalSHA+"\t") {
		t.Fatalf("non-atomic remote lost original: %s", original)
	}
	if proof := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", retireDeletionProofRef(partial)); proof != "" {
		t.Fatalf("non-atomic remote created proof: %s", proof)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Fatalf("non-atomic remote removed checkout: %v", err)
	}
}

func TestRetireRefusesIgnoredFilesAndUntrackedSymlinksBeforeStaging(t *testing.T) {
	for _, kind := range []string{"ignored", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			fixture := newGitFixture(t)
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-extra", WorkLog: WorkLogOptions{Model: "unknown"}})
			if err != nil {
				t.Fatal(err)
			}
			worktree := created[0].WorktreeDir
			gitTest(t, worktree, "push", "-u", "origin", "retire-extra")
			if kind == "ignored" {
				if err := os.WriteFile(filepath.Join(worktree, ".gitignore"), []byte("build/\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Join(worktree, "build"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(worktree, "build", "cache.bin"), []byte("cannot lose\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Symlink("README.md", filepath.Join(worktree, "linked.md")); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "archive.git")
			gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
			_, err = Retire(context.Background(), RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-extra", ArchiveRemote: archive, Apply: true,
				RemoteOwnership: retireAllowRemoteOwner,
				Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
					return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
				},
				OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
			})
			if err == nil || !strings.Contains(err.Error(), kind) {
				t.Fatalf("%s refusal: %v", kind, err)
			}
			if staged := gitTestOutput(t, worktree, "diff", "--cached", "--name-only"); staged != "" {
				t.Fatalf("%s refusal changed index: %s", kind, staged)
			}
		})
	}
}

func TestRetireRefusesPublicArchiveBeforeMutation(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-public", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	before := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	_, err = Retire(context.Background(), RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-public", Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: false, Repository: repository}, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "public") {
		t.Fatalf("public archive error: %v", err)
	}
	if after := gitTestOutput(t, worktree, "rev-parse", "HEAD"); after != before {
		t.Fatalf("public refusal changed source")
	}
}

func TestRetireRefusesOpenPRSecretPathAndHookFailure(t *testing.T) {
	for _, reason := range []string{"open-pr", "secret-path", "hook-failure"} {
		t.Run(reason, func(t *testing.T) {
			fixture := newGitFixture(t)
			created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-refusal", WorkLog: WorkLogOptions{Model: "unknown"}})
			if err != nil {
				t.Fatal(err)
			}
			worktree := created[0].WorktreeDir
			gitTest(t, worktree, "push", "-u", "origin", "retire-refusal")
			before := gitTestOutput(t, worktree, "rev-parse", "HEAD")
			if reason == "secret-path" {
				if err := os.WriteFile(filepath.Join(worktree, ".env.production"), []byte("x=y\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if reason == "hook-failure" {
				if err := os.WriteFile(filepath.Join(worktree, "change.txt"), []byte("content\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture.canonical, ".git", "hooks", "pre-commit"), []byte("#!/bin/sh\nexit 37\n"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			archive := filepath.Join(t.TempDir(), "archive.git")
			gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
			_, err = Retire(context.Background(), RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-refusal", ArchiveRemote: archive, Apply: true,
				RemoteOwnership: retireAllowRemoteOwner,
				Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
					return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
				},
				OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return reason == "open-pr", nil },
			})
			if err == nil {
				t.Fatal("retirement unexpectedly succeeded")
			}
			if head := gitTestOutput(t, worktree, "rev-parse", "HEAD"); head != before {
				t.Fatalf("refusal moved source HEAD")
			}
			if reason == "secret-path" {
				if staged := gitTestOutput(t, worktree, "diff", "--cached", "--name-only"); staged != "" {
					t.Fatalf("secret refusal changed index: %s", staged)
				}
			}
			if refs := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/retired/*"); refs != "" {
				t.Fatalf("refusal published retired ref: %s", refs)
			}
		})
	}
}

func TestRetireCommitsDeletionOnlySourceChange(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-deletion", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	if err := os.WriteFile(filepath.Join(worktree, "remove.txt"), []byte("remove\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree, "add", "remove.txt")
	gitTest(t, worktree, "commit", "-m", "add removable file")
	gitTest(t, worktree, "push", "-u", "origin", "retire-deletion")
	before := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(worktree, "remove.txt")); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	result, err := Retire(context.Background(), RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-deletion", ArchiveRemote: archive, Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != "complete" || result.SourceSHA == before {
		t.Fatalf("deletion was not committed: %#v", result)
	}
	if _, err := gitTestRun(fixture.canonical, "show", result.SourceSHA+":remove.txt"); err == nil {
		t.Fatal("deleted source file still exists in retired commit")
	}
}

func TestRetireRefusesChangedOriginalRemote(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-moved", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retire-moved")
	writer := filepath.Join(t.TempDir(), "writer")
	gitTest(t, t.TempDir(), "clone", fixture.remote, writer)
	configureGitUser(t, writer)
	gitTest(t, writer, "checkout", "retire-moved")
	if err := os.WriteFile(filepath.Join(writer, "remote.txt"), []byte("moved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, writer, "add", "remote.txt")
	gitTest(t, writer, "commit", "-m", "remote moved")
	gitTest(t, writer, "push", "origin", "retire-moved")
	_, err = Retire(context.Background(), RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-moved", Apply: true,
		RemoteOwnership: retireAllowRemoteOwner,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "remote branch moved") {
		t.Fatalf("changed ref error: %v", err)
	}
}

func TestRetireRechecksRemoteOwnerBeforeSourceMutation(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "retire-owner", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0].WorktreeDir
	gitTest(t, worktree, "push", "-u", "origin", "retire-owner")
	before := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(worktree, "pending.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "archive.git")
	gitTest(t, t.TempDir(), "init", "--bare", "--initial-branch=main", archive)
	checks := 0
	_, err = Retire(context.Background(), RetireOptions{ProjectsRoot: fixture.projectsRoot, Task: "retire-owner", ArchiveRemote: archive, Apply: true,
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
		RemoteOwnership: func(context.Context, string) error {
			checks++
			if checks == 2 {
				return errors.New("remote VM claimed task")
			}
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "remote VM claimed task") || checks != 2 {
		t.Fatalf("owner recheck: checks=%d err=%v", checks, err)
	}
	if head := gitTestOutput(t, worktree, "rev-parse", "HEAD"); head != before {
		t.Fatalf("source moved despite remote owner refusal: %s", head)
	}
	if staged := gitTestOutput(t, worktree, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("source staged despite remote owner refusal: %s", staged)
	}
	if refs := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/retired/*"); refs != "" {
		t.Fatalf("source published despite remote owner refusal: %s", refs)
	}
}
