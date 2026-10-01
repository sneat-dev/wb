package worktrees

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type repositoryRelocationReceiptFixture struct {
	home       string
	claim      workLogClaim
	intent     *workLogRelocationIntent
	intentPath string
	directory  string
}

func newRepositoryRelocationReceiptFixture(t *testing.T) repositoryRelocationReceiptFixture {
	t.Helper()
	home := t.TempDir()
	claim := workLogClaim{Version: 1, EffortID: "effort", RunID: "run", ClaimID: strings.Repeat("a", 64),
		Task: "task", Repository: "acme/app", Branch: "wb/transfer", Worktree: filepath.Join(t.TempDir(), "source")}
	run, runPath, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Close(); err != nil {
		t.Fatal(err)
	}
	intent, intentPath, err := appendRelocationIntentForRepository(home, claim, claim.Worktree,
		filepath.Join(t.TempDir(), "destination"), "repository", strings.Repeat("b", 40),
		"acme/app", "newco/renamed", "https://github.com/newco/renamed.git", relocationPlacementRecord{}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	return repositoryRelocationReceiptFixture{home: home, claim: claim, intent: intent, intentPath: intentPath,
		directory: filepath.Join(runPath, "relocations")}
}

func (fixture repositoryRelocationReceiptFixture) receiptPath() string {
	return filepath.Join(fixture.directory, relocationReceiptName(fixture.claim.ClaimID, fixture.intent.OperationID))
}

func (fixture repositoryRelocationReceiptFixture) assertIntentUnchanged(t *testing.T, initial []byte) {
	t.Helper()
	if got, err := os.ReadFile(fixture.intentPath); err != nil || string(got) != string(initial) {
		t.Fatalf("durable relocation intent changed: %q, %v", got, err)
	}
}

func TestRepositoryRelocationReceiptReplaysExactImmutableEvidence(t *testing.T) {
	t.Parallel()
	fixture := newRepositoryRelocationReceiptFixture(t)
	intentBytes, err := os.ReadFile(fixture.intentPath)
	if err != nil {
		t.Fatal(err)
	}
	firstAt := time.Date(2026, 10, 1, 1, 2, 3, 0, time.UTC)
	first, path, err := appendRelocationReceipt(fixture.home, fixture.claim, fixture.intent, firstAt)
	if err != nil || path != fixture.receiptPath() || first.OperationID != fixture.intent.OperationID {
		t.Fatalf("first exact receipt = %#v, %q, %v", first, path, err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, replayPath, err := appendRelocationReceipt(fixture.home, fixture.claim, fixture.intent, firstAt.Add(time.Hour))
	if err != nil || replayPath != path || !replayed.At.Equal(firstAt) {
		t.Fatalf("immutable receipt replay = %#v, %q, %v", replayed, replayPath, err)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(before) {
		t.Fatalf("replay rewrote receipt: %q, %v", after, err)
	}
	fixture.assertIntentUnchanged(t, intentBytes)
}

func TestRepositoryRelocationReceiptRefusesUnboundOrUnreadableEvidence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, want string
		change     func(*testing.T, repositoryRelocationReceiptFixture, *workLogRelocationIntent)
	}{
		{"invalid input", "record identity is incomplete", func(_ *testing.T, _ repositoryRelocationReceiptFixture, intent *workLogRelocationIntent) {
			intent.Branch = "other"
		}},
		{"missing durable intent", "not bound to its durable intent", func(_ *testing.T, _ repositoryRelocationReceiptFixture, intent *workLogRelocationIntent) {
			intent.OperationID = "different-operation"
		}},
		{"blocked journal directory", "relocations", func(t *testing.T, fixture repositoryRelocationReceiptFixture, _ *workLogRelocationIntent) {
			backup := fixture.directory + "-saved"
			if err := os.Rename(fixture.directory, backup); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.directory, []byte("blocked"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Rename(backup, fixture.directory) })
		}},
		{"malformed durable journal", "decode relocation journal", func(t *testing.T, fixture repositoryRelocationReceiptFixture, _ *workLogRelocationIntent) {
			if err := os.WriteFile(filepath.Join(fixture.directory, fixture.claim.ClaimID+"-bad.completed.json"), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture := newRepositoryRelocationReceiptFixture(t)
			original, err := os.ReadFile(fixture.intentPath)
			if err != nil {
				t.Fatal(err)
			}
			intent := *fixture.intent
			tc.change(t, fixture, &intent)
			if _, _, err := appendRelocationReceipt(fixture.home, fixture.claim, &intent, time.Now().UTC()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s receipt refusal = %v, want %q", tc.name, err, tc.want)
			}
			if tc.name == "blocked journal directory" {
				if raw, err := os.ReadFile(fixture.directory); err != nil || string(raw) != "blocked" {
					t.Fatalf("refusal changed obstructing journal entry: %q, %v", raw, err)
				}
				backupIntent := filepath.Join(fixture.directory+"-saved", filepath.Base(fixture.intentPath))
				if raw, err := os.ReadFile(backupIntent); err != nil || string(raw) != string(original) {
					t.Fatalf("refusal changed saved durable intent: %q, %v", raw, err)
				}
			} else {
				if _, err := os.Lstat(fixture.receiptPath()); !os.IsNotExist(err) {
					t.Fatalf("refusal published a receipt: %v", err)
				}
				fixture.assertIntentUnchanged(t, original)
			}
		})
	}
}

func TestRepositoryRelocationJournalRejectsCompletionWithoutIntent(t *testing.T) {
	t.Parallel()
	fixture := newRepositoryRelocationReceiptFixture(t)
	receipt := *fixture.intent
	receipt.Type = workLogRelocationType
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.receiptPath(), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.intentPath); err != nil {
		t.Fatal(err)
	}
	run, runPath, err := openWorkLogRun(fixture.home, fixture.claim.EffortID, fixture.claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	if _, err := openRelocationJournal(run, runPath, fixture.claim); err == nil || !strings.Contains(err.Error(), "not bound to its intent") {
		t.Fatalf("orphan completion admitted: %v", err)
	}
	if raw, err := os.ReadFile(fixture.receiptPath()); err != nil || len(raw) == 0 {
		t.Fatalf("journal refusal changed orphan evidence: %q, %v", raw, err)
	}
}
