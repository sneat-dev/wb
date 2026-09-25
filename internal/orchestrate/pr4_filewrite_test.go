package orchestrate

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR4 is task-9 PR-4's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR4 = errors.New("pr4 boom")

// The following tests exercise the filewrite.Injector-reachable error
// branches of all 15 task-9 PR-4 internal/orchestrate call sites, plus a
// leftover-temp-file check and a file-mode check on every site -- applying
// review-756's B1/B2 lessons (untested temp cleanup, unasserted file mode)
// up front rather than waiting for a review round to find the same gap
// again. Each production function's happy path (beyond the mode check
// below) is already exercised by its existing higher-level
// Acknowledge*/Persist*/Correct* tests elsewhere in this package; reaching
// a create, chmod, write, sync, close, rename/link, or dir-sync failure
// deterministically needs the injector.
//
// Category A is the CreateTemp/ChmodFile/Write/Sync/Close/Rename shape.
// Category B is CreateTemp/ChmodFile/Write/Sync/Close/LinkPath, optionally
// followed by a directory Sync. Every site in both categories writes its
// temp file at 0600 and publishes under the same directory it staged in.

// assertNoLeftoverPR4TempFile globs dir for pattern and fails the test if
// a temp file the failed write should have cleaned up still exists.
func assertNoLeftoverPR4TempFile(t *testing.T, dir, pattern string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
	}
}

// runPR4CategoryAFailureTests runs the standard 6 failure-injection
// subtests for one Category A call, given a fresh per-subtest *testing.T
// to build an isolated fixture from, and asserts no leftover temp file
// (matching tempGlob, resolved under the fixture's own directory) survives
// any of them.
func runPR4CategoryAFailureTests(t *testing.T, tempGlob string, makeCall func(t *testing.T) (string, func(*filewrite.Injector) error)) {
	t.Helper()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir, call := makeCall(t)
			inj := &filewrite.Injector{Step: step, Err: errBoomPR4}
			if err := call(inj); !errors.Is(err, errBoomPR4) {
				t.Fatalf("error = %v, want errBoomPR4", err)
			}
			assertNoLeftoverPR4TempFile(t, dir, tempGlob)
		})
	}
}

// runPR4CategoryBFailureTests runs the standard failure-injection subtests
// for one Category B call (CreateTemp/ChmodFile/Write/Sync/Close/LinkPath),
// adding a StepDirSync subtest when the call also syncs its directory
// after publishing, and asserts no leftover temp file survives any of
// them.
func runPR4CategoryBFailureTests(t *testing.T, tempGlob string, makeCall func(t *testing.T) (string, func(*filewrite.Injector) error), dirSync bool) {
	t.Helper()
	steps := []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepLink,
	}
	if dirSync {
		steps = append(steps, filewrite.StepDirSync)
	}
	for _, step := range steps {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir, call := makeCall(t)
			inj := &filewrite.Injector{Step: step, Err: errBoomPR4}
			if err := call(inj); !errors.Is(err, errBoomPR4) {
				t.Fatalf("error = %v, want errBoomPR4", err)
			}
			assertNoLeftoverPR4TempFile(t, dir, tempGlob)
		})
	}
}

// assertPR4PublishedMode0600 asserts path was published at 0600 -- the
// mode every task-9 PR-4 site's ChmodFile call asserts, review-756 B2's
// lesson applied up front.
func assertPR4PublishedMode0600(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("published file mode = %o, want 0600", perm)
	}
}

// --- persistWorktreeMergeReceipt (worktree_merge.go) ---

func TestPersistWorktreeMergeReceiptInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, ".merge-receipt-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "receipt.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistWorktreeMergeReceiptInjected(WorktreeMergeReceipt{ReceiptPath: path}, inj)
		}
	})
}

func TestPersistWorktreeMergeReceiptPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt.json")
	if err := persistWorktreeMergeReceipt(WorktreeMergeReceipt{ReceiptPath: path}); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- extractWorktreeMergeArchive (worktree_merge.go) ---
//
// No fixed mode to assert: each entry publishes at the archived
// header.Mode, not a constant this package chooses, and no temp file is
// staged (CreateOrTruncatePath writes the destination path directly), so
// neither of the other two lessons applies here.

func TestExtractWorktreeMergeArchiveInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{filewrite.StepOpenOrCreate, filewrite.StepClose} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			contents := orchCovTar(t, func(w *tar.Writer) {
				body := []byte("hi")
				if err := w.WriteHeader(&tar.Header{Name: "file.txt", Typeflag: tar.TypeReg, Mode: 0o600, Size: int64(len(body))}); err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write(body); err != nil {
					t.Fatal(err)
				}
			})
			archivePath := orchCovWriteArchive(t, contents)
			destination := t.TempDir()
			inj := &filewrite.Injector{Step: step, Err: errBoomPR4}
			if err := extractWorktreeMergeArchiveInjected(archivePath, destination, inj); !errors.Is(err, errBoomPR4) {
				t.Fatalf("extractWorktreeMergeArchiveInjected error = %v, want errBoomPR4", err)
			}
		})
	}
}

// --- persistPreparedWorktreeMergeRebatch (worktree_merge_ack.go) ---

func TestPersistPreparedWorktreeMergeRebatchInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, ".prepared-rebatch-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "rebatch.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistPreparedWorktreeMergeRebatchInjected(path, WorktreeMergePreparedRebatch{}, inj)
		}
	})
}

func TestPersistPreparedWorktreeMergeRebatchPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rebatch.json")
	if err := persistPreparedWorktreeMergeRebatchInjected(path, WorktreeMergePreparedRebatch{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistLandedFailureAcknowledgement (worktree_merge_ack.go) ---

func TestPersistLandedFailureAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, ".landed-validation-failed-ack-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "landed-failure-ack.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistLandedFailureAcknowledgementInjected(path, WorktreeMergeLandedFailureAcknowledgement{}, inj)
		}
	})
}

func TestPersistLandedFailureAcknowledgementPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "landed-failure-ack.json")
	if err := persistLandedFailureAcknowledgementInjected(path, WorktreeMergeLandedFailureAcknowledgement{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistValidationFailureSupersession (worktree_merge_ack.go) ---

func TestPersistValidationFailureSupersessionInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, ".validation-failed-supersession-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "validation-failure-supersession.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistValidationFailureSupersessionInjected(path, WorktreeMergeValidationFailureSupersession{}, inj)
		}
	})
}

func TestPersistValidationFailureSupersessionPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "validation-failure-supersession.json")
	if err := persistValidationFailureSupersessionInjected(path, WorktreeMergeValidationFailureSupersession{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistRetiredPublicationAcknowledgement (worktree_merge_retired_publication.go) ---

func TestPersistRetiredPublicationAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, ".retired-publication-ack-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "retired-publication-ack.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistRetiredPublicationAcknowledgementInjected(path, WorktreeMergeRetiredPublicationAcknowledgement{}, inj)
		}
	})
}

func TestPersistRetiredPublicationAcknowledgementPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "retired-publication-ack.json")
	if err := persistRetiredPublicationAcknowledgementInjected(path, WorktreeMergeRetiredPublicationAcknowledgement{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistStrandedLandingAcknowledgement (worktree_merge_stranded.go) ---

func TestPersistStrandedLandingAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, ".stranded-landing-ack-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "stranded-landing-ack.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistStrandedLandingAcknowledgementInjected(path, WorktreeMergeStrandedLandingAcknowledgement{}, inj)
		}
	})
}

func TestPersistStrandedLandingAcknowledgementPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "stranded-landing-ack.json")
	if err := persistStrandedLandingAcknowledgementInjected(path, WorktreeMergeStrandedLandingAcknowledgement{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistUnpublishedValidationFailureAcknowledgement (worktree_merge_unpublished_validation_failure.go) ---

func TestPersistUnpublishedValidationFailureAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, ".unpublished-validation-failure-ack-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "unpublished-validation-failure-ack.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistUnpublishedValidationFailureAcknowledgementInjected(path, WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, inj)
		}
	})
}

func TestPersistUnpublishedValidationFailureAcknowledgementPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "unpublished-validation-failure-ack.json")
	if err := persistUnpublishedValidationFailureAcknowledgementInjected(path, WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistReceiptCollisionAcknowledgement (worktree_merge_ack.go) ---

func TestPersistReceiptCollisionAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, ".receipt-collision-ack-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "receipt-collision-ack.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistReceiptCollisionAcknowledgementInjected(path, WorktreeMergeReceiptCollisionAcknowledgement{}, inj)
		}
	}, false)
}

func TestPersistReceiptCollisionAcknowledgementPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "receipt-collision-ack.json")
	if err := persistReceiptCollisionAcknowledgementInjected(path, WorktreeMergeReceiptCollisionAcknowledgement{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistConflictCandidateAdvance (worktree_merge_ack.go) ---

func TestPersistConflictCandidateAdvanceInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, ".conflict-candidate-advance-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "conflict-candidate-advance.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistConflictCandidateAdvanceInjected(path, WorktreeMergeConflictCandidateAdvance{}, inj)
		}
	}, true)
}

func TestPersistConflictCandidateAdvancePublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "conflict-candidate-advance.json")
	if err := persistConflictCandidateAdvanceInjected(path, WorktreeMergeConflictCandidateAdvance{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistLegacyValidationFailureIdentity (worktree_merge_ack.go) ---

func TestPersistLegacyValidationFailureIdentityInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, ".legacy-validation-failed-identity-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "legacy-validation-failure-identity.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistLegacyValidationFailureIdentityInjected(path, WorktreeMergeLegacyValidationFailureIdentity{}, inj)
		}
	}, false)
}

func TestPersistLegacyValidationFailureIdentityPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy-validation-failure-identity.json")
	if err := persistLegacyValidationFailureIdentityInjected(path, WorktreeMergeLegacyValidationFailureIdentity{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistLegacyConflictIdentity (worktree_merge_ack.go) ---

func TestPersistLegacyConflictIdentityInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, ".legacy-conflict-identity-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "legacy-conflict-identity.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistLegacyConflictIdentityInjected(path, WorktreeMergeLegacyConflictIdentity{}, inj)
		}
	}, false)
}

func TestPersistLegacyConflictIdentityPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "legacy-conflict-identity.json")
	if err := persistLegacyConflictIdentityInjected(path, WorktreeMergeLegacyConflictIdentity{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistMissingCleanupAcknowledgement (worktree_merge_ack.go) ---

func TestPersistMissingCleanupAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, ".missing-cleanup-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "missing-cleanup-ack.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistMissingCleanupAcknowledgementInjected(path, WorktreeMergeMissingCleanupAcknowledgement{}, inj)
		}
	}, false)
}

func TestPersistMissingCleanupAcknowledgementPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing-cleanup-ack.json")
	if err := persistMissingCleanupAcknowledgementInjected(path, WorktreeMergeMissingCleanupAcknowledgement{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistSelfSupersessionCorrection (worktree_merge_ack.go) ---

func TestPersistSelfSupersessionCorrectionInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, ".validation-failed-self-supersession-*.tmp", func(t *testing.T) (string, func(*filewrite.Injector) error) {
		dir := t.TempDir()
		path := filepath.Join(dir, "self-supersession-correction.json")
		return dir, func(inj *filewrite.Injector) error {
			return persistSelfSupersessionCorrectionInjected(path, WorktreeMergeSelfSupersessionCorrection{}, inj)
		}
	}, false)
}

func TestPersistSelfSupersessionCorrectionPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "self-supersession-correction.json")
	if err := persistSelfSupersessionCorrectionInjected(path, WorktreeMergeSelfSupersessionCorrection{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- persistPublishedCandidateAdoption (worktree_merge_adopt_published.go) ---
//
// TestPersistPublishedCandidateAdoptionFailsClosedOnExclusivePublish (in
// worktree_merge_adopt_published_test.go) already covers its StepLink
// failure branch with a real *filewrite.Injector; this adds the remaining
// steps plus the leftover-temp and mode checks.

func TestPersistPublishedCandidateAdoptionInjectedHonoursRemainingInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepDirSync,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "published-candidate-adoption.json")
			inj := &filewrite.Injector{Step: step, Err: errBoomPR4}
			if err := persistPublishedCandidateAdoptionInjected(path, WorktreeMergePublishedCandidateAdoption{}, inj); !errors.Is(err, errBoomPR4) {
				t.Fatalf("persistPublishedCandidateAdoptionInjected error = %v, want errBoomPR4", err)
			}
			assertNoLeftoverPR4TempFile(t, dir, ".published-candidate-adoption-*.tmp")
		})
	}
}

func TestPersistPublishedCandidateAdoptionPublishesAt0600(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "published-candidate-adoption.json")
	if err := persistPublishedCandidateAdoptionInjected(path, WorktreeMergePublishedCandidateAdoption{}, nil); err != nil {
		t.Fatal(err)
	}
	assertPR4PublishedMode0600(t, path)
}

// --- filewrite.LinkPath's real race, exercised via Injector.Hook rather
// than a package-var reassignment (review-756 N1/task-9 PR-4) ---

func TestPersistSelfSupersessionCorrectionInjectedHookCreatesARealRaceAtLinkPath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "self-supersession-correction.json")
	competing := []byte(`{"competing":true}` + "\n")
	hookRan := false
	inj := &filewrite.Injector{
		Step: filewrite.StepLink,
		Hook: func() {
			hookRan = true
			if err := os.WriteFile(path, competing, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	err := persistSelfSupersessionCorrectionInjected(path, WorktreeMergeSelfSupersessionCorrection{}, inj)
	if !hookRan {
		t.Fatal("Hook did not run")
	}
	if err == nil {
		t.Fatal("persistSelfSupersessionCorrectionInjected = nil, want a real os.Link EEXIST error from the hook's competing write")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(competing) {
		t.Fatalf("published content = %q, want the competing write to have won: %q", got, competing)
	}
}
