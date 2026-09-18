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
