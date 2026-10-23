package claudesettings

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMergeAgentHookSettingsIsIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{
	  "model": "opus",
	  "hooks": {
	    "PreToolUse": [
	      {"matcher": "Bash", "hooks": [{"type": "command", "command": "other-guard"}]}
	    ],
	    "Stop": [{"hooks": [{"type": "command", "command": "notify"}]}]
	  }
	}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed the settings file: %v", err)
	}
	shellCommand := "/usr/local/bin/wb hooks agent pre-tool-use 2>/dev/null; exit 0"

	document, changed, err := MergeAgentHook(path, shellCommand)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !changed {
		t.Fatal("the first merge reported no change")
	}
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatalf("persist the merged document: %v", err)
	}

	second, changed, err := MergeAgentHook(path, shellCommand)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if changed {
		t.Fatal("re-running the installer changed the settings file again")
	}
	if !bytes.Equal(document, second) {
		t.Fatalf("re-running the installer rewrote the document:\n%s\n---\n%s", document, second)
	}

	var settings map[string]any
	if err := json.Unmarshal(second, &settings); err != nil {
		t.Fatalf("the merged document is not valid JSON: %v", err)
	}
	if settings["model"] != "opus" {
		t.Fatalf("an unrelated key was lost: %v", settings["model"])
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if _, ok := hooks["Stop"]; !ok {
		t.Fatal("an unrelated hook event was lost")
	}
	entries, _ := hooks["PreToolUse"].([]any)
	if len(entries) != 2 {
		t.Fatalf("PreToolUse has %d entries, want the pre-existing one plus WB's", len(entries))
	}
	if !strings.Contains(string(second), "other-guard") {
		t.Fatal("a pre-existing PreToolUse hook was lost")
	}
	if !strings.Contains(string(second), AgentMatcher) {
		t.Fatalf("the WB matcher is missing from %s", second)
	}
}
func TestMergeAgentHookSettingsWidensAStaleMatcher(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	shellCommand := "/usr/local/bin/wb hooks agent pre-tool-use 2>/dev/null; exit 0"
	staleMatcher := `{
	  "hooks": {
	    "PreToolUse": [
	      {"matcher": "Bash|Write|Edit|MultiEdit|NotebookEdit", "hooks": [{"type": "command", "command": ` + strconv.Quote(shellCommand) + `, "timeout": 10}]}
	    ]
	  }
	}`
	if err := os.WriteFile(path, []byte(staleMatcher), 0o644); err != nil {
		t.Fatalf("seed the settings file: %v", err)
	}

	document, changed, err := MergeAgentHook(path, shellCommand)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !changed {
		t.Fatal("re-running install over a stale matcher reported no change")
	}
	if !strings.Contains(string(document), AgentMatcher) {
		t.Fatalf("the widened matcher is missing from %s", document)
	}

	var settings map[string]any
	if err := json.Unmarshal(document, &settings); err != nil {
		t.Fatalf("the merged document is not valid JSON: %v", err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks["PreToolUse"].([]any)
	if len(entries) != 1 {
		t.Fatalf("widening the matcher produced %d entries, want the same one entry updated in place", len(entries))
	}

	// A third run, now that the matcher matches, must be a true no-op.
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatal(err)
	}
	again, changedAgain, err := MergeAgentHook(path, shellCommand)
	if err != nil {
		t.Fatalf("third merge: %v", err)
	}
	if changedAgain {
		t.Fatal("re-running install a second time, after the matcher was widened, reported a change")
	}
	if !bytes.Equal(document, again) {
		t.Fatalf("re-running install rewrote the already-current document:\n%s\n---\n%s", document, again)
	}
}
func TestMergeAgentHookSettingsCreatesAMissingFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	document, changed, err := MergeAgentHook(path, "wb hooks agent pre-tool-use 2>/dev/null; exit 0")
	if err != nil || !changed {
		t.Fatalf("merge on a missing file: changed=%v err=%v", changed, err)
	}
	if err := WriteAtomically(path, document); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), "PreToolUse") {
		t.Fatalf("the installed document is missing the hook: %s", raw)
	}
}
func TestMergeAgentHookSettingsRefusesAnUnparseableFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := MergeAgentHook(path, "wb"); err == nil {
		t.Fatal("an unparseable settings file was accepted")
	}
}
func TestMergeAgentHookSettingsSurfacesNonNotExistReadErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings-is-a-directory.json")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err := MergeAgentHook(path, "wb hooks agent guard")
	if err == nil || !strings.Contains(err.Error(), "read "+path) {
		t.Fatalf("err = %v, want a read error naming %s", err, path)
	}
}
func TestAgentHookEntryMatcherStaleRejectsNonObjectEntry(t *testing.T) {
	t.Parallel()
	if agentHookEntryMatcherStale(42) {
		t.Fatal("want false for a non-object entry")
	}
}
