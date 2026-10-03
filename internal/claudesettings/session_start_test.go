package claudesettings

import (
	"os"
	"path/filepath"
	"strings"
	"testing" // AC: cov-rwi-03 unit03 seam list, cmd/wb/skills_hook_install.go
	// MergeSessionStart. A read failure that is not "file does not exist"
	// -- a settings path that is itself a directory -- must be reported as a
	// read error, not silently treated as an empty/absent settings file.
)

func TestMergeSkillsHookSettingsReportsANonNotExistReadError(t *testing.T) {
	t.Parallel()
	dirAsPath := t.TempDir()
	_, _, err := MergeSessionStart(dirAsPath, "wb skills hook run")
	if err == nil {
		t.Fatal("expected an error reading a directory as the settings file")
	}
	if !strings.Contains(err.Error(), "read "+dirAsPath) {
		t.Errorf("err = %v, want it to name the read failure on %s", err, dirAsPath)
	}
}

// TestMergeSkillsHookSettingsIsIdempotent keeps re-running the installer
// from stacking duplicate entries, and preserves every key WB does not own
// -- including an unrelated SessionStart entry someone else already
// registered.
func TestMergeSkillsHookSettingsIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{
	  "model": "opus",
	  "hooks": {
	    "SessionStart": [
	      {"hooks": [{"type": "command", "command": "other-greeter"}]}
	    ],
	    "PreToolUse": [{"hooks": [{"type": "command", "command": "wb hooks agent pre-tool-use"}]}]
	  }
	}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed the settings file: %v", err)
	}
	shellCommand := "/usr/local/bin/wb skills hook run 2>/dev/null; exit 0"

	document, changed, err := MergeSessionStart(path, shellCommand)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !changed {
		t.Fatal("the first merge reported no change")
	}
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatalf("persist the merged document: %v", err)
	}

	second, changed, err := MergeSessionStart(path, shellCommand)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if changed {
		t.Fatal("a second merge with the entry already present reported a change")
	}
	if string(second) != string(document) {
		t.Fatalf("a no-op merge changed the document:\nfirst:  %s\nsecond: %s", document, second)
	}

	for _, want := range []string{`"model": "opus"`, "other-greeter", "wb hooks agent pre-tool-use", "skills hook run"} {
		if !strings.Contains(string(document), want) {
			t.Errorf("merged document is missing %q:\n%s", want, document)
		}
	}
}
