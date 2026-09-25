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
// branches of all 15 task-9 PR-4 internal/orchestrate call sites. Each
// production function's happy path is already exercised by its existing
// higher-level Acknowledge*/Persist*/Correct* tests elsewhere in this
// package; reaching a create, chmod, write, sync, close, rename/link, or
// dir-sync failure deterministically needs the injector.
//
// Category A is the CreateTemp/ChmodFile/Write/Sync/Close/Rename shape.
// Category B is CreateTemp/ChmodFile/Write/Sync/Close/LinkPath, optionally
// followed by a directory Sync.

// runPR4CategoryAFailureTests runs the standard 6 failure-injection
// subtests for one Category A call, given a fresh per-subtest *testing.T
// to build an isolated fixture from.
func runPR4CategoryAFailureTests(t *testing.T, makeCall func(t *testing.T) func(*filewrite.Injector) error) {
	t.Helper()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			call := makeCall(t)
			inj := &filewrite.Injector{Step: step, Err: errBoomPR4}
			if err := call(inj); !errors.Is(err, errBoomPR4) {
				t.Fatalf("error = %v, want errBoomPR4", err)
			}
		})
	}
}

// runPR4CategoryBFailureTests runs the standard failure-injection subtests
// for one Category B call (CreateTemp/ChmodFile/Write/Sync/Close/LinkPath),
// adding a StepDirSync subtest when the call also syncs its directory
// after publishing.
func runPR4CategoryBFailureTests(t *testing.T, makeCall func(t *testing.T) func(*filewrite.Injector) error, dirSync bool) {
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
			call := makeCall(t)
			inj := &filewrite.Injector{Step: step, Err: errBoomPR4}
			if err := call(inj); !errors.Is(err, errBoomPR4) {
				t.Fatalf("error = %v, want errBoomPR4", err)
			}
		})
	}
}

// --- persistWorktreeMergeReceipt (worktree_merge.go) ---

func TestPersistWorktreeMergeReceiptInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "receipt.json")
		return func(inj *filewrite.Injector) error {
			return persistWorktreeMergeReceiptInjected(WorktreeMergeReceipt{ReceiptPath: path}, inj)
		}
	})
}

// --- extractWorktreeMergeArchive (worktree_merge.go) ---

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
	runPR4CategoryAFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "rebatch.json")
		return func(inj *filewrite.Injector) error {
			return persistPreparedWorktreeMergeRebatchInjected(path, WorktreeMergePreparedRebatch{}, inj)
		}
	})
}

// --- persistLandedFailureAcknowledgement (worktree_merge_ack.go) ---

func TestPersistLandedFailureAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "landed-failure-ack.json")
		return func(inj *filewrite.Injector) error {
			return persistLandedFailureAcknowledgementInjected(path, WorktreeMergeLandedFailureAcknowledgement{}, inj)
		}
	})
}

// --- persistValidationFailureSupersession (worktree_merge_ack.go) ---

func TestPersistValidationFailureSupersessionInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "validation-failure-supersession.json")
		return func(inj *filewrite.Injector) error {
			return persistValidationFailureSupersessionInjected(path, WorktreeMergeValidationFailureSupersession{}, inj)
		}
	})
}

// --- persistRetiredPublicationAcknowledgement (worktree_merge_retired_publication.go) ---

func TestPersistRetiredPublicationAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "retired-publication-ack.json")
		return func(inj *filewrite.Injector) error {
			return persistRetiredPublicationAcknowledgementInjected(path, WorktreeMergeRetiredPublicationAcknowledgement{}, inj)
		}
	})
}

// --- persistStrandedLandingAcknowledgement (worktree_merge_stranded.go) ---

func TestPersistStrandedLandingAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "stranded-landing-ack.json")
		return func(inj *filewrite.Injector) error {
			return persistStrandedLandingAcknowledgementInjected(path, WorktreeMergeStrandedLandingAcknowledgement{}, inj)
		}
	})
}

// --- persistUnpublishedValidationFailureAcknowledgement (worktree_merge_unpublished_validation_failure.go) ---

func TestPersistUnpublishedValidationFailureAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryAFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "unpublished-validation-failure-ack.json")
		return func(inj *filewrite.Injector) error {
			return persistUnpublishedValidationFailureAcknowledgementInjected(path, WorktreeMergeUnpublishedValidationFailureAcknowledgement{}, inj)
		}
	})
}

// --- persistReceiptCollisionAcknowledgement (worktree_merge_ack.go) ---

func TestPersistReceiptCollisionAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "receipt-collision-ack.json")
		return func(inj *filewrite.Injector) error {
			return persistReceiptCollisionAcknowledgementInjected(path, WorktreeMergeReceiptCollisionAcknowledgement{}, inj)
		}
	}, false)
}

// --- persistConflictCandidateAdvance (worktree_merge_ack.go) ---

func TestPersistConflictCandidateAdvanceInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "conflict-candidate-advance.json")
		return func(inj *filewrite.Injector) error {
			return persistConflictCandidateAdvanceInjected(path, WorktreeMergeConflictCandidateAdvance{}, inj)
		}
	}, true)
}

// --- persistLegacyValidationFailureIdentity (worktree_merge_ack.go) ---

func TestPersistLegacyValidationFailureIdentityInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "legacy-validation-failure-identity.json")
		return func(inj *filewrite.Injector) error {
			return persistLegacyValidationFailureIdentityInjected(path, WorktreeMergeLegacyValidationFailureIdentity{}, inj)
		}
	}, false)
}

// --- persistLegacyConflictIdentity (worktree_merge_ack.go) ---

func TestPersistLegacyConflictIdentityInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "legacy-conflict-identity.json")
		return func(inj *filewrite.Injector) error {
			return persistLegacyConflictIdentityInjected(path, WorktreeMergeLegacyConflictIdentity{}, inj)
		}
	}, false)
}

// --- persistMissingCleanupAcknowledgement (worktree_merge_ack.go) ---

func TestPersistMissingCleanupAcknowledgementInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "missing-cleanup-ack.json")
		return func(inj *filewrite.Injector) error {
			return persistMissingCleanupAcknowledgementInjected(path, WorktreeMergeMissingCleanupAcknowledgement{}, inj)
		}
	}, false)
}

// --- persistSelfSupersessionCorrection (worktree_merge_ack.go) ---

func TestPersistSelfSupersessionCorrectionInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	runPR4CategoryBFailureTests(t, func(t *testing.T) func(*filewrite.Injector) error {
		path := filepath.Join(t.TempDir(), "self-supersession-correction.json")
		return func(inj *filewrite.Injector) error {
			return persistSelfSupersessionCorrectionInjected(path, WorktreeMergeSelfSupersessionCorrection{}, inj)
		}
	}, false)
}

// --- persistPublishedCandidateAdoption (worktree_merge_adopt_published.go) ---
//
// TestPersistPublishedCandidateAdoptionFailsClosedOnExclusivePublish (in
// worktree_merge_adopt_published_test.go) already covers its StepLink
// failure branch with a real *filewrite.Injector; this adds the remaining
// steps.

func TestPersistPublishedCandidateAdoptionInjectedHonoursRemainingInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepDirSync,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "published-candidate-adoption.json")
			inj := &filewrite.Injector{Step: step, Err: errBoomPR4}
			if err := persistPublishedCandidateAdoptionInjected(path, WorktreeMergePublishedCandidateAdoption{}, inj); !errors.Is(err, errBoomPR4) {
				t.Fatalf("persistPublishedCandidateAdoptionInjected error = %v, want errBoomPR4", err)
			}
		})
	}
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
