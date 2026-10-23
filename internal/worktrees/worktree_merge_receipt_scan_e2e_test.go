//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/retiredcandidateack"
)

func TestE2EAbsorbedReceiptScanStopsAtFirstMatchingUnacknowledgedReceipt(t *testing.T) {
	t.Parallel()
	repo, head, target := newAbsorbedConflictProofRepo(t)
	home := t.TempDir()
	task, worktree, branch := "candidate", "/fixture/candidate", "wb/integration/candidate"
	valid := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, head, []string{head}, "valid")
	writeAbsorbedConflictAck(t, valid, task, worktree, branch, head, target, true)
	var first map[string]any
	if err := json.Unmarshal(valid.receiptBytes, &first); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(filepath.Dir(valid.receiptPath), "00-first.json")
	first["receipt_path"] = firstPath
	firstBytes, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firstPath, firstBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	proof, matched, err := findAbsorbedConflictCleanupProof(context.Background(), home, repo, task, worktree, branch, head, target)
	if err != nil || proof != nil || matched != firstPath {
		t.Fatalf("first unacknowledged receipt: proof=%+v matched=%q err=%v, want nil and %q", proof, matched, err, firstPath)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment for the bare origin.
func TestE2ERetiredReceiptScanStopsAtFirstTamperedSidecar(t *testing.T) {
	fixture := newGitFixture(t)
	worktree := fixture.canonical
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	task, branch := "candidate", "wb/integration/candidate"
	reports := filepath.Join(fixture.home, "reports", "worktree-merge")
	first := filepath.Join(reports, "00-first.json")
	second := filepath.Join(reports, "01-second.json")
	writeRetiredPrepareCandidateFixture(t, fixture.home, first, task, worktree, branch, "deleted-target", head, head, retiredPrepareSource())
	writeRetiredPrepareCandidateFixture(t, fixture.home, second, task, worktree, branch, "deleted-target", head, head, retiredPrepareSource())
	contents, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, append(contents, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	var later retiredPrepareCandidateReceipt
	laterBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(laterBytes, &later); err != nil {
		t.Fatal(err)
	}
	if _, err := retiredcandidateack.Load(retiredcandidateack.Path(second), later.identity()); err != nil {
		t.Fatalf("later receipt must have a valid acknowledgement to prove first-match refusal: %v", err)
	}
	if hasRetiredPrepareCandidateAcknowledgement(fixture.projectsRoot, task, worktree, branch, head) {
		t.Fatal("adoption accepted a later receipt after the first matched receipt was tampered")
	}
	if hasRetiredPrepareCandidateAcknowledgement(fixture.projectsRoot, "another-task", worktree, branch, head) {
		t.Fatal("adoption accepted unrelated task")
	}
	proof, err := findRetiredPrepareCandidateAcknowledgement(context.Background(), fixture.home, fixture.canonical, task, worktree, branch, head, "main", head)
	if err != nil || proof != nil {
		t.Fatalf("cleanup accepted a later receipt: proof=%+v err=%v", proof, err)
	}
	proof, err = findRetiredPrepareCandidateAcknowledgement(context.Background(), fixture.home, fixture.canonical, "another-task", worktree, branch, head, "main", head)
	if err != nil || proof != nil {
		t.Fatalf("cleanup accepted unrelated task: proof=%+v err=%v", proof, err)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment for this native journey.
func TestE2ERetiredPrepareReceiptAdoptionThenCleanup(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "branch", "deleted-target", "main")
	gitTest(t, fixture.canonical, "push", "origin", "deleted-target")
	task := "retired-prepare-adoption-cleanup"
	worktree := filepath.Join(fixture.projectsRoot, ".worktrees", task, "acme", "app")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		t.Fatal(err)
	}
	branch := "wb/integration/" + task
	gitTest(t, fixture.canonical, "worktree", "add", "-b", branch, worktree, "deleted-target")
	configureGitUser(t, worktree)
	head := gitTestOutput(t, worktree, "rev-parse", "HEAD")
	manifest := newCreatedManifest(task)
	manifest.Worktree, manifest.Repository, manifest.Branch, manifest.Base, manifest.BaseSHA = worktree, "acme/app", branch, "deleted-target", head
	if err := WriteManifest(worktree, manifest); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(fixture.home, "reports", "worktree-merge", "retired-prepare-adoption-cleanup.json")
	writeRetiredPrepareCandidateFixture(t, fixture.home, receiptPath, task, worktree, branch, "deleted-target", head, head, retiredPrepareSource())
	adopted, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: worktree, Apply: true})
	if err != nil || len(adopted) != 1 || adopted[0].Action != AdoptAdopted {
		t.Fatalf("adopt exact receipted candidate: %+v, %v", adopted, err)
	}
	gitTest(t, fixture.canonical, "push", "origin", ":deleted-target")
	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main", GitHub: true})
	if err != nil || len(listed) == 0 {
		t.Fatalf("fresh cleanup proof for adopted candidate: %+v, %v", listed, err)
	}
	for _, entry := range listed {
		if entry.WorktreeDir != worktree || entry.RetiredPrepareCandidateAcknowledgementPath != retiredcandidateack.Path(receiptPath) {
			t.Fatalf("fresh cleanup proof for adopted candidate: %+v", entry)
		}
	}
	planned, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: task, Base: "main", OlderThan: 0})
	if err != nil || len(planned.Results) == 0 {
		t.Fatalf("cleanup plan for adopted candidate: %+v, %v", planned, err)
	}
	for _, result := range planned.Results {
		if !result.Eligible || result.Proof != "retired_prepare_candidate_acknowledgement" {
			t.Fatalf("cleanup plan for adopted candidate: %+v", result)
		}
	}
}
