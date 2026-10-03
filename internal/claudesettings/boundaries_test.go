package claudesettings

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeReadOnlyVariantsShareParentSettingsFixture(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Parent cleanup is owned by t.TempDir; parallel children finish first.
	fixtures := map[string]string{"empty": " \n", "invalid": "{bad", "new": "{}", "existing": `{"hooks":{"SessionStart":[{"hooks":[{"command":"mine"}]}]},"other":true}`}
	for name, content := range fixtures {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, original := range fixtures {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(dir, name)
			result, changed, err := MergeSessionStart(p, "mine")
			if name == "invalid" {
				if err == nil {
					t.Fatal("invalid JSON accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if changed != (name != "existing") {
					t.Fatalf("changed=%v", changed)
				}
				var doc map[string]any
				if e := json.Unmarshal(result, &doc); e != nil {
					t.Fatal(e)
				}
			}
			actual, e := os.ReadFile(p)
			if e != nil || string(actual) != original {
				t.Fatalf("source mutated: %q %v", actual, e)
			}
		})
	}
}

func TestSettingsEncodingRejectsUnencodableValues(t *testing.T) {
	t.Parallel()
	if _, err := Encode(map[string]any{"unsupported": make(chan int)}); err == nil {
		t.Fatal("unencodable settings accepted")
	}
}

func TestAtomicSettingsWriteIsPrivateAndCleansTemporaryFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	content := []byte("{\"private\":true}\n")
	if err := WriteAtomically(path, content); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, content) {
		t.Fatalf("actual=%q err=%v", actual, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("info=%v err=%v", info, err)
	}
	assertNoLeftoverSettingsTempFile(t, dir)
	bad := filepath.Join(dir, "file")
	if err := os.WriteFile(bad, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomically(filepath.Join(bad, "settings.json"), content); err == nil {
		t.Fatal("non-directory parent accepted")
	}
}

func TestEntryPresentSkipsNonmatchingHandlers(t *testing.T) {
	t.Parallel()
	for _, entry := range []any{map[string]any{}, map[string]any{"hooks": []any{map[string]any{"command": "other"}, map[string]any{"command": false}}}} {
		if EntryPresent(entry, "mine") {
			t.Fatal("nonmatching entry accepted")
		}
	}
}
