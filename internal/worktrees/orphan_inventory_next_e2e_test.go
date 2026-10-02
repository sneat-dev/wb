//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // The existing native orphan fixture pins HOME and WB_PROJECTS_ROOT.
func TestE2EOrphanInventoryExplainsBrokenCloneWithoutMutatingEvidence(t *testing.T) {
	fixture := newOrphanFixture(t)
	path := fixture.addWorktree(filepath.Join(fixture.store, "visible", "acme", "app"), "visible", true)
	head := gitTestOutput(t, path, "rev-parse", "HEAD")
	if _, err := AppendPrompt(path, PromptHeader{Source: PromptSourceHuman}, []byte("retained private prompt")); err != nil {
		t.Fatal(err)
	}
	for _, task := range []string{"z-residue", "a-residue"} {
		residue := filepath.Join(fixture.store, task, "acme", "app")
		if err := os.MkdirAll(residue, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(residue, "evidence"), []byte(task), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	broken := filepath.Join(fixture.projectsRoot, "acme", "broken")
	if err := os.MkdirAll(filepath.Join(broken, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := []byte("retained incomplete repository\n")
	if err := os.WriteFile(filepath.Join(broken, ".git", "evidence"), sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.projectsRoot, "acme", "ordinary-file"), []byte("notes"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := fixture.report(t, time.Now().UTC())
	if report.Totals.Residue != 2 || len(report.Residue) != 2 || report.Residue[0].Task != "a-residue" || report.Residue[1].Task != "z-residue" {
		t.Fatalf("residue ordering: %+v", report.Residue)
	}
	visible, ok := findOrphan(report, "visible")
	if !ok || !visible.HasPrompts || visible.PromptCount != 1 {
		t.Fatalf("prompt inventory: %+v", visible)
	}
	for _, residue := range report.Residue {
		if got, err := os.ReadFile(filepath.Join(residue.Path, "evidence")); err != nil || string(got) != residue.Task {
			t.Fatalf("read-only residue evidence: %q %v", got, err)
		}
	}
	if _, ok := findOrphan(report, "visible"); !ok {
		t.Fatal("broken clone hid valid registration")
	}
	if len(report.Unscanned) != 1 || !strings.HasPrefix(report.Unscanned[0], broken+": ") {
		t.Fatalf("unscanned: %v", report.Unscanned)
	}
	after, err := os.ReadFile(filepath.Join(broken, ".git", "evidence"))
	if err != nil || !reflect.DeepEqual(after, sentinel) {
		t.Fatalf("evidence changed: %q %v", after, err)
	}
	if after := gitTestOutput(t, path, "rev-parse", "HEAD"); after != head {
		t.Fatalf("HEAD changed: %s", after)
	}
}

//nolint:paralleltest // The existing native orphan fixture pins HOME and WB_PROJECTS_ROOT.
func TestE2EBackfillRechecksNativeInventoryBeforeAdoption(t *testing.T) {
	fixture := newOrphanFixture(t)
	path := fixture.addWorktree(filepath.Join(fixture.store, "adopt", "acme", "app"), "adopt", true)
	report := fixture.report(t, time.Now().UTC())
	if len(report.Families) != 1 || len(report.Families[0].Worktrees) != 1 || report.Families[0].Worktrees[0].Path != path || report.Families[0].Worktrees[0].HasManifest {
		t.Fatalf("native prerequisite inventory: %+v", report)
	}
	marker, err := os.ReadFile(filepath.Join(path, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	var nativeCause error
	observed := false
	results := backfillOrphanReport(t.Context(), report, true, func(ctx context.Context, worktree string) (Manifest, error) {
		if worktree != path {
			t.Fatalf("reconstruction path=%s", worktree)
		}
		if err := os.Remove(filepath.Join(path, ".git")); err != nil {
			t.Fatal(err)
		}
		observed = true
		manifest, err := ReconstructManifest(ctx, worktree)
		nativeCause = err
		return manifest, err
	})
	if !observed || nativeCause == nil || len(results) != 1 || results[0].Action != BackfillSkipped || results[0].Reason != nativeCause.Error() {
		t.Fatalf("rechecked adoption: %+v cause=%v observed=%v", results, nativeCause, observed)
	}
	if _, err := ReadManifest(path); err == nil {
		t.Fatal("refused adoption published a manifest")
	}
	if err := os.WriteFile(filepath.Join(path, ".git"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gitTestOutput(t, path, "branch", "--show-current"); got != "adopt" {
		t.Fatalf("branch changed: %s", got)
	}
}

func TestE2EOrphanRecheckUsesOwnedImmutableClaim(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	runPath := filepath.Join(root, "run")
	claimsPath := filepath.Join(runPath, "claims")
	if err := os.MkdirAll(claimsPath, 0o700); err != nil {
		t.Fatal(err)
	}
	claim := workLogClaim{Version: 1, Lifecycle: "active", EffortID: "task", RunID: "run", Task: "task", Repository: "acme/app", Worktree: filepath.Join(root, "checkout"), Branch: "feature/task", Base: "main", BaseSHA: strings.Repeat("a", 40)}
	id, err := expectedWorkLogClaimID(claim)
	if err != nil {
		t.Fatal(err)
	}
	claim.ClaimID = id
	if err := validateOrphanedClaimIdentity(claim); err != nil {
		t.Fatalf("native valid identity prerequisite: %v", err)
	}
	claimPath := filepath.Join(claimsPath, id+".json")
	writeOrphanDraftClaim(t, claimPath, claim)
	run, err := os.Open(runPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	if err := recheckOrphanedClaim(run, claim); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(claimPath, []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recheckOrphanedClaim(run, claim); err == nil || !strings.HasPrefix(err.Error(), "reread immutable claim:") {
		t.Fatalf("parse refusal: %v", err)
	}
	invalid := claim
	invalid.Lifecycle = "terminal"
	writeOrphanDraftClaim(t, claimPath, invalid)
	if err := recheckOrphanedClaim(run, claim); err == nil || err.Error() != "immutable claim identity is incomplete or invalid" {
		t.Fatalf("identity refusal: %v", err)
	}
	changed := claim
	changed.Task = "different-task"
	writeOrphanDraftClaim(t, claimPath, changed)
	if err := validateOrphanedClaimIdentity(changed); err != nil {
		t.Fatalf("valid changed identity prerequisite: %v", err)
	}
	if err := recheckOrphanedClaim(run, claim); err == nil || !strings.Contains(err.Error(), "no longer describe") {
		t.Fatalf("changed claim refusal: %v", err)
	}
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	_, cause := openPrivateChild(run, "claims", false)
	if cause == nil {
		t.Fatal("closed owned directory unexpectedly reopened")
	}
	if err := recheckOrphanedClaim(run, claim); !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "open immutable claims:") {
		t.Fatalf("owned native cause: %v control=%v", err, cause)
	}
	var retained workLogClaim
	raw, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect refusal must preserve the last immutable occupant, not restore its predecessor.
	if err := json.Unmarshal(raw, &retained); err != nil || !reflect.DeepEqual(retained, changed) {
		t.Fatalf("claim changed on refusal: %+v %v", retained, err)
	}
}

//nolint:paralleltest // The existing native orphan fixture pins HOME and WB_PROJECTS_ROOT.
func TestE2EOrphanMissingIdentityFallbacksAreReadOnly(t *testing.T) {
	fixture := newOrphanFixture(t)
	clone := canonicalClone{path: fixture.canonical, repository: "acme/app"}
	// These are defensive linked-registry inputs. No claim that native Git emits an empty branch for a named branch.
	missing := filepath.Join(fixture.root, ".worktrees", "invalid effort")
	for _, tc := range []struct{ branch, want string }{{"feature/topic/part", "topic.part"}, {"", "invalid effort"}} {
		got := inspectOrphan(t.Context(), fixture.home, clone, linkedWorktree{path: missing, branch: tc.branch}, fixture.store, fixture.legacyHome, "main", 14*24*time.Hour, time.Now())
		if got.EffortID != tc.want || !got.Missing || got.Disposition != DispositionReview || !evidenceContains(got.Evidence, "working tree is gone") {
			t.Fatalf("missing defensive identity: %+v", got)
		}
		if _, err := os.Lstat(missing); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspection created checkout: %v", err)
		}
	}
}
