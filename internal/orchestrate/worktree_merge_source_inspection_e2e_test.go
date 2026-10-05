//go:build e2e

package orchestrate

import (
	"errors"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestE2EMergeSourceCanonicalUsesNativeCommonDirectory(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "canonical-source", "feature/canonical-source", "source.txt", "source\n")
	physical, err := filepath.EvalSymlinks(f.canonical)
	if err != nil {
		t.Fatal(err)
	}
	// Canonical Git returns a relative .git; linked Git returns the shared absolute common directory.
	for _, path := range []string{f.canonical, source.WorktreeDir} {
		got, err := canonicalForMergeSource(t.Context(), path)
		if err != nil || got != physical {
			t.Fatalf("canonical for %s = %q, %v; want %q", path, got, err, physical)
		}
	}
}

func TestE2EMergeSourceCanonicalPreservesExactNativeReadErrors(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "canonical-errors", "feature/canonical-errors", "source.txt", "source\n")
	root := strings.TrimSpace(runEngineGit(t, source.WorktreeDir, "rev-parse", "--show-toplevel"))
	sentinel := errors.New("named canonical observation refused")
	for _, tc := range []struct{ name, dir, argv string }{
		{"root", source.WorktreeDir, "rev-parse --show-toplevel"},
		{"common", root, "rev-parse --git-common-dir"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := conflictNegativeRunner{Runner: runner.New(), dir: tc.dir, argv: tc.argv, err: sentinel}
			got, err := canonicalForMergeSourceWithRunner(t.Context(), run, source.WorktreeDir)
			if got != "" || !errors.Is(err, sentinel) {
				t.Fatalf("negative observation = %q, %v", got, err)
			}
			deferred, peekErr := peekWorktreeMergeValidationDeferral(t.Context(), run, filepath.Abs, f.githubDir, []string{source.WorktreeDir}, "", WorktreeMergeRouteAuto, false, false)
			if deferred || !errors.Is(peekErr, sentinel) || !strings.HasPrefix(peekErr.Error(), "resolve source canonical clone: ") {
				t.Fatalf("Peek canonical observation = %v, %v", deferred, peekErr)
			}
		})
	}
}

func TestE2EMergeSourceInspectionPreservesNativeOrderIdentityAndDuplicateHead(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	first := createMergeSource(t, f, "first-source", "feature/first-source", "first.txt", "first\n")
	second := createMergeSource(t, f, "second-source", "feature/second-source", "second.txt", "second\n")
	sources, repository, canonical, inspectionErr := inspectWorktreeMergeSources(t.Context(), f.githubDir, []string{second.WorktreeDir, first.WorktreeDir}, "main")
	expected := []WorktreeMergeSource{
		{Task: "second-source", Worktree: second.WorktreeDir, Branch: second.Branch, SHA: strings.TrimSpace(runEngineGit(t, second.WorktreeDir, "rev-parse", "HEAD"))},
		{Task: "first-source", Worktree: first.WorktreeDir, Branch: first.Branch, SHA: strings.TrimSpace(runEngineGit(t, first.WorktreeDir, "rev-parse", "HEAD"))},
	}
	// Actual Guard reports physical native paths; never substitute a simulated common directory.
	var err error
	for i := range expected {
		expected[i].Worktree, err = filepath.EvalSymlinks(expected[i].Worktree)
		if err != nil {
			t.Fatal(err)
		}
	}
	physical, err := filepath.EvalSymlinks(f.canonical)
	if err != nil {
		t.Fatal(err)
	}
	if inspectionErr != nil || !reflect.DeepEqual(sources, expected) || repository != f.repository.Slug || canonical != physical {
		t.Fatalf("native ordered inspection = %+v, %q, %q, %v", sources, repository, canonical, inspectionErr)
	}
	sources, repository, canonical, err = inspectWorktreeMergeSources(t.Context(), f.githubDir, []string{first.WorktreeDir, first.WorktreeDir}, "main")
	if sources != nil || repository != "" || canonical != "" || err == nil || !strings.Contains(err.Error(), "was supplied more than once") {
		t.Fatalf("native duplicate HEAD = %+v, %q, %q, %v", sources, repository, canonical, err)
	}
	empty, repository, canonical, err := inspectWorktreeMergeSources(t.Context(), f.githubDir, nil, "main")
	if empty == nil || len(empty) != 0 || repository != "" || canonical != "" || err != nil {
		t.Fatalf("empty inspection = %+v, %q, %q, %v", empty, repository, canonical, err)
	}
}

func TestE2EMergeSourceInspectionNegativeReadsKeepRealCustody(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "inspection-errors", "feature/inspection-errors", "source.txt", "source\n")
	guard, err := worktrees.Guard(t.Context(), source.WorktreeDir, worktrees.GuardOptions{ProjectsRoot: f.githubDir, Base: "main"})
	if err != nil || guard.Kind != "linked" {
		t.Fatalf("actual custody = %+v, %v", guard, err)
	}
	view, err := worktrees.LoadWorkLogView(t.Context(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: guard.Path})
	if err != nil || view.Claim == nil {
		t.Fatalf("actual claim = %+v, %v", view, err)
	}
	sentinel := errors.New("named source read refused after native custody")
	for _, tc := range []struct{ name, argv, prefix string }{
		{"cleanliness", "status --porcelain=v1", "source " + source.WorktreeDir + ": "},
		{"HEAD", "rev-parse --verify HEAD^{commit}", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			run := conflictNegativeRunner{Runner: runner.New(), dir: guard.Path, argv: tc.argv, err: sentinel}
			sources, repository, canonical, err := inspectWorktreeMergeSourcesWithRunner(t.Context(), run, f.githubDir, []string{source.WorktreeDir}, "main")
			if sources != nil || repository != "" || canonical != "" || !errors.Is(err, sentinel) || !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Fatalf("negative actual read = %+v, %q, %q, %v", sources, repository, canonical, err)
			}
			deferred, peekErr := peekWorktreeMergeValidationDeferral(t.Context(), run, filepath.Abs, f.githubDir, []string{source.WorktreeDir}, "main", WorktreeMergeRouteAuto, false, false)
			if deferred || !errors.Is(peekErr, sentinel) || !strings.HasPrefix(peekErr.Error(), tc.prefix) {
				t.Fatalf("Peek source observation = %v, %v", deferred, peekErr)
			}
		})
	}
}

func TestE2EMergeSourceInspectionRejectsNativeInvalidCustodyAndJournal(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"missing", "canonical", "dirty", "missing claim", "unreadable prompts", "transient"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newExplicitRootEngineFixture(t)
			source := createMergeSource(t, f, "refusal-source", "feature/refusal-source", "source.txt", "source\n")
			path, want := source.WorktreeDir, ""
			switch mode {
			case "missing":
				path = filepath.Join(t.TempDir(), "absent")
				want = "guard source "
			case "canonical":
				path = f.canonical
				want = "must be a non-transient WB linked worktree"
			case "dirty":
				writeEngineFile(t, filepath.Join(path, "untracked.txt"), "dirty\n")
				want = "source " + path + ": "
			case "missing claim":
				if err := os.Remove(filepath.Join(path, ".wb-worklog", "recovery.json")); err != nil {
					t.Fatal(err)
				}
				want = "has no authoritative active Work Log claim"
			case "unreadable prompts":
				prompts := filepath.Join(path, ".wb", "local", "prompts")
				if err := os.RemoveAll(prompts); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(prompts, []byte("not a directory\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				want = "load Work Log for source "
			case "transient":
				for i, name := range []string{"one.txt", "two.txt", "three.txt"} {
					writeEngineFile(t, filepath.Join(path, name), name)
					runEngineGit(t, path, "add", name)
					runEngineGit(t, path, "commit", "-m", string(rune('a'+i)))
				}
				runEngineGit(t, path, "bisect", "start", "HEAD", "HEAD~4")
				t.Cleanup(func() { runEngineGit(t, path, "bisect", "reset") })
				guard, err := worktrees.Guard(t.Context(), path, worktrees.GuardOptions{ProjectsRoot: f.githubDir, Base: "main"})
				if err != nil || !guard.Transient {
					t.Fatalf("actual transient custody=%+v, %v", guard, err)
				}
				want = "must be a non-transient WB linked worktree"
			}
			sources, repository, canonical, err := inspectWorktreeMergeSources(t.Context(), f.githubDir, []string{path}, "main")
			if sources != nil || repository != "" || canonical != "" || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("native %s refusal = %+v, %q, %q, %v", mode, sources, repository, canonical, err)
			}
		})
	}
}

func TestE2EMergeSourceInspectionRejectsRealCrossRepositorySources(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	first := createMergeSource(t, f, "one-repository", "feature/one-repository", "one.txt", "one\n")
	other := f
	other.canonical = filepath.Join(f.githubDir, "acme", "other")
	runEngineGit(t, f.githubDir, "clone", "--no-hardlinks", f.repository.CloneURL, other.canonical)
	runEngineGit(t, other.canonical, "config", "user.name", "WB Test")
	runEngineGit(t, other.canonical, "config", "user.email", "wb@example.test")
	other.repository = Repository{Slug: "acme/other", Path: other.canonical, CloneURL: f.repository.CloneURL}
	second := createMergeSource(t, other, "other-repository", "feature/other-repository", "other.txt", "other\n")
	sources, repository, canonical, err := inspectWorktreeMergeSources(t.Context(), f.githubDir, []string{first.WorktreeDir, second.WorktreeDir}, "main")
	if sources != nil || repository != "" || canonical != "" || err == nil || !strings.Contains(err.Error(), "all source worktrees must belong to one repository; got acme/app and acme/other") {
		t.Fatalf("actual cross-repository refusal=%+v, %q, %q, %v", sources, repository, canonical, err)
	}
}

func TestE2EPeekMergeDeferralPreservesNormalizationAndNativeRefusals(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("normalization failed without process cwd mutation")
	called := 0
	abs := func(input string) (string, error) {
		called++
		if input != "relative-root" {
			t.Fatalf("normalizer input=%q", input)
		}
		return "", sentinel
	}
	deferred, err := peekWorktreeMergeValidationDeferral(t.Context(), runner.New(), abs, " relative-root ", nil, "main", WorktreeMergeRouteAuto, false, false)
	if deferred || err == nil || err.Error() != "projects root is required" || called != 1 {
		t.Fatalf("normalization refusal=%v, %v calls=%d", deferred, err, called)
	}
	deferred, err = peekWorktreeMergeValidationDeferral(t.Context(), runner.New(), func(string) (string, error) { return "", nil }, "anything", nil, "main", WorktreeMergeRouteAuto, false, false)
	if deferred || err == nil || err.Error() != "projects root is required" {
		t.Fatalf("empty normalized root=%v, %v", deferred, err)
	}
	// Existing policy normalizes empty input to cwd before checking emptiness.
	deferred, err = PeekWorktreeMergeValidationDeferral(t.Context(), "", nil, "main", WorktreeMergeRouteAuto, false, false)
	if deferred || err == nil || err.Error() != "at least one source worktree is required" {
		t.Fatalf("native empty root normalization=%v, %v", deferred, err)
	}
	path := filepath.Join(t.TempDir(), "missing")
	deferred, err = PeekWorktreeMergeValidationDeferral(t.Context(), t.TempDir(), []string{path}, "", WorktreeMergeRouteAuto, false, false)
	if deferred || err == nil || !strings.HasPrefix(err.Error(), "resolve source canonical clone: ") {
		t.Fatalf("native canonical refusal=%v, %v", deferred, err)
	}
	deferred, err = PeekWorktreeMergeValidationDeferral(t.Context(), t.TempDir(), []string{path}, "main", WorktreeMergeRouteAuto, false, false)
	if deferred || err == nil || !strings.HasPrefix(err.Error(), "guard source ") {
		t.Fatalf("native inspection refusal=%v, %v", deferred, err)
	}
}

func TestE2EPeekMergeDeferralUsesNativeDefaultTargetAndPreservesPlanErrors(t *testing.T) {
	t.Parallel()
	f := newExplicitRootEngineFixture(t)
	source := createMergeSource(t, f, "peek-source", "feature/peek-source", "source.txt", "source\n")
	deferred, err := PeekWorktreeMergeValidationDeferral(t.Context(), " "+f.githubDir+" ", []string{source.WorktreeDir}, "", WorktreeMergeRoute("not-a-route"), false, false)
	if deferred || err == nil || err.Error() != "unsupported merge route \"not-a-route\"" {
		t.Fatalf("native default target and route refusal=%v, %v", deferred, err)
	}
	// Remove only this fixture's remote and cached default: the actual DefaultBranch owner must refuse.
	runEngineGit(t, f.canonical, "remote", "remove", "origin")
	deferred, err = PeekWorktreeMergeValidationDeferral(t.Context(), f.githubDir, []string{source.WorktreeDir}, "", WorktreeMergeRouteAuto, false, false)
	if deferred || err == nil || !strings.HasPrefix(err.Error(), "resolve remote default branch: ") {
		t.Fatalf("native default branch refusal=%v, %v", deferred, err)
	}
}
