package worktrees

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkLogProjectionNextReservationEnumerationRetainsClosedHandleCauses(t *testing.T) {
	t.Parallel()
	for _, phase := range []workLogProjectionBoundary{workLogReservationEffortsRead, workLogReservationRunsRead} {
		t.Run(string(phase), func(t *testing.T) {
			t.Parallel()
			home, _ := newPreApplyReservationFixture(t)
			var nativeCause error
			candidates, err := findPreApplyRenameReservationsObserved(home, "destination", projectionNextCloseEnumeration(t, phase, &nativeCause))
			if candidates != nil || nativeCause == nil || !errors.Is(err, nativeCause) {
				t.Fatalf("enumeration refusal=%+v %v", candidates, err)
			}
			candidates, err = findPreApplyRenameReservations(home, "destination")
			if err != nil || len(candidates) != 1 {
				t.Fatalf("evidence changed=%+v %v", candidates, err)
			}
		})
	}
}
func TestWorkLogProjectionNextEvidenceRefusesUnreadableClaimsDirectory(t *testing.T) {
	t.Parallel()
	_, run, _ := projectionNextRun(t)
	claims, err := openPrivateChild(run, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := claims.Close(); err != nil {
		t.Fatal(err)
	}
	if !hasWorkLogClaimsOrTerminalsObserved(run, projectionNextClose(t, workLogEvidenceDirectoryRead)) {
		t.Fatal("unreadable evidence was considered empty")
	}
	missingHome, worktree := projectionNextTemp(t), projectionNextTemp(t)
	if err := corroborateWorkLogProjection(missingHome, worktree, "", workLogProjection{EffortID: "task", RunID: "run"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing private run cause=%v", err)
	}
	for _, namespace := range []string{missingHome, worktree} {
		if entries, err := os.ReadDir(namespace); err != nil || len(entries) != 0 {
			t.Fatalf("missing-run refusal changed namespace %s: %v %v", namespace, entries, err)
		}
	}
	if hasWorkLogClaimsOrTerminals(run) {
		t.Fatal("empty original namespace changed")
	}
}
func TestWorkLogProjectionNextLegacyMigrationKeepsPublishedClaimOnReadRefusal(t *testing.T) {
	t.Parallel()
	home, run, path := projectionNextRun(t)
	legacy := legacyWorkLogClaim{Version: 1, EffortID: "task", RunID: "run", Task: "task", Repository: "acme/app", Worktree: filepath.Join(projectionNextTemp(t), "checkout"), Branch: "wb/task", Base: "main", BaseSHA: strings.Repeat("a", 40), RecordedAt: time.Unix(1700000000, 0).UTC()}
	if err := writeJSONImmutableAt(run, "claim.json", legacy, false); err != nil {
		t.Fatal(err)
	}
	var nativeCause error
	if err := migrateLegacySingletonClaimObserved(run, path, home, "task", "run", projectionNextCloseEnumeration(t, workLogMigrationClaimsRead, &nativeCause)); nativeCause == nil || !errors.Is(err, nativeCause) {
		t.Fatalf("claim enumeration cause=%v", err)
	}
	names, err := os.ReadDir(filepath.Join(path, "claims"))
	if err != nil || len(names) != 1 {
		t.Fatalf("published claim evidence=%v %v", names, err)
	}
	if _, err := os.Lstat(filepath.Join(path, "legacy-claim-migration.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("false migration receipt=%v", err)
	}
	if err := migrateLegacySingletonClaim(run, path, home, "task", "run"); err != nil {
		t.Fatalf("retry=%v", err)
	}
}
func TestWorkLogProjectionNextDirectoryRefusalsUseOwnedDescriptors(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"chmod closed", "identity closed"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			worktree := projectionNextTemp(t)
			hit := false
			observe := func(phase workLogProjectionBoundary, file *os.File) {
				if phase != workLogProjectionDirectoryOpened {
					return
				}
				hit = true
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			directory, err := openWorkLogProjectionDirectoryObserved(worktree, kind == "chmod closed", observe)
			if kind == "identity closed" { // The observation needs an existing readable namespace.
				if hit || directory != nil || !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing namespace=%v %v hit=%v", directory, err, hit)
				}
				original, createErr := openWorkLogProjectionDirectory(worktree, true)
				if createErr != nil {
					t.Fatal(createErr)
				}
				if err := original.Close(); err != nil {
					t.Fatal(err)
				}
				directory, err = openWorkLogProjectionDirectoryObserved(worktree, false, observe)
			}
			if !hit || directory != nil || err == nil {
				t.Fatalf("owned directory refusal=%v %v hit=%v", directory, err, hit)
			}
			if kind == "identity closed" && !strings.Contains(err.Error(), "directory path changed") {
				t.Fatalf("identity diagnostic=%v", err)
			}
		})
	}
}
func TestWorkLogProjectionNextRemovalRetainsNativeRefusals(t *testing.T) {
	t.Parallel()
	for _, phase := range []workLogProjectionBoundary{workLogProjectionBeforeRemove, workLogProjectionBeforeSync, workLogProjectionBeforeRootReopen} {
		t.Run(string(phase), func(t *testing.T) {
			t.Parallel()
			worktree := projectionNextTemp(t)
			directory, err := openWorkLogProjectionDirectory(worktree, true)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(worktree, workLogProjectionDirectory, workLogProjectionName)
			projectionNextWrite(t, path, []byte("private evidence"))
			if err := directory.Close(); err != nil {
				t.Fatal(err)
			}
			hit := false
			observe := func(got workLogProjectionBoundary, file *os.File) {
				if got != phase {
					return
				}
				hit = true
				if phase == workLogProjectionBeforeRootReopen {
					if err := os.Rename(worktree, worktree+".held"); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := file.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = removeWorkLogProjectionObserved(worktree, observe)
			if !hit || err == nil {
				t.Fatalf("remove refusal=%v hit=%v", err, hit)
			}
			if phase == workLogProjectionBeforeRemove {
				if raw, err := os.ReadFile(path); err != nil || string(raw) != "private evidence" {
					t.Fatalf("evidence=%q %v", raw, err)
				}
			} else {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("removed projection=%v", err)
				}
			}
			if phase == workLogProjectionBeforeSync && !errors.Is(err, os.ErrClosed) {
				t.Fatalf("sync cause=%v", err)
			}
		})
	}
}
