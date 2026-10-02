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

func TestRelocationJournalTailEnumerationRefusals(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"closed directory", "duplicate intent", "duplicate receipt"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fixture := newRepositoryRelocationReceiptFixture(t)
			if kind == "duplicate receipt" {
				if _, _, err := appendRelocationReceipt(fixture.home, fixture.claim, fixture.intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			run, runPath, err := openWorkLogRun(fixture.home, fixture.claim.EffortID, fixture.claim.RunID, false)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = run.Close() })
			initial, err := os.ReadFile(fixture.intentPath)
			if err != nil {
				t.Fatal(err)
			}
			called := false
			var readCause error
			enumeration := func(directory *os.File) ([]string, error) {
				called = true
				if kind == "closed directory" {
					if err := directory.Close(); err != nil {
						t.Fatal(err)
					}
					if _, err := directory.Stat(); !errors.Is(err, os.ErrClosed) {
						t.Fatalf("owned directory still open: %v", err)
					}
					names, err := directory.Readdirnames(-1)
					if err == nil {
						t.Fatal("closed owned directory enumeration succeeded")
					}
					readCause = err
					return names, err
				}
				names, err := directory.Readdirnames(-1)
				if err != nil {
					t.Fatal(err)
				}
				name := filepath.Base(fixture.intentPath)
				if kind == "duplicate receipt" {
					name = filepath.Base(fixture.receiptPath())
				}
				found := false
				for _, entry := range names {
					if entry == name {
						found = true
					}
				}
				if !found {
					t.Fatalf("native enumeration %q lacks bound record %q", names, name)
				}
				// This is a defensive arbitrary-enumeration-input contract. It does not
				// claim that native Readdirnames produced a duplicate on this platform.
				return append(names, name), nil
			}
			_, err = openRelocationJournalWithNames(run, runPath, fixture.claim, enumeration)
			if !called || err == nil {
				t.Fatalf("enumeration refusal called=%v error=%v", called, err)
			}
			if kind == "closed directory" {
				if !errors.Is(err, readCause) {
					t.Fatalf("read cause replaced: got=%v cause=%v", err, readCause)
				}
			} else {
				want := "duplicate relocation intent"
				if kind == "duplicate receipt" {
					want = "duplicate relocation completion"
				}
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("wrong duplicate refusal: %v", err)
				}
			}
			fixture.assertIntentUnchanged(t, initial)
		})
	}
}

func TestRelocationJournalTailRefusesNativeNamespaceOccupant(t *testing.T) {
	t.Parallel()
	fixture := newRepositoryRelocationReceiptFixture(t)
	if err := os.Rename(fixture.directory, fixture.directory+"-retained"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.directory, []byte("occupant"), 0600); err != nil {
		t.Fatal(err)
	}
	run, runPath, err := openWorkLogRun(fixture.home, fixture.claim.EffortID, fixture.claim.RunID, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	_, err = openRelocationJournal(run, runPath, fixture.claim)
	if err == nil {
		t.Fatal("non-directory occupant accepted as journal")
	}
	if raw, err := os.ReadFile(fixture.directory); err != nil || string(raw) != "occupant" {
		t.Fatalf("namespace occupant changed=%q %v", raw, err)
	}
}

func TestRelocationJournalTailChainAndPendingAdmissionErrors(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"missing run", "malformed journal"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fixture := newRepositoryRelocationReceiptFixture(t)
			claim := fixture.claim
			if kind == "missing run" {
				claim.RunID = "absent-run"
			} else {
				if err := os.WriteFile(filepath.Join(fixture.directory, claim.ClaimID+"-bad.intent.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			resolved, err := resolveRelocationChain(fixture.home, claim)
			if err == nil || resolved.worktree != claim.Worktree || resolved.repository != claim.Repository || resolved.receipt != nil {
				t.Fatalf("chain error changed claim: %+v %v", resolved, err)
			}
			if kind == "malformed journal" && !strings.Contains(err.Error(), "decode relocation journal") {
				t.Fatalf("wrong chain boundary: %v", err)
			}
			intent, path, err := pendingRelocationIntent(fixture.home, claim, fixture.intent.Destination, claim.Branch, fixture.intent.HeadSHA)
			if err == nil || intent != nil || path != "" {
				t.Fatalf("pending admission=%+v %q %v", intent, path, err)
			}
			if kind == "malformed journal" && !strings.Contains(err.Error(), "decode relocation journal") {
				t.Fatalf("wrong pending boundary: %v", err)
			}
		})
	}
}

func TestRelocationJournalTailOrdersValidBoundChains(t *testing.T) {
	t.Parallel()
	for _, sameTime := range []bool{true, false} {
		name := "chronology"
		if sameTime {
			name = "equal time operation ID"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newRepositoryRelocationReceiptFixture(t)
			if err := os.Remove(fixture.intentPath); err != nil {
				t.Fatal(err)
			}
			first := *fixture.intent
			first.OperationID = "z-first"
			first.At = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			second := first
			second.OperationID = "a-second"
			second.Source = first.Destination
			second.Destination = filepath.Join(t.TempDir(), "final")
			second.SourceRepository = first.DestinationRepository
			second.DestinationRepository = "final/location"
			second.RemoteURL = "https://github.com/final/location.git"
			second.At = first.At.Add(time.Hour)
			if sameTime {
				first.OperationID = "a-first"
				second.OperationID = "z-second"
				second.At = first.At
			}
			journalTailWriteBoundPair(t, fixture, first)
			journalTailWriteBoundPair(t, fixture, second)
			// Prove both record and durable binding admission before testing ordering.
			run, runPath, err := openWorkLogRun(fixture.home, fixture.claim.EffortID, fixture.claim.RunID, false)
			if err != nil {
				t.Fatal(err)
			}
			journal, err := openRelocationJournal(run, runPath, fixture.claim)
			if closeErr := run.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil || len(journal.intents) != 2 || len(journal.receipts) != 2 {
				t.Fatalf("bound fixture journal=%+v %v", journal, err)
			}
			resolved, err := resolveRelocationChain(fixture.home, fixture.claim)
			wantPath := filepath.Join(fixture.directory, relocationReceiptName(fixture.claim.ClaimID, second.OperationID))
			if err != nil || resolved.receipt == nil || resolved.receipt.OperationID != second.OperationID || resolved.repository != second.DestinationRepository || resolved.worktree != second.Destination || resolved.receiptPath != wantPath {
				t.Fatalf("valid chain resolved=%+v %v", resolved, err)
			}
		})
	}
}

func TestRelocationJournalTailPendingStates(t *testing.T) {
	t.Parallel()
	for _, completed := range []bool{false, true} {
		name := "unmatched pending"
		if completed {
			name = "completed intent"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newRepositoryRelocationReceiptFixture(t)
			if completed {
				if _, _, err := appendRelocationReceipt(fixture.home, fixture.claim, fixture.intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			head := fixture.intent.HeadSHA
			if !completed {
				head = "different-head"
			}
			intent, path, err := pendingRelocationIntent(fixture.home, fixture.claim, fixture.intent.Destination, fixture.claim.Branch, head)
			if err != nil || intent != nil || path != "" {
				t.Fatalf("non-pending state=%+v %q %v", intent, path, err)
			}
			if !completed {
				intent, path, err = pendingRelocationIntent(fixture.home, fixture.claim, fixture.intent.Destination, fixture.claim.Branch, fixture.intent.HeadSHA)
				if err != nil || intent == nil || *intent != *fixture.intent || path != fixture.intentPath {
					t.Fatalf("exact pending state=%+v %q %v", intent, path, err)
				}
			}
		})
	}
}

func journalTailWriteBoundPair(t *testing.T, fixture repositoryRelocationReceiptFixture, intent workLogRelocationIntent) {
	t.Helper()
	if err := validateRelocationRecord(intent, fixture.claim, true); err != nil {
		t.Fatalf("invalid chain fixture: %v", err)
	}
	receipt := intent
	receipt.Type = workLogRelocationType
	if err := validateRelocationRecord(receipt, fixture.claim, false); err != nil {
		t.Fatalf("invalid receipt fixture: %v", err)
	}
	for _, record := range []workLogRelocationIntent{intent, receipt} {
		name := relocationIntentName(fixture.claim.ClaimID, record.OperationID)
		if record.Type == workLogRelocationType {
			name = relocationReceiptName(fixture.claim.ClaimID, record.OperationID)
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(fixture.directory, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
