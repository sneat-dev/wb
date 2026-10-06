//go:build e2e

package orchestrate

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2EEngineCreationRecordsPinnedCheckoutBase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newExplicitRootEngineFixture(t)
	options := fixture.options()
	options.Commit = true
	pinned := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "origin/main^{commit}"))
	advanced := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", pinned+"^{tree}", "-p", pinned, "-m", "owned remote advance"))
	canonicalHead := runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")
	canonicalContents := mustReadEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt"))
	armed, consumed := false, 0
	observed := &engineStageObservedRunner{Runner: defaultRunner, after: func(dir, name string, args []string) error {
		if !armed || consumed != 0 || dir != fixture.canonical || name != "git" || !reflect.DeepEqual(args, []string{"rev-parse", "--verify", "origin/main^{commit}"}) {
			return nil
		}
		consumed++
		if _, _, err := runCommand(ctx, defaultRunner, options.Timeout, 0, fixture.canonical, "git", "push", "origin", advanced+":refs/heads/main"); err != nil {
			return err
		}
		_, _, err := runCommand(ctx, defaultRunner, options.Timeout, 0, fixture.canonical, "git", "update-ref", "refs/remotes/origin/main", advanced)
		return err
	}}
	options.run = observed
	handler := engineStageInspectionHandler{textHandler: textHandler{}, before: func() error { armed = true; return nil }}
	results, runErr := Run(ctx, []Repository{fixture.repository}, handler, options)
	t.Logf("actual public creation: results=%+v err=%v pinned=%s advanced=%s observed=%d", results, runErr, pinned, advanced, consumed)
	if consumed != 1 {
		t.Fatalf("actual pinned-read boundary not observed once: %d", consumed)
	}
	if len(results) != 1 || results[0].WorktreeDir == "" {
		t.Fatalf("public creation did not expose its actual checkout: %+v", results)
	}
	checkout := results[0].WorktreeDir
	if base := strings.TrimSpace(runEngineGit(t, checkout, "merge-base", advanced, "HEAD")); base != pinned {
		t.Errorf("actual checkout lineage must originate at pinned commit: got=%s want=%s", base, pinned)
	}
	manifest, err := worktrees.ReadManifest(checkout)
	if err != nil {
		t.Fatal(err)
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: checkout})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("actual records: manifest=%+v claim=%+v notes=%v", manifest, view.Claim, view.Notes)
	if manifest.BaseSHA != pinned {
		t.Errorf("immutable creation manifest must name actual pinned checkout base: got=%s want=%s", manifest.BaseSHA, pinned)
	}
	if view.Claim == nil || view.Claim.BaseSHA != pinned || view.Claim.ClaimID != manifest.ClaimID {
		t.Errorf("creation must publish a corroborated pinned claim: claim=%+v notes=%v", view.Claim, view.Notes)
	}
	if runErr != nil || results[0].Status != "committed" {
		t.Errorf("pinned native creation must complete public clean commit: results=%+v err=%v", results, runErr)
	} else if _, err := worktrees.Guard(ctx, checkout, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main", Admission: worktrees.AdmissionEnforce}); err != nil {
		t.Errorf("pinned creation admission: %v", err)
	}
	if head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "origin/main")); head != advanced {
		t.Errorf("actual current origin did not advance: %s", head)
	}
	if head := runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"); head != canonicalHead {
		t.Error("creation changed canonical checkout HEAD")
	}
	if contents := mustReadEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt")); contents != canonicalContents {
		t.Error("creation changed canonical contents")
	}
}

func TestE2EEngineAuthenticatedResumePreservesCreationBase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fixture := newExplicitRootEngineFixture(t)
	options := fixture.options()
	options.Commit = true
	initial, err := Run(ctx, []Repository{fixture.repository}, textHandler{}, options)
	if err != nil || len(initial) != 1 || initial[0].Status != "committed" {
		t.Fatalf("initial native clean commit: results=%+v err=%v", initial, err)
	}
	checkout := initial[0].WorktreeDir
	before, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: checkout, IncludePromptBodies: true})
	if err != nil || before.Claim == nil || before.Manifest == nil {
		t.Fatalf("initial authenticated creation records: view=%+v err=%v", before, err)
	}
	if _, err := worktrees.Guard(ctx, checkout, worktrees.GuardOptions{ProjectsRoot: fixture.githubDir, Base: "main", Admission: worktrees.AdmissionEnforce}); err != nil {
		t.Fatal(err)
	}
	writeEngineFile(t, filepath.Join(checkout, "unrelated.txt"), "retained owned contents\n")
	runEngineGit(t, checkout, "add", "unrelated.txt")
	runEngineGit(t, checkout, "commit", "-m", "owned unrelated contents")
	writeEngineFile(t, filepath.Join(checkout, "dependency.txt"), "old\n")
	registration := runEngineGit(t, fixture.canonical, "worktree", "list", "--porcelain")
	canonicalHead := runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")
	canonicalContents := mustReadEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt"))
	pinned := before.Claim.BaseSHA
	advanced := strings.TrimSpace(runEngineGit(t, fixture.canonical, "commit-tree", pinned+"^{tree}", "-p", pinned, "-m", "owned remote advance"))
	runEngineGit(t, fixture.canonical, "push", "origin", advanced+":refs/heads/main")
	options.Resume = true
	resumed, resumeErr := Run(ctx, []Repository{fixture.repository}, textHandler{}, options)
	t.Logf("actual public authenticated resume: results=%+v err=%v pinned=%s advanced=%s", resumed, resumeErr, pinned, advanced)
	after, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: fixture.githubDir, Worktree: checkout, IncludePromptBodies: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Manifest, before.Manifest) || !reflect.DeepEqual(after.Prompts, before.Prompts) || !reflect.DeepEqual(after.Claim, before.Claim) {
		t.Errorf("authenticated resume changed immutable creation records: before=%+v after=%+v", before, after)
	}
	if resumeErr != nil || len(resumed) != 1 || resumed[0].Status != "committed" || resumed[0].WorktreeDir != checkout {
		t.Errorf("authenticated registered checkout must resume across current-origin advance: results=%+v err=%v", resumed, resumeErr)
	} else if contents := mustReadEngineFile(t, filepath.Join(checkout, "dependency.txt")); contents != "new\n" {
		t.Errorf("resume did not apply in actual registered checkout: %q", contents)
	}
	if contents := mustReadEngineFile(t, filepath.Join(checkout, "unrelated.txt")); contents != "retained owned contents\n" {
		t.Error("resume changed unrelated owned contents")
	}
	if actual := runEngineGit(t, fixture.canonical, "worktree", "list", "--porcelain"); actual != registration {
		t.Error("resume changed actual Git registration")
	}
	if head := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "origin/main")); head != advanced {
		t.Errorf("public canonical fetch did not observe actual remote advance: %s", head)
	}
	if head := runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"); head != canonicalHead {
		t.Error("resume changed canonical checkout HEAD")
	}
	if contents := mustReadEngineFile(t, filepath.Join(fixture.canonical, "dependency.txt")); contents != canonicalContents {
		t.Error("resume changed canonical contents")
	}
}
