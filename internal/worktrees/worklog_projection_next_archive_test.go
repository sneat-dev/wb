package worktrees

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func projectionNextTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return path
}
func projectionNextRun(t *testing.T) (string, *os.File, string) {
	t.Helper()
	home := projectionNextTemp(t)
	run, path, err := openWorkLogRun(home, "task", "run", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	return home, run, path
}
func projectionNextWrite(t *testing.T, path string, bytes []byte) {
	t.Helper()
	if err := os.WriteFile(path, bytes, 0600); err != nil {
		t.Fatal(err)
	}
}
func projectionNextClose(t *testing.T, phase workLogProjectionBoundary) workLogProjectionObservation {
	t.Helper()
	return func(got workLogProjectionBoundary, file *os.File) {
		if got == phase {
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// projectionNextCloseEnumeration captures the real directory enumeration cause
// after closing this call's owned file. Go's Readdirnames cause need not be os.ErrClosed.
func projectionNextCloseEnumeration(t *testing.T, phase workLogProjectionBoundary, nativeCause *error) workLogProjectionObservation {
	t.Helper()
	return func(got workLogProjectionBoundary, file *os.File) {
		if got != phase {
			return
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		_, probeErr := file.Readdirnames(-1)
		var pathErr *os.PathError
		if !errors.As(probeErr, &pathErr) || pathErr.Err == nil {
			t.Fatalf("closed owned enumeration probe=%v", probeErr)
		}
		*nativeCause = pathErr.Err
	}
}
func TestWorkLogProjectionNextArchiveMetadataKeepsImmutableEvidence(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"wrong metadata", "malformed metadata", "timestamp failure", "same digest winner", "foreign digest winner"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			_, run, path := projectionNextRun(t)
			options, err := (WorkLogOptions{}).WithOriginalPromptFromStdin([]byte("immutable instruction\n"))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Unix(1700000000, 0).UTC()
			var beforeRename func(*os.File, string)
			switch kind {
			case "wrong metadata":
				projectionNextWrite(t, filepath.Join(path, "original-prompt.json"), []byte(`{"version":1,"sha256":"foreign"}`))
			case "malformed metadata":
				projectionNextWrite(t, filepath.Join(path, "original-prompt.json"), []byte("{bad"))
			case "timestamp failure":
				now = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
			default:
				beforeRename = func(_ *os.File, name string) {
					if name != "original-prompt.json" {
						t.Fatalf("metadata boundary name=%s", name)
					}
					digest := options.snapshot.Digest
					if kind == "foreign digest winner" {
						digest = "foreign"
					}
					value := workLogPromptMetadata{Version: 1, SHA256: digest, CapturedAt: now.Add(time.Second)}
					encoded, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					projectionNextWrite(t, filepath.Join(path, name), encoded)
				}
			}
			archive, digest, err := ensureOriginalPromptArchiveBeforeRename(run, options, now, beforeRename)
			if kind == "same digest winner" {
				if err != nil || archive != "original-prompt.txt" || digest != options.snapshot.Digest {
					t.Fatalf("same digest winner=%q %q %v", archive, digest, err)
				}
			} else {
				if err == nil || archive != "" || digest != "" {
					t.Fatalf("metadata refusal=%q %q %v", archive, digest, err)
				}
				if kind == "timestamp failure" {
					var cause *json.MarshalerError
					if !errors.As(err, &cause) {
						t.Fatalf("timestamp cause=%v", err)
					}
				}
			}
			raw, readErr := os.ReadFile(filepath.Join(path, "original-prompt.txt"))
			if readErr != nil || string(raw) != "immutable instruction\n" {
				t.Fatalf("archive evidence=%q %v", raw, readErr)
			}
			if kind == "foreign digest winner" {
				var metadata workLogPromptMetadata
				if readErr := readJSONAt(run, "original-prompt.json", &metadata); readErr != nil || metadata.SHA256 != "foreign" {
					t.Fatalf("winner evidence=%+v %v", metadata, readErr)
				}
			}
		})
	}
}
func TestWorkLogProjectionNextRunIndexRefusesMalformedIdentity(t *testing.T) {
	t.Parallel()
	_, run, path := projectionNextRun(t)
	projectionNextWrite(t, filepath.Join(path, "run.json"), []byte("{bad"))
	if err := ensureWorkLogRunIndex(run, "task", "run"); err == nil {
		t.Fatal("malformed index accepted")
	}
	if raw, err := os.ReadFile(filepath.Join(path, "run.json")); err != nil || string(raw) != "{bad" {
		t.Fatalf("index changed=%q %v", raw, err)
	}
}
func TestWorkLogProjectionNextExtensionRetainsNativeLateRefusals(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"source removed", "run removed", "archive unreadable"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			home, run, path := projectionNextRun(t)
			prompt := filepath.Join(projectionNextTemp(t), "instruction.txt")
			projectionNextWrite(t, prompt, []byte("exact instruction\n"))
			options := WorkLogOptions{OriginalPrompt: prompt}
			if _, _, err := ensureOriginalPromptArchive(run, options, time.Unix(1700000000, 0).UTC()); err != nil {
				t.Fatal(err)
			}
			if err := run.Close(); err != nil {
				t.Fatal(err)
			}
			claim := workLogClaim{Version: 1, EffortID: "task", RunID: "run", ClaimID: strings.Repeat("a", 64), Lifecycle: "active", Model: "unknown"}
			requested := WorkLogOptions{}
			if kind == "source removed" {
				requested.OriginalPrompt = prompt
			}
			hit := false
			observe := func(phase workLogProjectionBoundary, _ *os.File) {
				if phase != workLogExtensionValidated {
					return
				}
				hit = true
				switch kind {
				case "source removed":
					if err := os.Remove(prompt); err != nil {
						t.Fatal(err)
					}
				case "run removed":
					if err := os.Rename(path, path+".moved"); err != nil {
						t.Fatal(err)
					}
				case "archive unreadable":
					if err := os.Remove(filepath.Join(path, "original-prompt.txt")); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(filepath.Join(path, "original-prompt.txt"), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := workLogOptionsForClaimExtensionObserved(home, requested, claim, observe)
			if !hit || err == nil {
				t.Fatalf("late refusal=%v hit=%v", err, hit)
			}
			if kind != "archive unreadable" && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("native missing cause=%v", err)
			}
			if kind == "archive unreadable" && !strings.Contains(err.Error(), "reuse existing original prompt archive") {
				t.Fatalf("archive diagnostic=%v", err)
			}
		})
	}
	if err := validateResumeWorkLogRequest(projectionNextTemp(t), WorkLogOptions{}, workLogClaim{EffortID: "task", RunID: "run"}); err == nil || !strings.Contains(err.Error(), "project current execution identity") {
		t.Fatalf("missing identity=%v", err)
	}
}
