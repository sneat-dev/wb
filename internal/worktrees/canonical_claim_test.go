package worktrees

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var canonicalClaimEpoch = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// canonicalClaimFixture is a plain git clone on a feature branch with a
// private Work Log home, built without touching process-global state so the
// tests that use it can run in parallel.
type canonicalClaimFixture struct {
	home   string
	clone  string
	result CreateResult
}

func newCanonicalClaimFixture(t *testing.T) canonicalClaimFixture {
	t.Helper()
	root := t.TempDir()
	clone := filepath.Join(root, "acme", "app")
	if err := os.MkdirAll(clone, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, clone, "init", "--initial-branch=main")
	configureGitUser(t, clone)
	gitTest(t, clone, "commit", "--allow-empty", "-m", "base")
	base := gitTestOutput(t, clone, "rev-parse", "HEAD")
	gitTest(t, clone, "checkout", "-b", "feature/canonical")
	return canonicalClaimFixture{
		home:  filepath.Join(root, "wbhome"),
		clone: clone,
		result: CreateResult{Repository: "acme/app", CanonicalDir: clone, WorktreeDir: clone,
			Branch: "feature/canonical", Base: "main", BaseSHA: base},
	}
}

func (fixture canonicalClaimFixture) record(t *testing.T, options WorkLogOptions) workLogClaim {
	t.Helper()
	options.Model = "unknown"
	if _, err := recordWorkLogWithHooks(fixture.home, "canonical-task", fixture.result, options, workLogPublicationHooks{}); err != nil {
		t.Fatalf("record claim: %v", err)
	}
	claim, _, _, err := activeWorkLogClaimReadOnly(fixture.home, fixture.clone)
	if err != nil {
		t.Fatalf("read recorded claim: %v", err)
	}
	return claim
}

func (fixture canonicalClaimFixture) recordCanonical(t *testing.T, lease time.Time) workLogClaim {
	t.Helper()
	return fixture.record(t, WorkLogOptions{Mode: ClaimModeCanonical, LeaseExpiresAt: lease})
}

func (fixture canonicalClaimFixture) seal(t *testing.T) {
	t.Helper()
	head := gitTestOutput(t, fixture.clone, "rev-parse", "HEAD")
	if err := sealWorkLogForRecycle(fixture.home, fixture.clone, head, "discarded"); err != nil {
		t.Fatal(err)
	}
}

func TestRecordedCanonicalClaimCarriesModeAndLease(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	lease := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	claim := fixture.recordCanonical(t, lease)
	if claim.Mode != ClaimModeCanonical || claim.LeaseExpiresAt == nil || !claim.LeaseExpiresAt.Equal(lease) {
		t.Fatalf("claim mode/lease = %q/%v, want canonical/%v", claim.Mode, claim.LeaseExpiresAt, lease)
	}
	if worktreeID := workLogClaimID("canonical-task", fixture.result); claim.ClaimID == worktreeID {
		t.Fatal("canonical claim shares the identity of the same checkout as a worktree claim")
	}
}

func TestWorktreeClaimKeepsItsIdentityWhenModeIsDefaultOrExplicit(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	plain := fixture.record(t, WorkLogOptions{})
	if plain.Mode != "" || plain.LeaseExpiresAt != nil {
		t.Fatalf("default claim mode/lease = %q/%v, want none", plain.Mode, plain.LeaseExpiresAt)
	}
	if want := workLogClaimID("canonical-task", fixture.result); plain.ClaimID != want {
		t.Fatalf("default claim identity changed: %s != %s", plain.ClaimID, want)
	}
	explicit := newCanonicalClaimFixture(t).record(t, WorkLogOptions{Mode: ClaimModeWorktree})
	if explicit.Mode != ClaimModeWorktree {
		t.Fatalf("explicit mode = %q", explicit.Mode)
	}
}

func TestCanonicalClaimCannotBeRewrittenAsAWorktreeClaim(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
	rewritten := claim
	rewritten.Mode = ""
	rewritten.LeaseExpiresAt = nil
	if err := validateStaticWorkLogClaim(rewritten, claim.EffortID, claim.RunID); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("worktree rewrite of a canonical claim was accepted: %v", err)
	}
	if err := validateStaticWorkLogClaim(claim, claim.EffortID, claim.RunID); err != nil {
		t.Fatalf("genuine canonical claim rejected: %v", err)
	}
}

func TestClaimModeAndLeaseValidation(t *testing.T) {
	t.Parallel()
	now := canonicalClaimEpoch
	future := now.Add(time.Hour)
	cases := []struct {
		name    string
		mode    string
		lease   time.Time
		wantErr string
	}{
		{"default mode without lease", "", time.Time{}, ""},
		{"explicit worktree without lease", ClaimModeWorktree, time.Time{}, ""},
		{"canonical with future lease", ClaimModeCanonical, future, ""},
		{"unknown mode", "shared", time.Time{}, "claim mode"},
		{"canonical without lease", ClaimModeCanonical, time.Time{}, "lease_expires_at"},
		{"canonical with lease not after record time", ClaimModeCanonical, now, "lease_expires_at"},
		{"worktree with lease", ClaimModeWorktree, future, "valid only for a canonical claim"},
		{"default with lease", "", future, "valid only for a canonical claim"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateClaimModeLease(tc.mode, tc.lease, now)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestRecordWorkLogRefusesInvalidModeBeforeWritingAnything(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	_, err := recordWorkLogWithHooks(fixture.home, "canonical-task", fixture.result,
		WorkLogOptions{Model: "unknown", Mode: ClaimModeCanonical}, workLogPublicationHooks{})
	if err == nil {
		t.Fatal("canonical claim without a lease was recorded")
	}
	if _, statErr := os.Stat(fixture.home); !os.IsNotExist(statErr) {
		t.Fatalf("a refused claim wrote Work Log state: %v", statErr)
	}
}

func TestLookupReportsNoneWhenCloneHasNoClaim(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	got, err := LookupCanonicalClaim(fixture.home, fixture.clone, canonicalClaimEpoch)
	if err != nil || got.State != CanonicalClaimNone {
		t.Fatalf("lookup = %+v, %v; want none", got, err)
	}
}

func TestLookupDistinguishesLiveFromLapsedByInjectedClock(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	lease := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	claim := fixture.recordCanonical(t, lease)
	for _, tc := range []struct {
		name string
		now  time.Time
		want CanonicalClaimState
	}{
		{"just before expiry", lease.Add(-time.Nanosecond), CanonicalClaimLive},
		{"at expiry", lease, CanonicalClaimLapsed},
		{"long after expiry", lease.Add(48 * time.Hour), CanonicalClaimLapsed},
	} {
		got, err := LookupCanonicalClaim(fixture.home, fixture.clone, tc.now)
		if err != nil || got.State != tc.want {
			t.Fatalf("%s: lookup = %+v, %v; want %s", tc.name, got, err, tc.want)
		}
		if got.Task != "canonical-task" || got.Branch != "feature/canonical" || got.Repository != "acme/app" ||
			got.ClaimID != claim.ClaimID || !got.LeaseExpiresAt.Equal(lease) {
			t.Fatalf("%s: lookup identity = %+v", tc.name, got)
		}
	}
}

func TestLookupHonoursLeaseExtensionEvidence(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	lease := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	claim := fixture.recordCanonical(t, lease)
	first := lease.Add(time.Hour)
	second := lease.Add(2 * time.Hour)
	for _, expiry := range []time.Time{first, second} {
		if _, err := appendCanonicalLeaseExtension(fixture.home, claim, canonicalClaimEpoch, expiry); err != nil {
			t.Fatalf("extend lease: %v", err)
		}
	}
	got, err := LookupCanonicalClaim(fixture.home, fixture.clone, lease.Add(90*time.Minute))
	if err != nil || got.State != CanonicalClaimLive || !got.LeaseExpiresAt.Equal(second) {
		t.Fatalf("lookup after extensions = %+v, %v; want live until %v", got, err, second)
	}
	if got, err := LookupCanonicalClaim(fixture.home, fixture.clone, second); err != nil || got.State != CanonicalClaimLapsed {
		t.Fatalf("lookup at extended expiry = %+v, %v; want lapsed", got, err)
	}
	claimJSON, err := os.ReadFile(filepath.Join(runPathFor(fixture, claim), "claims", claim.ClaimID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(claimJSON), lease.Format(time.RFC3339)) || strings.Contains(string(claimJSON), second.Format(time.RFC3339)) {
		t.Fatalf("immutable claim was mutated by an extension: %s", claimJSON)
	}
}

func TestLeaseExtensionRefusals(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	lease := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	claim := fixture.recordCanonical(t, lease)
	if _, err := appendCanonicalLeaseExtension(fixture.home, claim, canonicalClaimEpoch, lease); err == nil {
		t.Fatal("an extension that does not move the expiry forward was accepted")
	}
	if _, err := appendCanonicalLeaseExtension(fixture.home, claim, time.Time{}, lease.Add(time.Hour)); err == nil {
		t.Fatal("an extension without a timestamp was accepted")
	}
	worktreeClaim := claim
	worktreeClaim.Mode = ""
	if _, err := appendCanonicalLeaseExtension(fixture.home, worktreeClaim, canonicalClaimEpoch, lease.Add(time.Hour)); err == nil {
		t.Fatal("a lease extension was recorded for a worktree-mode claim")
	}
	missing := claim
	missing.RunID = "no-such-run"
	if _, err := appendCanonicalLeaseExtension(fixture.home, missing, canonicalClaimEpoch, lease.Add(time.Hour)); err == nil {
		t.Fatal("an extension was recorded for a run that does not exist")
	}
	broken := claim
	broken.ClaimID = "not-a-claim-id"
	if _, err := appendCanonicalLeaseExtension(fixture.home, broken, canonicalClaimEpoch, lease.Add(time.Hour)); err == nil {
		t.Fatal("an extension was recorded for an invalid claim id")
	}
}

func TestLookupReportsSealedAfterTheClaimIsSealed(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
	fixture.seal(t)
	// The sealed clone is back on its base branch: the claim branch no longer
	// corroborates, which must not turn "sealed" into an error.
	gitTest(t, fixture.clone, "checkout", "main")
	got, err := LookupCanonicalClaim(fixture.home, fixture.clone, canonicalClaimEpoch)
	if err != nil || got.State != CanonicalClaimSealed || got.ClaimID != claim.ClaimID || got.Task != "canonical-task" {
		t.Fatalf("lookup = %+v, %v; want sealed", got, err)
	}
}

func TestLookupFailsClosedOnUnreadableState(t *testing.T) {
	t.Parallel()
	now := canonicalClaimEpoch
	expectError := func(t *testing.T, fixture canonicalClaimFixture, want string) {
		t.Helper()
		got, err := LookupCanonicalClaim(fixture.home, fixture.clone, now)
		if err == nil {
			t.Fatalf("lookup = %+v, want error", got)
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want %q", err, want)
		}
		if got.State != "" {
			t.Fatalf("failed lookup reported state %q", got.State)
		}
	}
	t.Run("relative path", func(t *testing.T) {
		t.Parallel()
		if got, err := LookupCanonicalClaim(t.TempDir(), "relative/clone", now); err == nil {
			t.Fatalf("relative path lookup = %+v, want error", got)
		}
	})
	t.Run("zero clock", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		if got, err := LookupCanonicalClaim(fixture.home, fixture.clone, time.Time{}); err == nil {
			t.Fatalf("zero clock lookup = %+v, want error", got)
		}
	})
	t.Run("malformed projection", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
		corruptProjection(t, fixture.clone)
		expectError(t, fixture, "")
	})
	t.Run("claim file missing", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
		if err := os.Remove(filepath.Join(runPathFor(fixture, claim), "claims", claim.ClaimID+".json")); err != nil {
			t.Fatal(err)
		}
		expectError(t, fixture, "")
	})
	t.Run("claim on another branch", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
		gitTest(t, fixture.clone, "checkout", "main")
		expectError(t, fixture, "")
	})
	t.Run("worktree claim at the clone path", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		fixture.record(t, WorkLogOptions{})
		expectError(t, fixture, "not a canonical claim")
	})
	t.Run("sealed worktree claim at the clone path", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		fixture.record(t, WorkLogOptions{})
		fixture.seal(t)
		expectError(t, fixture, "not a canonical claim")
	})
	t.Run("sealed claim whose terminal record is missing", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
		fixture.seal(t)
		if err := os.Remove(filepath.Join(runPathFor(fixture, claim), "terminals", claim.ClaimID+".json")); err != nil {
			t.Fatal(err)
		}
		expectError(t, fixture, "")
	})
	t.Run("sealed claim whose run is missing", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
		fixture.seal(t)
		if err := os.RemoveAll(runPathFor(fixture, claim)); err != nil {
			t.Fatal(err)
		}
		expectError(t, fixture, "")
	})
	t.Run("sealed claim file missing", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
		fixture.seal(t)
		if err := os.Remove(filepath.Join(runPathFor(fixture, claim), "claims", claim.ClaimID+".json")); err != nil {
			t.Fatal(err)
		}
		expectError(t, fixture, "")
	})
	t.Run("sealed terminal for a different claim", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
		fixture.seal(t)
		rewriteJSONField(t, filepath.Join(runPathFor(fixture, claim), "terminals", claim.ClaimID+".json"), "claim_id", strings.Repeat("a", 64))
		expectError(t, fixture, "does not match")
	})
	t.Run("sealed claim recorded for another path", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
		fixture.seal(t)
		rewriteJSONField(t, filepath.Join(runPathFor(fixture, claim), "claims", claim.ClaimID+".json"), "worktree", "/elsewhere")
		expectError(t, fixture, "does not match")
	})
	t.Run("canonical claim with its lease removed", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
		rewriteJSONField(t, filepath.Join(runPathFor(fixture, claim), "claims", claim.ClaimID+".json"), "lease_expires_at", nil)
		expectError(t, fixture, "")
	})
}

func TestLookupFailsClosedOnBadLeaseExtensionEvidence(t *testing.T) {
	t.Parallel()
	lease := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	setup := func(t *testing.T) (canonicalClaimFixture, workLogClaim, string) {
		t.Helper()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, lease)
		for i := 1; i <= 2; i++ {
			if _, err := appendCanonicalLeaseExtension(fixture.home, claim, canonicalClaimEpoch, lease.Add(time.Duration(i)*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		return fixture, claim, filepath.Join(runPathFor(fixture, claim), "lease-extensions", claim.ClaimID)
	}
	mutations := []struct {
		name   string
		mutate func(t *testing.T, directory string)
	}{
		{"unparseable event", func(t *testing.T, directory string) {
			overwrite(t, filepath.Join(directory, "0001.json"), []byte("{"))
		}},
		{"non-json filename", func(t *testing.T, directory string) {
			overwrite(t, filepath.Join(directory, "stray.txt"), []byte("x"))
		}},
		{"event for another claim", func(t *testing.T, directory string) {
			rewriteJSONField(t, filepath.Join(directory, "0001.json"), "claim_id", strings.Repeat("b", 64))
		}},
		{"unknown event type", func(t *testing.T, directory string) {
			rewriteJSONField(t, filepath.Join(directory, "0001.json"), "type", "other")
		}},
		{"missing timestamp", func(t *testing.T, directory string) {
			rewriteJSONField(t, filepath.Join(directory, "0001.json"), "at", "0001-01-01T00:00:00Z")
		}},
		{"filename does not match event id", func(t *testing.T, directory string) {
			rewriteJSONField(t, filepath.Join(directory, "0001.json"), "extension_id", "0009")
		}},
		{"forked chain", func(t *testing.T, directory string) {
			rewriteJSONField(t, filepath.Join(directory, "0002.json"), "predecessor_id", "0007")
		}},
		{"expiry moves backwards", func(t *testing.T, directory string) {
			rewriteJSONField(t, filepath.Join(directory, "0002.json"), "lease_expires_at", lease.Format(time.RFC3339))
		}},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fixture, _, directory := setup(t)
			tc.mutate(t, directory)
			if got, err := LookupCanonicalClaim(fixture.home, fixture.clone, canonicalClaimEpoch); err == nil {
				t.Fatalf("lookup = %+v, want error", got)
			}
		})
	}
	t.Run("extension history is not a directory", func(t *testing.T) {
		t.Parallel()
		fixture, claim, directory := setup(t)
		if err := os.RemoveAll(directory); err != nil {
			t.Fatal(err)
		}
		overwrite(t, directory, []byte("x"))
		if got, err := LookupCanonicalClaim(fixture.home, fixture.clone, canonicalClaimEpoch); err == nil {
			t.Fatalf("lookup = %+v, want error", got)
		}
		if _, err := appendCanonicalLeaseExtension(fixture.home, claim, canonicalClaimEpoch, lease.Add(9*time.Hour)); err == nil {
			t.Fatal("extension appended onto unreadable history")
		}
	})
}

func TestLookupIsReadOnlyAndRereadsOnEveryCall(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	lease := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	claim := fixture.recordCanonical(t, lease)
	before := snapshotTree(t, fixture.home) + snapshotTree(t, filepath.Join(fixture.clone, ".wb-worklog"))
	if got, err := LookupCanonicalClaim(fixture.home, fixture.clone, lease.Add(-time.Minute)); err != nil || got.State != CanonicalClaimLive {
		t.Fatalf("first lookup = %+v, %v", got, err)
	}
	if after := snapshotTree(t, fixture.home) + snapshotTree(t, filepath.Join(fixture.clone, ".wb-worklog")); after != before {
		t.Fatalf("lookup changed Work Log state:\n%s\n---\n%s", before, after)
	}
	// A claim sealed between two calls is seen by the second: nothing is cached.
	fixture.seal(t)
	if got, err := LookupCanonicalClaim(fixture.home, fixture.clone, lease.Add(-time.Minute)); err != nil || got.State != CanonicalClaimSealed || got.ClaimID != claim.ClaimID {
		t.Fatalf("second lookup = %+v, %v; want sealed", got, err)
	}
}

func runPathFor(fixture canonicalClaimFixture, claim workLogClaim) string {
	return filepath.Join(fixture.home, "worklogs", claim.EffortID, "runs", claim.RunID)
}

func corruptProjection(t *testing.T, clone string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(clone, ".wb-worklog", "*.json"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("projection files = %v, %v", matches, err)
	}
	for _, match := range matches {
		overwrite(t, match, []byte("{"))
	}
}

func overwrite(t *testing.T, path string, content []byte) {
	t.Helper()
	_ = os.Chmod(path, 0o600)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

// rewriteJSONField sets (or, for a nil value, deletes) one top-level JSON
// field of an immutable record, simulating on-disk tampering.
func rewriteJSONField(t *testing.T, path, field string, value any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(content, &record); err != nil {
		t.Fatal(err)
	}
	if value == nil {
		delete(record, field)
	} else {
		record[field] = value
	}
	content, err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	overwrite(t, path, content)
}

func snapshotTree(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		line := path + " " + info.ModTime().String()
		if !info.IsDir() {
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			line += " " + string(content)
		}
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func TestLookupFailsClosedWhenSealedClaimFailsStaticValidation(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
	fixture.seal(t)
	rewriteJSONField(t, filepath.Join(runPathFor(fixture, claim), "claims", claim.ClaimID+".json"), "lease_expires_at", nil)
	if got, err := LookupCanonicalClaim(fixture.home, fixture.clone, canonicalClaimEpoch); err == nil {
		t.Fatalf("lookup = %+v, want error", got)
	}
}

func TestEffectiveCanonicalLeaseFailsClosedWhenRunCannotBeReopened(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
	failing := func(string, string, string, bool) (*os.File, string, error) { return nil, "", os.ErrPermission }
	if _, err := effectiveCanonicalLeaseWith(failing, fixture.home, claim); err == nil {
		t.Fatal("lease was projected without its run")
	}
}

func TestLeaseChainFailsClosedWhenHistoryCannotBeListed(t *testing.T) {
	t.Parallel()
	fixture := newCanonicalClaimFixture(t)
	claim := fixture.recordCanonical(t, time.Now().UTC().Add(time.Hour))
	closed, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = closed.Close()
	if _, _, err := projectLeaseChain(closed, claim); err == nil {
		t.Fatal("unlistable extension history was accepted")
	}
}

func TestLeaseExtensionFailsClosedWhenStorageIsUnusable(t *testing.T) {
	t.Parallel()
	lease := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	t.Run("claim lock cannot be taken", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, lease)
		overwrite(t, filepath.Join(runPathFor(fixture, claim), "locks"), []byte("x"))
		if _, err := appendCanonicalLeaseExtension(fixture.home, claim, canonicalClaimEpoch, lease.Add(time.Hour)); err == nil {
			t.Fatal("extension was recorded without the claim lock")
		}
	})
	t.Run("history directory cannot be created", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, lease)
		run := runPathFor(fixture, claim)
		if err := os.MkdirAll(filepath.Join(run, "locks"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(run, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(run, 0o700) })
		if _, err := appendCanonicalLeaseExtension(fixture.home, claim, canonicalClaimEpoch, lease.Add(time.Hour)); err == nil {
			t.Fatal("extension was recorded without a history directory")
		}
	})
	t.Run("event cannot be written", func(t *testing.T) {
		t.Parallel()
		fixture := newCanonicalClaimFixture(t)
		claim := fixture.recordCanonical(t, lease)
		failing := func(*os.File, string, any, bool) error { return os.ErrPermission }
		if _, err := appendLeaseExtensionWith(failing, fixture.home, claim, canonicalClaimEpoch, lease.Add(time.Hour)); err == nil {
			t.Fatal("extension was reported recorded though the write failed")
		}
		if got, err := LookupCanonicalClaim(fixture.home, fixture.clone, canonicalClaimEpoch); err != nil || !got.LeaseExpiresAt.Equal(lease) {
			t.Fatalf("failed extension changed the lease: %+v, %v", got, err)
		}
	})
}
