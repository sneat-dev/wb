//go:build e2e

package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EEngineLegacyResumeRecoversNativeCreationBase(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"registered missing", "retained branch", "matching manifest", "blank manifest", "conflicting manifest", "corrupt manifest", "origin refusal", "merge base refusal", "zero timeout"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newExplicitRootEngineFixture(t)
			options := f.options()
			options.Commit, options.Resume = true, true
			if mode == "zero timeout" {
				options.Timeout = 0
			}
			pinned := strings.TrimSpace(runEngineGit(t, f.canonical, "rev-parse", "origin/main^{commit}"))
			path := filepath.Join(f.githubDir, ".worktrees", options.Operation, "acme", "app")
			if mode == "retained branch" {
				runEngineGit(t, f.canonical, "branch", options.Branch, pinned)
			} else {
				runEngineGit(t, f.canonical, "worktree", "add", "-b", options.Branch, path, pinned)
				writeEngineFile(t, filepath.Join(path, "unrelated.txt"), "owned unrelated\n")
				runEngineGit(t, path, "add", "unrelated.txt")
				runEngineGit(t, path, "commit", "-m", "owned unrelated content")
			}
			var manifestBefore worktrees.Manifest
			if strings.Contains(mode, "manifest") {
				manifestBefore = worktrees.Manifest{Version: 1, EffortID: worktreeEffortID(options.Operation, "acme", "app"), EffortKind: worktrees.EffortKindTask, Repository: f.repository.Slug, Worktree: path, Branch: options.Branch, Base: "main", BaseSHA: pinned, CreatedAt: time.Now().UTC(), Provenance: worktrees.ProvenanceCreated}
				if mode == "blank manifest" {
					manifestBefore.Worktree, manifestBefore.Base, manifestBefore.BaseSHA = "", "", ""
				}
				if mode == "conflicting manifest" {
					manifestBefore.BaseSHA = strings.Repeat("f", 40)
				}
				if err := worktrees.WriteManifest(path, manifestBefore); err != nil {
					t.Fatal(err)
				}
				if mode == "corrupt manifest" {
					writeEngineFile(t, filepath.Join(path, ".wb", "local", "manifest.yaml"), "invalid: [")
				}
			}
			advanced := strings.TrimSpace(runEngineGit(t, f.canonical, "commit-tree", pinned+"^{tree}", "-p", pinned, "-m", "owned target advance"))
			runEngineGit(t, f.canonical, "push", "origin", advanced+":refs/heads/main")
			if mode == "merge base refusal" {
				orphan := strings.TrimSpace(runEngineGit(t, f.canonical, "commit-tree", pinned+"^{tree}", "-m", "owned disjoint history"))
				runEngineGit(t, path, "reset", "--hard", orphan)
			}
			if mode == "origin refusal" {
				options.run = &engineStageObservedRunner{Runner: defaultRunner, name: "git", args: []string{"rev-parse", "--verify", "origin/main^{commit}"}, cause: os.ErrPermission}
				// Sync uses the same command before inspection: arm only at the
				// existing inspection boundary so this row reaches legacy proof.
				observed := options.run.(*engineStageObservedRunner)
				observed.name = ""
				options.run = observed
			}
			canonicalBefore := mustReadEngineFile(t, filepath.Join(f.canonical, "dependency.txt"))
			handler := engineStageInspectionHandler{textHandler: textHandler{}, before: func() error {
				if mode == "origin refusal" {
					options.run.(*engineStageObservedRunner).name = "git"
				}
				return nil
			}}
			results, err := Run(ctx, []Repository{f.repository}, handler, options)
			negative := mode == "conflicting manifest" || mode == "corrupt manifest" || mode == "origin refusal" || mode == "merge base refusal"
			if negative {
				if err == nil || len(results) != 1 || results[0].Status != "failed" {
					t.Fatalf("native legacy refusal: %+v %v", results, err)
				}
				if got := mustReadEngineFile(t, filepath.Join(path, "dependency.txt")); got != "old\n" {
					t.Fatalf("refusal applied: %q", got)
				}
			} else {
				if err != nil || len(results) != 1 || results[0].Status != "committed" || results[0].WorktreeDir != path {
					t.Fatalf("legacy public clean commit: %+v %v", results, err)
				}
				view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: path})
				if err != nil || view.Claim == nil || view.Claim.BaseSHA != pinned {
					t.Fatalf("legacy authenticated original base: %+v %v", view, err)
				}
				if _, err := worktrees.Guard(ctx, path, worktrees.GuardOptions{ProjectsRoot: f.githubDir, Base: "main", Admission: worktrees.AdmissionEnforce}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "matching manifest" || mode == "blank manifest" || mode == "conflicting manifest" {
				after, err := worktrees.ReadManifest(path)
				if err != nil || !reflect.DeepEqual(after, manifestBefore) {
					t.Fatalf("rewrote legacy immutable manifest: %+v %v", after, err)
				}
			}
			if got := mustReadEngineFile(t, filepath.Join(f.canonical, "dependency.txt")); got != canonicalBefore {
				t.Fatal("legacy flow mutated canonical")
			}
			if mode != "retained branch" && mode != "merge base refusal" {
				if got := mustReadEngineFile(t, filepath.Join(path, "unrelated.txt")); got != "owned unrelated\n" {
					t.Fatalf("changed unrelated contents: %q", got)
				}
			}
		})
	}
}

func TestE2EEngineAuthenticatedResumeRefusesUnusableClaimsAndChangedTarget(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"corrupt claim", "terminal claim", "changed target"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newExplicitRootEngineFixture(t)
			options := f.options()
			options.Commit = true
			initial, err := Run(ctx, []Repository{f.repository}, textHandler{}, options)
			if err != nil || len(initial) != 1 || initial[0].Status != "committed" {
				t.Fatalf("initial creation: %+v %v", initial, err)
			}
			path := initial[0].WorktreeDir
			view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: path})
			if err != nil || view.Claim == nil {
				t.Fatalf("initial authenticated claim: %+v %v", view, err)
			}
			if mode == "corrupt claim" {
				writeEngineFile(t, view.Claim.ClaimPath, "invalid: [")
			}
			if mode == "terminal claim" {
				if _, err := worktrees.LogFinalize(ctx, worktrees.LogFinalizeOptions{ProjectsRoot: f.githubDir, Worktree: path, Result: "success", Message: "owned terminal witness", Apply: true}); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed target" {
				runEngineGit(t, f.canonical, "push", "origin", "origin/main:refs/heads/other")
				options.Ref = "other"
			}
			writeEngineFile(t, filepath.Join(path, "dependency.txt"), "old\n")
			before, err := worktrees.ReadManifest(path)
			if err != nil {
				t.Fatal(err)
			}
			options.Resume = true
			results, err := Run(ctx, []Repository{f.repository}, textHandler{}, options)
			if err == nil || len(results) != 1 || results[0].Status != "failed" || !strings.Contains(err.Error(), "record worktree Work Log claim:") {
				t.Fatalf("claim refusal: %+v %v", results, err)
			}
			after, readErr := worktrees.ReadManifest(path)
			if readErr != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("claim refusal changed immutable manifest: %+v %v", after, readErr)
			}
			if got := mustReadEngineFile(t, filepath.Join(path, "dependency.txt")); got != "old\n" {
				t.Fatalf("claim refusal applied: %q", got)
			}
		})
	}
}
