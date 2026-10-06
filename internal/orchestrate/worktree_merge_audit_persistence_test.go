package orchestrate

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// Static DTOs exercise actual persistence bytes and publication disposition.
// Native recovery witnesses remain responsible for Git, claims and custody.
func remainingMergeAuditCases() []mergeAcknowledgementCase {
	return []mergeAcknowledgementCase{
		acknowledgementCase("retired publication", ".retired-publication-ack-*.tmp", true, func(at time.Time) WorktreeMergeRetiredPublicationAcknowledgement {
			return WorktreeMergeRetiredPublicationAcknowledgement{ID: "audit", RecordedAt: at}
		}, persistRetiredPublicationAcknowledgementInjected),
		acknowledgementCase("stranded landing", ".stranded-landing-ack-*.tmp", true, func(at time.Time) WorktreeMergeStrandedLandingAcknowledgement {
			return WorktreeMergeStrandedLandingAcknowledgement{ID: "audit", RecordedAt: at}
		}, persistStrandedLandingAcknowledgementInjected),
		acknowledgementCase("unpublished validation", ".unpublished-validation-failure-ack-*.tmp", true, func(at time.Time) WorktreeMergeUnpublishedValidationFailureAcknowledgement {
			return WorktreeMergeUnpublishedValidationFailureAcknowledgement{ID: "audit", RecordedAt: at}
		}, persistUnpublishedValidationFailureAcknowledgementInjected),
		acknowledgementCase("published adoption", ".published-candidate-adoption-*.tmp", false, func(at time.Time) WorktreeMergePublishedCandidateAdoption {
			return WorktreeMergePublishedCandidateAdoption{ID: "audit", RecordedAt: at}
		}, persistPublishedCandidateAdoptionInjected),
	}
}

func TestRemainingMergeAuditPersistencePreservesBytesAndPublicationPolicy(t *testing.T) {
	t.Parallel()
	for _, tc := range remainingMergeAuditCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "private-audit", "record.json")
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
				t.Fatalf("published bytes=%q error=%v want=%q", got, err, want)
			}
			if runtime.GOOS != "windows" {
				info, err := os.Stat(filepath.Dir(path))
				if err != nil || info.Mode().Perm() != 0o700 {
					t.Fatalf("directory=%v error=%v", info, err)
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
				t.Fatalf("exclusive publication error=%v", err)
			}
			got, err = os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("publication policy bytes=%q error=%v want=%q", got, err, want)
			}
			assertNoLeftoverPR4TempFile(t, filepath.Dir(path), tc.temporaryPattern)
		})
	}
}

func TestRemainingMergeAuditPersistenceMarshalRefusalPrecedesDirectoryEffects(t *testing.T) {
	t.Parallel()
	for _, tc := range remainingMergeAuditCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			parent := filepath.Join(t.TempDir(), "not-created")
			invalid := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			_, expected := tc.contents(invalid)
			if expected == nil {
				t.Fatal("actual time encoder accepted year10000")
			}
			if err := tc.write(filepath.Join(parent, "record.json"), invalid, nil); err == nil || err.Error() != expected.Error() {
				t.Fatalf("marshal error=%v expected=%v", err, expected)
			}
			if _, err := os.Lstat(parent); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("marshal refusal changed parent: %v", err)
			}
		})
	}
}

func TestRemainingMergeAuditPersistenceKeepsPrimaryErrorsAndCleansStaging(t *testing.T) {
	t.Parallel()
	for _, tc := range remainingMergeAuditCases() {
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
				t.Fatalf("blocked parent error=%v", err)
			}
			got, err := os.ReadFile(blocker)
			if err != nil || string(got) != "unchanged" {
				t.Fatalf("blocker changed bytes=%q error=%v", got, err)
			}
			path := filepath.Join(dir, "record.json")
			var short *filewrite.ShortWriteError
			if err := tc.write(path, at, &filewrite.Injector{Step: filewrite.StepShortWrite, ShortBytes: 3}); !errors.As(err, &short) {
				t.Fatalf("short write error=%v", err)
			}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("short write published destination: %v", err)
			}
			assertNoLeftoverPR4TempFile(t, dir, tc.temporaryPattern)
			sentinel := errors.New("primary write refusal")
			if err := tc.write(path, at, &filewrite.Injector{Step: filewrite.StepWrite, Err: sentinel}); err != sentinel {
				t.Fatalf("primary identity=%v expected=%v", err, sentinel)
			}
			assertNoLeftoverPR4TempFile(t, dir, tc.temporaryPattern)
		})
	}
}

func TestPublishedAdoptionPersistenceKeepsLinkedBytesAfterSyncRefusal(t *testing.T) {
	t.Parallel()
	tc := remainingMergeAuditCases()[3]
	path := filepath.Join(t.TempDir(), "private-audit", "record.json")
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	sentinel := errors.New("parent sync refusal")
	if err := tc.write(path, at, &filewrite.Injector{Step: filewrite.StepDirSync, Err: sentinel}); err != sentinel {
		t.Fatalf("post-link sync error=%v", err)
	}
	want, err := tc.contents(at)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("linked evidence lost bytes=%q error=%v", got, err)
	}
	assertNoLeftoverPR4TempFile(t, filepath.Dir(path), tc.temporaryPattern)
	if err := tc.write(path, at, nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("linked receipt overwritten after error: %v", err)
	}
}
