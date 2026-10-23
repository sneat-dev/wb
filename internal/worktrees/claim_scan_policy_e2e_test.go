//go:build e2e

package worktrees

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The missing-checkout lookup must stop at the first sorted error or match.
// Active summaries intentionally tolerate those same stray claim entries.
func TestE2EPrivateClaimReadersKeepDistinctCorruptionAndFirstMatchPolicies(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	worktree := filepath.Join(t.TempDir(), "missing-checkout")
	claimID := strings.Repeat("a", 64)
	claims := filepath.Join(home, "worklogs", "task-one", "runs", "run-one", "claims")
	wtLifeCovWriteJSON(t, filepath.Join(claims, claimID+".json"), workLogClaim{
		EffortID: "task-one", RunID: "run-one", ClaimID: claimID,
		Task: "task-one", Worktree: worktree, Lifecycle: "active",
	})
	validWorktree := t.TempDir()
	gitTest(t, validWorktree, "init")
	valid, err := recordWorkLogWithHooks(home, "task-two", CreateResult{
		Repository: "acme/app", WorktreeDir: validWorktree, Branch: "wb/task-two", Base: "main",
		BaseSHA: strings.Repeat("a", 40),
	}, WorkLogOptions{EffortID: "task-two", RunID: "run-two", AgentID: "codex", Model: "unknown"}, workLogPublicationHooks{})
	if err != nil {
		t.Fatal(err)
	}
	visitCount := 0
	visit := func(claims *os.File, id string, claim workLogClaim) {
		visitCount++
		var held workLogClaim
		if err := readJSONAt(claims, id+".json", &held); err != nil || id != valid.ClaimID || claim.Worktree != validWorktree || held.ClaimID != id {
			t.Fatalf("tolerant reader yielded wrong claim or descriptor: id=%q claim=%#v held=%#v err=%v", id, claim, held, err)
		}
	}
	if err := walkActiveWorkLogClaims(home, visit); err != nil || visitCount != 1 {
		t.Fatalf("tolerant reader lost later valid claim: count=%d, err=%v", visitCount, err)
	}
	unsafeRun := filepath.Join(home, "worklogs", "task-one", "runs", ".earlier")
	if err := os.Mkdir(unsafeRun, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil || !strings.Contains(err.Error(), "unsafe Work Log run") {
		t.Fatalf("strict reader skipped earlier unsafe run: %v", err)
	}
	visitCount = 0
	if err := walkActiveWorkLogClaims(home, visit); err != nil || visitCount != 1 {
		t.Fatalf("tolerant reader lost valid claim after unsafe run: count=%d, err=%v", visitCount, err)
	}
	if err := os.Remove(unsafeRun); err != nil {
		t.Fatal(err)
	}
	linkedRun := filepath.Join(home, "worklogs", "task-one", "runs", "run-0")
	if err := os.Symlink(claims, linkedRun); err != nil {
		t.Fatal(err)
	}
	if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil {
		t.Fatal("strict reader followed a run symlink before the matching claim")
	}
	visitCount = 0
	if err := walkActiveWorkLogClaims(home, visit); err != nil || visitCount != 1 {
		t.Fatalf("tolerant reader lost valid claim after run symlink: count=%d, err=%v", visitCount, err)
	}
	if err := os.Remove(linkedRun); err != nil {
		t.Fatal(err)
	}
	unsafeClaim := filepath.Join(claims, "0-invalid.json")
	if err := os.WriteFile(unsafeClaim, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := activeWorkLogClaimAtPath(home, worktree, nil); err == nil || !strings.Contains(err.Error(), "unsafe Work Log claim entry") {
		t.Fatalf("strict reader skipped earlier unsafe claim: %v", err)
	}
	if err := os.Remove(unsafeClaim); err != nil {
		t.Fatal(err)
	}
	claim, err := activeWorkLogClaimAtPath(home, worktree, nil)
	if err != nil || claim == nil || claim.Worktree != worktree {
		t.Fatalf("strict reader lost first match: claim=%#v, err=%v", claim, err)
	}
	// A later malformed entry cannot supersede the first sorted exact match.
	if err := os.WriteFile(filepath.Join(claims, "z-invalid.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	claim, err = activeWorkLogClaimAtPath(home, worktree, nil)
	if err != nil || claim == nil || claim.Worktree != worktree {
		t.Fatalf("strict reader inspected past first match: claim=%#v, err=%v", claim, err)
	}
}

func TestE2EActiveClaimReaderSkipsUnreadableTerminalWithoutDiscardingClaim(t *testing.T) {
	t.Parallel()
	home, worktree := t.TempDir(), t.TempDir()
	gitTest(t, worktree, "init")
	const effort, run = "task-one", "run-one"
	outcome, err := recordWorkLogWithHooks(home, effort, CreateResult{
		Repository: "acme/app", WorktreeDir: worktree, Branch: "wb/task-one", Base: "main",
		BaseSHA: strings.Repeat("a", 40),
	}, WorkLogOptions{EffortID: effort, RunID: run, AgentID: "codex", Model: "unknown"}, workLogPublicationHooks{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	visit := func(_ *os.File, _ string, _ workLogClaim) { count++ }
	if err := walkActiveWorkLogClaims(home, visit); err != nil || count != 1 {
		t.Fatalf("active claim before terminal: count=%d, err=%v", count, err)
	}
	terminalPath := filepath.Join(home, "worklogs", effort, "runs", run, "terminals", outcome.ClaimID+".json")
	if err := os.MkdirAll(filepath.Dir(terminalPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(terminalPath, []byte("invalid JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	count = 0
	if err := walkActiveWorkLogClaims(home, visit); err != nil || count != 0 {
		t.Fatalf("unreadable terminal must hide claim: count=%d, err=%v", count, err)
	}
	if claim, err := activeWorkLogClaimAtPath(home, worktree, nil); err != nil || claim == nil || claim.ClaimID != outcome.ClaimID {
		t.Fatalf("strict path reader unexpectedly consulted terminal: claim=%#v, err=%v", claim, err)
	}
}
