//go:build e2e

package worktrees

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// These direct readers intentionally keep their different policies: summary
// enumeration is tolerant of a lost/malformed candidate, while orphan lookup
// refuses the first malformed matching file and absence proof fails closed.
func TestE2EActiveClaimWalkRefusesRedirectedRootAndMalformedChildren(t *testing.T) {
	t.Parallel()
	t.Run("redirected worklogs root", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		outside := t.TempDir()
		sentinel := filepath.Join(outside, "keep")
		if err := os.WriteFile(sentinel, []byte("outside bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(home, "worklogs")); err != nil {
			t.Fatal(err)
		}
		visits := 0
		err := walkActiveWorkLogClaims(home, func(*os.File, string, workLogClaim) { visits++ })
		if (!errors.Is(err, syscall.ELOOP) && !errors.Is(err, syscall.ENOTDIR)) || visits != 0 {
			t.Fatalf("redirected root = visits %d, err %v; want no visit and no-follow refusal", visits, err)
		}
		if target, readErr := os.Readlink(filepath.Join(home, "worklogs")); readErr != nil || target != outside {
			t.Fatalf("redirected worklogs entry changed: %q, %v", target, readErr)
		}
		if got, readErr := os.ReadFile(sentinel); readErr != nil || !bytes.Equal(got, []byte("outside bytes")) {
			t.Fatalf("outside content changed: %q, %v", got, readErr)
		}
	})
	for _, child := range []string{"runs", "claims"} {
		t.Run(child+" is a regular file", func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			path := filepath.Join(home, "worklogs", "task")
			if child == "claims" {
				path = filepath.Join(path, "runs", "run")
			}
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			blocked := filepath.Join(path, child)
			before := []byte("occupied child")
			if err := os.WriteFile(blocked, before, 0o600); err != nil {
				t.Fatal(err)
			}
			visits := 0
			err := walkActiveWorkLogClaims(home, func(*os.File, string, workLogClaim) { visits++ })
			if !errors.Is(err, syscall.ENOTDIR) || visits != 0 {
				t.Fatalf("%s file = visits %d, err %v; want no visit and ENOTDIR", child, visits, err)
			}
			if got, readErr := os.ReadFile(blocked); readErr != nil || !bytes.Equal(got, before) {
				t.Fatalf("occupied %s bytes changed: %q, %v", child, got, readErr)
			}
		})
	}
}

func TestE2EActiveClaimWalkSkipsLaterUnreadableDirectoryAfterHeldVisit(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"effort", "run"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			first := publishClaimForEvidenceWalk(t, home, "a-task", "a-run")
			laterTask := "z-task"
			if scope == "run" {
				laterTask = "a-task"
			}
			_ = publishClaimForEvidenceWalk(t, home, laterTask, "z-run")
			later := filepath.Join(home, "worklogs", laterTask)
			if scope == "run" {
				later = filepath.Join(later, "runs", "z-run")
			}
			if info, err := os.Stat(later); err != nil || !info.IsDir() {
				t.Fatalf("later %s was not a directory before traversal: %v, %v", scope, info, err)
			}
			t.Cleanup(func() { _ = os.Chmod(later, 0o700) })
			visits := 0
			err := walkActiveWorkLogClaims(home, func(claims *os.File, claimID string, claim workLogClaim) {
				visits++
				if claimID != first.ClaimID || claim.EffortID != "a-task" {
					t.Errorf("wrong held first claim: id=%s, claim=%#v", claimID, claim)
				}
				var reread workLogClaim
				if readErr := readJSONAt(claims, claimID+".json", &reread); readErr != nil || reread.ClaimID != claimID {
					t.Errorf("held claims descriptor lost during visit: claim=%#v, err=%v", reread, readErr)
				}
				if chmodErr := os.Chmod(later, 0); chmodErr != nil {
					t.Errorf("make later %s unreadable: %v", scope, chmodErr)
				}
			})
			if err != nil || visits != 1 {
				t.Fatalf("unreadable later %s traversal = visits %d, err %v", scope, visits, err)
			}
			if err := os.Chmod(later, 0o700); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(later); err != nil || !info.IsDir() {
				t.Fatalf("later %s directory changed: %v, %v", scope, info, err)
			}
		})
	}
}

type orphanLookupDraftFixture struct {
	projectsRoot string
	userHome     string
	home         string
	claimPath    string
	claim        workLogClaim
	claimBytes   []byte
}

func newOrphanLookupDraftFixture(t *testing.T) orphanLookupDraftFixture {
	t.Helper()
	root := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv(wbhome.EnvOverride, root)
	home := filepath.Join(root, ".wb")
	published := publishClaimForEvidenceWalk(t, home, "task", "run")
	raw, err := os.ReadFile(published.ClaimPath)
	if err != nil {
		t.Fatal(err)
	}
	return orphanLookupDraftFixture{
		projectsRoot: root, userHome: userHome, home: home,
		claimPath: published.ClaimPath, claim: readOrphanTestClaim(t, published.ClaimPath), claimBytes: raw,
	}
}

func (fixture orphanLookupDraftFixture) assertNoTerminal(t *testing.T) {
	t.Helper()
	terminal := filepath.Join(filepath.Dir(filepath.Dir(fixture.claimPath)), "terminals", fixture.claim.ClaimID+".json")
	if _, err := os.Lstat(terminal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lookup wrote terminal %s: %v", terminal, err)
	}
}

func writeOrphanDraftClaim(t *testing.T, path string, claim workLogClaim) {
	t.Helper()
	raw, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

//nolint:paralleltest // HOME is pinned to isolate the retired read layout.
func TestE2EFindOrphanedClaimRequiresUniqueValidImmutableIdentity(t *testing.T) {
	//nolint:paralleltest // The fixture sets HOME and the WB root for this subtest.
	t.Run("one exact claim and no match", func(t *testing.T) {
		fixture := newOrphanLookupDraftFixture(t)
		got, err := findOrphanedClaim(fixture.projectsRoot, fixture.claim.Task, fixture.claim.ClaimID)
		if err != nil || got.home != fixture.home || !reflect.DeepEqual(got.claim, fixture.claim) {
			t.Fatalf("unique claim = %#v, %v; want exact published identity/home", got, err)
		}
		missing := strings.Repeat("f", 64)
		if missing == fixture.claim.ClaimID {
			missing = strings.Repeat("e", 64)
		}
		if _, err := findOrphanedClaim(fixture.projectsRoot, fixture.claim.Task, missing); err == nil ||
			!strings.Contains(err.Error(), "was not found") {
			t.Fatalf("missing claim lookup = %v", err)
		}
		if raw, err := os.ReadFile(fixture.claimPath); err != nil || !bytes.Equal(raw, fixture.claimBytes) {
			t.Fatalf("lookup changed immutable claim: %q, %v", raw, err)
		}
		fixture.assertNoTerminal(t)
	})
	//nolint:paralleltest // The fixture sets HOME and the WB root for this subtest.
	t.Run("dangling matching claim path", func(t *testing.T) {
		fixture := newOrphanLookupDraftFixture(t)
		if err := os.Remove(fixture.claimPath); err != nil {
			t.Fatal(err)
		}
		missing := filepath.Join(t.TempDir(), "missing-target")
		if err := os.Symlink(missing, fixture.claimPath); err != nil {
			t.Fatal(err)
		}
		_, err := findOrphanedClaim(fixture.projectsRoot, fixture.claim.Task, fixture.claim.ClaimID)
		if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "read orphaned claim") {
			t.Fatalf("dangling matching claim lookup = %v", err)
		}
		if got, readErr := os.Readlink(fixture.claimPath); readErr != nil || got != missing {
			t.Fatalf("matching symlink changed: %q, %v", got, readErr)
		}
		fixture.assertNoTerminal(t)
	})
	//nolint:paralleltest // The fixture sets HOME and the WB root for this subtest.
	t.Run("malformed matching JSON", func(t *testing.T) {
		fixture := newOrphanLookupDraftFixture(t)
		bad := []byte(`{"incomplete":`)
		if err := os.WriteFile(fixture.claimPath, bad, 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := findOrphanedClaim(fixture.projectsRoot, fixture.claim.Task, fixture.claim.ClaimID)
		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) || !strings.Contains(err.Error(), "parse orphaned claim") {
			t.Fatalf("malformed matching claim lookup = %v", err)
		}
		if got, readErr := os.ReadFile(fixture.claimPath); readErr != nil || !bytes.Equal(got, bad) {
			t.Fatalf("malformed claim bytes changed: %q, %v", got, readErr)
		}
		fixture.assertNoTerminal(t)
	})
	//nolint:paralleltest // The fixture sets HOME and the WB root for this subtest.
	t.Run("wrong task is ignored", func(t *testing.T) {
		fixture := newOrphanLookupDraftFixture(t)
		wrong := fixture.claim
		wrong.Task = "different-task"
		writeOrphanDraftClaim(t, fixture.claimPath, wrong)
		if _, err := findOrphanedClaim(fixture.projectsRoot, fixture.claim.Task, fixture.claim.ClaimID); err == nil ||
			!strings.Contains(err.Error(), "was not found") {
			t.Fatalf("wrong-task claim lookup = %v", err)
		}
		fixture.assertNoTerminal(t)
	})
	//nolint:paralleltest // The fixture sets HOME and the WB root for this subtest.
	t.Run("incomplete identity is refused", func(t *testing.T) {
		fixture := newOrphanLookupDraftFixture(t)
		invalid := fixture.claim
		invalid.BaseSHA = "not-an-object-id"
		writeOrphanDraftClaim(t, fixture.claimPath, invalid)
		if _, err := findOrphanedClaim(fixture.projectsRoot, fixture.claim.Task, fixture.claim.ClaimID); err == nil ||
			!strings.Contains(err.Error(), "validate orphaned claim") ||
			!strings.Contains(err.Error(), "immutable claim identity is incomplete or invalid") {
			t.Fatalf("incomplete identity lookup = %v", err)
		}
		fixture.assertNoTerminal(t)
	})
	//nolint:paralleltest // The fixture sets HOME and the WB root for this subtest.
	t.Run("changed valid identity has wrong digest", func(t *testing.T) {
		fixture := newOrphanLookupDraftFixture(t)
		invalid := fixture.claim
		invalid.Branch = "wb/other-branch"
		writeOrphanDraftClaim(t, fixture.claimPath, invalid)
		if _, err := findOrphanedClaim(fixture.projectsRoot, fixture.claim.Task, fixture.claim.ClaimID); err == nil ||
			!strings.Contains(err.Error(), "validate orphaned claim") ||
			!strings.Contains(err.Error(), "immutable claim digest mismatch") {
			t.Fatalf("changed-digest lookup = %v", err)
		}
		fixture.assertNoTerminal(t)
	})
	//nolint:paralleltest // The fixture sets HOME and the WB root for this subtest.
	t.Run("duplicate exact claims in distinct homes", func(t *testing.T) {
		fixture := newOrphanLookupDraftFixture(t)
		legacyHome := filepath.Join(fixture.userHome, ".wb")
		legacyClaim := filepath.Join(legacyHome, "worklogs", fixture.claim.EffortID, "runs", fixture.claim.RunID, "claims", fixture.claim.ClaimID+".json")
		if err := os.MkdirAll(filepath.Join(legacyHome, "worktrees"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(legacyClaim), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacyClaim, fixture.claimBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := findOrphanedClaim(fixture.projectsRoot, fixture.claim.Task, fixture.claim.ClaimID); err == nil ||
			!strings.Contains(err.Error(), "more than one WB home") {
			t.Fatalf("duplicate exact claim lookup = %v", err)
		}
		for _, path := range []string{fixture.claimPath, legacyClaim} {
			if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, fixture.claimBytes) {
				t.Fatalf("duplicate claim %s changed: %q, %v", path, got, err)
			}
		}
		fixture.assertNoTerminal(t)
	})
}

//nolint:paralleltest // HOME overrides must remain serial with other native fixtures.
func TestE2EFindOrphanedClaimPropagatesResolverAndGlobErrors(t *testing.T) {
	//nolint:paralleltest // t.Setenv pins HOME to isolate WB home resolution.
	t.Run("cyclic projects root", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		root := filepath.Join(t.TempDir(), "cycle")
		if err := os.Symlink("cycle", root); err != nil {
			t.Fatal(err)
		}
		if _, err := findOrphanedClaim(root, "task", strings.Repeat("a", 64)); err == nil ||
			!strings.Contains(err.Error(), "too many links") {
			t.Fatalf("cyclic-root resolution = %v", err)
		}
		if target, err := os.Readlink(root); err != nil || target != "cycle" {
			t.Fatalf("cyclic root changed: %q, %v", target, err)
		}
	})
	//nolint:paralleltest // t.Setenv pins HOME to isolate WB home resolution.
	t.Run("glob metacharacter in real projects root", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		root := filepath.Join(t.TempDir(), "project[")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := findOrphanedClaim(root, "task", strings.Repeat("a", 64)); !errors.Is(err, filepath.ErrBadPattern) {
			t.Fatalf("literal root with malformed glob syntax = %v, want ErrBadPattern", err)
		}
		if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
			t.Fatalf("glob error changed projects root: entries=%v err=%v", entries, err)
		}
	})
}

type orphanAbsenceDraftFixture struct {
	git       *gitFixture
	candidate orphanedClaimCandidate
	claimPath string
	claimRaw  []byte
	head      string
}

func newOrphanAbsenceDraftFixture(t *testing.T) orphanAbsenceDraftFixture {
	t.Helper()
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "orphan-absence-draft",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := readOrphanTestClaim(t, created[0].WorkLogPath)
	claimRaw, err := os.ReadFile(created[0].WorkLogPath)
	if err != nil {
		t.Fatal(err)
	}
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "worktree", "remove", created[0].WorktreeDir)
	gitTest(t, fixture.canonical, "update-ref", "-d", "refs/heads/"+created[0].Branch)
	candidate, err := findOrphanedClaim(fixture.projectsRoot, claim.Task, claim.ClaimID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(candidate.claim, claim) {
		t.Fatalf("looked-up claim changed: got %#v, want %#v", candidate.claim, claim)
	}
	if _, err := os.Lstat(candidate.claim.Worktree); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree still exists before absence test: %v", err)
	}
	if got := gitTestOutput(t, fixture.canonical, "for-each-ref", "--format=%(objectname)", "refs/heads/"+claim.Branch); got != "" {
		t.Fatalf("local branch still exists before absence test: %q", got)
	}
	return orphanAbsenceDraftFixture{git: fixture, candidate: candidate, claimPath: created[0].WorkLogPath, claimRaw: claimRaw, head: head}
}

func (fixture orphanAbsenceDraftFixture) assertImmutableAndUnsealed(t *testing.T) {
	t.Helper()
	if got, err := os.ReadFile(fixture.claimPath); err != nil || !bytes.Equal(got, fixture.claimRaw) {
		t.Fatalf("absence check changed immutable claim: %q, %v", got, err)
	}
	if _, err := os.Lstat(fixture.candidate.terminalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("absence check published terminal: %v", err)
	}
}

//nolint:paralleltest // newGitFixture pins process environment for real Git.
func TestE2EInspectOrphanedClaimAbsenceStopsAtExactNegativeProofBoundary(t *testing.T) {
	//nolint:paralleltest // The real-Git fixture sets process environment for this subtest.
	t.Run("worktree ancestor is a regular file", func(t *testing.T) {
		fixture := newOrphanAbsenceDraftFixture(t)
		parent := filepath.Dir(fixture.candidate.claim.Worktree)
		moved := parent + "-moved"
		if err := os.Rename(parent, moved); err != nil {
			t.Fatal(err)
		}
		before := []byte("occupied worktree parent")
		if err := os.WriteFile(parent, before, 0o600); err != nil {
			t.Fatal(err)
		}
		evidence, err := inspectOrphanedClaimAbsence(context.Background(), fixture.git.projectsRoot, fixture.candidate, "founder", "negative proof")
		if evidence != nil || !errors.Is(err, syscall.ENOTDIR) || !strings.Contains(err.Error(), "inspect claimed worktree path") {
			t.Fatalf("blocked worktree ancestor = %#v, %v", evidence, err)
		}
		if got, readErr := os.ReadFile(parent); readErr != nil || !bytes.Equal(got, before) {
			t.Fatalf("worktree-parent blocker changed: %q, %v", got, readErr)
		}
		if info, statErr := os.Stat(moved); statErr != nil || !info.IsDir() {
			t.Fatalf("original worktree root changed after refusal: %v, %v", info, statErr)
		}
		fixture.assertImmutableAndUnsealed(t)
	})
	//nolint:paralleltest // The real-Git fixture sets process environment for this subtest.
	t.Run("invalid recorded repository coordinate", func(t *testing.T) {
		fixture := newOrphanAbsenceDraftFixture(t)
		candidate := fixture.candidate
		candidate.claim.Repository = "../outside"
		_, directErr := CanonicalRepositoryPath(fixture.git.projectsRoot, candidate.claim.Repository)
		if directErr == nil {
			t.Fatal("invalid coordinate unexpectedly has a canonical path")
		}
		evidence, err := inspectOrphanedClaimAbsence(context.Background(), fixture.git.projectsRoot, candidate, "founder", "negative proof")
		if evidence != nil || err == nil || err.Error() != directErr.Error() {
			t.Fatalf("invalid-coordinate boundary = %#v, %v; want %v", evidence, err, directErr)
		}
		fixture.assertImmutableAndUnsealed(t)
	})
	//nolint:paralleltest // The real-Git fixture sets process environment for this subtest.
	t.Run("canonical Git metadata occupied after Git ref cleanup", func(t *testing.T) {
		fixture := newOrphanAbsenceDraftFixture(t)
		gitDir := filepath.Join(fixture.git.canonical, ".git")
		originalHead, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
		if err != nil {
			t.Fatal(err)
		}
		moved := gitDir + "-moved"
		if err := os.Rename(gitDir, moved); err != nil {
			t.Fatal(err)
		}
		before := []byte("occupied canonical Git metadata")
		if err := os.WriteFile(gitDir, before, 0o600); err != nil {
			t.Fatal(err)
		}
		evidence, err := inspectOrphanedClaimAbsence(context.Background(), fixture.git.projectsRoot, fixture.candidate, "founder", "negative proof")
		if evidence != nil || err == nil || !strings.Contains(err.Error(), "inspect Git worktree registration") {
			t.Fatalf("unopenable canonical registration = %#v, %v", evidence, err)
		}
		if got, readErr := os.ReadFile(gitDir); readErr != nil || !bytes.Equal(got, before) {
			t.Fatalf("canonical Git metadata occupant changed: %q, %v", got, readErr)
		}
		if got, readErr := os.ReadFile(filepath.Join(moved, "HEAD")); readErr != nil || !bytes.Equal(got, originalHead) {
			t.Fatalf("retained canonical HEAD bytes changed: %q, %v", got, readErr)
		}
		fixture.assertImmutableAndUnsealed(t)
	})
	//nolint:paralleltest // The real-Git fixture sets process environment for this subtest.
	t.Run("context canceled only after registration observation", func(t *testing.T) {
		fixture := newOrphanAbsenceDraftFixture(t)
		base, cancel := context.WithCancel(context.Background())
		defer cancel()
		seenRegistration := false
		ctx := withCanonicalGitInterceptor(base, func(_ context.Context, args []string, runSecure func() ([]byte, error)) ([]byte, error) {
			if len(args) == 3 && args[0] == "worktree" && args[1] == "list" && args[2] == "--porcelain" {
				output, err := runSecure()
				if err == nil {
					seenRegistration = true
					cancel()
				}
				return output, err
			}
			return runSecure()
		})
		evidence, err := inspectOrphanedClaimAbsence(ctx, fixture.git.projectsRoot, fixture.candidate, "founder", "negative proof")
		if evidence != nil || !seenRegistration || !errors.Is(err, context.Canceled) ||
			!strings.Contains(err.Error(), "inspect branch") {
			t.Fatalf("post-registration canceled local-ref observation = %#v, %v; registered=%t", evidence, err, seenRegistration)
		}
		fixture.assertImmutableAndUnsealed(t)
	})
	//nolint:paralleltest // The real-Git fixture sets process environment for this subtest.
	t.Run("remote query fails after local absence", func(t *testing.T) {
		fixture := newOrphanAbsenceDraftFixture(t)
		injected := errors.New("injected remote query failure")
		fake := runnertest.New(t)
		fake.ExpectArgv([]string{"git", "-C", fixture.git.canonical, "ls-remote", "--heads", "origin", "refs/heads/" + fixture.candidate.claim.Branch}, runner.Result{}, injected)
		evidence, err := inspectOrphanedClaimAbsence(withGitRunner(context.Background(), fake), fixture.git.projectsRoot, fixture.candidate, "founder", "negative proof")
		if evidence != nil || err == nil || !strings.Contains(err.Error(), "inspect remote branch") ||
			!strings.Contains(err.Error(), injected.Error()) {
			t.Fatalf("post-local remote failure = %#v, %v", evidence, err)
		}
		if got := gitTestOutput(t, fixture.git.canonical, "rev-parse", "HEAD"); got != fixture.head {
			t.Fatalf("canonical HEAD changed: %s, want %s", got, fixture.head)
		}
		fixture.assertImmutableAndUnsealed(t)
	})
	//nolint:paralleltest // The real-Git fixture sets process environment for this subtest.
	t.Run("terminal ancestor is a regular file", func(t *testing.T) {
		fixture := newOrphanAbsenceDraftFixture(t)
		parent := filepath.Dir(fixture.candidate.terminalPath)
		moved := ""
		if _, err := os.Lstat(parent); err == nil {
			moved = parent + "-moved"
			if renameErr := os.Rename(parent, moved); renameErr != nil {
				t.Fatal(renameErr)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		before := []byte("occupied terminal parent")
		if err := os.WriteFile(parent, before, 0o600); err != nil {
			t.Fatal(err)
		}
		evidence, err := inspectOrphanedClaimAbsence(context.Background(), fixture.git.projectsRoot, fixture.candidate, "founder", "negative proof")
		if evidence != nil || !errors.Is(err, syscall.ENOTDIR) || !strings.Contains(err.Error(), "inspect terminal record") {
			t.Fatalf("blocked terminal ancestor = %#v, %v", evidence, err)
		}
		if got, readErr := os.ReadFile(parent); readErr != nil || !bytes.Equal(got, before) {
			t.Fatalf("terminal-parent blocker changed: %q, %v", got, readErr)
		}
		if err := os.Remove(parent); err != nil {
			t.Fatal(err)
		}
		if moved != "" {
			if err := os.Rename(moved, parent); err != nil {
				t.Fatal(err)
			}
		}
		fixture.assertImmutableAndUnsealed(t)
	})
}
