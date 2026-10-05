//go:build e2e

package orchestrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EEngineMaterializationRefusesTrackedPlacementAndUnsafeTask(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"tracked store policy", "unsafe task"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newExplicitRootEngineFixture(t)
			options, err := Normalize(f.options())
			if err != nil {
				t.Fatal(err)
			}
			want := "invalid worktree task"
			if mode == "tracked store policy" {
				writeEngineFile(t, filepath.Join(f.canonical, ".wb", "worktrees.yaml"), "version: 1\nworktrees:\n  store: central\n")
				runEngineGit(t, f.canonical, "add", ".wb/worktrees.yaml")
				runEngineGit(t, f.canonical, "commit", "-m", "tracked placement refusal")
				runEngineGit(t, f.canonical, "push", "origin", "main")
				want = "must not set worktrees.store"
			} else {
				options.Operation = "../outside"
			}
			before := runEngineGit(t, f.canonical, "worktree", "list", "--porcelain")
			path, placement, base, resumed, err := operationWorktreePath(context.Background(), f.canonical, f.repository.Slug, options, ResolvedBase{Ref: "main"})
			if err == nil || !strings.Contains(err.Error(), want) || path != "" || placement.Root != "" || base != "" || resumed {
				t.Fatalf("native placement refusal: path=%q placement=%+v base=%q resumed=%t err=%v", path, placement, base, resumed, err)
			}
			if after := runEngineGit(t, f.canonical, "worktree", "list", "--porcelain"); after != before {
				t.Fatalf("refusal changed actual registrations: before=%s after=%s", before, after)
			}
			if got := mustReadEngineFile(t, filepath.Join(f.canonical, "dependency.txt")); got != "old\n" {
				t.Fatalf("refusal changed canonical contents: %q", got)
			}
		})
	}
}

func TestE2EEngineMaterializationJournalFaultsPreserveCheckoutAndRetry(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"manifest", "prompt", "claim"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			f := newExplicitRootEngineFixture(t)
			options, err := Normalize(f.options())
			if err != nil {
				t.Fatal(err)
			}
			home, err := wbhome.EnsureRoot(f.githubDir)
			if err != nil {
				t.Fatal(err)
			}
			path, placement, baseSHA, resumed, err := operationWorktreePath(context.Background(), f.canonical, f.repository.Slug, options, ResolvedBase{Ref: "main"})
			if err != nil || resumed {
				t.Fatalf("actual placement: %q, %v", path, err)
			}
			created, err := prepareWorktree(context.Background(), f.canonical, f.repository.Slug, path, placement, baseSHA, resumed, options.Branch, "origin/main", options)
			if err != nil || created == nil {
				t.Fatalf("actual secure checkout creation: %+v, %v", created, err)
			}
			t.Cleanup(created.Close)
			blocker, want := filepath.Join(path, ".wb"), "record worktree manifest:"
			if mode == "prompt" {
				blocker, want = filepath.Join(path, ".wb", "local", "prompts"), "record worktree originating instruction:"
			}
			if mode == "claim" {
				blocker, want = filepath.Join(home, "worklogs"), "record worktree Work Log claim:"
			}
			writeEngineFile(t, blocker, "owned physical blocker\n")
			armed := true
			t.Cleanup(func() {
				if armed {
					if err := os.Remove(blocker); err != nil {
						t.Error(err)
					}
				}
			})
			err = recordWorktreeManifest(context.Background(), home, f.canonical, path, f.repository, ResolvedBase{Ref: "main"}, options)
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("wrong native journal refusal stage: %v, want prefix %q", err, want)
			}
			if got := mustReadEngineFile(t, blocker); got != "owned physical blocker\n" {
				t.Fatalf("blocker changed: %q", got)
			}
			if got := mustReadEngineFile(t, filepath.Join(path, "dependency.txt")); got != "old\n" {
				t.Fatalf("refusal changed published checkout: %q", got)
			}
			if got := mustReadEngineFile(t, filepath.Join(f.canonical, "dependency.txt")); got != "old\n" {
				t.Fatalf("refusal changed canonical: %q", got)
			}
			if got := strings.TrimSpace(runEngineGit(t, path, "branch", "--show-current")); got != options.Branch {
				t.Fatalf("refusal changed actual branch: %q", got)
			}
			if mode != "manifest" {
				manifest, readErr := worktrees.ReadManifest(path)
				if readErr != nil || manifest.BaseSHA != baseSHA || manifest.Branch != options.Branch {
					t.Fatalf("earlier immutable manifest lost: %+v, %v", manifest, readErr)
				}
			}
			if err := os.Remove(blocker); err != nil {
				t.Fatal(err)
			}
			armed = false
			if err := recordWorktreeManifest(context.Background(), home, f.canonical, path, f.repository, ResolvedBase{Ref: "main"}, options); err != nil {
				t.Fatalf("native journal retry: %v", err)
			}
			manifest, err := worktrees.ReadManifest(path)
			if err != nil || manifest.BaseSHA != baseSHA {
				t.Fatalf("actual base manifest: %+v, %v", manifest, err)
			}
			view, err := worktrees.LoadWorkLogView(context.Background(), worktrees.LoadWorkLogOptions{ProjectsRoot: f.githubDir, Worktree: path})
			if err != nil || view.Claim == nil || view.Claim.ClaimID != manifest.ClaimID {
				t.Fatalf("native corroborated claim: %+v, %v", view, err)
			}
			guard, err := worktrees.Guard(context.Background(), path, worktrees.GuardOptions{ProjectsRoot: f.githubDir, Base: "main", Admission: worktrees.AdmissionEnforce})
			if err != nil || guard.Admission == nil || !guard.Admission.Admitted {
				t.Fatalf("native retry admission: %+v, %v", guard, err)
			}
		})
	}
}
