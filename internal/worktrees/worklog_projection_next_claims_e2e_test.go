//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

//nolint:paralleltest // The existing native publication fixture sets WB and Git process environment.
func TestE2EWorkLogProjectionNextClaimCorroborationUsesNativeGitFailures(t *testing.T) {
	for _, kind := range []string{"private claim removed", "invalid static claim", "relocated identity mismatch", "relocated branch mismatch", "missing ancestor", "corrupt HEAD"} {
		//nolint:paralleltest // Each case owns an existing process-environment fixture.
		t.Run(kind, func(t *testing.T) {
			fixture := newWorkLogCoverageBatchFixture(t, "projection-next-claim")
			claim := fixture.outcome.claim
			projection, err := readWorkLogProjection(fixture.worktree)
			if err != nil {
				t.Fatal(err)
			}
			head := gitTestOutput(t, fixture.worktree, "rev-parse", "HEAD")
			switch kind {
			case "private claim removed":
				if err := os.Remove(fixture.outcome.ClaimPath); err != nil {
					t.Fatal(err)
				}
				if err := corroborateWorkLogProjection(fixture.home, fixture.worktree, head, projection); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("private evidence cause=%v", err)
				}
			case "invalid static claim":
				claim.Version = 999
				if err := corroborateClaimGit(fixture.worktree, head, projection, claim, func(string) error { t.Fatal("invalid claim reached branch comparison"); return nil }); err == nil || !strings.Contains(err.Error(), "identity metadata") {
					t.Fatalf("static identity=%v", err)
				}
			case "relocated identity mismatch":
				claim.RunID = "foreign"
				if err := corroborateRelocatedClaim(fixture.worktree, head, projection, claim); err == nil || !strings.Contains(err.Error(), "immutable active claim") {
					t.Fatalf("relocated identity=%v", err)
				}
			case "relocated branch mismatch":
				claim.Branch = "wb/foreign"
				claim.ClaimID, err = expectedWorkLogClaimID(claim)
				if err != nil {
					t.Fatal(err)
				}
				projection.ClaimID = claim.ClaimID
				if err := validateStaticWorkLogClaim(claim, projection.EffortID, projection.RunID); err != nil {
					t.Fatalf("fixture identity=%v", err)
				}
				if err := corroborateRelocatedClaim(fixture.worktree, head, projection, claim); err == nil || !strings.Contains(err.Error(), "does not match private claim") {
					t.Fatalf("native branch=%v", err)
				}
			case "missing ancestor":
				claim.BaseSHA = strings.Repeat("f", 40)
				claim.ClaimID, err = expectedWorkLogClaimID(claim)
				if err != nil {
					t.Fatal(err)
				}
				projection.ClaimID = claim.ClaimID
				if err := validateStaticWorkLogClaim(claim, projection.EffortID, projection.RunID); err != nil {
					t.Fatalf("fixture identity=%v", err)
				}
				if err := corroborateClaimGit(fixture.worktree, head, projection, claim, func(string) error { t.Fatal("wrong branch boundary"); return nil }); err == nil || !strings.Contains(err.Error(), "not descended from claimed base") {
					t.Fatalf("native ancestry=%v", err)
				}
			case "corrupt HEAD":
				path := filepath.Join(fixture.worktree, ".git", "HEAD")
				projectionNextWrite(t, path, []byte("invalid HEAD\n"))
				if err := corroborateProjectionWithPrivateClaim(fixture.home, fixture.worktree, projection); err == nil {
					t.Fatal("corrupt native HEAD accepted")
				}
				if err := corroborateClaimGit(fixture.worktree, head, projection, claim, func(string) error { t.Fatal("corrupt HEAD reached branch comparison"); return nil }); err == nil || !strings.Contains(err.Error(), "read the live branch") {
					t.Fatalf("native branch command=%v", err)
				}
			}
		})
	}
}

//nolint:paralleltest // The existing native fixture sets WB and Git process environment.
func TestE2EWorkLogProjectionNextLegacyPointerRemovalRetainsChangedEvidence(t *testing.T) {
	fixture := newWorkLogCoverageBatchFixture(t, "projection-next-remove-legacy")
	projection, err := readWorkLogProjection(fixture.worktree)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeLegacyProjectionAt(fixture.worktree, projection); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(fixture.worktree, legacyWorkLogProjectionName)
	currentPath := filepath.Join(fixture.worktree, workLogProjectionDirectory, workLogProjectionName)
	before, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	observe := func(phase workLogProjectionBoundary, _ *os.File) {
		if phase != workLogProjectionCorroborated {
			return
		}
		hit = true
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	_, err = readWorkLogProjectionForClaimObserved(fixture.home, fixture.worktree, observe)
	if !hit || err == nil {
		t.Fatalf("changed legacy pointer=%v hit=%v", err, hit)
	}
	if info, err := os.Lstat(path); err != nil || !info.IsDir() {
		t.Fatalf("successor evidence=%v %v", info, err)
	}
	if after, err := os.ReadFile(currentPath); err != nil || string(after) != string(before) {
		t.Fatalf("current evidence=%q %v", after, err)
	}
}

//nolint:paralleltest // The existing native transfer fixture sets process-wide Git environment.
func TestE2EWorkLogProjectionNextRelocationCorroborationRechecksPrivateAndOriginEvidence(t *testing.T) {
	for _, kind := range []string{"journal changes after resolution", "origin changes after completed resolution"} {
		//nolint:paralleltest // Each case owns the existing process-environment transfer fixture.
		t.Run(kind, func(t *testing.T) {
			fixture := newRelocationLegacyProofFixture(t)
			projection, err := readWorkLogProjection(fixture.moved)
			if err != nil {
				t.Fatal(err)
			}
			run, path, err := openWorkLogRun(fixture.home, fixture.claim.EffortID, fixture.claim.RunID, false)
			if err != nil {
				t.Fatal(err)
			}
			directory, err := openPrivateChild(run, "relocations", true)
			if err != nil {
				t.Fatal(err)
			}
			if err := directory.Close(); err != nil {
				t.Fatal(err)
			}
			if err := run.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "origin changes after completed resolution" {
				intent, _, err := appendRelocationIntentForRepository(fixture.home, fixture.claim, fixture.claim.Worktree, fixture.moved, "repository", fixture.entry.HeadSHA, fixture.claim.Repository, fixture.entry.Repository, fixture.newRemote, relocationPlacementRecord{}, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				if _, _, err := appendRelocationReceipt(fixture.home, fixture.claim, intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			hit := false
			observe := func(phase workLogProjectionBoundary, _ *os.File) {
				if phase != workLogClaimRelocationResolved {
					return
				}
				hit = true
				if kind == "journal changes after resolution" {
					projectionNextWrite(t, filepath.Join(path, "relocations", relocationIntentName(fixture.claim.ClaimID, "changed")), []byte("{bad"))
				} else {
					gitTest(t, fixture.moved, "remote", "set-url", "origin", fixture.oldRemote)
				}
			}
			err = corroborateClaimAtPathObserved(fixture.home, fixture.moved, fixture.entry.HeadSHA, projection, fixture.claim, observe)
			if !hit || err == nil {
				t.Fatalf("late corroboration=%v hit=%v", err, hit)
			}
			want := "decode relocation journal"
			if kind == "origin changes after completed resolution" {
				want = "origin does not identify"
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("wrong corroboration phase=%v want=%q", err, want)
			}
			if got := gitTestOutput(t, fixture.moved, "rev-parse", "HEAD"); got != fixture.entry.HeadSHA {
				t.Fatalf("head changed=%s", got)
			}
		})
	}
}

//nolint:paralleltest // Native Git fixture mutates process environment.
func TestE2EWorkLogProjectionNextLegacyRecordRejectsMissingClaimProjection(t *testing.T) {
	fixture := newRelocationLegacyProofFixture(t)
	path := filepath.Join(fixture.moved, workLogProjectionDirectory, workLogProjectionName)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := recordLegacyRepositoryRelocationForCleanup(context.Background(), fixture.home, fixture.projectsRoot, fixture.entry, nil); err == nil || !strings.Contains(err.Error(), "not an eligible legacy repository relocation") {
		t.Fatalf("missing projection admission=%v", err)
	}
}
