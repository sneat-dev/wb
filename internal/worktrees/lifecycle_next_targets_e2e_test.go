//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestE2ELifecycleNextRecordedBaseRefusesInvalidManifest(t *testing.T) {
	t.Parallel()
	path := t.TempDir()
	gitTest(t, path, "init")
	manifest := newCreatedManifest("task")
	manifest.Worktree = path
	manifest.Base = "invalid base"
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(path, ".wb", "local", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if base, err := resolveRecordedWorktreeBase(context.Background(), "", path, "main"); base != "" || err == nil || !strings.Contains(err.Error(), "has invalid base") {
		t.Fatalf("manifest admission=%q %v", base, err)
	}
	inspection := lifecycleInspection{ctx: context.Background(), worktree: path, base: "main"}
	if err := inspection.locate(); err == nil || !strings.Contains(err.Error(), "has invalid base") || inspection.result.WorktreeDir != "" {
		t.Fatalf("location admitted invalid immutable target: %+v %v", inspection.result, err)
	}
	after, err := os.ReadFile(filepath.Join(path, ".wb", "local", "manifest.yaml"))
	if err != nil || string(before) != string(after) {
		t.Fatalf("refusal rewrote immutable target: %v", err)
	}
}

//nolint:paralleltest // The native session fixture configures process-wide home and Git environment.
func TestE2ELifecycleNextParkedTargetUsesValidatedFallback(t *testing.T) {
	fixture := newParkedTargetCompletionFixture(t, "resume-lifecycle-fallback")
	manifest, err := ReadManifest(fixture.worktree)
	if err != nil || manifest.Base == "" || manifest.Base == "main" {
		t.Fatalf("native parked manifest=%+v %v", manifest, err)
	}
	manifestPath := filepath.Join(fixture.worktree, ".wb", "local", "manifest.yaml")
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	claim, _, _, err := activeWorkLogClaimReadOnly(fixture.base.home, fixture.worktree)
	if err != nil || claim.AcquiredVia != "parked_session_resume" {
		t.Fatalf("parked authority fixture=%+v %v", claim, err)
	}
	base, err := resolveRecordedWorktreeBase(context.Background(), fixture.base.home, fixture.worktree, "main")
	if err != nil || base != "main" {
		t.Fatalf("parked target fallback=%q %v", base, err)
	}
	if after, err := os.ReadFile(manifestPath); err != nil || string(after) != string(before) {
		t.Fatalf("fallback rewrote immutable recorded base %q: %v", manifest.Base, err)
	}
}

//nolint:paralleltest // Create and the native Git fixture configure process-wide environment.
func TestE2ELifecycleNextRecordedBaseRefusesInvalidPrivateClaim(t *testing.T) {
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{ProjectsRoot: fixture.projectsRoot, Operation: "lifecycle-invalid-target", WorkLog: WorkLogOptions{Model: "unknown"}})
	if err != nil || len(created) != 1 {
		t.Fatalf("native claim fixture=%+v %v", created, err)
	}
	path := created[0].WorktreeDir
	claim, projection, claimPath, err := activeWorkLogClaimReadOnly(fixture.home, path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	claim.Base = "invalid base"
	claim.ClaimID, err = expectedWorkLogClaimID(claim)
	if err != nil {
		t.Fatal(err)
	}
	projection.ClaimID = claim.ClaimID
	candidate := filepath.Join(filepath.Dir(claimPath), claim.ClaimID+".json")
	wtLifeCovWriteJSON(t, candidate, claim)
	if err := writeWorkLogProjection(path, projection); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(path, ".wb", "local", "manifest.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := activeWorkLogClaimReadOnly(fixture.home, path); err != nil {
		t.Fatalf("fixture failed before target grammar admission: %v", err)
	}
	if base, err := resolveRecordedWorktreeBase(context.Background(), fixture.home, path, "main"); base != "" || err == nil || !strings.Contains(err.Error(), "work log target record") || !strings.Contains(err.Error(), "invalid base") {
		t.Fatalf("private target admission=%q %v", base, err)
	}
	if data, err := os.ReadFile(claimPath); err != nil || string(data) != string(original) {
		t.Fatalf("original immutable claim was changed: %v", err)
	}
	if _, err := os.Stat(candidate); err != nil {
		t.Fatalf("refusal destroyed malformed authority: %v", err)
	}
}

func TestE2ELifecycleNextRecordedBaseRetainsMalformedProjection(t *testing.T) {
	t.Parallel()
	path, home := t.TempDir(), t.TempDir()
	directory := filepath.Join(path, workLogProjectionDirectory)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(directory, workLogProjectionName)
	bytes := []byte("{malformed native recovery projection")
	if err := os.WriteFile(projection, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	_, control := readWorkLogProjection(path)
	var nativeSyntax *json.SyntaxError
	if !errors.As(control, &nativeSyntax) {
		t.Fatalf("native decode prerequisite=%v", control)
	}
	base, err := resolveRecordedWorktreeBase(context.Background(), home, path, "main")
	var syntax *json.SyntaxError
	if base != "" || !errors.As(err, &syntax) || syntax.Offset != nativeSyntax.Offset || !strings.Contains(err.Error(), "read Work Log target record") {
		t.Fatalf("malformed target admitted=%q %v", base, err)
	}
	after, readErr := os.ReadFile(projection)
	if readErr != nil || string(after) != string(bytes) {
		t.Fatalf("refusal changed recovery evidence: %q %v", after, readErr)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("refusal published private claim state: %v %v", entries, err)
	}
}
