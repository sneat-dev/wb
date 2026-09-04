package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestSessionReceiveRepositoryFromRemoteIsStrict(t *testing.T) {
	local := filepath.Join(t.TempDir(), "remotes", "acme", "app.git")
	tests := []struct {
		remote string
		want   string
		ok     bool
	}{
		{remote: "git@github.com:acme/app.git", want: "acme/app", ok: true},
		{remote: "ssh://github.com/acme/app.git", want: "acme/app", ok: true},
		{remote: "ssh://git@github.com/acme/app.git", want: "acme/app", ok: true},
		{remote: "https://github.com/acme/app.git", want: "acme/app", ok: true},
		{remote: local, want: "acme/app", ok: true},
		{remote: "ssh://git:secret@github.com/acme/app.git"},
		{remote: "https://user:secret@github.com/acme/app.git"},
		{remote: "relative/acme/app.git"},
		{remote: "https://github.com/prefix/acme/app.git"},
		{remote: "https://github.com/acme/../app.git"},
		{remote: "-option-like"},
		{remote: "https://github.com/acme/app.git?token=secret"},
	}
	for _, test := range tests {
		t.Run(test.remote, func(t *testing.T) {
			got, err := sessionReceiveRepositoryFromRemote(test.remote)
			if test.ok && (err != nil || got != test.want) {
				t.Fatalf("repository = %q, err = %v, want %q", got, err, test.want)
			}
			if !test.ok && err == nil {
				t.Fatalf("repository = %q, want strict refusal", got)
			}
		})
	}
}

func TestReceiveSessionBundleSecurelyClonesMissingCanonicalRepository(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	if err := os.RemoveAll(fixture.canonical); err != nil {
		t.Fatal(err)
	}

	created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot: fixture.projectsRoot,
		Request:      fixture.request,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCanonical, err := filepath.EvalSymlinks(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	if created.Reused || created.CanonicalDir != wantCanonical {
		t.Fatalf("created result = %#v", created)
	}
	if got := gitTestOutput(t, fixture.canonical, "remote", "get-url", "origin"); got != fixture.remote {
		t.Fatalf("cloned origin = %q, want %q", got, fixture.remote)
	}
	if got := gitTestOutput(t, fixture.canonical, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
		t.Fatalf("new canonical clone is dirty: %q", got)
	}
	if got := gitTestOutput(t, created.WorktreeDir, "rev-parse", "HEAD"); got != fixture.request.BundleCommit {
		t.Fatalf("target HEAD = %s, want %s", got, fixture.request.BundleCommit)
	}
}

func TestReceiveSessionBundleCreatesAndReusesExactPinnedWorktree(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	planned, err := SessionReceiveWorktreePath(fixture.projectsRoot, fixture.request)
	if err != nil || !sameSessionReceivePath(planned, fixture.targetWorktree()) {
		t.Fatalf("planned worktree = %q, err = %v, want %q", planned, err, fixture.targetWorktree())
	}
	fetchHeadPath := filepath.Join(fixture.canonical, ".git", "FETCH_HEAD")
	fetchHeadSentinel := []byte("caller-owned FETCH_HEAD sentinel\n")
	if err := os.WriteFile(fetchHeadPath, fetchHeadSentinel, 0o644); err != nil {
		t.Fatal(err)
	}

	created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot: fixture.projectsRoot,
		Request:      fixture.request,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Repository != "acme/app" || created.Commit != fixture.request.BundleCommit || created.Reused {
		t.Fatalf("created result = %#v", created)
	}
	wantPath := fixture.targetWorktree()
	if created.WorktreeDir != wantPath {
		t.Fatalf("worktree = %q, want %q", created.WorktreeDir, wantPath)
	}
	if got := gitTestOutput(t, created.WorktreeDir, "rev-parse", "HEAD"); got != fixture.request.BundleCommit {
		t.Fatalf("target HEAD = %s, want %s", got, fixture.request.BundleCommit)
	}
	if got := gitTestOutput(t, created.WorktreeDir, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
		t.Fatalf("target worktree dirty: %q", got)
	}
	if got, err := os.ReadFile(fetchHeadPath); err != nil || string(got) != string(fetchHeadSentinel) {
		t.Fatalf("shared FETCH_HEAD changed to %q, err = %v", got, err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "--verify", sessionReceiveFetchRef(fixture.request.HandoffID)); got != fixture.request.BundleCommit {
		t.Fatalf("private fetched tip = %s, want %s", got, fixture.request.BundleCommit)
	}
	if raw, err := os.ReadFile(filepath.Join(created.WorktreeDir, filepath.FromSlash(fixture.request.HandoverPath))); err != nil || string(raw) != string(fixture.handover) {
		t.Fatalf("target handover = %q, err = %v", raw, err)
	}

	reused, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot: fixture.projectsRoot,
		Request:      fixture.request,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.WorktreeDir != created.WorktreeDir || reused.Commit != created.Commit {
		t.Fatalf("reused result = %#v, created = %#v", reused, created)
	}
	if count := strings.Count(gitTestOutput(t, fixture.canonical, "worktree", "list", "--porcelain"), "worktree "+created.WorktreeDir); count != 1 {
		t.Fatalf("target worktree registration count = %d, want 1", count)
	}
}

func TestReceiveSessionBundleUsesConfiguredSharedRoot(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	configHome := t.TempDir()
	sharedRoot := filepath.Join(t.TempDir(), "shared-worktrees")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+sharedRoot+"\n")
	planned, err := SessionReceiveWorktreePath(fixture.projectsRoot, fixture.request)
	physicalSharedRoot, err := resolvePlacementPath(sharedRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(physicalSharedRoot, "session-"+fixture.request.HandoffID, "acme", "app")
	if err != nil || !sameSessionReceivePath(planned, want) {
		t.Fatalf("planned worktree = %q, err = %v, want configured shared path %q", planned, err, want)
	}

	created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot: fixture.projectsRoot,
		Request:      fixture.request,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sameSessionReceivePath(created.WorktreeDir, want) {
		t.Fatalf("worktree = %q, want configured shared path %q", created.WorktreeDir, want)
	}
	if _, err := os.Stat(fixture.targetWorktree()); !os.IsNotExist(err) {
		t.Fatalf("local default path exists after configured placement: %v", err)
	}
}

func TestReceiveSessionBundleReusesRegisteredPathAfterPlacementChanges(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot: fixture.projectsRoot,
		Request:      fixture.request,
	})
	if err != nil {
		t.Fatal(err)
	}
	configHome := t.TempDir()
	sharedRoot := filepath.Join(t.TempDir(), "shared-worktrees")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	mustWriteBranchConfig(t, filepath.Join(configHome, "wb", "worktrees.yaml"), "version: 1\nworktrees:\n  root: "+sharedRoot+"\n")

	replayed, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot: fixture.projectsRoot,
		Request:      fixture.request,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Reused || !sameSessionReceivePath(replayed.WorktreeDir, created.WorktreeDir) {
		t.Fatalf("replayed result = %#v, created = %#v", replayed, created)
	}
	fence, digest := acquireSessionReceiveFence(t, fixture, filepath.Join(fixture.home, sessionmove.DirName))
	verified, err := VerifyReceivedSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot: fixture.projectsRoot, Request: fixture.request,
		RequestDigest: digest, ExecutionLock: fence,
	})
	if err != nil || !sameSessionReceivePath(verified.WorktreeDir, created.WorktreeDir) {
		t.Fatalf("verified result = %#v, err = %v, want %q", verified, err, created.WorktreeDir)
	}
	path, err := SessionReceiveWorktreePath(fixture.projectsRoot, fixture.request)
	if err != nil || !sameSessionReceivePath(path, created.WorktreeDir) {
		t.Fatalf("recovered session path = %q, err = %v, want %q", path, err, created.WorktreeDir)
	}
	configuredPath := filepath.Join(sharedRoot, "session-"+fixture.request.HandoffID, "acme", "app")
	if _, err := os.Stat(configuredPath); !os.IsNotExist(err) {
		t.Fatalf("changed placement created a second session checkout: %v", err)
	}
}

func TestVerifyReceivedSessionBundleIgnoresLaterRemoteAdvance(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request})
	if err != nil {
		t.Fatal(err)
	}
	fixture.advanceBranch(t, "legitimate later source work")
	fence, digest := acquireSessionReceiveFence(t, fixture, filepath.Join(fixture.home, sessionmove.DirName))
	verified, err := VerifyReceivedSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot,
		Request: fixture.request, RequestDigest: digest, ExecutionLock: fence})
	if err != nil {
		t.Fatal(err)
	}
	if !verified.Reused || verified.WorktreeDir != created.WorktreeDir || verified.Commit != fixture.request.BundleCommit {
		t.Fatalf("verified=%#v created=%#v", verified, created)
	}
}

func TestReceiveSessionBundleReclaimsInterruptedOperationLockUnderExecutionFence(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	operation := "session-" + fixture.request.HandoffID
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	if err := os.MkdirAll(operationRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	lockContents := []byte("operation=" + operation + "\npid=2147483647\n")
	if err := os.WriteFile(filepath.Join(operationRoot, ".lock"), lockContents, 0o600); err != nil {
		t.Fatal(err)
	}
	fence, digest := acquireSessionReceiveFence(t, fixture, filepath.Join(fixture.home, sessionmove.DirName))

	if _, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot:  fixture.projectsRoot,
		Request:       fixture.request,
		RequestDigest: digest,
		ExecutionLock: fence,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(operationRoot, ".lock")); !os.IsNotExist(err) {
		t.Fatalf("interrupted operation lock was not safely retired: %v", err)
	}
}

func TestReceiveSessionBundleDoesNotReclaimInterruptedLockUnderSameHandoffFenceFromDifferentStore(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	operation := "session-" + fixture.request.HandoffID
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	if err := os.MkdirAll(operationRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(operationRoot, ".lock")
	lockContents := []byte("operation=" + operation + "\npid=2147483647\n")
	if err := os.WriteFile(lockPath, lockContents, 0o600); err != nil {
		t.Fatal(err)
	}
	fence, digest := acquireSessionReceiveFence(t, fixture, filepath.Join(fixture.root, "unrelated-store", sessionmove.DirName))

	if _, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot:  fixture.projectsRoot,
		Request:       fixture.request,
		RequestDigest: digest,
		ExecutionLock: fence,
	}); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("error = %v, want interrupted-lock refusal", err)
	}
	if got, err := os.ReadFile(lockPath); err != nil || string(got) != string(lockContents) {
		t.Fatalf("different handoff fence changed lock to %q, err = %v", got, err)
	}
	fixture.requireNoTargetWorktree(t)
}

func TestReceiveSessionBundleRecoversExactInterruptedReceivePublication(t *testing.T) {
	for _, state := range []string{"staged", "published_before_repair", "published_after_repair"} {
		t.Run(state, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			operation := "session-" + fixture.request.HandoffID
			lockRoot := fixture.operationLockRoot()
			operationRoot := fixture.physicalWorktreesRoot()
			stagePath := filepath.Join(operationRoot, ".wb-stage-0123456789abcdef0123456789abcdef")
			stageCheckout := filepath.Join(stagePath, "checkout")
			if err := os.MkdirAll(stagePath, 0o700); err != nil {
				t.Fatal(err)
			}
			lockContents := []byte("operation=" + operation + "\npid=2147483647\n")
			if err := os.MkdirAll(lockRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(lockRoot, ".lock"), lockContents, 0o600); err != nil {
				t.Fatal(err)
			}
			pinBranch := "wb-session/" + fixture.request.HandoffID
			gitTest(t, fixture.canonical, "worktree", "add", "--quiet", "-b", pinBranch, stageCheckout, fixture.request.BundleCommit)
			target := fixture.targetWorktree()
			if state != "staged" {
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(stageCheckout, target); err != nil {
					t.Fatal(err)
				}
				if state == "published_after_repair" {
					gitTest(t, fixture.canonical, "worktree", "repair", target)
				}
			}
			fence, digest := acquireSessionReceiveFence(t, fixture, filepath.Join(fixture.home, sessionmove.DirName))

			recovered, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
				ProjectsRoot: fixture.projectsRoot, Request: fixture.request,
				RequestDigest: digest, ExecutionLock: fence,
			})
			if err != nil {
				t.Fatal(err)
			}
			if recovered.WorktreeDir != target || !recovered.Reused {
				t.Fatalf("recovered result = %#v, want reused deterministic target %s", recovered, target)
			}
			if got := gitTestOutput(t, target, "rev-parse", "HEAD"); got != fixture.request.BundleCommit {
				t.Fatalf("recovered target HEAD = %s, want %s", got, fixture.request.BundleCommit)
			}
			occupied, registeredPath, err := branchWorktreeCanonical(context.Background(), mustOpenCanonical(t, recovered.CanonicalDir), pinBranch)
			if err != nil {
				t.Fatal(err)
			}
			if !occupied || filepath.Clean(registeredPath) != filepath.Clean(target) {
				t.Fatalf("recovered pin registration = occupied %t path %q, want %q", occupied, registeredPath, target)
			}
			if _, err := os.Lstat(stageCheckout); !os.IsNotExist(err) {
				t.Fatalf("interrupted staged checkout remains after recovery: %v", err)
			}
			if _, err := os.Lstat(stagePath); !os.IsNotExist(err) {
				t.Fatalf("active interrupted stage remains after recovery: %v", err)
			}
		})
	}
}

func TestReceiveSessionBundleNeverRecoversPinBranchFromArbitraryPath(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	operation := "session-" + fixture.request.HandoffID
	operationRoot := filepath.Join(fixture.home, "worktrees", operation)
	if err := os.MkdirAll(operationRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(operationRoot, ".lock"), []byte("operation="+operation+"\npid=2147483647\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	arbitrary := filepath.Join(fixture.root, "arbitrary-checkouts", "checkout")
	if err := os.MkdirAll(filepath.Dir(arbitrary), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", "--quiet", "-b", "wb-session/"+fixture.request.HandoffID, arbitrary, fixture.request.BundleCommit)
	fence, digest := acquireSessionReceiveFence(t, fixture, filepath.Join(fixture.home, sessionmove.DirName))

	if _, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
		ProjectsRoot: fixture.projectsRoot, Request: fixture.request,
		RequestDigest: digest, ExecutionLock: fence,
	}); err == nil {
		t.Fatal("arbitrary pin-branch checkout was recovered")
	}
	fixture.requireNoTargetWorktree(t)
	if got := gitTestOutput(t, arbitrary, "rev-parse", "HEAD"); got != fixture.request.BundleCommit {
		t.Fatalf("arbitrary checkout was changed to %s", got)
	}
}

func TestReceiveSessionBundleRefusesUnsafeInterruptedReceiveStage(t *testing.T) {
	tests := []struct {
		name      string
		stageName string
		mutate    func(t *testing.T, fixture *sessionReceiveFixture, operationRoot, checkout string)
		want      string
	}{
		{
			name: "reserved prefix without exact hex identity", stageName: ".wb-stage-crash-remnant",
			want: "outside its exact WB stage",
		},
		{
			name: "dirty checkout", stageName: ".wb-stage-0123456789abcdef0123456789abcdef",
			mutate: func(t *testing.T, _ *sessionReceiveFixture, _, checkout string) {
				if err := os.WriteFile(filepath.Join(checkout, "dirty.txt"), []byte("not admitted\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "dirty",
		},
		{
			name: "wrong commit", stageName: ".wb-stage-0123456789abcdef0123456789abcdef",
			mutate: func(t *testing.T, fixture *sessionReceiveFixture, _, checkout string) {
				gitTest(t, checkout, "reset", "--hard", fixture.request.SourceWorkCommit)
			},
			want: "pin branch",
		},
		{
			name: "ambiguous second active stage", stageName: ".wb-stage-0123456789abcdef0123456789abcdef",
			mutate: func(t *testing.T, _ *sessionReceiveFixture, operationRoot, _ string) {
				if err := os.Mkdir(filepath.Join(operationRoot, ".wb-stage-fedcba9876543210fedcba9876543210"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "ambiguous",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			operation := "session-" + fixture.request.HandoffID
			lockRoot := fixture.operationLockRoot()
			operationRoot := fixture.physicalWorktreesRoot()
			stageCheckout := filepath.Join(operationRoot, test.stageName, "checkout")
			if err := os.MkdirAll(filepath.Dir(stageCheckout), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(lockRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(lockRoot, ".lock")
			lockContents := []byte("operation=" + operation + "\npid=2147483647\n")
			if err := os.WriteFile(lockPath, lockContents, 0o600); err != nil {
				t.Fatal(err)
			}
			gitTest(t, fixture.canonical, "worktree", "add", "--quiet", "-b", "wb-session/"+fixture.request.HandoffID, stageCheckout, fixture.request.BundleCommit)
			if test.mutate != nil {
				test.mutate(t, fixture, operationRoot, stageCheckout)
			}
			fence, digest := acquireSessionReceiveFence(t, fixture, filepath.Join(fixture.home, sessionmove.DirName))

			if _, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{
				ProjectsRoot: fixture.projectsRoot, Request: fixture.request,
				RequestDigest: digest, ExecutionLock: fence,
			}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
			fixture.requireNoTargetWorktree(t)
			if got, err := os.ReadFile(lockPath); err != nil || string(got) != string(lockContents) {
				t.Fatalf("unsafe recovery changed interrupted lock to %q, err = %v", got, err)
			}
		})
	}
}

func TestReceiveSessionBundleFetchesExactRemoteDespiteOriginConfigTOCTOU(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	wrong := filepath.Join(fixture.root, "alternate-remotes", "acme", "app.git")
	options := SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request}
	options.afterFetchRemoteAuthentication = func() {
		gitTest(t, fixture.canonical, "remote", "set-url", "origin", wrong)
	}

	_, err := ReceiveSessionBundle(context.Background(), options)
	if err == nil || !strings.Contains(err.Error(), "origin logical identity") {
		t.Fatalf("error = %v, want post-fetch origin reauthentication refusal", err)
	}
	if got := gitTestOutput(t, fixture.canonical, "rev-parse", "--verify", sessionReceiveFetchRef(fixture.request.HandoffID)); got != fixture.request.BundleCommit {
		t.Fatalf("exact safe request remote was not fetched: got %s, want %s", got, fixture.request.BundleCommit)
	}
	fixture.requireNoTargetWorktree(t)
}

func TestReceiveSessionBundleRefusesMovedBranchBeforeTargetWorktree(t *testing.T) {
	fixture := newSessionReceiveFixture(t)
	fixture.advanceBranch(t, "remote moved after handoff")

	_, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request})
	if err == nil || !strings.Contains(err.Error(), "remote branch tip moved") {
		t.Fatalf("error = %v, want moved branch refusal", err)
	}
	fixture.requireNoTargetWorktree(t)
}

func TestReceiveSessionBundleRefusesInvalidBundleEvidenceBeforeTargetWorktree(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(t *testing.T, fixture *sessionReceiveFixture)
		wantErr string
	}{
		{
			name: "wrong canonical origin identity",
			mutate: func(t *testing.T, fixture *sessionReceiveFixture) {
				wrong := filepath.Join(fixture.root, "other-remotes", "acme", "app.git")
				gitTest(t, fixture.canonical, "remote", "set-url", "origin", wrong)
			},
			wantErr: "origin logical identity",
		},
		{
			name: "wrong origin host with same repository slug",
			mutate: func(t *testing.T, fixture *sessionReceiveFixture) {
				fixture.request.RepositoryRemote = "https://github.com/acme/app.git"
				gitTest(t, fixture.canonical, "remote", "set-url", "origin", "https://evil.example/acme/app.git")
			},
			wantErr: "origin logical identity",
		},
		{
			name: "missing source commit",
			mutate: func(_ *testing.T, fixture *sessionReceiveFixture) {
				fixture.request.SourceWorkCommit = strings.Repeat("f", 40)
			},
			wantErr: "source work commit is missing",
		},
		{
			name: "source commit is not ancestor",
			mutate: func(t *testing.T, fixture *sessionReceiveFixture) {
				tree := gitTestOutput(t, fixture.canonical, "rev-parse", fixture.request.BundleCommit+"^{tree}")
				fixture.request.SourceWorkCommit = gitTestOutput(t, fixture.canonical, "commit-tree", tree, "-m", "unrelated root")
			},
			wantErr: "not an ancestor",
		},
		{
			name: "handover digest mismatch",
			mutate: func(_ *testing.T, fixture *sessionReceiveFixture) {
				fixture.request.HandoverDigest = sessionmove.DigestBytes([]byte("different handover\n"))
			},
			wantErr: "handover digest mismatch",
		},
		{
			name: "handover blob missing from bundle commit",
			mutate: func(t *testing.T, fixture *sessionReceiveFixture) {
				gitTest(t, fixture.canonical, "checkout", fixture.request.Branch)
				gitTest(t, fixture.canonical, "rm", "--", fixture.request.HandoverPath)
				gitTest(t, fixture.canonical, "commit", "-m", "remove handover")
				gitTest(t, fixture.canonical, "push", "origin", "HEAD:refs/heads/"+fixture.request.Branch)
				fixture.request.BundleCommit = gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
				gitTest(t, fixture.canonical, "checkout", "main")
			},
			wantErr: "read tracked handover blob",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			test.mutate(t, fixture)
			_, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, test.wantErr)
			}
			fixture.requireNoTargetWorktree(t)
		})
	}
}

func TestReceiveSessionBundleRefusesUnsafeReuse(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, fixture *sessionReceiveFixture, worktree string)
		want   string
	}{
		{
			name: "dirty",
			mutate: func(t *testing.T, _ *sessionReceiveFixture, worktree string) {
				if err := os.WriteFile(filepath.Join(worktree, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			want: "dirty",
		},
		{
			name: "wrong head",
			mutate: func(t *testing.T, fixture *sessionReceiveFixture, worktree string) {
				gitTest(t, worktree, "reset", "--hard", fixture.request.SourceWorkCommit)
			},
			want: "pin branch",
		},
		{
			name: "detached head",
			mutate: func(t *testing.T, _ *sessionReceiveFixture, worktree string) {
				gitTest(t, worktree, "checkout", "--detach")
			},
			want: "pin branch",
		},
		{
			name: "wrong attached branch",
			mutate: func(t *testing.T, _ *sessionReceiveFixture, worktree string) {
				gitTest(t, worktree, "checkout", "-b", "wrong-target-branch")
			},
			want: "pin branch",
		},
		{
			name: "pin ref drift",
			mutate: func(t *testing.T, fixture *sessionReceiveFixture, _ string) {
				gitTest(t, fixture.canonical, "update-ref", "refs/heads/wb-session/"+fixture.request.HandoffID, fixture.request.SourceWorkCommit)
			},
			want: "pin branch",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newSessionReceiveFixture(t)
			created, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request})
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, fixture, created.WorktreeDir)
			if _, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("wrong common dir", func(t *testing.T) {
		fixture := newSessionReceiveFixture(t)
		target := fixture.targetWorktree()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		gitTest(t, filepath.Dir(target), "init", target)
		if _, err := ReceiveSessionBundle(context.Background(), SessionReceiveOptions{ProjectsRoot: fixture.projectsRoot, Request: fixture.request}); err == nil || !strings.Contains(err.Error(), "linked worktree") {
			t.Fatalf("error = %v, want wrong common-dir refusal", err)
		}
	})
}

type sessionReceiveFixture struct {
	root         string
	projectsRoot string
	home         string
	remote       string
	canonical    string
	request      sessionmove.Request
	handover     []byte
}

func newSessionReceiveFixture(t *testing.T) *sessionReceiveFixture {
	t.Helper()
	neutralizeGitSigning(t)
	root := t.TempDir()
	home := filepath.Join(root, ".wb")
	t.Setenv(wbhome.EnvOverride, home)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	remote := filepath.Join(root, "remotes", "acme", "app.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "init", "--bare", "--initial-branch=main", remote)
	projectsRoot := filepath.Join(root, "projects")
	canonical := filepath.Join(projectsRoot, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, root, "clone", remote, canonical)
	configureGitUser(t, canonical)
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("# app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "add", "README.md")
	gitTest(t, canonical, "commit", "-m", "initial")
	gitTest(t, canonical, "push", "-u", "origin", "main")
	sourceCommit := gitTestOutput(t, canonical, "rev-parse", "HEAD")
	branch := "feature/session-move"
	gitTest(t, canonical, "checkout", "-b", branch)
	handoverPath := ".wb/handoffs/handoff-123.md"
	handover := []byte("# exact portable handover\n\nContinue on target.\n")
	if err := os.MkdirAll(filepath.Join(canonical, ".wb", "handoffs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonical, filepath.FromSlash(handoverPath)), handover, 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, canonical, "add", "--", handoverPath)
	gitTest(t, canonical, "commit", "-m", "add session handover")
	gitTest(t, canonical, "push", "-u", "origin", "HEAD:refs/heads/"+branch)
	bundleCommit := gitTestOutput(t, canonical, "rev-parse", "HEAD")
	gitTest(t, canonical, "checkout", "main")
	request := sessionmove.Request{
		SchemaVersion: sessionmove.RequestSchemaVersion,
		HandoffID:     "handoff-123", SuccessorWBSessionID: "wbs-successor", PredecessorWBSessionID: "wbs-source",
		SourceMachine: "laptop", TargetMachine: "target-vm", RepositoryRemote: remote, Branch: branch,
		SourceWorkCommit: sourceCommit, BundleCommit: bundleCommit,
		HandoverPath: handoverPath, HandoverDigest: sessionmove.DigestBytes(handover),
		SourceRuntime: "codex", SourceModel: "gpt-5", RequestedHarness: "codex",
		WorkLogReference:   "worklog:session-move/session-move-run/" + strings.Repeat("a", 64),
		SourceOfferMessage: "Session handoff offered", SourceOfferNextAction: "Continue from " + handoverPath,
		CreatedAt: time.Date(2026, time.August, 25, 12, 30, 0, 0, time.UTC),
	}
	request.SourceOfferDigest = sessionmove.DigestSourceOffer(request.SourceOfferMessage, request.SourceOfferNextAction)
	resolvedHome, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	return &sessionReceiveFixture{
		root: root, projectsRoot: projectsRoot, home: resolvedHome, remote: remote, canonical: canonical,
		request: request, handover: handover,
	}
}

func (fixture *sessionReceiveFixture) targetWorktree() string {
	return filepath.Join(fixture.physicalCanonical(), ".worktrees", "session-"+fixture.request.HandoffID)
}

func (fixture *sessionReceiveFixture) operationLockRoot() string {
	return filepath.Join(fixture.home, "worktrees", "session-"+fixture.request.HandoffID)
}

func (fixture *sessionReceiveFixture) physicalWorktreesRoot() string {
	return filepath.Join(fixture.physicalCanonical(), ".worktrees")
}

func (fixture *sessionReceiveFixture) physicalCanonical() string {
	resolved, err := filepath.EvalSymlinks(fixture.canonical)
	if err != nil {
		return fixture.canonical
	}
	return resolved
}

func sameSessionReceivePath(left, right string) bool {
	resolve := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return path
	}
	return resolve(left) == resolve(right)
}

func (fixture *sessionReceiveFixture) requireNoTargetWorktree(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(fixture.targetWorktree()); !os.IsNotExist(err) {
		t.Fatalf("target worktree exists after refusal: %v", err)
	}
}

func (fixture *sessionReceiveFixture) advanceBranch(t *testing.T, message string) {
	t.Helper()
	gitTest(t, fixture.canonical, "checkout", fixture.request.Branch)
	if err := os.WriteFile(filepath.Join(fixture.canonical, "remote-moved.txt"), []byte(message+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "remote-moved.txt")
	gitTest(t, fixture.canonical, "commit", "-m", message)
	gitTest(t, fixture.canonical, "push", "origin", "HEAD:refs/heads/"+fixture.request.Branch)
	gitTest(t, fixture.canonical, "checkout", "main")
}

func acquireSessionReceiveFence(t *testing.T, fixture *sessionReceiveFixture, storeRoot string) (*sessionmove.ExecutionLock, sessionmove.Digest) {
	t.Helper()
	raw, err := sessionmove.EncodeRequest(fixture.request)
	if err != nil {
		t.Fatal(err)
	}
	digest := sessionmove.DigestBytes(raw)
	store := sessionmove.NewStore(storeRoot)
	if _, err := store.Admit(raw, digest); err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireExecutionLock(context.Background(), fixture.request.HandoffID, digest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fence.Close() })
	return fence, digest
}

func mustOpenCanonical(t *testing.T, path string) *canonicalRepository {
	t.Helper()
	canonical, err := openCanonicalRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(canonical.close)
	return canonical
}
