package orchestrate

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

type mergeAcknowledgementCase struct {
	name, temporaryPattern string
	replaces               bool
	write                  func(string, time.Time, *filewrite.Injector) error
	contents               func(time.Time) ([]byte, error)
}

func acknowledgementCase[T mergeAcknowledgementDocument](name, pattern string, replaces bool, document func(time.Time) T, write func(string, T, *filewrite.Injector) error) mergeAcknowledgementCase {
	return mergeAcknowledgementCase{name: name, temporaryPattern: pattern, replaces: replaces,
		write: func(path string, at time.Time, inj *filewrite.Injector) error {
			return write(path, document(at), inj)
		}, contents: func(at time.Time) ([]byte, error) {
			contents, err := json.MarshalIndent(document(at), "", "  ")
			return append(contents, '\n'), err
		}}
}

// These cases exercise serialization/publication, not receipt authorization.
// Every write calls the actual typed acknowledgement producer.
func mergeAcknowledgementCases() []mergeAcknowledgementCase {
	return []mergeAcknowledgementCase{
		acknowledgementCase("receipt collision", ".receipt-collision-ack-*.tmp", false, func(at time.Time) WorktreeMergeReceiptCollisionAcknowledgement {
			return WorktreeMergeReceiptCollisionAcknowledgement{ID: "audit", RecordedAt: at}
		}, persistReceiptCollisionAcknowledgementInjected),
		acknowledgementCase("prepared rebatch", ".prepared-rebatch-*.tmp", true, func(at time.Time) WorktreeMergePreparedRebatch {
			return WorktreeMergePreparedRebatch{ID: "audit", RecordedAt: at}
		}, persistPreparedWorktreeMergeRebatchInjected),
		acknowledgementCase("landed failure", ".landed-validation-failed-ack-*.tmp", true, func(at time.Time) WorktreeMergeLandedFailureAcknowledgement {
			return WorktreeMergeLandedFailureAcknowledgement{ID: "audit", RecordedAt: at}
		}, persistLandedFailureAcknowledgementInjected),
		acknowledgementCase("conflict advance", ".conflict-candidate-advance-*.tmp", false, func(at time.Time) WorktreeMergeConflictCandidateAdvance {
			return WorktreeMergeConflictCandidateAdvance{ID: "audit", RecordedAt: at}
		}, persistConflictCandidateAdvanceInjected),
		acknowledgementCase("legacy validation identity", ".legacy-validation-failed-identity-*.tmp", false, func(at time.Time) WorktreeMergeLegacyValidationFailureIdentity {
			return WorktreeMergeLegacyValidationFailureIdentity{ID: "audit", RecordedAt: at}
		}, persistLegacyValidationFailureIdentityInjected),
		acknowledgementCase("legacy conflict identity", ".legacy-conflict-identity-*.tmp", false, func(at time.Time) WorktreeMergeLegacyConflictIdentity {
			return WorktreeMergeLegacyConflictIdentity{ID: "audit", RecordedAt: at}
		}, persistLegacyConflictIdentityInjected),
		acknowledgementCase("missing cleanup", ".missing-cleanup-*.tmp", false, func(at time.Time) WorktreeMergeMissingCleanupAcknowledgement {
			return WorktreeMergeMissingCleanupAcknowledgement{ID: "audit", RecordedAt: at}
		}, persistMissingCleanupAcknowledgementInjected),
		acknowledgementCase("validation supersession", ".validation-failed-supersession-*.tmp", true, func(at time.Time) WorktreeMergeValidationFailureSupersession {
			return WorktreeMergeValidationFailureSupersession{ID: "audit", RecordedAt: at}
		}, persistValidationFailureSupersessionInjected),
		acknowledgementCase("self-supersession correction", ".validation-failed-self-supersession-*.tmp", false, func(at time.Time) WorktreeMergeSelfSupersessionCorrection {
			return WorktreeMergeSelfSupersessionCorrection{ID: "audit", RecordedAt: at}
		}, persistSelfSupersessionCorrectionInjected),
	}
}

func TestMergeAcknowledgementPersistencePreservesBytesAndCollisionPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range mergeAcknowledgementCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "audit", "record.json")
			at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
			want, err := tc.contents(at)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.write(path, at, nil); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("published bytes = %q, error = %v, want %q", got, err, want)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(filepath.Dir(path))
				if err != nil || info.Mode().Perm() != 0o700 {
					t.Fatalf("private parent mode: info=%v err=%v", info, err)
				}
				assertPR4PublishedMode0600(t, path)
			}
			later := at.Add(time.Minute)
			err = tc.write(path, later, nil)
			if tc.replaces {
				if err != nil {
					t.Fatal(err)
				}
				want, err = tc.contents(later)
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, os.ErrExist) {
				t.Fatalf("existing immutable record error = %v, want os.ErrExist", err)
			}
			got, err = os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("collision policy bytes = %q, error = %v, want %q", got, err, want)
			}
			assertNoLeftoverPR4TempFile(t, filepath.Dir(path), tc.temporaryPattern)
		})
	}
}

func TestMergeAcknowledgementPersistenceRejectsInvalidTimeBeforeFilesystemChanges(t *testing.T) {
	t.Parallel()
	for _, tc := range mergeAcknowledgementCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent := filepath.Join(t.TempDir(), "not-created")
			invalid := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			_, encoderErr := tc.contents(invalid)
			if encoderErr == nil {
				t.Fatal("actual time encoder accepted year10000")
			}
			if err := tc.write(filepath.Join(parent, "record.json"), invalid, nil); err == nil || err.Error() != encoderErr.Error() {
				t.Fatalf("marshal error = %v, want %v", err, encoderErr)
			}
			if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("marshal refusal changed filesystem: %v", err)
			}
		})
	}
}

func TestMergeAcknowledgementPersistenceReturnsActualDirectoryAndShortWriteErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range mergeAcknowledgementCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			blocker := filepath.Join(dir, "regular-file")
			if err := os.WriteFile(blocker, []byte("unchanged"), 0o600); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
			var pathErr *os.PathError
			if err := tc.write(filepath.Join(blocker, "record.json"), at, nil); !errors.As(err, &pathErr) || pathErr.Op != "mkdir" {
				t.Fatalf("actual blocked-directory error = %v, want mkdir PathError", err)
			}
			got, err := os.ReadFile(blocker)
			if err != nil || string(got) != "unchanged" {
				t.Fatalf("blocked directory changed: bytes=%q err=%v", got, err)
			}
			path := filepath.Join(dir, "record.json")
			var short *filewrite.ShortWriteError
			if err := tc.write(path, at, &filewrite.Injector{Step: filewrite.StepShortWrite, ShortBytes: 3}); !errors.As(err, &short) {
				t.Fatalf("short-write error = %v, want ShortWriteError", err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed write published a destination: %v", err)
			}
			assertNoLeftoverPR4TempFile(t, dir, tc.temporaryPattern)
		})
	}
}

func TestConflictAcknowledgementKeepsPublishedRecordOnDirectoryErrors(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"open", "sync"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			temporaryPath, path := filepath.Join(dir, "staged.tmp"), filepath.Join(dir, "audit.json")
			contents := []byte("actual staged record\n")
			if err := os.WriteFile(temporaryPath, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			// Only the failed OS observation is simulated. The destination
			// comes from the real no-replace link before that failure.
			sentinel := errors.New("directory " + stage + " refusal")
			openDirectory := os.Open
			var inj *filewrite.Injector
			if stage == "open" {
				openDirectory = func(got string) (*os.File, error) {
					if got != dir {
						t.Fatalf("opened directory = %q, want %q", got, dir)
					}
					return nil, sentinel
				}
			} else {
				inj = &filewrite.Injector{Step: filewrite.StepDirSync, Err: sentinel}
			}
			if err := publishConflictAcknowledgement(temporaryPath, path, inj, openDirectory); !errors.Is(err, sentinel) {
				t.Fatalf("post-publication error = %v, want sentinel", err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, contents) {
				t.Fatalf("linked evidence lost after %s error: bytes=%q err=%v", stage, got, err)
			}
		})
	}
}
