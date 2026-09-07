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
)

// These tests exercise cleanup's recognition of a validated
// `wb worktree merge acknowledge-absorbed-conflict` acknowledgement as
// landing proof for a candidate whose head was never itself pushed anywhere
// -- the exact incident of 2026-09-07: three receipted sources landed, but
// the integration candidate's own branch was never published, and the
// ordinary "was never pushed" safety refused to retire it even after an
// operator reviewed and acknowledged that its content had already reached
// main. They build the receipt and acknowledgement sidecars directly, the
// same shape `internal/orchestrate` writes, rather than importing that
// package (which itself imports internal/worktrees and would cycle).

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
}

// writeAbsorbedConflictReceipt writes a worktree-merge receipt with just the
// fields findAbsorbedConflictCleanupProof reads. note lets a test rewrite the
// same receipt with different bytes (a tamper) while keeping every identity
// field the matcher checks unchanged.
func writeAbsorbedConflictReceipt(t *testing.T, home, task, worktree, branch, candidateSHA string, sourceSHAs []string, note string) absorbedConflictReceiptFixture {
	t.Helper()
	reportsDir := filepath.Join(home, "reports", "worktree-merge")
	if err := os.MkdirAll(reportsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Join(reportsDir, task+".json")
	sources := make([]map[string]string, 0, len(sourceSHAs))
	for index, sha := range sourceSHAs {
		sources = append(sources, map[string]string{
			"task": fmt.Sprintf("source-%d", index), "worktree": "/tmp/does-not-matter",
			"branch": fmt.Sprintf("source-%d", index), "sha": sha,
		})
	}
	receipt := map[string]any{
		"schema_version": 1,
		"receipt_path":   receiptPath,
		"note":           note,
		"candidate":      map[string]string{"task": task, "worktree": worktree, "branch": branch, "sha": candidateSHA},
		"sources":        sources,
	}
	contents, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receiptPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return absorbedConflictReceiptFixture{receiptPath: receiptPath, receiptBytes: contents}
}

// writeAbsorbedConflictAck writes an absorbed-conflict acknowledgement
// sidecar bound to fixture's exact current bytes (or, when valid is false, to
// a wrong hash -- simulating a receipt edited since the acknowledgement was
// written).
func writeAbsorbedConflictAck(t *testing.T, fixture absorbedConflictReceiptFixture, task, worktree, branch, candidateSHA, currentTargetSHA string, valid bool) string {
	t.Helper()
	digest := sha256.Sum256(fixture.receiptBytes)
	hash := hex.EncodeToString(digest[:])
	if !valid {
		hash = strings.Repeat("0", len(hash))
	}
	ack := map[string]any{
		"schema_version":     1,
		"status":             "absorbed_conflict_acknowledged",
		"receipt_path":       fixture.receiptPath,
		"receipt_sha256":     hash,
		"candidate_task":     task,
		"candidate_worktree": worktree,
		"candidate_branch":   branch,
		"candidate_sha":      candidateSHA,
		"current_target_sha": currentTargetSHA,
		"actor":              "founder@example.com",
		"reason":             "test acknowledgement",
		"recorded_at":        "2026-09-07T15:10:32Z",
	}
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	ackPath := fixture.receiptPath + absorbedConflictAckSuffix
	if err := os.WriteFile(ackPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return ackPath
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

	proof, receiptPath, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, second)
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

	proof, receiptPath, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, second)
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

	proof, receiptPath, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, second)
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

	proof, _, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, first)
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
	proof, receiptPath, err := findAbsorbedConflictCleanupProof(context.Background(), home, repoDir, task, worktree, second)
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
