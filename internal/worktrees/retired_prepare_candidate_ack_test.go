package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/retiredcandidateack"
)

func writeRetiredPrepareCandidateFixture(t *testing.T, home, receiptPath, task, worktree, branch, target, targetSHA, defaultSHA string, sources []retiredcandidateack.Source) {
	t.Helper()
	receipt := retiredPrepareCandidateReceipt{
		ReceiptPath: receiptPath, ID: "retired-prepare-receipt", Phase: "prepare", Status: "conflict",
		Lane: "merge-acme-app-retired", Repository: "acme/app", Target: target, TargetSHA: targetSHA,
		Candidate: retiredcandidateack.Source{Task: task, Worktree: worktree, Branch: branch}, Sources: sources,
	}
	bytes, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(receiptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, append(bytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := receipt.identity()
	hash, err := retiredcandidateack.FileSHA256(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	ack := retiredcandidateack.Acknowledgement{
		SchemaVersion: retiredcandidateack.SchemaVersion, Status: retiredcandidateack.Status,
		ReceiptPath: receiptPath, ReceiptSHA256: hash, ReceiptID: identity.ID, ReceiptPhase: identity.Phase,
		ReceiptStatus: identity.Status, Lane: identity.Lane, Repository: identity.Repository,
		Target: identity.Target, TargetSHA: identity.TargetSHA, Candidate: identity.Candidate, Sources: identity.Sources,
		DefaultBranch: "main", DefaultSHA: defaultSHA, Actor: "reviewer", Reason: "deterministic fixture",
		RecordedAt: time.Date(2026, time.September, 22, 0, 0, 0, 0, time.UTC),
	}
	ack.ID = retiredcandidateack.ComputeID(ack)
	if err := retiredcandidateack.Persist(retiredcandidateack.Path(receiptPath), ack); err != nil {
		t.Fatal(err)
	}
}

func retiredPrepareSource() []retiredcandidateack.Source {
	return []retiredcandidateack.Source{{
		Task: "original-source", Worktree: "/fixture/source", Branch: "feature/source",
		SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}
}

func TestAdoptCurrentLayoutOnlyWithRetiredPrepareCandidateAcknowledgement(t *testing.T) {
	fixture := newGitFixture(t)
	sharedRoot := filepath.Join(fixture.projectsRoot, ".worktrees")
	path := filepath.Join(sharedRoot, "retired-prepare-adoption", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	branch := "wb/integration/retired-prepare-adoption"
	gitTest(t, fixture.canonical, "worktree", "add", "-b", branch, path, "main")
	configureGitUser(t, path)
	head := gitTestOutput(t, path, "rev-parse", "HEAD")
	manifest := newCreatedManifest("retired-prepare-adoption")
	manifest.Worktree, manifest.Repository, manifest.Branch, manifest.Base, manifest.BaseSHA = path, "acme/app", branch, "deleted-target", head
	if err := WriteManifest(path, manifest); err != nil {
		t.Fatal(err)
	}

	without, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(without) != 1 || without[0].Action != AdoptSkipped {
		t.Fatalf("current layout without sidecar = %#v", without)
	}
	receiptPath := filepath.Join(fixture.home, "reports", "worktree-merge", "retired-prepare.json")
	writeRetiredPrepareCandidateFixture(t, fixture.home, receiptPath, manifest.EffortID, path, branch, "deleted-target", head, head, retiredPrepareSource())

	with, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(with) != 1 || with[0].Action != AdoptWouldAdopt || with[0].Task != manifest.EffortID {
		t.Fatalf("current layout with exact acknowledgement = %#v", with)
	}
	if err := os.WriteFile(receiptPath, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tampered, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Base: "main", Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(tampered) != 1 || tampered[0].Action != AdoptSkipped {
		t.Fatalf("tampered receipt admitted current-layout adoption = %#v", tampered)
	}
}

func TestCleanupRecoversRetiredPrepareCandidateAfterRecordedTargetDeleted(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "branch", "deleted-target", "main")
	gitTest(t, fixture.canonical, "push", "origin", "deleted-target")
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "retired-prepare-cleanup", Base: "deleted-target",
		WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := created[0]
	head := gitTestOutput(t, candidate.WorktreeDir, "rev-parse", "HEAD")
	if head != candidate.BaseSHA {
		t.Fatalf("candidate head = %s, want immutable target %s", head, candidate.BaseSHA)
	}
	gitTest(t, fixture.canonical, "push", "origin", ":deleted-target")
	defaultSHA := remoteBranchForTest(t, fixture.canonical, "main")
	receiptPath := filepath.Join(fixture.home, "reports", "worktree-merge", "retired-prepare-cleanup.json")
	writeRetiredPrepareCandidateFixture(t, fixture.home, receiptPath, "retired-prepare-cleanup", candidate.WorktreeDir, candidate.Branch, "deleted-target", head, defaultSHA, retiredPrepareSource())

	listed, err := List(context.Background(), ListOptions{ProjectsRoot: fixture.projectsRoot, Task: "retired-prepare-cleanup", Base: "main", GitHub: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || !listed[0].IntegratedAtOrigin || listed[0].Base != "main" ||
		listed[0].RetiredPrepareCandidateAcknowledgementPath != retiredcandidateack.Path(receiptPath) {
		t.Fatalf("retired prepare recovery = %#v", listed)
	}
	planned, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "retired-prepare-cleanup", Base: "main", OlderThan: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Results) != 1 || !planned.Results[0].Eligible || planned.Results[0].Proof != "retired_prepare_candidate_acknowledgement" {
		t.Fatalf("retired prepare cleanup plan = %#v", planned)
	}
	applied, err := Cleanup(context.Background(), CleanupOptions{ProjectsRoot: fixture.projectsRoot, Task: "retired-prepare-cleanup", Base: "main", Apply: true, OlderThan: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Results) != 1 || !applied.Results[0].Applied || !applied.Results[0].WorktreeGone ||
		applied.Results[0].Proof != "retired_prepare_candidate_acknowledgement" {
		t.Fatalf("retired prepare cleanup apply = %#v", applied)
	}
	if _, err := os.Stat(candidate.WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("retired candidate worktree remains after descriptor-safe cleanup: %v", err)
	}
}

func TestRetiredPrepareCandidateAcknowledgementRejectsChangedDefaultTarget(t *testing.T) {
	fixture := newGitFixture(t)
	gitTest(t, fixture.canonical, "checkout", "-b", "deleted-target")
	if err := os.WriteFile(filepath.Join(fixture.canonical, "target.txt"), []byte("target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "add", "target.txt")
	gitTest(t, fixture.canonical, "commit", "-m", "target only")
	gitTest(t, fixture.canonical, "checkout", "main")
	path := filepath.Join(filepath.Dir(fixture.projectsRoot), "external", "retired-proof", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "wb/integration/retired-proof", path, "deleted-target")
	configureGitUser(t, path)
	head := gitTestOutput(t, path, "rev-parse", "HEAD")
	receiptPath := filepath.Join(fixture.home, "reports", "worktree-merge", "retired-proof.json")
	writeRetiredPrepareCandidateFixture(t, fixture.home, receiptPath, "retired-proof", path, "wb/integration/retired-proof", "deleted-target", head, head, retiredPrepareSource())
	// The acknowledgement's default SHA is the candidate itself, but this
	// unrelated default tip cannot contain it. A stale/default-ref change is
	// never cleanup authority.
	main := gitTestOutput(t, fixture.canonical, "rev-parse", "main")
	proof, err := findRetiredPrepareCandidateAcknowledgement(context.Background(), fixture.home, fixture.canonical, "retired-proof", path, "wb/integration/retired-proof", head, "main", main)
	if err != nil {
		t.Fatal(err)
	}
	if proof != nil {
		t.Fatalf("changed default target accepted stale acknowledgement: %#v", proof)
	}
}
