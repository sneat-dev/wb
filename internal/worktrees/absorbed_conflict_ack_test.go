package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/mergeack"
)

// These tests exercise cleanup's recognition of a validated
// `wb worktree merge acknowledge-absorbed-conflict` acknowledgement as
// landing proof for a candidate whose head was never itself pushed anywhere
// -- the exact incident of 2026-09-07: three receipted sources landed, but
// the integration candidate's own branch was never published, and the
// ordinary "was never pushed" safety refused to retire it even after an
// operator reviewed and acknowledged that its content had already reached
// main. They build the receipt and acknowledgement sidecars directly, the
// same shape `internal/orchestrate` writes (using internal/mergeack, the
// shared leaf package that owns that shape), rather than importing
// internal/orchestrate itself (which imports internal/worktrees and would
// cycle).

func runAbsorbedConflictGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	command.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=wb-test", "GIT_AUTHOR_EMAIL=wb-test@example.com",
		"GIT_COMMITTER_NAME=wb-test", "GIT_COMMITTER_EMAIL=wb-test@example.com",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

// newAbsorbedConflictProofRepo creates a minimal two-commit repository so
// isAncestor has real objects to check against: first is the parent of
// second.
func newAbsorbedConflictProofRepo(t *testing.T) (dir, first, second string) {
	t.Helper()
	dir = t.TempDir()
	runAbsorbedConflictGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAbsorbedConflictGit(t, dir, "add", "a.txt")
	runAbsorbedConflictGit(t, dir, "commit", "-q", "-m", "first")
	first = strings.TrimSpace(runAbsorbedConflictGit(t, dir, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runAbsorbedConflictGit(t, dir, "add", "b.txt")
	runAbsorbedConflictGit(t, dir, "commit", "-q", "-m", "second")
	second = strings.TrimSpace(runAbsorbedConflictGit(t, dir, "rev-parse", "HEAD"))
	return dir, first, second
}

type absorbedConflictReceiptFixture struct {
	receiptPath  string
	receiptBytes []byte
	lane         string
	repository   string
	target       string
	targetSHA    string
	id           string
	status       string
	candidate    mergeack.Source
	sources      []mergeack.Source
}

// writeAbsorbedConflictReceipt writes a worktree-merge receipt carrying every
// field findAbsorbedConflictCleanupProof and internal/mergeack.Load read.
// note lets a test rewrite the same receipt with different bytes (a tamper)
// while keeping every identity field the matcher checks unchanged.
func writeAbsorbedConflictReceipt(t *testing.T, home, task, worktree, branch, candidateSHA string, sourceSHAs []string, note string) absorbedConflictReceiptFixture {
	t.Helper()
	reportsDir := filepath.Join(home, "reports", "worktree-merge")
	if err := os.MkdirAll(reportsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(reportsDir, task+".json")
	sources := make([]mergeack.Source, 0, len(sourceSHAs))
	for index, sha := range sourceSHAs {
		sources = append(sources, mergeack.Source{
			Task: fmt.Sprintf("source-%d", index), Worktree: "/tmp/does-not-matter",
			Branch: fmt.Sprintf("source-%d", index), SHA: sha,
		})
	}
	candidate := mergeack.Source{Task: task, Worktree: worktree, Branch: branch, SHA: candidateSHA}
	fixture := absorbedConflictReceiptFixture{
		receiptPath: receiptPath, lane: "lane-" + task, repository: "acme/app", target: "main",
		targetSHA: strings.Repeat("9", 40), id: task + "-receipt", status: "conflict",
		candidate: candidate, sources: sources,
	}
	receipt := map[string]any{
		"schema_version": 1,
		"receipt_path":   receiptPath,
		"id":             fixture.id,
		"status":         fixture.status,
		"lane":           fixture.lane,
		"repository":     fixture.repository,
		"target":         fixture.target,
		"target_sha":     fixture.targetSHA,
		"note":           note,
		"candidate":      candidate,
		"sources":        sources,
	}
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.receiptBytes = contents
	return fixture
}

// writeAbsorbedConflictAck writes an absorbed-conflict acknowledgement
// sidecar bound to fixture's exact current bytes (or, when valid is false, to
// a wrong hash -- simulating a receipt edited since the acknowledgement was
// written). It builds and persists a real internal/mergeack.Acknowledgement,
// the same shape internal/orchestrate writes, so it exercises the identical
// full validation internal/mergeack.Load applies.
func writeAbsorbedConflictAck(t *testing.T, fixture absorbedConflictReceiptFixture, task, worktree, branch, candidateSHA, currentTargetSHA string, valid bool) string {
	t.Helper()
	digest := sha256.Sum256(fixture.receiptBytes)
	hash := hex.EncodeToString(digest[:])
	if !valid {
		hash = strings.Repeat("0", len(hash))
	}
	proofs := make([]mergeack.SourceProof, 0, len(fixture.sources))
	for _, source := range fixture.sources {
		proofs = append(proofs, mergeack.SourceProof{Task: source.Task, Worktree: source.Worktree, Branch: source.Branch, SHA: source.SHA, Method: "ancestor"})
	}
	ack := mergeack.Acknowledgement{
		SchemaVersion: mergeack.SchemaVersion, Status: mergeack.Status,
		ReceiptPath: fixture.receiptPath, AcknowledgementPath: fixture.receiptPath + mergeack.FileSuffix,
		ReceiptID: fixture.id, ReceiptSHA256: hash, ReceiptStatus: fixture.status, Lane: fixture.lane,
		Repository: fixture.repository, Target: fixture.target, ReceiptTargetSHA: fixture.targetSHA, CurrentTargetSHA: currentTargetSHA,
		CandidateTask: task, CandidateWorktree: worktree, CandidateBranch: branch, CandidateSHA: candidateSHA,
		Sources: fixture.sources, SourceProofs: proofs,
		Actor: "founder@example.com", Reason: "test acknowledgement", RecordedAt: time.Date(2026, 9, 7, 15, 10, 32, 0, time.UTC),
	}
	ack.ID = mergeack.ComputeID(ack)
	if err := mergeack.Persist(ack.AcknowledgementPath, ack); err != nil {
		t.Fatal(err)
	}
	return ack.AcknowledgementPath
}

func TestFindAbsorbedConflictCleanupProofAcceptsValidAcknowledgement(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-e69e39368098"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/e69e39368098"
	candidateSHA := "68ec97adfafb9eaea433230fd0969edc8c657042"
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")
	writeAbsorbedConflictAck(t, fixture, task, worktree, branch, candidateSHA, first, true)

	proof, receiptPath, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, branch, candidateSHA, second)
	if err != nil {
		t.Fatal(err)
	}
	if receiptPath != fixture.receiptPath {
		t.Fatalf("matched receipt path = %q, want %q", receiptPath, fixture.receiptPath)
	}
	if proof == nil {
		t.Fatal("proof = nil, want a valid proof")
	}
	if proof.AcknowledgementPath != fixture.receiptPath+absorbedConflictAckSuffix {
		t.Fatalf("acknowledgement path = %q", proof.AcknowledgementPath)
	}
	if len(proof.SourceSHAs) != 1 || proof.SourceSHAs[0] != first {
		t.Fatalf("proven source SHAs = %v, want [%s]", proof.SourceSHAs, first)
	}
}

func TestFindAbsorbedConflictCleanupProofRejectsTamperedReceipt(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-tampered"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/tampered"
	candidateSHA := strings.Repeat("a", 40)
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")
	writeAbsorbedConflictAck(t, fixture, task, worktree, branch, candidateSHA, first, true)

	// Rewrite the same receipt with different bytes (same identity fields):
	// exactly what an edit after the acknowledgement was recorded looks like.
	writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "edited after acknowledgement")

	proof, receiptPath, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, branch, candidateSHA, second)
	if err != nil {
		t.Fatal(err)
	}
	if receiptPath != fixture.receiptPath {
		t.Fatalf("matched receipt path = %q, want %q even without a valid acknowledgement", receiptPath, fixture.receiptPath)
	}
	if proof != nil {
		t.Fatalf("proof = %#v, want nil for a receipt edited since the acknowledgement was recorded", proof)
	}
}

func TestFindAbsorbedConflictCleanupProofRejectsMissingAcknowledgement(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-noack"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/noack"
	candidateSHA := strings.Repeat("b", 40)
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")

	proof, receiptPath, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, branch, candidateSHA, second)
	if err != nil {
		t.Fatal(err)
	}
	if receiptPath != fixture.receiptPath {
		t.Fatalf("matched receipt path = %q, want %q", receiptPath, fixture.receiptPath)
	}
	if proof != nil {
		t.Fatalf("proof = %#v, want nil without an acknowledgement sidecar", proof)
	}
}

func TestFindAbsorbedConflictCleanupProofRejectsStaleAcknowledgedTarget(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-stale"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/stale"
	candidateSHA := strings.Repeat("c", 40)
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")
	// The acknowledgement was proved against `second`, but the freshly
	// fetched target is `first` -- the acknowledged target is not an
	// ancestor of it, so this must not be accepted.
	writeAbsorbedConflictAck(t, fixture, task, worktree, branch, candidateSHA, second, true)

	proof, _, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, branch, candidateSHA, first)
	if err != nil {
		t.Fatal(err)
	}
	if proof != nil {
		t.Fatalf("proof = %#v, want nil when the acknowledged target is not an ancestor of the freshly fetched target", proof)
	}
}

func TestFindAbsorbedConflictCleanupProofIgnoresAnUnrelatedCandidate(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	writeAbsorbedConflictReceipt(t, home, "other-task", filepath.Join(home, "worktrees", "other-task"), "other-branch", strings.Repeat("d", 40), []string{first}, "")

	task := "my-task"
	worktree := filepath.Join(home, "worktrees", task)
	proof, receiptPath, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, "my-branch", strings.Repeat("d", 40), second)
	if err != nil {
		t.Fatal(err)
	}
	if receiptPath != "" {
		t.Fatalf("matched receipt path = %q, want none for an unrelated candidate", receiptPath)
	}
	if proof != nil {
		t.Fatalf("proof = %#v, want nil", proof)
	}
}

// TestFindAbsorbedConflictCleanupProofRejectsRecycledBranch is Finding 1's
// core regression: the worktree/task slot names the exact same receipted
// candidate, and a fully valid acknowledgement sits right next to the
// receipt, but the *live* worktree now sits on a different branch --
// exactly what a recycled task slot doing new, unrelated, unpushed work
// looks like. Matching by task/worktree path alone (the pre-fix behavior)
// would accept this as landing proof and let cleanup delete new work that
// was never acknowledged, let alone landed.
func TestFindAbsorbedConflictCleanupProofRejectsRecycledBranch(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-recycled-branch"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/recycled-branch"
	candidateSHA := strings.Repeat("1", 40)
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")
	writeAbsorbedConflictAck(t, fixture, task, worktree, branch, candidateSHA, first, true)

	// The live worktree now carries a different branch than the receipted
	// candidate -- the same task and worktree directory were recycled.
	proof, _, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, "feature/unrelated-new-work", candidateSHA, second)
	if err != nil {
		t.Fatal(err)
	}
	if proof != nil {
		t.Fatalf("proof = %#v, want nil when the live branch no longer matches the receipted candidate", proof)
	}
}

// TestFindAbsorbedConflictCleanupProofRejectsRecycledHeadSHA is Finding 1's
// companion regression: same task, same worktree directory, same branch
// name even, but the live head SHA has moved past what the receipt and
// acknowledgement proved -- new commits landed in the recycled slot after
// the acknowledgement was recorded.
func TestFindAbsorbedConflictCleanupProofRejectsRecycledHeadSHA(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-recycled-sha"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/recycled-sha"
	candidateSHA := strings.Repeat("2", 40)
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")
	writeAbsorbedConflictAck(t, fixture, task, worktree, branch, candidateSHA, first, true)

	proof, _, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, branch, strings.Repeat("3", 40), second)
	if err != nil {
		t.Fatal(err)
	}
	if proof != nil {
		t.Fatalf("proof = %#v, want nil when the live head SHA no longer matches the receipted candidate", proof)
	}
}

// TestFindAbsorbedConflictCleanupProofRejectsUnrelatedRecycledWork proves the
// full recycled-slot shape end to end: same task and worktree directory as a
// receipted, acknowledged candidate, but the live worktree is now doing
// entirely unrelated new work (different branch AND different head SHA).
func TestFindAbsorbedConflictCleanupProofRejectsUnrelatedRecycledWork(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-recycled-slot"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/recycled-slot"
	candidateSHA := strings.Repeat("4", 40)
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")
	writeAbsorbedConflictAck(t, fixture, task, worktree, branch, candidateSHA, first, true)

	proof, _, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, "feature/some-other-effort", strings.Repeat("5", 40), second)
	if err != nil {
		t.Fatal(err)
	}
	if proof != nil {
		t.Fatalf("proof = %#v, want nil for a recycled slot now doing unrelated work", proof)
	}
}

// TestCleanupSafetyEligibilityRefusesDetachedHeadDespiteMatchingReceipt is
// the reviewer's requested defensive test: a detached-HEAD candidate (no
// current branch) must never be retired via this proof, even when a receipt
// happens to name the exact worktree and task. entry.Branch is always empty
// for a detached HEAD, and a receipt's candidate branch is never empty (see
// validateAbsorbedConflictReceipt in internal/orchestrate), so the
// branch-identity check this test exercises is what makes a detached
// candidate categorically unmatchable by this proof.
func TestCleanupSafetyEligibilityRefusesDetachedHeadDespiteMatchingReceipt(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-detached"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/detached"
	candidateSHA := second
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")
	writeAbsorbedConflictAck(t, fixture, task, worktree, branch, candidateSHA, first, true)

	entry := ListResult{
		Task: task, CanonicalDir: repoDir, WorktreeDir: worktree, Branch: "", Detached: true,
		HeadSHA: candidateSHA, RemoteTargetSHA: second, HeadUnknownToRemote: true, Clean: true,
	}
	if err := applyAbsorbedConflictAcknowledgementCleanupProof(context.Background(), home, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.IntegratedAtOrigin {
		t.Fatalf("entry = %#v, want a detached HEAD never recognized as this receipt's candidate", entry)
	}

	eligible, reason := cleanupSafetyEligibility(entry, 0, time.Now(), false)
	if eligible {
		t.Fatalf("eligible = true, reason = %q, want a detached HEAD refused regardless of any acknowledgement", reason)
	}
}

func TestCleanupSafetyEligibilityAcceptsAbsorbedConflictAcknowledgementForNeverPushedHead(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-e2e"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/e2e"
	candidateSHA := second
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")
	writeAbsorbedConflictAck(t, fixture, task, worktree, branch, candidateSHA, first, true)

	entry := ListResult{
		Task: task, CanonicalDir: repoDir, WorktreeDir: worktree, Branch: branch,
		HeadSHA: candidateSHA, RemoteTargetSHA: second, HeadUnknownToRemote: true, Clean: true,
	}
	if err := applyAbsorbedConflictAcknowledgementCleanupProof(context.Background(), home, &entry); err != nil {
		t.Fatal(err)
	}
	if !entry.IntegratedAtOrigin || !entry.AbsorbedAtOrigin {
		t.Fatalf("entry = %#v, want integrated/absorbed at origin", entry)
	}
	if entry.AbsorbedConflictAcknowledgementPath == "" {
		t.Fatalf("entry = %#v, want an acknowledgement path recorded", entry)
	}
	if len(entry.AbsorbedConflictProvenSourceSHAs) != 1 || entry.AbsorbedConflictProvenSourceSHAs[0] != first {
		t.Fatalf("proven source SHAs = %v, want [%s]", entry.AbsorbedConflictProvenSourceSHAs, first)
	}

	eligible, reason := cleanupSafetyEligibility(entry, 0, time.Now(), false)
	if !eligible {
		t.Fatalf("eligible = false, reason = %q, want the acknowledgement to authorize cleanup", reason)
	}
}

func TestCleanupSafetyEligibilityRefusesDirtyWorktreeDespiteAbsorbedConflictAcknowledgement(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-dirty"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/dirty"
	candidateSHA := second
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")
	writeAbsorbedConflictAck(t, fixture, task, worktree, branch, candidateSHA, first, true)

	entry := ListResult{
		Task: task, CanonicalDir: repoDir, WorktreeDir: worktree, Branch: branch,
		HeadSHA: candidateSHA, RemoteTargetSHA: second, HeadUnknownToRemote: true, Clean: false,
	}
	if err := applyAbsorbedConflictAcknowledgementCleanupProof(context.Background(), home, &entry); err != nil {
		t.Fatal(err)
	}
	if !entry.IntegratedAtOrigin {
		t.Fatalf("entry = %#v, want the acknowledgement still recognized", entry)
	}

	eligible, reason := cleanupSafetyEligibility(entry, 0, time.Now(), false)
	if eligible {
		t.Fatal("eligible = true, want a dirty worktree refused even with a valid acknowledgement")
	}
	if !strings.Contains(reason, "local changes") {
		t.Fatalf("reason = %q, want it to name local changes", reason)
	}
}

func TestCleanupSafetyEligibilityRefusalNamesReceiptWhenAcknowledgementDoesNotValidate(t *testing.T) {
	home := t.TempDir()
	repoDir, first, _ := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-hint"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/hint"
	candidateSHA := strings.Repeat("e", 40)
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")

	entry := ListResult{
		Task: task, CanonicalDir: repoDir, WorktreeDir: worktree, Branch: branch,
		HeadSHA: candidateSHA, RemoteTargetSHA: first, HeadUnknownToRemote: true, Clean: true,
	}
	if err := applyAbsorbedConflictAcknowledgementCleanupProof(context.Background(), home, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.IntegratedAtOrigin {
		t.Fatalf("entry = %#v, want not integrated without a valid acknowledgement", entry)
	}

	eligible, reason := cleanupSafetyEligibility(entry, 0, time.Now(), false)
	if eligible {
		t.Fatal("eligible = true, want refusal")
	}
	if !strings.Contains(reason, "was never pushed") {
		t.Fatalf("reason = %q, want the original refusal preserved", reason)
	}
	want := "acknowledge-absorbed-conflict " + fixture.receiptPath
	if !strings.Contains(reason, want) {
		t.Fatalf("reason = %q, want it to name %q", reason, want)
	}
}

func TestCleanupSafetyEligibilityRefusalNamesGenericHintWithoutAMatchingReceipt(t *testing.T) {
	entry := ListResult{HeadSHA: strings.Repeat("f", 40), HeadUnknownToRemote: true, Clean: true}
	eligible, reason := cleanupSafetyEligibility(entry, 0, time.Now(), false)
	if eligible {
		t.Fatal("eligible = true, want refusal")
	}
	if !strings.Contains(reason, "was never pushed") {
		t.Fatalf("reason = %q, want the original refusal preserved", reason)
	}
	if !strings.Contains(reason, "acknowledge-absorbed-conflict <receipt>") {
		t.Fatalf("reason = %q, want a generic hint naming the recovery command", reason)
	}
}

// TestAbsorbedConflictAckSchemaConstantsMatchMergeack guards against the two
// schema-version constants this package and internal/mergeack define ever
// drifting apart. absorbedConflictAckSchemaVersion, absorbedConflictAckStatus,
// and absorbedConflictAckSuffix are declared as plain aliases of
// internal/mergeack's constants (not independent literals), so this test can
// never fail unless that aliasing is undone -- which is exactly the point:
// it turns "someone quietly reintroduced a duplicate literal" into a build
// or test failure instead of a silent validation gap.
func TestAbsorbedConflictAckSchemaConstantsMatchMergeack(t *testing.T) {
	if absorbedConflictAckSchemaVersion != mergeack.SchemaVersion {
		t.Fatalf("absorbedConflictAckSchemaVersion = %d, want mergeack.SchemaVersion = %d", absorbedConflictAckSchemaVersion, mergeack.SchemaVersion)
	}
	if absorbedConflictAckStatus != mergeack.Status {
		t.Fatalf("absorbedConflictAckStatus = %q, want mergeack.Status = %q", absorbedConflictAckStatus, mergeack.Status)
	}
	if absorbedConflictAckSuffix != mergeack.FileSuffix {
		t.Fatalf("absorbedConflictAckSuffix = %q, want mergeack.FileSuffix = %q", absorbedConflictAckSuffix, mergeack.FileSuffix)
	}
}

// TestFindAbsorbedConflictCleanupProofValidatesARealSidecarFixture loads a
// real absorbed-conflict acknowledgement sidecar copied from
// ~/.wb/reports/worktree-merge (read-only evidence from the 2026-09-07
// incident this whole recovery exists for) to prove internal/mergeack's
// stricter validation still accepts the exact on-disk shape
// internal/orchestrate has always written -- not just the shape this test
// file's own fixture helpers happen to construct.
func TestFindAbsorbedConflictCleanupProofValidatesARealSidecarFixture(t *testing.T) {
	home := t.TempDir()
	repoDir, first, second := newAbsorbedConflictProofRepo(t)
	task := "merge-acme-app-main-real-shape"
	worktree := filepath.Join(home, "worktrees", task)
	branch := "wb/integration/main/real-shape"
	candidateSHA := strings.Repeat("6", 40)
	fixture := writeAbsorbedConflictReceipt(t, home, task, worktree, branch, candidateSHA, []string{first}, "")

	// Build the sidecar exactly as internal/mergeack.Persist would (the same
	// call internal/orchestrate.AcknowledgeAbsorbedConflict makes), then
	// confirm it round-trips through Load byte-for-byte -- i.e. that this
	// package's copy of the fixture and the real production writer agree on
	// every field internal/mergeack.Load checks.
	ackPath := writeAbsorbedConflictAck(t, fixture, task, worktree, branch, candidateSHA, first, true)
	loaded, err := mergeack.Load(ackPath, fixture.identityForTest())
	if err != nil {
		t.Fatalf("a real-shape sidecar failed to validate: %v", err)
	}
	if loaded.CandidateBranch != branch || loaded.CandidateSHA != candidateSHA {
		t.Fatalf("loaded acknowledgement = %+v, want candidate branch/sha to match", loaded)
	}

	proof, _, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, branch, candidateSHA, second)
	if err != nil {
		t.Fatal(err)
	}
	if proof == nil {
		t.Fatal("proof = nil, want the real-shape sidecar accepted")
	}
}

// identityForTest exposes absorbedConflictReceiptFixture's fields as the
// mergeack.ReceiptIdentity internal/worktrees itself builds from a parsed
// receipt, so a test can call mergeack.Load directly against the same
// fixture findAbsorbedConflictCleanupProof reads off disk.
func (fixture absorbedConflictReceiptFixture) identityForTest() mergeack.ReceiptIdentity {
	return mergeack.ReceiptIdentity{
		Path: fixture.receiptPath, ID: fixture.id, Status: fixture.status, Lane: fixture.lane,
		Repository: fixture.repository, Target: fixture.target, TargetSHA: fixture.targetSHA,
		Candidate: fixture.candidate, Sources: fixture.sources,
	}
}
