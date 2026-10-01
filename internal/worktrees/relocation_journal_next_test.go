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

func TestRelocationJournalOwnerClosesBothDescriptors(t *testing.T) {
	t.Parallel()
	fixture := newRepositoryRelocationReceiptFixture(t)
	owned, err := openLockedRelocationJournal(fixture.home, fixture.claim)
	if err != nil {
		t.Fatal(err)
	}
	if got := owned.journal.intents[fixture.intent.OperationID]; got != *fixture.intent {
		t.Fatalf("owned snapshot=%+v", got)
	}
	directory, run := owned.records, owned.locked.directory
	owned.close()
	for _, file := range []*os.File{directory, run} {
		if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
			t.Fatalf("owned descriptor remains open: %v", err)
		}
	}
}

func TestRelocationJournalOwnerRefusesNativeAdmissionFailures(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"run", "private child", "journal"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newRepositoryRelocationReceiptFixture(t)
			claim := fixture.claim
			want := ""
			switch name {
			case "run":
				claim.RunID = "../outside"
				want = "run"
			case "private child":
				if err := os.Rename(fixture.directory, fixture.directory+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(fixture.directory, []byte("occupant"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "relocations"
			case "journal":
				if err := os.WriteFile(filepath.Join(fixture.directory, fixture.claim.ClaimID+"-malformed.intent.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "decode relocation journal"
			}
			owned, err := openLockedRelocationJournal(fixture.home, claim)
			if err == nil || owned != nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("%s admission=%+v %v", name, owned, err)
			}
			if name == "private child" {
				if raw, err := os.ReadFile(fixture.directory); err != nil || string(raw) != "occupant" {
					t.Fatalf("occupant changed=%q %v", raw, err)
				}
			}
			if _, err := os.Lstat(fixture.receiptPath()); err == nil {
				t.Fatal("admission failure published completion")
			}
		})
	}
}

func TestRelocationJournalReceiptRechecksCurrentBytesAfterSnapshot(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"malformed", "collision", "closed descriptor"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newRepositoryRelocationReceiptFixture(t)
			at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
			first, path, err := appendRelocationReceipt(fixture.home, fixture.claim, fixture.intent, at)
			if err != nil {
				t.Fatal(err)
			}
			original, err := os.ReadFile(fixture.intentPath)
			if err != nil {
				t.Fatal(err)
			}
			readerCalled := false
			read := func(directory *os.File, filename string) ([]byte, error) {
				readerCalled = true
				if filename != filepath.Base(path) {
					t.Fatalf("reread filename=%q", filename)
				}
				switch name {
				case "malformed":
					if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				case "collision":
					replacement := *first
					replacement.HeadSHA = strings.Repeat("c", 40)
					raw, err := json.Marshal(replacement)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
				case "closed descriptor":
					if err := directory.Close(); err != nil {
						t.Fatal(err)
					}
					if _, err := directory.Stat(); !errors.Is(err, os.ErrClosed) {
						t.Fatalf("descriptor not closed: %v", err)
					}
				}
				return readBytesAt(directory, filename)
			}
			receipt, receiptPath, err := appendRelocationReceiptWithRead(fixture.home, fixture.claim, fixture.intent, at.Add(time.Hour), read)
			if err == nil || receipt != nil || receiptPath != "" || !readerCalled {
				t.Fatalf("reread outcome=%+v %q %v, readerCalled=%v", receipt, receiptPath, err, readerCalled)
			}
			if name == "malformed" && !strings.Contains(err.Error(), "decode existing relocation receipt") {
				t.Fatalf("malformed reread error=%v", err)
			}
			if name == "collision" && !strings.Contains(err.Error(), "relocation completion collision") {
				t.Fatalf("collision reread error=%v", err)
			}
			fixture.assertIntentUnchanged(t, original)
			if name == "malformed" {
				if raw, err := os.ReadFile(path); err != nil || string(raw) != "{" {
					t.Fatalf("malformed evidence rewritten=%q %v", raw, err)
				}
			}
			if name == "collision" {
				var got workLogRelocationReceipt
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				if got.HeadSHA != strings.Repeat("c", 40) {
					t.Fatalf("competing evidence changed=%+v", got)
				}
			}
		})
	}
}

func TestRelocationJournalIntentAndReceiptRetainPublicationFailures(t *testing.T) {
	t.Parallel()
	t.Run("intent invalid timestamp", func(t *testing.T) {
		t.Parallel()
		fixture := newRepositoryRelocationReceiptFixture(t)
		intent, path, err := appendRelocationIntentForRepository(fixture.home, fixture.claim, fixture.claim.Worktree, filepath.Join(t.TempDir(), "next"), "repository", strings.Repeat("b", 40), "acme/app", "newco/renamed", "https://github.com/newco/renamed.git", relocationPlacementRecord{}, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		var marshal *json.MarshalerError
		if !errors.As(err, &marshal) || intent != nil || path != "" {
			t.Fatalf("intent timestamp publication=%+v %q %v", intent, path, err)
		}
	})
	t.Run("receipt invalid timestamp", func(t *testing.T) {
		t.Parallel()
		fixture := newRepositoryRelocationReceiptFixture(t)
		intentBytes, err := os.ReadFile(fixture.intentPath)
		if err != nil {
			t.Fatal(err)
		}
		receipt, path, err := appendRelocationReceipt(fixture.home, fixture.claim, fixture.intent, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		var marshal *json.MarshalerError
		if !errors.As(err, &marshal) || receipt != nil || path != "" {
			t.Fatalf("receipt timestamp publication=%+v %q %v", receipt, path, err)
		}
		if _, err := os.Stat(fixture.receiptPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed publication left receipt=%v", err)
		}
		fixture.assertIntentUnchanged(t, intentBytes)
	})
	t.Run("intent complete placement and exact retry", func(t *testing.T) {
		t.Parallel()
		fixture := newRepositoryRelocationReceiptFixture(t)
		root := t.TempDir()
		destination := filepath.Join(root, "checkout")
		at := time.Now().UTC()
		intent, path, err := appendRelocationIntentForRepository(fixture.home, fixture.claim, fixture.claim.Worktree, destination, "repository", strings.Repeat("b", 40), "acme/app", "newco/renamed", "https://github.com/newco/renamed.git", relocationPlacementRecord{Root: root, Relative: "checkout"}, at)
		if err != nil || intent.DestinationRoot != root || intent.DestinationRelative != "checkout" {
			t.Fatalf("placement intent=%+v %v", intent, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		again, againPath, err := appendRelocationIntentForRepository(fixture.home, fixture.claim, fixture.claim.Worktree, destination, "repository", strings.Repeat("b", 40), "acme/app", "newco/renamed", "https://github.com/newco/renamed.git", relocationPlacementRecord{Root: root, Relative: "checkout"}, at.Add(time.Hour))
		if err != nil || againPath != path || *again != *intent {
			t.Fatalf("retry intent=%+v %q %v", again, againPath, err)
		}
		if after, err := os.ReadFile(path); err != nil || string(after) != string(raw) {
			t.Fatalf("immutable intent rewritten=%q %v", after, err)
		}
	})
	t.Run("intent journal failure", func(t *testing.T) {
		t.Parallel()
		fixture := newRepositoryRelocationReceiptFixture(t)
		if err := os.WriteFile(filepath.Join(fixture.directory, fixture.claim.ClaimID+"-malformed.intent.json"), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		intent, path, err := appendRelocationIntentForRepository(fixture.home, fixture.claim, fixture.claim.Worktree, filepath.Join(t.TempDir(), "next"), "repository", strings.Repeat("b", 40), "acme/app", "newco/renamed", "https://github.com/newco/renamed.git", relocationPlacementRecord{}, time.Now().UTC())
		if err == nil || intent != nil || path != "" || !strings.Contains(err.Error(), "decode relocation journal") {
			t.Fatalf("intent journal failure=%+v %q %v", intent, path, err)
		}
	})
}

func TestRelocationJournalMultiplePendingIntentsRefuseNewPublication(t *testing.T) {
	t.Parallel()
	fixture := newRepositoryRelocationReceiptFixture(t)
	competing := *fixture.intent
	competing.OperationID = "competing-operation"
	raw, err := json.Marshal(competing)
	if err != nil {
		t.Fatal(err)
	}
	competingPath := filepath.Join(fixture.directory, relocationIntentName(fixture.claim.ClaimID, competing.OperationID))
	if err := os.WriteFile(competingPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	intent, path, err := appendRelocationIntentForRepository(fixture.home, fixture.claim, fixture.intent.Source, fixture.intent.Destination, fixture.intent.To, fixture.intent.HeadSHA, fixture.intent.SourceRepository, fixture.intent.DestinationRepository, fixture.intent.RemoteURL, relocationPlacementRecord{}, time.Now().UTC())
	if err == nil || !strings.Contains(err.Error(), "multiple pending relocation intents") || intent != nil || path != "" {
		t.Fatalf("ambiguous replay=%+v %q %v", intent, path, err)
	}
	after, err := os.ReadDir(fixture.directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("ambiguous replay published %d -> %d records", len(before), len(after))
	}
	if got, err := os.ReadFile(competingPath); err != nil || string(got) != string(raw) {
		t.Fatalf("competing immutable intent changed=%q %v", got, err)
	}
}

func TestRelocationJournalRecordRefusalsRemainIndependent(t *testing.T) {
	t.Parallel()
	fixture := newRepositoryRelocationReceiptFixture(t)
	base := *fixture.intent
	cases := []struct {
		name, want string
		change     func(*workLogRelocationIntent)
	}{
		{"immutable claim identity", "record identity", func(r *workLogRelocationIntent) { r.Branch = "different" }},
		{"half placement", "placement record is incomplete", func(r *workLogRelocationIntent) { r.DestinationRoot = filepath.Dir(r.Destination) }},
		{"absolute placement", "placement record does not describe", func(r *workLogRelocationIntent) {
			r.DestinationRoot = filepath.Dir(r.Destination)
			r.DestinationRelative = r.Destination
		}},
		{"source repository", "source identity is invalid", func(r *workLogRelocationIntent) { r.SourceRepository = "invalid" }},
		{"destination repository", "destination identity is invalid", func(r *workLogRelocationIntent) { r.DestinationRepository = "invalid" }},
		{"remote binding", "identity is incomplete or invalid", func(r *workLogRelocationIntent) { r.RemoteURL = "https://github.com/other/repo.git" }},
		{"local repository fields", "unexpectedly changes repository identity", func(r *workLogRelocationIntent) { r.To = "local" }},
		{"identical local paths", "source and destination are identical", func(r *workLogRelocationIntent) {
			r.To = "local"
			r.SourceRepository = ""
			r.DestinationRepository = ""
			r.RemoteURL = ""
			r.Destination = r.Source
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			record := base
			tc.change(&record)
			if err := validateRelocationRecord(record, fixture.claim, true); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("independent record refusal=%v, want %q", err, tc.want)
			}
		})
	}
	if err := validateRelocationRecord(base, fixture.claim, true); err != nil {
		t.Fatalf("valid counterpart rejected: %v", err)
	}
}

func TestRelocationJournalChainRefusesDiscontinuousIdentity(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"path", "repository"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			fixture := newRepositoryRelocationReceiptFixture(t)
			intent := *fixture.intent
			if name == "path" {
				intent.Source = filepath.Join(t.TempDir(), "unbound-source")
			} else {
				intent.SourceRepository = "other/source"
			}
			// Both intent and completion agree and pass journal binding. Only chain
			// continuity against the immutable claim may refuse this pair.
			raw, err := json.Marshal(intent)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.intentPath, raw, 0600); err != nil {
				t.Fatal(err)
			}
			receipt := intent
			receipt.Type = workLogRelocationType
			raw, err = json.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fixture.receiptPath(), raw, 0600); err != nil {
				t.Fatal(err)
			}
			resolution, err := resolveRelocationChain(fixture.home, fixture.claim)
			want := "does not continue the immutable claim path"
			if name == "repository" {
				want = "does not continue the immutable claim repository"
			}
			if err == nil || !strings.Contains(err.Error(), want) || resolution.worktree != filepath.Clean(fixture.claim.Worktree) || resolution.repository != fixture.claim.Repository {
				t.Fatalf("independent chain refusal=%+v %v", resolution, err)
			}
		})
	}
}
