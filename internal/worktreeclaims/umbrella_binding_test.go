package worktreeclaims

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidateUmbrellaBindingFieldsAndEntry(t *testing.T) {
	t.Parallel()
	if err := ValidateUmbrellaBindingFields(ClaimUmbrellaBinding{Umbrella: "github:sneat-co/backstage#845"}); err != nil {
		t.Fatalf("valid umbrella binding refused: %v", err)
	}
	for _, bad := range []string{"", "no-colon", "github:sneat-co/backstage #845", "github:a\nb"} {
		if err := ValidateUmbrellaBindingFields(ClaimUmbrellaBinding{Umbrella: bad}); err == nil {
			t.Fatalf("invalid umbrella %q accepted", bad)
		}
	}

	dir := t.TempDir()
	claimID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	claim := Claim{Version: 2, EffortID: "effort", RunID: "run", ClaimID: claimID, Task: "effort", Repository: "sneat-co/backstage"}
	writeTestJSON(t, filepath.Join(dir, claimID+".json"), claim)
	writeTestJSON(t, filepath.Join(dir, claimID+UmbrellaBindingSuffix), ClaimUmbrellaBinding{
		Umbrella:   "github:sneat-co/backstage#845",
		RecordedAt: time.Now().UTC(),
	})

	dirFile, err := os.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dirFile.Close() })

	readJSON := func(_ *os.File, name string, dest any) error {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, dest)
	}
	files := map[string]bool{claimID + ".json": true, claimID + UmbrellaBindingSuffix: true}
	if err := ValidateUmbrellaBindingEntry(dirFile, claimID+UmbrellaBindingSuffix, files, readJSON); err != nil {
		t.Fatalf("ValidateUmbrellaBindingEntry valid = %v", err)
	}
	if err := ValidateUmbrellaBindingEntry(dirFile, "bad"+UmbrellaBindingSuffix, files, readJSON); err == nil {
		t.Fatal("invalid claimID accepted")
	}
	if err := ValidateUmbrellaBindingEntry(dirFile, claimID+UmbrellaBindingSuffix, map[string]bool{}, readJSON); err == nil {
		t.Fatal("orphaned sidecar accepted")
	}
	if err := ValidateUmbrellaBindingEntry(dirFile, claimID+UmbrellaBindingSuffix, files, nil); err == nil {
		t.Fatal("nil reader accepted")
	}

	// Corrupted sidecar JSON fails read.
	if err := os.WriteFile(filepath.Join(dir, claimID+UmbrellaBindingSuffix), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateUmbrellaBindingEntry(dirFile, claimID+UmbrellaBindingSuffix, files, readJSON); err == nil {
		t.Fatal("malformed sidecar JSON accepted")
	}

	// Missing RecordedAt fails validation.
	writeTestJSON(t, filepath.Join(dir, claimID+UmbrellaBindingSuffix), ClaimUmbrellaBinding{Umbrella: "github:sneat-co/backstage#845"})
	if err := ValidateUmbrellaBindingEntry(dirFile, claimID+UmbrellaBindingSuffix, files, readJSON); err == nil {
		t.Fatal("zero RecordedAt accepted")
	}

	// Claim read failure propagates.
	writeTestJSON(t, filepath.Join(dir, claimID+UmbrellaBindingSuffix), ClaimUmbrellaBinding{
		Umbrella:   "github:sneat-co/backstage#845",
		RecordedAt: time.Now().UTC(),
	})
	failClaim := func(f *os.File, name string, dest any) error {
		if name == claimID+".json" {
			return errors.New("claim read failed")
		}
		return readJSON(f, name, dest)
	}
	if err := ValidateUmbrellaBindingEntry(dirFile, claimID+UmbrellaBindingSuffix, files, failClaim); err == nil {
		t.Fatal("claim read error ignored")
	}
}

func TestPublishAndTerminalHistoryValidateUmbrellaSidecars(t *testing.T) {
	t.Parallel()
	f := newSidecarPublicationFixture(t)
	umbrellaPath := filepath.Join(filepath.Dir(f.bindingPath), f.first.ClaimID+UmbrellaBindingSuffix)
	writeTestJSON(t, umbrellaPath, ClaimUmbrellaBinding{
		Umbrella:   "github:sneat-co/backstage#845",
		RecordedAt: time.Now().UTC(),
	})

	// Valid umbrella sidecar lets sibling publication proceed.
	if _, err := f.ports.PublishClaim(f.home, f.run, f.runPath, f.next, PublicationHooks{}); err != nil {
		t.Fatalf("PublishClaim with valid umbrella sidecar: %v", err)
	}

	// Unsafe umbrella sidecar (orphan) fails PublishClaim.
	f2 := newSidecarPublicationFixture(t)
	orphanPath := filepath.Join(filepath.Dir(f2.bindingPath), "invalid"+UmbrellaBindingSuffix)
	writeTestJSON(t, orphanPath, ClaimUmbrellaBinding{Umbrella: "github:sneat-co/backstage#845", RecordedAt: time.Now().UTC()})
	if _, err := f2.ports.PublishClaim(f2.home, f2.run, f2.runPath, f2.next, PublicationHooks{}); err == nil {
		t.Fatal("PublishClaim accepted unsafe umbrella sidecar")
	}

	// Invalid umbrella sidecar body fails PublishClaim.
	f3 := newSidecarPublicationFixture(t)
	badPath := filepath.Join(filepath.Dir(f3.bindingPath), f3.first.ClaimID+UmbrellaBindingSuffix)
	writeTestJSON(t, badPath, ClaimUmbrellaBinding{Umbrella: "invalid"})
	if _, err := f3.ports.PublishClaim(f3.home, f3.run, f3.runPath, f3.next, PublicationHooks{}); err == nil {
		t.Fatal("PublishClaim accepted invalid umbrella sidecar")
	}

	// Terminal history scan accepts valid umbrella sidecar and rejects orphan/invalid/reopen failures.
	home, expectation, claim := terminalHistoryFixture(t)
	addRemovedHistoryRun(t, home, expectation, claim)
	claimsDir := filepath.Join(home, "worklogs", claim.EffortID, "runs", claim.RunID, "claims")
	sidecar := filepath.Join(claimsDir, claim.ClaimID+UmbrellaBindingSuffix)
	writeTestJSON(t, sidecar, ClaimUmbrellaBinding{Umbrella: "github:sneat-co/backstage#845", RecordedAt: time.Now().UTC()})
	ports := terminalHistoryTestPorts()
	if err := ports.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation}); err != nil {
		t.Fatalf("ValidateRemovedTerminalWorkLogs with valid umbrella sidecar: %v", err)
	}

	// Reopen failure on umbrella sidecar.
	openClaimsCount := 0
	failReopen := terminalHistoryTestPorts()
	origOpen := failReopen.OpenChild
	failReopen.OpenChild = func(parent *os.File, name string, create bool) (*os.File, error) {
		if name == "claims" {
			openClaimsCount++
			if openClaimsCount == 2 {
				return nil, errors.New("reopen claims failed")
			}
		}
		return origOpen(parent, name, create)
	}
	if err := failReopen.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation}); err == nil {
		t.Fatal("expected reopen failure for umbrella sidecar")
	}

	// Corrupt umbrella sidecar fails terminal history validation.
	writeTestJSON(t, sidecar, ClaimUmbrellaBinding{Umbrella: "broken"})
	if err := ports.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation}); err == nil {
		t.Fatal("expected error for invalid umbrella sidecar")
	}

	// Orphan umbrella sidecar fails terminal history validation.
	_ = os.Remove(sidecar)
	writeTestJSON(t, filepath.Join(claimsDir, "bad"+UmbrellaBindingSuffix), ClaimUmbrellaBinding{Umbrella: "github:sneat-co/backstage#845", RecordedAt: time.Now().UTC()})
	if err := ports.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation}); err == nil {
		t.Fatal("expected error for unsafe umbrella sidecar")
	}
}

func writeTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
