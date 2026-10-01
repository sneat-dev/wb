//go:build e2e

package worktrees

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func publishClaimForEvidenceWalk(t *testing.T, home, task, run string) WorkLogPublicationOutcome {
	t.Helper()
	worktree := t.TempDir()
	gitTest(t, worktree, "init")
	outcome, err := recordWorkLogWithHooks(home, task, CreateResult{
		Repository: "acme/app", WorktreeDir: worktree, Branch: "wb/" + task,
		Base: "main", BaseSHA: strings.Repeat("a", 40),
	}, WorkLogOptions{EffortID: task, RunID: run, AgentID: "codex", Model: "unknown"}, workLogPublicationHooks{})
	if err != nil {
		t.Fatal(err)
	}
	return outcome
}

func TestE2EActiveClaimWalkSkipsRemovedLaterEntriesAndKeepsHeldSibling(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"effort", "run"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			first := publishClaimForEvidenceWalk(t, home, "a-task", "a-run")
			laterTask := "z-task"
			if scope == "run" {
				laterTask = "a-task"
			}
			_ = publishClaimForEvidenceWalk(t, home, laterTask, "z-run")
			later := filepath.Join(home, "worklogs", laterTask)
			if scope == "run" {
				later = filepath.Join(later, "runs", "z-run")
			}
			if _, err := os.Stat(later); err != nil {
				t.Fatalf("later %s was not published: %v", scope, err)
			}
			visits := 0
			err := walkActiveWorkLogClaims(home, func(claims *os.File, claimID string, claim workLogClaim) {
				visits++
				if claimID != first.ClaimID || claim.EffortID != "a-task" {
					t.Errorf("wrong first claim: %q, %+v", claimID, claim)
				}
				var held workLogClaim
				if readErr := readJSONAt(claims, claimID+".json", &held); readErr != nil || held.ClaimID != claimID {
					t.Errorf("visitor lost held claim descriptor: %+v, %v", held, readErr)
				}
				if removeErr := os.RemoveAll(later); removeErr != nil {
					t.Errorf("remove later %s after enumeration: %v", scope, removeErr)
				}
				if _, statErr := os.Stat(later); !errors.Is(statErr, os.ErrNotExist) {
					t.Errorf("later %s still exists after removal: %v", scope, statErr)
				}
			})
			if err != nil || visits != 1 {
				t.Fatalf("walk after later %s removal: visits=%d err=%v", scope, visits, err)
			}
			firstPath := filepath.Join(home, "worklogs", "a-task", "runs", "a-run", "claims", first.ClaimID+".json")
			if _, err := os.Stat(firstPath); err != nil {
				t.Fatalf("valid sibling claim was changed: %v", err)
			}
		})
	}
}

type legacyEvidenceRun struct {
	home, path, claimID string
	directory           *os.File
	claim               legacyWorkLogClaim
}

func newLegacyEvidenceRun(t *testing.T) legacyEvidenceRun {
	t.Helper()
	home := t.TempDir()
	directory, path, err := openWorkLogRun(home, "task", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	claim := legacyWorkLogClaim{
		Version: 1, EffortID: "task", RunID: "run", Task: "task", Repository: "acme/app",
		Worktree: filepath.Join(t.TempDir(), "checkout"), Branch: "wb/task", Base: "main",
		BaseSHA: strings.Repeat("a", 40), RecordedAt: time.Unix(1_700_000_000, 0).UTC(),
	}
	if err := writeJSONImmutableAt(directory, "claim.json", claim, false); err != nil {
		t.Fatal(err)
	}
	claimID := workLogClaimID("task", CreateResult{
		Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch,
		Base: claim.Base, BaseSHA: claim.BaseSHA,
	})
	return legacyEvidenceRun{home: home, path: path, claimID: claimID, directory: directory, claim: claim}
}

func (fixture legacyEvidenceRun) migrate() error {
	return migrateLegacySingletonClaim(fixture.directory, fixture.path, fixture.home, "task", "run")
}

func TestE2ELegacyClaimMigrationRefusesCorruptAndRedirectedAuthority(t *testing.T) {
	t.Parallel()
	t.Run("corrupt singleton", func(t *testing.T) {
		t.Parallel()
		fixture := newLegacyEvidenceRun(t)
		if err := os.WriteFile(filepath.Join(fixture.path, "claim.json"), []byte(`{"bad":}`), 0o600); err != nil {
			t.Fatal(err)
		}
		var syntax *json.SyntaxError
		if err := fixture.migrate(); !errors.As(err, &syntax) {
			t.Fatalf("corrupt singleton error = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.path, "claims")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("corrupt singleton published claims: %v", err)
		}
	})
	t.Run("mismatched identity", func(t *testing.T) {
		t.Parallel()
		fixture := newLegacyEvidenceRun(t)
		fixture.claim.EffortID = "other"
		wtLifeCovWriteJSON(t, filepath.Join(fixture.path, "claim.json"), fixture.claim)
		if err := fixture.migrate(); err == nil || !strings.Contains(err.Error(), "legacy claim identity") {
			t.Fatalf("mismatched singleton error = %v", err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.path, "claims")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("mismatched singleton published claims: %v", err)
		}
	})
	t.Run("redirected claims directory", func(t *testing.T) {
		t.Parallel()
		fixture := newLegacyEvidenceRun(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(fixture.path, "claims")); err != nil {
			t.Fatal(err)
		}
		if err := fixture.migrate(); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("redirected claim migration error = %v", err)
		}
		if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
			t.Fatalf("migration wrote outside held run: entries=%v err=%v", entries, err)
		}
	})
	t.Run("conflicting claim bytes", func(t *testing.T) {
		t.Parallel()
		fixture := newLegacyEvidenceRun(t)
		claimPath := filepath.Join(fixture.path, "claims", fixture.claimID+".json")
		if err := os.Mkdir(filepath.Dir(claimPath), 0o700); err != nil {
			t.Fatal(err)
		}
		before := []byte("{\"conflict\":true}\n")
		if err := os.WriteFile(claimPath, before, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := fixture.migrate(); err == nil || !strings.Contains(err.Error(), "immutable file already exists") {
			t.Fatalf("conflicting claim migration error = %v", err)
		}
		if after, err := os.ReadFile(claimPath); err != nil || string(after) != string(before) {
			t.Fatalf("conflicting claim bytes changed: %q, %v", after, err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.path, "legacy-claim-migration.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("migration receipt published after claim conflict: %v", err)
		}
	})
	t.Run("unreadable projection inventory", func(t *testing.T) {
		t.Parallel()
		fixture := newLegacyEvidenceRun(t)
		inventory := filepath.Join(fixture.home, "worktrees")
		if err := os.Mkdir(inventory, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(inventory, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(inventory, 0o700) })
		if err := fixture.migrate(); !errors.Is(err, os.ErrPermission) {
			t.Fatalf("unreadable projection inventory error = %v", err)
		}
		if _, err := os.Stat(filepath.Join(fixture.path, "claims", fixture.claimID+".json")); err != nil {
			t.Fatalf("claim was not published before inventory failure: %v", err)
		}
		if _, err := os.Lstat(filepath.Join(fixture.path, "legacy-claim-migration.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inventory failure published migration receipt: %v", err)
		}
	})
	for _, tc := range []struct {
		name, bytes string
		wantSyntax  bool
	}{
		{name: "receipt version", bytes: `{"version":2}`},
		{name: "corrupt receipt", bytes: `{"bad":}`, wantSyntax: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newLegacyEvidenceRun(t)
			receiptPath := filepath.Join(fixture.path, "legacy-claim-migration.json")
			if err := os.WriteFile(receiptPath, []byte(tc.bytes), 0o600); err != nil {
				t.Fatal(err)
			}
			err := fixture.migrate()
			var syntax *json.SyntaxError
			if tc.wantSyntax && !errors.As(err, &syntax) || !tc.wantSyntax && (err == nil || !strings.Contains(err.Error(), "version mismatch")) {
				t.Fatalf("%s error = %v", tc.name, err)
			}
			if after, err := os.ReadFile(receiptPath); err != nil || string(after) != tc.bytes {
				t.Fatalf("existing migration receipt changed: %q, %v", after, err)
			}
			if _, err := os.Stat(filepath.Join(fixture.path, "claims", fixture.claimID+".json")); err != nil {
				t.Fatalf("claim was not durably published before receipt read: %v", err)
			}
		})
	}
}
