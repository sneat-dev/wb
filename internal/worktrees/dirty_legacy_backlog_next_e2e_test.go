//go:build e2e

package worktrees

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

//nolint:paralleltest // Existing Git fixture pins process-wide Git and WB environment.
func TestE2EDirtyLegacyBacklogOwnedReadFailures(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	root := filepath.Join(t.TempDir(), "worktrees")
	entry := ListResult{Task: "task", Repository: "acme/app", CanonicalDir: fixture.canonical, WorktreesRoot: root, WorktreeDir: filepath.Join(root, "task", "acme", "app"), Branch: "feature/task", Base: "main", HeadSHA: head}
	record := newLifecycleBacklogRecord(fixture.projectsRoot, entry, string(AbortDiscarded))
	if err := persistLifecycleBacklog(fixture.home, &record, lifecycleStageComplete); err != nil {
		t.Fatal(err)
	}
	recordPath := lifecycleBacklogPath(fixture.home, record.ID)
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"discarded lookup", "resumable load"} {
		//nolint:paralleltest // Both operations share this private native fixture's process environment.
		t.Run(kind, func(t *testing.T) {
			var owned *os.File
			var cause error
			read := func(directory *os.File) ([]os.DirEntry, error) {
				owned = directory
				if err := directory.Close(); err != nil {
					t.Fatal(err)
				}
				_, control := readLifecycleBacklogEntries(directory)
				if control == nil {
					t.Fatal("closed directory control unexpectedly succeeded")
				}
				cause = control
				var pathErr *os.PathError
				if errors.As(control, &pathErr) {
					cause = pathErr.Err
				}
				return readLifecycleBacklogEntries(directory)
			}
			var err error
			if kind == "discarded lookup" {
				var proof *DiscardedLifecycleBacklogProof
				proof, err = findDiscardedLifecycleBacklogProofWithRead(t.Context(), fixture.projectsRoot, entry.Repository, entry.Base, entry.Task, entry.WorktreeDir, entry.Branch, entry.HeadSHA, read)
				if proof != nil {
					t.Fatalf("read refusal returned proof: %+v", proof)
				}
			} else {
				records, quarantine, loadErr := loadResumableLifecycleBacklogWithRead(t.Context(), fixture.home, fixture.projectsRoot, []string{root}, map[string]bool{"task": true}, "", string(AbortDiscarded), read)
				err = loadErr
				if records != nil || quarantine != nil {
					t.Fatalf("read refusal returned inventory: %+v %+v", records, quarantine)
				}
			}
			if owned == nil || cause == nil || !errors.Is(err, cause) {
				t.Fatalf("owned read refusal: %v actual control=%v", err, cause)
			}
			if _, statErr := owned.Stat(); !errors.Is(statErr, os.ErrClosed) {
				t.Fatalf("original owned file not closed: %v", statErr)
			}
			after, readErr := os.ReadFile(recordPath)
			if readErr != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("durable receipt changed: %q %v", after, readErr)
			}
		})
	}
}

func TestE2EDirtyLegacyBacklogRelativeFailureRetainsNativeCause(t *testing.T) {
	t.Parallel()
	_, _, record := wtLogCovBacklogRecord(t, "removed")
	if err := validateLifecycleBacklog(record); err != nil {
		t.Fatalf("valid native default prerequisite: %v", err)
	}
	_, cause := filepath.Rel("relative-control", record.WorktreeDir)
	if cause == nil {
		t.Fatal("native mixed relative/absolute Rel did not refuse")
	}
	observed := false
	// This is an injected boundary carrying a captured native Rel cause, not a Windows runtime claim.
	err := validateLifecycleBacklogWithRel(record, func(root, path string) (string, error) {
		if root != filepath.Clean(record.WorktreesRoot) || path != filepath.Clean(record.WorktreeDir) {
			t.Fatalf("relative query changed: %s %s", root, path)
		}
		observed = true
		return "", cause
	})
	if !observed || !errors.Is(err, cause) || !strings.HasPrefix(err.Error(), "resolve lifecycle backlog worktree:") {
		t.Fatalf("relative refusal: %v observed=%v", err, observed)
	}
}

//nolint:paralleltest // Existing Git fixture pins process-wide Git and WB environment.
func TestE2EDirtyLegacyVacantBacklogRejectsChangedOwnedCanonical(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	root := filepath.Join(t.TempDir(), "worktrees")
	entry := ListResult{Task: "task", Repository: "acme/app", CanonicalDir: fixture.canonical, WorktreesRoot: root, WorktreeDir: filepath.Join(root, "task", "acme", "app"), Branch: "feature/task", Base: "main", HeadSHA: head}
	record := newLifecycleBacklogRecord(fixture.projectsRoot, entry, "removed")
	if err := persistLifecycleBacklog(fixture.home, &record, lifecycleStageSealed); err != nil {
		t.Fatal(err)
	}
	recordPath := lifecycleBacklogPath(fixture.home, record.ID)
	before, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	_, lockCause := os.Open(entry.WorktreeDir)
	if lockCause == nil {
		t.Fatal("native absent-directory refusal prerequisite unexpectedly succeeded")
	}
	// The incoming refusal is a captured native absent-directory cause, not a reproduced lock-acquisition race.
	observed := false
	old := fixture.canonical + "-retained"
	err = completeVacantLifecycleBacklogObserved(context.Background(), fixture.home, &record, lockCause, func(canonical *canonicalRepository) {
		if err := canonical.validate(); err != nil {
			t.Fatalf("owned canonical prerequisite: %v", err)
		}
		if err := os.Rename(fixture.canonical, old); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(fixture.canonical, 0o700); err != nil {
			t.Fatal(err)
		}
		observed = true
		if err := canonical.validate(); err == nil {
			t.Fatal("actual replacement did not change held canonical identity")
		}
	})
	if !observed || err != lockCause {
		t.Fatalf("vacant refusal: %v observed=%v", err, observed)
	}
	after, readErr := os.ReadFile(recordPath)
	if readErr != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("receipt changed: %q %v", after, readErr)
	}
	if after := gitTestOutput(t, old, "rev-parse", "HEAD"); after != head {
		t.Fatalf("retained HEAD changed: %s", after)
	}
}
