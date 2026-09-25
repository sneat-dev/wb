package wbconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/filewrite"
)

func TestSetRemoteHubInjectedHonoursInjectedFailures(t *testing.T) {
	t.Parallel()
	for _, step := range []filewrite.Step{
		filewrite.StepOpenOrCreate, filewrite.StepChmod, filewrite.StepWrite,
		filewrite.StepSync, filewrite.StepClose, filewrite.StepRename,
	} {
		step := step
		t.Run(string(step), func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "config.yaml")
			inj := &filewrite.Injector{Step: step, Err: errBoomPR8}
			err := setRemoteHubInjected(path, "https://hub.example.com", "machine-1", "/abs/token", inj)
			if step == filewrite.StepWrite {
				// yaml.Encoder's internal emitter stringifies a write error
				// into its own error state (writerc.go:
				// yaml_emitter_set_writer_error) rather than wrapping it, so
				// errors.Is cannot see through it here; only the message
				// text survives.
				if err == nil || !strings.Contains(err.Error(), errBoomPR8.Error()) {
					t.Fatalf("setRemoteHubInjected(write failure) = %v, want it to mention %q", err, errBoomPR8)
				}
			} else if !errors.Is(err, errBoomPR8) {
				t.Fatalf("setRemoteHubInjected(%s failure) = %v, want errBoomPR8", step, err)
			}
			matches, globErr := filepath.Glob(filepath.Join(dir, ".wb-config-*"))
			if globErr != nil {
				t.Fatal(globErr)
			}
			if len(matches) != 0 {
				t.Fatalf("leftover temp file(s) after injected failure: %v", matches)
			}
			if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
				t.Fatalf("failed write published a visible config: %v", statErr)
			}
		})
	}
}

// TestSetRemoteHubInjectedPublishesAt0600 is review-763's chmod-preset-Hook
// lesson applied to this site: os.CreateTemp already creates its temp file
// at 0600, the same as this site's own final published mode, so a plain
// end-to-end 0600 assertion cannot tell a real chmod call from a deleted
// one.
func TestSetRemoteHubInjectedPublishesAt0600(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	hookRan := false
	inj := &filewrite.Injector{Step: filewrite.StepChmod, Hook: func() {
		hookRan = true
		matches, err := filepath.Glob(filepath.Join(dir, ".wb-config-*"))
		if err != nil || len(matches) != 1 {
			t.Fatalf("locate temporary file before chmod: matches=%v err=%v", matches, err)
		}
		if err := os.Chmod(matches[0], 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	if err := setRemoteHubInjected(path, "https://hub.example.com", "machine-1", "/abs/token", inj); err != nil {
		t.Fatal(err)
	}
	if !hookRan {
		t.Fatal("Hook did not run before the real chmod")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("published config mode = %o, want 0600", perm)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) == 0 {
		t.Fatal("published config is empty")
	}
}

func TestSetRemoteHubInjectedRejectsAnUnparsableExistingConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("not: [valid: yaml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := setRemoteHubInjected(path, "https://hub.example.com", "machine-1", "/abs/token", nil); err == nil {
		t.Fatal("setRemoteHubInjected(unparsable config) = nil, want an error")
	}
}
