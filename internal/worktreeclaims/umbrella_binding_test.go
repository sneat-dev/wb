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

func TestValidateLandingBindingAndValidatedSidecarReaders(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	claimID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	sha := "1111111111111111111111111111111111111111"
	now := time.Now().UTC()

	claim := Claim{Version: 2, EffortID: "effort", RunID: "run", ClaimID: claimID, Task: "effort", Repository: "sneat-co/backstage"}
	writeTestJSON(t, filepath.Join(dir, claimID+".json"), claim)

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

	// Missing sidecars return found=false, err=nil; direct internal validators fail with ErrNotExist.
	if _, found, err := ReadValidatedPullRequestBinding(dirFile, claim, readJSON); err != nil || found {
		t.Fatalf("missing PR binding = (%v, %v), want (false, nil)", found, err)
	}
	if _, found, err := ReadValidatedUmbrellaBinding(dirFile, claim, readJSON); err != nil || found {
		t.Fatalf("missing umbrella binding = (%v, %v), want (false, nil)", found, err)
	}
	if _, found, err := ReadValidatedLandingBinding(dirFile, claim, readJSON); err != nil || found {
		t.Fatalf("missing landing binding = (%v, %v), want (false, nil)", found, err)
	}
	files := map[string]bool{
		claimID + ".json":                  true,
		claimID + PullRequestBindingSuffix: true,
		claimID + UmbrellaBindingSuffix:    true,
		claimID + LandingBindingSuffix:     true,
	}
	if err := ValidatePullRequestBindingEntry(dirFile, claimID+PullRequestBindingSuffix, files, readJSON); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing PR entry err = %v, want ErrNotExist", err)
	}
	if err := ValidateUmbrellaBindingEntry(dirFile, claimID+UmbrellaBindingSuffix, files, readJSON); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing umbrella entry err = %v, want ErrNotExist", err)
	}
	if err := ValidateLandingBindingEntry(dirFile, claimID+LandingBindingSuffix, files, readJSON); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing landing entry err = %v, want ErrNotExist", err)
	}

	// Mismatched sibling claim ID fails all three entry validators.
	badClaim := claim
	badClaim.ClaimID = otherID
	writeTestJSON(t, filepath.Join(dir, claimID+".json"), badClaim)
	if err := ValidatePullRequestBindingEntry(dirFile, claimID+PullRequestBindingSuffix, files, readJSON); err == nil {
		t.Fatal("mismatched claimID accepted for PR binding")
	}
	if err := ValidateUmbrellaBindingEntry(dirFile, claimID+UmbrellaBindingSuffix, files, readJSON); err == nil {
		t.Fatal("mismatched claimID accepted for umbrella binding")
	}
	if err := ValidateLandingBindingEntry(dirFile, claimID+LandingBindingSuffix, files, readJSON); err == nil {
		t.Fatal("mismatched claimID accepted for landing binding")
	}
	writeTestJSON(t, filepath.Join(dir, claimID+".json"), claim)

	// Valid landing binding passes validation and reader.
	validLanding := ClaimLandingBinding{
		ClaimID: claimID, Task: "effort", Repository: "sneat-co/backstage",
		Target: "main", LandedSHA: sha, PullRequest: 42,
		PullRequestURL: "https://github.com/sneat-co/backstage/pull/42", RecordedAt: now,
	}
	if err := ValidateLandingBindingFields(validLanding); err != nil {
		t.Fatalf("valid landing binding refused: %v", err)
	}
	writeTestJSON(t, filepath.Join(dir, claimID+LandingBindingSuffix), validLanding)
	if err := ValidateLandingBindingEntry(dirFile, claimID+LandingBindingSuffix, files, readJSON); err != nil {
		t.Fatalf("ValidateLandingBindingEntry valid = %v", err)
	}
	gotLanding, found, err := ReadValidatedLandingBinding(dirFile, claim, readJSON)
	if err != nil || !found || gotLanding.LandedSHA != sha {
		t.Fatalf("ReadValidatedLandingBinding = (%+v, %v, %v)", gotLanding, found, err)
	}

	// Invalid landing binding fields / orphaned / nil reader / unreadable claim / mismatched identity.
	if err := ValidateLandingBindingEntry(dirFile, "bad"+LandingBindingSuffix, files, readJSON); err == nil {
		t.Fatal("bad claimID landing accepted")
	}
	if err := ValidateLandingBindingEntry(dirFile, claimID+LandingBindingSuffix, map[string]bool{}, readJSON); err == nil {
		t.Fatal("orphaned landing accepted")
	}
	if err := ValidateLandingBindingEntry(dirFile, claimID+LandingBindingSuffix, files, nil); err == nil {
		t.Fatal("nil reader landing accepted")
	}
	failClaim := func(f *os.File, name string, dest any) error {
		if name == claimID+".json" {
			return errors.New("claim boom")
		}
		return readJSON(f, name, dest)
	}
	if err := ValidateLandingBindingEntry(dirFile, claimID+LandingBindingSuffix, files, failClaim); err == nil {
		t.Fatal("claim read error ignored for landing")
	}
	for _, bad := range []ClaimLandingBinding{
		{ClaimID: otherID, Task: "effort", Repository: "sneat-co/backstage", Target: "main", LandedSHA: sha, RecordedAt: now},
		{ClaimID: claimID, Task: "other", Repository: "sneat-co/backstage", Target: "main", LandedSHA: sha, RecordedAt: now},
		{ClaimID: claimID, Task: "effort", Repository: "other/repo", Target: "main", LandedSHA: sha, RecordedAt: now},
		{ClaimID: claimID, Task: "effort", Repository: "sneat-co/backstage", Target: "", LandedSHA: sha, RecordedAt: now},
		{ClaimID: claimID, Task: "effort", Repository: "sneat-co/backstage", Target: "main", LandedSHA: "not-a-sha", RecordedAt: now},
		{ClaimID: claimID, Task: "effort", Repository: "sneat-co/backstage", Target: "main", LandedSHA: sha, PullRequest: 42, PullRequestURL: "https://github.com/sneat-co/backstage/pull/99", RecordedAt: now},
		{ClaimID: claimID, Task: "effort", Repository: "sneat-co/backstage", Target: "main", LandedSHA: sha},
	} {
		writeTestJSON(t, filepath.Join(dir, claimID+LandingBindingSuffix), bad)
		if err := ValidateLandingBindingEntry(dirFile, claimID+LandingBindingSuffix, files, readJSON); err == nil {
			t.Fatalf("invalid landing binding %+v accepted", bad)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, claimID+LandingBindingSuffix), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidateLandingBindingEntry(dirFile, claimID+LandingBindingSuffix, files, readJSON); err == nil {
		t.Fatal("corrupt landing JSON accepted")
	}

	// Non-SHA LandedSHA in PR binding is rejected, and ValidatePullRequestBindingEntry refuses bad/orphaned/nil inputs.
	if err := ValidatePullRequestBindingEntry(dirFile, "bad"+PullRequestBindingSuffix, files, readJSON); err == nil {
		t.Fatal("bad claimID PR binding accepted")
	}
	if err := ValidatePullRequestBindingFields(ClaimPullRequestBinding{
		Repository: "sneat-co/backstage", PullRequest: 1, URL: "https://github.com/sneat-co/backstage/pull/1", LandedSHA: "bad-sha",
	}); err == nil {
		t.Fatal("non-SHA LandedSHA accepted in PR binding")
	}

	// PublishClaim and ValidateRemovedTerminalWorkLogs accept valid .landed.json and reject orphan/invalid/reopen errors.
	f := newSidecarPublicationFixture(t)
	landingPath := filepath.Join(filepath.Dir(f.bindingPath), f.first.ClaimID+LandingBindingSuffix)
	writeTestJSON(t, landingPath, ClaimLandingBinding{
		ClaimID: f.first.ClaimID, Task: f.first.Task, Repository: f.first.Repository,
		Target: "main", LandedSHA: sha, RecordedAt: now,
	})
	if _, err := f.ports.PublishClaim(f.home, f.run, f.runPath, f.next, PublicationHooks{}); err != nil {
		t.Fatalf("PublishClaim with valid landing sidecar: %v", err)
	}
	f2 := newSidecarPublicationFixture(t)
	writeTestJSON(t, filepath.Join(filepath.Dir(f2.bindingPath), "invalid"+LandingBindingSuffix), validLanding)
	if _, err := f2.ports.PublishClaim(f2.home, f2.run, f2.runPath, f2.next, PublicationHooks{}); err == nil {
		t.Fatal("PublishClaim accepted unsafe landing sidecar")
	}
	f3 := newSidecarPublicationFixture(t)
	writeTestJSON(t, filepath.Join(filepath.Dir(f3.bindingPath), f3.first.ClaimID+LandingBindingSuffix), ClaimLandingBinding{ClaimID: f3.first.ClaimID})
	if _, err := f3.ports.PublishClaim(f3.home, f3.run, f3.runPath, f3.next, PublicationHooks{}); err == nil {
		t.Fatal("PublishClaim accepted invalid landing sidecar")
	}

	home, expectation, histClaim := terminalHistoryFixture(t)
	addRemovedHistoryRun(t, home, expectation, histClaim)
	histClaimsDir := filepath.Join(home, "worklogs", histClaim.EffortID, "runs", histClaim.RunID, "claims")
	histLanding := filepath.Join(histClaimsDir, histClaim.ClaimID+LandingBindingSuffix)
	writeTestJSON(t, histLanding, ClaimLandingBinding{
		ClaimID: histClaim.ClaimID, Task: histClaim.Task, Repository: histClaim.Repository,
		Target: "main", LandedSHA: sha, RecordedAt: now,
	})
	histPorts := terminalHistoryTestPorts()
	if err := histPorts.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation}); err != nil {
		t.Fatalf("ValidateRemovedTerminalWorkLogs with valid landing sidecar: %v", err)
	}
	openCount := 0
	failReopen := terminalHistoryTestPorts()
	origOpen := failReopen.OpenChild
	failReopen.OpenChild = func(parent *os.File, name string, create bool) (*os.File, error) {
		if name == "claims" {
			openCount++
			if openCount == 2 {
				return nil, errors.New("reopen claims failed")
			}
		}
		return origOpen(parent, name, create)
	}
	if err := failReopen.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation}); err == nil {
		t.Fatal("expected reopen failure for landing sidecar")
	}
	writeTestJSON(t, histLanding, ClaimLandingBinding{ClaimID: histClaim.ClaimID})
	if err := histPorts.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation}); err == nil {
		t.Fatal("expected error for invalid landing sidecar in terminal history")
	}
	_ = os.Remove(histLanding)
	writeTestJSON(t, filepath.Join(histClaimsDir, "bad"+LandingBindingSuffix), validLanding)
	if err := histPorts.ValidateRemovedTerminalWorkLogs(home, []TerminalWorkLogExpectation{expectation}); err == nil {
		t.Fatal("expected error for unsafe landing sidecar in terminal history")
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
