package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// errBoomPR7 is task-9 PR-7's sentinel injected failure, distinct from any
// other package's or task's sentinel so errors.Is never accidentally
// matches a different test's error by coincidence.
var errBoomPR7 = errors.New("pr7 boom")

// pr7AgentID derives a validateAgentID-shaped ID (IDPrefix + 32 hex chars)
// deterministically from a label, so each subtest gets its own distinct,
// valid agent ID without needing real randomness.
func pr7AgentID(label string) string {
	sum := sha256.Sum256([]byte(label))
	return IDPrefix + hex.EncodeToString(sum[:16])
}

func TestStoreSaveInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			store := Store{Root: dir}
			agentID := pr7AgentID("pr7-save-" + string(step))
			if err := os.MkdirAll(store.Dir(agentID), 0o700); err != nil {
				t.Fatal(err)
			}
			inj := &filewrite.Injector{Step: step, Err: errBoomPR7}
			err := store.saveInjected(Record{AgentID: agentID}, inj)
			if !errors.Is(err, errBoomPR7) {
				t.Fatalf("saveInjected(%s failure) = %v, want errBoomPR7", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(store.Dir(agentID), ".run-*.json"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
			}
			if _, statErr := os.Stat(filepath.Join(store.Dir(agentID), recordFileName)); !os.IsNotExist(statErr) {
				t.Fatalf("failed save published a visible record: %v", statErr)
			}
		})
	}
}

// TestStoreSaveInjectedPublishesAt0600 is review-763's chmod-preset-Hook
// lesson applied to this site: os.CreateTemp already creates its temp file
// at 0600, the same as this site's own final published mode, so a plain
// end-to-end 0600 assertion cannot tell a real chmod call from a deleted
// one. The Hook fires immediately before the real ChmodFile syscall and
// pre-sets the not-yet-published temp file to 0644, so the final 0600 can
// only be true if the real chmod ran.
func TestStoreSaveInjectedPublishesAt0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := Store{Root: dir}
	agentID := pr7AgentID("pr7-save-mode")
	if err := os.MkdirAll(store.Dir(agentID), 0o700); err != nil {
		t.Fatal(err)
	}
	hookRan := false
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Hook: func() {
		hookRan = true
		matches, err := filepath.Glob(filepath.Join(store.Dir(agentID), ".run-*.json"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("locate temporary file before chmod: matches=%v err=%v", matches, err)
		}
		if err := os.Chmod(matches[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	if err := store.saveInjected(Record{AgentID: agentID}, inj); err != nil {
		t.Fatal(err)
	}
	if !hookRan {
		t.Fatal("Hook did not run before the real chmod")
	}
	info, err := os.Stat(filepath.Join(store.Dir(agentID), recordFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("published record mode = %o, want 0600", perm)
	}
}
