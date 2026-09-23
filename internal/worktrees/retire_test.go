package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestRetireResumesAfterRemotePhases(t *testing.T) {
	for _, phase := range []string{"source_published", "archive_published", "original_deleted", "worktree_removed"} {
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
			if phase == "worktree_removed" {
				wantPartialPhase = "original_deleted"
			}
			if err == nil || !strings.Contains(err.Error(), "simulated interruption") || partial.Phase != wantPartialPhase {
				t.Fatalf("partial phase=%s err=%v", partial.Phase, err)
			}
			options.afterPhase = nil
			finished, err := Retire(context.Background(), options)
			if err != nil {
				t.Fatal(err)
			}
			if finished.Phase != "complete" || finished.SourceSHA != partial.SourceSHA || finished.RetiredRef != partial.RetiredRef {
				t.Fatalf("resume changed identity: partial=%#v finished=%#v", partial, finished)
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
			if refs := gitTestOutput(t, fixture.canonical, "ls-remote", "origin", "refs/heads/retired/*"); refs != "" {
				t.Fatalf("refusal published retired ref: %s", refs)
			}
		})
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
		Inspect: func(_ context.Context, repository string) (RetiredArchiveInspection, error) {
			return RetiredArchiveInspection{Exists: true, Private: true, Repository: repository}, nil
		},
		OpenPullRequests: func(context.Context, string, string, string) (bool, error) { return false, nil },
	})
	if err == nil || !strings.Contains(err.Error(), "remote branch moved") {
		t.Fatalf("changed ref error: %v", err)
	}
}
