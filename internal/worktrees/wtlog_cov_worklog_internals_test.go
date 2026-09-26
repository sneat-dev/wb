package worktrees

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestWtLogCovCorroborateExistingRunPrompt(t *testing.T) {
	home := t.TempDir()
	if err := corroborateExistingRunPrompt(home, "effort", "missing-run", WorkLogOptions{}); err != nil {
		t.Fatalf("absent run must be accepted: %v", err)
	}

	// A run with no archive, no metadata, and no index accepts a new prompt.
	runDir, _, err := openWorkLogRun(home, "effort", "fresh-run", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := corroborateExistingRunPrompt(home, "effort", "fresh-run", WorkLogOptions{}); err != nil {
		t.Fatalf("empty run must be accepted: %v", err)
	}
	_ = runDir.Close()

	body := []byte("exact original request\n")
	digest := sha256.Sum256(body)
	hexDigest := hex.EncodeToString(digest[:])
	metadata := workLogPromptMetadata{Version: 1, SHA256: hexDigest, SourceReference: "file", CapturedAt: time.Now().UTC()}

	archiveRun, _, err := openWorkLogRun(home, "effort", "archive-run", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = archiveRun.Close() }()
	if err := writeBytesImmutableAt(archiveRun, "original-prompt.txt", body, 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := corroborateExistingRunPrompt(home, "effort", "archive-run", WorkLogOptions{}); err == nil || !strings.Contains(err.Error(), "already has an original prompt") {
		t.Fatalf("archive without supplied bytes error = %v", err)
	}
	same := WorkLogOptions{originalPromptContents: body}
	if err := corroborateExistingRunPrompt(home, "effort", "archive-run", same); err != nil {
		t.Fatalf("identical prompt bytes rejected without metadata: %v", err)
	}
	if err := writeJSONImmutableAt(archiveRun, "original-prompt.json", metadata, false); err != nil {
		t.Fatal(err)
	}
	if err := corroborateExistingRunPrompt(home, "effort", "archive-run", same); err != nil {
		t.Fatalf("identical prompt bytes with metadata rejected: %v", err)
	}
	different := WorkLogOptions{originalPromptContents: []byte("other bytes\n")}
	if err := corroborateExistingRunPrompt(home, "effort", "archive-run", different); err == nil || !strings.Contains(err.Error(), "different original prompt bytes") {
		t.Fatalf("different prompt bytes error = %v", err)
	}

	// Metadata that disagrees with the archive is refused.
	badMetadataRun, _, err := openWorkLogRun(home, "effort", "bad-metadata", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = badMetadataRun.Close() }()
	if err := writeBytesImmutableAt(badMetadataRun, "original-prompt.txt", body, 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(badMetadataRun, "original-prompt.json", workLogPromptMetadata{Version: 2, SHA256: hexDigest}, false); err != nil {
		t.Fatal(err)
	}
	if err := corroborateExistingRunPrompt(home, "effort", "bad-metadata", same); err == nil || !strings.Contains(err.Error(), "does not match its immutable archive") {
		t.Fatalf("bad metadata error = %v", err)
	}

	// Metadata without its archive is refused.
	orphanRun, _, err := openWorkLogRun(home, "effort", "orphan-metadata", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = orphanRun.Close() }()
	if err := writeJSONImmutableAt(orphanRun, "original-prompt.json", metadata, false); err != nil {
		t.Fatal(err)
	}
	if err := corroborateExistingRunPrompt(home, "effort", "orphan-metadata", WorkLogOptions{}); err == nil || !strings.Contains(err.Error(), "metadata without its immutable archive") {
		t.Fatalf("orphan metadata error = %v", err)
	}

	// A run index without a prompt is refused.
	indexRun, _, err := openWorkLogRun(home, "effort", "indexed-run", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = indexRun.Close() }()
	if err := writeBytesAtomicAt(indexRun, "run.json", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := corroborateExistingRunPrompt(home, "effort", "indexed-run", WorkLogOptions{}); err == nil || !strings.Contains(err.Error(), "already exists without an immutable original prompt") {
		t.Fatalf("indexed run error = %v", err)
	}

	// A claim without a prompt is refused.
	claimRun, _, err := openWorkLogRun(home, "effort", "claimed-run", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = claimRun.Close() }()
	claims, err := openPrivateChild(claimRun, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBytesAtomicAt(claims, strings.Repeat("a", 64)+".json", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = claims.Close()
	if err := corroborateExistingRunPrompt(home, "effort", "claimed-run", WorkLogOptions{}); err == nil || !strings.Contains(err.Error(), "already has a claim without an immutable original prompt") {
		t.Fatalf("claimed run error = %v", err)
	}

	// Malformed metadata is surfaced rather than ignored.
	malformedRun, _, err := openWorkLogRun(home, "effort", "malformed-metadata", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = malformedRun.Close() }()
	if err := writeBytesImmutableAt(malformedRun, "original-prompt.txt", body, 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := writeBytesAtomicAt(malformedRun, "original-prompt.json", []byte("{ not json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := corroborateExistingRunPrompt(home, "effort", "malformed-metadata", same); err == nil {
		t.Fatal("malformed prompt metadata was accepted")
	}
}

// wtLogCovRemovedTerminalHome builds one complete removed-terminal layout and
// returns the home, projects root, expectation, and claim id.
func wtLogCovRemovedTerminalHome(t *testing.T) (string, string, TerminalWorkLogExpectation, string) {
	t.Helper()
	projectsRoot := t.TempDir()
	// The home derives from the projects root now, so the fixture writes into
	// <projectsRoot>/.wb and ValidateRemovedTerminalWorkLogs finds it there.
	home := filepath.Join(projectsRoot, ".wb")
	t.Setenv(wbhome.EnvOverride, projectsRoot)
	t.Setenv(wbhome.EnvMigrationCompat, "")
	task, run := "removed-task", "removed-run"
	baseSHA := strings.Repeat("a", 40)
	claim := workLogClaim{Version: 1, EffortID: task, RunID: run, Task: task, Repository: "acme/app",
		Worktree: "/tmp/removed-worktree", Branch: "wb/removed", Base: "main", BaseSHA: baseSHA,
		Lifecycle: "active", RecordedAt: time.Now().UTC()}
	claim.ClaimID = workLogClaimID(claim.EffortID, CreateResult{Repository: claim.Repository,
		WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})

	runDir, _, err := openWorkLogRun(home, task, run, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runDir.Close() }()
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(claims, claim.ClaimID+".json", claim, false); err != nil {
		t.Fatal(err)
	}
	_ = claims.Close()

	terminalClaim := claim
	terminalClaim.Lifecycle = "terminal"
	terminal := workLogTerminalRecord{workLogClaim: terminalClaim, FinalCommit: "final-commit",
		Disposition: "removed", SealedAt: time.Now().UTC()}
	terminals, err := openPrivateChild(runDir, "terminals", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(terminals, claim.ClaimID+".json", terminal, false); err != nil {
		t.Fatal(err)
	}
	_ = terminals.Close()

	outbox, err := openWorkLogOutbox(home, task, true)
	if err != nil {
		t.Fatal(err)
	}
	event := workLogPublicEvent{Version: 1, Type: "worktree.sealed", At: terminal.SealedAt, EffortID: task, RunID: run,
		ClaimID: claim.ClaimID, Repository: claim.Repository, Branch: claim.Branch, Base: claim.Base, BaseSHA: baseSHA,
		FinalCommit: terminal.FinalCommit, Lifecycle: "terminal", Disposition: "removed"}
	if err := writeJSONImmutableAt(outbox, run+"-"+claim.ClaimID+"-sealed.json", event, false); err != nil {
		t.Fatal(err)
	}
	_ = outbox.Close()

	expectation := TerminalWorkLogExpectation{Task: task, Repository: "acme/app", Worktree: "/tmp/removed-worktree",
		Branch: "wb/removed", Base: "main", FinalCommit: "final-commit"}
	return home, projectsRoot, expectation, claim.ClaimID
}

func TestWtLogCovValidateRemovedTerminalWorkLogs(t *testing.T) {
	if err := ValidateRemovedTerminalWorkLogs(t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "no terminal Work Log expectations") {
		t.Fatalf("empty expectations error = %v", err)
	}
	_, projectsRoot, expectation, claimID := wtLogCovRemovedTerminalHome(t)
	if err := ValidateRemovedTerminalWorkLogs(projectsRoot, []TerminalWorkLogExpectation{expectation}); err != nil {
		t.Fatalf("valid removed terminal rejected: %v", err)
	}
	// The expectation key ignores the final commit, so a different final commit
	// is not a duplicate but does fail the exact terminal comparison.
	wrongCommit := expectation
	wrongCommit.FinalCommit = "other-commit"
	if err := ValidateRemovedTerminalWorkLogs(projectsRoot, []TerminalWorkLogExpectation{wrongCommit}); err == nil || !strings.Contains(err.Error(), "does not exactly corroborate") {
		t.Fatalf("wrong final commit error = %v", err)
	}
	duplicate := []TerminalWorkLogExpectation{expectation, expectation}
	if err := ValidateRemovedTerminalWorkLogs(projectsRoot, duplicate); err == nil || !strings.Contains(err.Error(), "duplicate terminal Work Log expectation") {
		t.Fatalf("duplicate expectation error = %v", err)
	}
	invalid := expectation
	invalid.Repository = ""
	if err := ValidateRemovedTerminalWorkLogs(projectsRoot, []TerminalWorkLogExpectation{invalid}); err == nil || !strings.Contains(err.Error(), "invalid terminal Work Log expectation") {
		t.Fatalf("invalid expectation error = %v", err)
	}
	missing := expectation
	missing.Task = "other-task"
	if err := ValidateRemovedTerminalWorkLogs(projectsRoot, []TerminalWorkLogExpectation{missing}); err == nil {
		t.Fatal("missing task terminal was accepted")
	}
	if !validClaimID(claimID) {
		t.Fatalf("claim id = %q", claimID)
	}
}

//nolint:paralleltest // wtLogCovRemovedTerminalHome calls t.Setenv for its private WB home.
func TestReadRemovedTerminalWorkLogClaimBaseUsesExactSealedEvidence(t *testing.T) {
	_, projectsRoot, expectation, _ := wtLogCovRemovedTerminalHome(t)
	base, err := ReadRemovedTerminalWorkLogClaimBase(projectsRoot, expectation)
	if err != nil || base != strings.Repeat("a", 40) {
		t.Fatalf("read exact removed claim base = %q, %v", base, err)
	}
	wrongHead := expectation
	wrongHead.FinalCommit = "different-commit"
	if base, err := ReadRemovedTerminalWorkLogClaimBase(projectsRoot, wrongHead); err == nil || base != "" {
		t.Fatalf("mismatched terminal head returned base %q, %v", base, err)
	}
	invalid := expectation
	invalid.Branch = ""
	if base, err := ReadRemovedTerminalWorkLogClaimBase(projectsRoot, invalid); err == nil || base != "" {
		t.Fatalf("incomplete expectation returned base %q, %v", base, err)
	}
}

func TestWtLogCovPreApplyRenameReservationHelpers(t *testing.T) {
	home := t.TempDir()
	effort, run := "task", "run"
	runDir, runPath, err := openWorkLogRun(home, effort, run, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runDir.Close() }()

	if candidate, found, err := readPreApplyRenameReservation(runDir, effort, run, "new-task"); err != nil || found {
		t.Fatalf("empty run = %#v/%t/%v", candidate, found, err)
	}
	if hasWorkLogClaimsOrTerminals(runDir) {
		t.Fatal("empty run reported claims or terminals")
	}
	if legacyUnclaimedPromptReservation(runDir) {
		t.Fatal("empty run reported a legacy reservation")
	}

	body := []byte("reserved prompt\n")
	digest := sha256.Sum256(body)
	hexDigest := hex.EncodeToString(digest[:])
	if err := writeBytesImmutableAt(runDir, "original-prompt.txt", body, 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(runDir, "original-prompt.json", workLogPromptMetadata{Version: 1, SHA256: hexDigest}, false); err != nil {
		t.Fatal(err)
	}
	if !legacyUnclaimedPromptReservation(runDir) {
		t.Fatal("legacy unclaimed prompt reservation was not recognized")
	}
	// With a run index it stops looking like a legacy reservation.
	if err := writeBytesAtomicAt(runDir, "run.json", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if legacyUnclaimedPromptReservation(runDir) {
		t.Fatal("run index did not disqualify the legacy reservation")
	}
	if err := os.Remove(filepath.Join(runPath, "run.json")); err != nil {
		t.Fatal(err)
	}

	// The legacy shape is only accepted when the effort names the new task.
	candidate, found, err := readPreApplyRenameReservation(runDir, effort, run, effort)
	if err != nil || !found || candidate.OldTask != "legacy-unclaimed" || candidate.PromptSHA256 != hexDigest {
		t.Fatalf("legacy reservation = %#v/%t/%v", candidate, found, err)
	}
	if _, found, err := readPreApplyRenameReservation(runDir, "other-effort", run, "new-task"); err != nil || found {
		t.Fatalf("legacy reservation for another effort = %t/%v", found, err)
	}
	// A mismatched archive digest is refused.
	if err := os.WriteFile(filepath.Join(runPath, "original-prompt.json"),
		[]byte(`{"version":1,"sha256":"`+strings.Repeat("f", 64)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readPreApplyRenameReservation(runDir, effort, run, effort); err == nil {
		t.Fatal("legacy reservation with a mismatched digest was accepted")
	}

	// An explicit reservation receipt is accepted and can be terminalized.
	explicitRun, _, err := openWorkLogRun(home, "explicit-effort", "explicit-run", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = explicitRun.Close() }()
	if err := writeBytesImmutableAt(explicitRun, "original-prompt.txt", body, 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(explicitRun, "original-prompt.json", workLogPromptMetadata{Version: 1, SHA256: hexDigest}, false); err != nil {
		t.Fatal(err)
	}
	reservation := preApplyRenameReservation{Version: 1, OldTask: "old-task", NewTask: "new-task",
		EffortID: "explicit-effort", RunID: "explicit-run", PromptSHA256: hexDigest, ReservedAt: time.Now().UTC()}
	if err := writeJSONImmutableAt(explicitRun, preApplyRenameReservationName, reservation, false); err != nil {
		t.Fatal(err)
	}
	candidate, found, err = readPreApplyRenameReservation(explicitRun, "explicit-effort", "explicit-run", "new-task")
	if err != nil || !found || candidate.terminalized || candidate.OldTask != "old-task" {
		t.Fatalf("explicit reservation = %#v/%t/%v", candidate, found, err)
	}
	terminal := preApplyRenameTerminal{Version: 1, OldTask: "old-task", NewTask: "new-task",
		EffortID: "explicit-effort", RunID: "explicit-run", PromptSHA256: hexDigest,
		Disposition: string(AbortDiscarded), SealedAt: time.Now().UTC()}
	if err := writeJSONImmutableAt(explicitRun, preApplyRenameTerminalName, terminal, false); err != nil {
		t.Fatal(err)
	}
	candidate, found, err = readPreApplyRenameReservation(explicitRun, "explicit-effort", "explicit-run", "new-task")
	if err != nil || !found || !candidate.terminalized {
		t.Fatalf("terminalized reservation = %#v/%t/%v", candidate, found, err)
	}

	// Invalid reservation identity is refused.
	invalidRun, _, err := openWorkLogRun(home, "invalid-effort", "invalid-run", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = invalidRun.Close() }()
	if err := writeJSONImmutableAt(invalidRun, preApplyRenameReservationName, preApplyRenameReservation{Version: 2}, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readPreApplyRenameReservation(invalidRun, "invalid-effort", "invalid-run", "new-task"); err == nil {
		t.Fatal("invalid reservation identity was accepted")
	}

	// A claim suppresses reservation recovery.
	claimedRun, _, err := openWorkLogRun(home, "claimed-effort", "claimed-run", true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = claimedRun.Close() }()
	claims, err := openPrivateChild(claimedRun, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBytesAtomicAt(claims, strings.Repeat("b", 64)+".json", []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = claims.Close()
	if !hasWorkLogClaimsOrTerminals(claimedRun) {
		t.Fatal("claim directory did not suppress reservation recovery")
	}
	if _, found, err := readPreApplyRenameReservation(claimedRun, "claimed-effort", "claimed-run", "new-task"); err != nil || found {
		t.Fatalf("reservation with a claim = %t/%v", found, err)
	}
}

func TestWtLogCovReadWorkLogProjectionForClaimBranches(t *testing.T) {
	worktree := t.TempDir()
	home := t.TempDir()
	if _, err := readWorkLogProjectionForClaim(home, worktree); !os.IsNotExist(err) && err != errWorkLogProjectionNotFound {
		t.Fatalf("empty worktree error = %v", err)
	}
	if _, err := readWorkLogProjectionForReadOnlyClaim(worktree); err != errWorkLogProjectionNotFound {
		t.Fatalf("empty read-only projection error = %v", err)
	}

	projection := workLogProjection{Version: 1, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64), Lifecycle: "active"}
	if err := writeWorkLogProjectionAt(worktree, projection); err != nil {
		t.Fatal(err)
	}
	loaded, err := readWorkLogProjectionForReadOnlyClaim(worktree)
	if err != nil || loaded != projection {
		t.Fatalf("current projection = %#v/%v", loaded, err)
	}
	if loaded, err := readWorkLogProjectionForClaim(home, worktree); err != nil || loaded != projection {
		t.Fatalf("claim projection = %#v/%v", loaded, err)
	}

	// A disagreeing legacy pointer is refused.
	legacy := projection
	legacy.ClaimID = strings.Repeat("b", 64)
	if err := writeLegacyProjectionAt(worktree, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorkLogProjectionForClaim(home, worktree); err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("disagreeing projections error = %v", err)
	}
	if _, err := readWorkLogProjectionForReadOnlyClaim(worktree); err == nil {
		t.Fatal("disagreeing projections were accepted read-only")
	}
}

// writeWorkLogProjectionAt writes a current projection without requiring Git.
func writeWorkLogProjectionAt(worktree string, projection workLogProjection) error {
	directory := filepath.Join(worktree, workLogProjectionDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	content, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, workLogProjectionName), append(content, '\n'), 0o600)
}

func writeLegacyProjectionAt(worktree string, projection workLogProjection) error {
	content, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(worktree, legacyWorkLogProjectionName), append(content, '\n'), 0o600)
}
