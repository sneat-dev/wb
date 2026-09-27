//go:build darwin || linux

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGCApplyRetiresEmptyUnscopedCanonicalStage(t *testing.T) {
	fixture := newGitFixture(t)
	worktreesRoot := filepath.Join(fixture.canonical, ".worktrees")
	if err := os.MkdirAll(worktreesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(worktreesRoot, testRetiredStage)
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	installPerCommitPullRequestFixture(t, nil)

	planned, err := GC(context.Background(), GCOptions{ProjectsRoot: fixture.projectsRoot, SkipSizes: true})
	if err != nil {
		t.Fatal(err)
	}
	artifact := gcArtifactAtPath(t, planned, stage)
	if !artifact.Eligible || artifact.Applied || artifact.Disposition != "empty_unscoped_local_retired_stage" {
		t.Fatalf("dry-run artifact = %#v", artifact)
	}
	if _, statErr := os.Stat(stage); statErr != nil {
		t.Fatalf("dry run removed empty retired stage: %v", statErr)
	}

	applied, err := GC(context.Background(), GCOptions{ProjectsRoot: fixture.projectsRoot, Apply: true, SkipSizes: true})
	if err != nil {
		t.Fatal(err)
	}
	artifact = gcArtifactAtPath(t, applied, stage)
	if !artifact.Applied || artifact.Disposition != "retired_empty_unscoped_local_stage" {
		t.Fatalf("applied artifact = %#v", artifact)
	}
	if _, statErr := os.Lstat(stage); !os.IsNotExist(statErr) {
		t.Fatalf("empty retired stage survived gc --apply: %v", statErr)
	}
}

func TestRetireEmptyUnscopedLocalStagePreservesStageThatBecameNonEmpty(t *testing.T) {
	worktreesRoot := t.TempDir()
	stage := filepath.Join(worktreesRoot, testRetiredStage)
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "recovery-evidence"), []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	artifacts := []LifecycleArtifact{{WorktreesRoot: worktreesRoot, Path: stage, Kind: lifecycleArtifactKindStage, State: "quarantined", Disposition: "empty_unscoped_local_retired_stage", Eligible: true}}

	retireEmptyUnscopedLocalStages(artifacts)

	artifact := artifacts[0]
	if artifact.Applied || artifact.Eligible || !strings.Contains(artifact.Reason, "became non-empty") {
		t.Fatalf("changed retired stage = %#v", artifact)
	}
	if _, statErr := os.Stat(filepath.Join(stage, "recovery-evidence")); statErr != nil {
		t.Fatalf("gc removed recovery evidence from a changed stage: %v", statErr)
	}
}

func TestRetireEmptyUnscopedLocalStageRefusesRenameReplacementRace(t *testing.T) {
	worktreesRoot := t.TempDir()
	stage := filepath.Join(worktreesRoot, testRetiredStage)
	escaped := filepath.Join(worktreesRoot, "concurrently-moved-stage")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts := []LifecycleArtifact{{WorktreesRoot: worktreesRoot, Path: stage, Kind: lifecycleArtifactKindStage, State: "quarantined", Disposition: "empty_unscoped_local_retired_stage", Eligible: true}}
	afterAuthorization := func() {
		if err := os.Rename(stage, escaped); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(stage, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	retireEmptyUnscopedLocalStagesAfterAuthorization(artifacts, afterAuthorization)

	artifact := artifacts[0]
	if artifact.Applied || artifact.Eligible || !strings.Contains(artifact.Reason, "identity changed") {
		t.Fatalf("renamed retired stage = %#v", artifact)
	}
	for _, path := range []string{stage, escaped} {
		if info, statErr := os.Stat(path); statErr != nil || !info.IsDir() {
			t.Fatalf("race participant %s was not preserved: %v, %v", path, info, statErr)
		}
	}
}

func TestRetireEmptyUnscopedLocalStageDoesNotClaimReplacementRemoval(t *testing.T) {
	worktreesRoot := t.TempDir()
	stage := filepath.Join(worktreesRoot, testRetiredStage)
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts := []LifecycleArtifact{{WorktreesRoot: worktreesRoot, Path: stage, Kind: lifecycleArtifactKindStage, State: "quarantined", Disposition: "empty_unscoped_local_retired_stage", Eligible: true}}
	var escaped string
	beforeRemoval := func(retiredName string) {
		isolated := filepath.Join(worktreesRoot, retiredName)
		escaped = isolated + "-concurrently-moved"
		if err := os.Rename(isolated, escaped); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(isolated, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	retireEmptyUnscopedLocalStagesWithHooks(artifacts, nil, beforeRemoval)

	artifact := artifacts[0]
	if artifact.Applied || artifact.Eligible || !strings.Contains(artifact.Reason, "exact stage was preserved") {
		t.Fatalf("final replacement race = %#v", artifact)
	}
	if info, statErr := os.Stat(escaped); statErr != nil || !info.IsDir() {
		t.Fatalf("the exact inspected stage was not preserved: %v, %v", info, statErr)
	}
}

func TestRetireEmptyUnscopedLocalStageRevalidatesPlannedIdentity(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		change func(t *testing.T, root, stage string) string
		want   string
	}{
		{"wrong parent", func(t *testing.T, root, stage string) string {
			return filepath.Join(root, "other", testRetiredStage)
		}, "exact canonical-local identity"},
		{"wrong name", func(t *testing.T, root, stage string) string {
			return filepath.Join(root, "ordinary-stage")
		}, "exact canonical-local identity"},
		{"missing stage", func(t *testing.T, root, stage string) string {
			if err := os.Remove(stage); err != nil {
				t.Fatal(err)
			}
			return stage
		}, "already absent"},
		{"symlink stage", func(t *testing.T, root, stage string) string {
			if err := os.Remove(stage); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), stage); err != nil {
				t.Fatal(err)
			}
			return stage
		}, "without following links"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root := t.TempDir()
			stage := filepath.Join(root, testRetiredStage)
			if err := os.Mkdir(stage, 0o700); err != nil {
				t.Fatal(err)
			}
			path := scenario.change(t, root, stage)
			artifacts := []LifecycleArtifact{{WorktreesRoot: root, Path: path, Kind: lifecycleArtifactKindStage, State: "quarantined", Disposition: dispositionEmptyUnscopedLocalRetiredStage, Eligible: true}}
			retireEmptyUnscopedLocalStages(artifacts)
			got := artifacts[0]
			if !strings.Contains(got.Reason, scenario.want) {
				t.Fatalf("result = %#v", got)
			}
			if scenario.name == "missing stage" {
				if !got.Applied || got.Disposition != dispositionRetiredEmptyUnscopedLocalStage {
					t.Fatalf("absent stage result = %#v", got)
				}
			} else if got.Applied || got.Eligible {
				t.Fatalf("unsafe stage was retired: %#v", got)
			}
			if scenario.name != "missing stage" {
				if _, err := os.Lstat(stage); err != nil {
					t.Fatalf("original stage changed: %v", err)
				}
			}
		})
	}
}

func TestRetireEmptyUnscopedLocalStagePreservesIsolatedStageChangedBeforeRemoval(t *testing.T) {
	root := t.TempDir()
	stage := filepath.Join(root, testRetiredStage)
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	artifacts := []LifecycleArtifact{{WorktreesRoot: root, Path: stage, Kind: lifecycleArtifactKindStage, State: "quarantined", Disposition: dispositionEmptyUnscopedLocalRetiredStage, Eligible: true}}
	retireEmptyUnscopedLocalStagesWithHooks(artifacts, nil, func(name string) {
		if err := os.WriteFile(filepath.Join(root, name, "new-evidence"), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	got := artifacts[0]
	if got.Applied || got.Eligible || !strings.Contains(got.Reason, "remove isolated empty retired") || got.ArchivePath == "" {
		t.Fatalf("changed isolated stage = %#v", got)
	}
	if data, err := os.ReadFile(filepath.Join(got.ArchivePath, "new-evidence")); err != nil || string(data) != "keep" {
		t.Fatalf("isolation lost new evidence: %q, %v", data, err)
	}
}

func gcArtifactAtPath(t *testing.T, outcome GCOutcome, path string) LifecycleArtifact {
	t.Helper()
	for _, artifact := range outcome.Artifacts {
		if artifact.Path == path {
			return artifact
		}
	}
	t.Fatalf("artifact %s missing from gc outcome: %#v", path, outcome.Artifacts)
	return LifecycleArtifact{}
}
