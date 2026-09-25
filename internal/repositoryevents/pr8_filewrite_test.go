package repositoryevents

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR8 is task-9 PR-8's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally matches
// a different test's error by coincidence.
var errBoomPR8 = errors.New("pr8 boom")

func TestQueuePersistInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepRename,
		filewrite.StepDirSync,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			queue := &Queue{directory: dir}
			item := &job{Schema: 1, Event: repositoryevent.Event{ID: "evt-1"}}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			err := queue.persistInjected(item, inj)
			if !errors.Is(err, errBoomPR8) {
				t.Fatalf("persistInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".job-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			path := filepath.Join(dir, eventFileName(item.Event.ID)+".json")
			if step != filewrite.StepDirSync {
				if len(matches) != 0 {
					t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
				}
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Fatalf("failed write published a visible job file: %v", statErr)
				}
				return
			}
			// The rename already succeeded by the time the directory sync is
			// injected to fail, so the job file is (correctly) already
			// published.
			if _, statErr := os.Stat(path); statErr != nil {
				t.Fatalf("dir-sync failure unexpectedly lost the published job file: %v", statErr)
			}
		})
	}
}

// TestQueuePersistInjectedPublishesAt0600 is review-763's chmod-preset-Hook
// lesson applied to this site: os.CreateTemp already creates its temp file
// at 0600, the same as this site's own final published mode, so a plain
// end-to-end 0600 assertion cannot tell a real chmod call from a deleted
// one.
func TestQueuePersistInjectedPublishesAt0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	queue := &Queue{directory: dir}
	item := &job{Schema: 1, Event: repositoryevent.Event{ID: "evt-1"}}
	hookRan := false
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Hook: func() {
		hookRan = true
		matches, err := filepath.Glob(filepath.Join(dir, ".job-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("locate temporary file before chmod: matches=%v err=%v", matches, err)
		}
		if err := os.Chmod(matches[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	if err := queue.persistInjected(item, inj); err != nil {
		t.Fatal(err)
	}
	if !hookRan {
		t.Fatal("Hook did not run before the real chmod")
	}
	path := filepath.Join(dir, eventFileName(item.Event.ID)+".json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("published job file mode = %o, want 0600", perm)
	}
}

func TestQueuePersistInjectedRunsBeforePersistFirst(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	queue := &Queue{directory: dir, beforePersist: func(*job) error { return errBoomPR8 }}
	item := &job{Schema: 1, Event: repositoryevent.Event{ID: "evt-1"}}
	if err := queue.persistInjected(item, nil); !errors.Is(err, errBoomPR8) {
		t.Fatalf("persistInjected(beforePersist failure) = %v, want errBoomPR8", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".job-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("beforePersist failure still staged a temp file: %v", matches)
	}
}
