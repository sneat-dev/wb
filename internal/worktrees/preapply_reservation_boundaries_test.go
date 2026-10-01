package worktrees

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func newPreApplyReservationFixture(t *testing.T) (home, runPath string) {
	t.Helper()
	home = t.TempDir()
	options, err := (WorkLogOptions{EffortID: "destination", RunID: "run", Model: "unknown"}).WithOriginalPromptFromStdin([]byte("exact recycle prompt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePreApplyRenameWorkLog(home, "source", "destination", options); err != nil {
		t.Fatal(err)
	}
	return home, filepath.Join(home, "worklogs", "destination", "runs", "run")
}

func TestPreApplyReservationScannerKeepsStrictDirectoryAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		mutate  func(*testing.T, string, string)
		wantErr bool
		wantOne bool
	}{
		{"invalid effort and run names are skipped", func(t *testing.T, home, run string) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(home, "worklogs", "-unsafe"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(filepath.Dir(run), "-unsafe"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, false, true},
		{"an effort without runs is skipped", func(t *testing.T, home, _ string) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(home, "worklogs", "empty"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, false, true},
		{"symlinked effort refuses traversal", func(t *testing.T, home, _ string) {
			t.Helper()
			if err := os.Symlink(t.TempDir(), filepath.Join(home, "worklogs", "a-symlink")); err != nil {
				t.Fatal(err)
			}
		}, true, false},
		{"non-directory runs refuses traversal", func(t *testing.T, home, _ string) {
			t.Helper()
			effort := filepath.Join(home, "worklogs", "a-file")
			if err := os.Mkdir(effort, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(effort, "runs"), []byte("blocked"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, true, false},
		{"symlinked run refuses traversal", func(t *testing.T, _ string, run string) {
			t.Helper()
			if err := os.Symlink(t.TempDir(), filepath.Join(filepath.Dir(run), "a-symlink")); err != nil {
				t.Fatal(err)
			}
		}, true, false},
		{"malformed reservation refuses evidence", func(t *testing.T, _ string, run string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(run, preApplyRenameReservationName), []byte("{"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, true, false},
		{"missing prompt refuses evidence", func(t *testing.T, _ string, run string) {
			t.Helper()
			if err := os.Remove(filepath.Join(run, "original-prompt.txt")); err != nil {
				t.Fatal(err)
			}
		}, true, false},
		{"missing metadata refuses evidence", func(t *testing.T, _ string, run string) {
			t.Helper()
			if err := os.Remove(filepath.Join(run, "original-prompt.json")); err != nil {
				t.Fatal(err)
			}
		}, true, false},
		{"unreadable claim entry suppresses reservation", func(t *testing.T, _ string, run string) {
			t.Helper()
			if err := os.Symlink(t.TempDir(), filepath.Join(run, "claims")); err != nil {
				t.Fatal(err)
			}
		}, false, false},
		{"terminal entries suppress reservation", func(t *testing.T, _ string, run string) {
			t.Helper()
			terminalDir := filepath.Join(run, "terminals")
			if err := os.Mkdir(terminalDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(terminalDir, "owned.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home, run := newPreApplyReservationFixture(t)
			tc.mutate(t, home, run)
			found, err := findPreApplyRenameReservations(home, "destination")
			if (err != nil) != tc.wantErr {
				t.Fatalf("reservation scan = %#v, %v; want error=%t", found, err, tc.wantErr)
			}
			if err == nil && (len(found) == 1) != tc.wantOne {
				t.Fatalf("reservation scan = %#v; want one=%t", found, tc.wantOne)
			}
		})
	}
}

func TestLegacyPreApplyReservationRequiresExactPromptOnlyRun(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	runDir, _, err := openWorkLogRun(home, "destination", "legacy-run", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runDir.Close() })
	runPath := filepath.Join(home, "worklogs", "destination", "runs", "legacy-run")
	prompt := []byte("legacy exact prompt\n")
	digest := sha256.Sum256(prompt)
	wantDigest := hex.EncodeToString(digest[:])
	if err := writeBytesImmutableAt(runDir, "original-prompt.txt", prompt, 0o600, false); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(runDir, "original-prompt.json", workLogPromptMetadata{Version: 1, SHA256: wantDigest}, false); err != nil {
		t.Fatal(err)
	}
	found, err := findPreApplyRenameReservations(home, "destination")
	if err != nil || len(found) != 1 || found[0].OldTask != "legacy-unclaimed" {
		t.Fatalf("exact legacy prompt = %#v, %v", found, err)
	}
	if err := os.Mkdir(filepath.Join(runPath, "run.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	found, err = findPreApplyRenameReservations(home, "destination")
	if err != nil || len(found) != 0 {
		t.Fatalf("run.json directory became an unclaimed reservation: %#v, %v", found, err)
	}
	if err := os.Remove(filepath.Join(runPath, "run.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runPath, "original-prompt.txt"), []byte("changed bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := findPreApplyRenameReservations(home, "destination"); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("changed legacy prompt = %v, want digest refusal", err)
	}
}

func TestPreApplyReservationAbortKeepsPromptAndRefusesOccupiedShell(t *testing.T) {
	t.Parallel()
	home, run := newPreApplyReservationFixture(t)
	resolution := wbhome.Resolution{Write: wbhome.Layout{Home: home}}
	promptPath := filepath.Join(run, "original-prompt.txt")
	before, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatal(err)
	}
	result, found, err := abortPreApplyRenameReservations(resolution, "destination", AbortOptions{})
	if err != nil || !found || len(result) != 1 || result[0].Applied {
		t.Fatalf("dry-run result = %#v, found=%t, err=%v", result, found, err)
	}
	if _, err := os.Stat(filepath.Join(run, preApplyRenameTerminalName)); !os.IsNotExist(err) {
		t.Fatalf("dry-run published terminal: %v", err)
	}
	shell := filepath.Join(home, "worktrees", "destination")
	if err := os.MkdirAll(shell, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shell, "unexpected"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, found, err = abortPreApplyRenameReservations(resolution, "destination", AbortOptions{Apply: true})
	if err == nil || !found || len(result) != 1 || result[0].Applied || !strings.Contains(err.Error(), "preserve it") {
		t.Fatalf("occupied shell result = %#v, found=%t, err=%v", result, found, err)
	}
	if _, err := os.Stat(filepath.Join(run, preApplyRenameTerminalName)); !os.IsNotExist(err) {
		t.Fatalf("occupied shell published terminal: %v", err)
	}
	after, err := os.ReadFile(promptPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("prompt changed across abort refusal: %q/%v", after, err)
	}
	replayHome, replayRun := newPreApplyReservationFixture(t)
	replayResolution := wbhome.Resolution{Write: wbhome.Layout{Home: replayHome}}
	result, found, err = abortPreApplyRenameReservations(replayResolution, "destination", AbortOptions{Apply: true})
	if err != nil || !found || len(result) != 1 || !result[0].Applied {
		t.Fatalf("recovered abort result = %#v, found=%t, err=%v", result, found, err)
	}
	result, found, err = abortPreApplyRenameReservations(replayResolution, "destination", AbortOptions{Apply: true})
	if err != nil || !found || len(result) != 1 || !result[0].Applied {
		t.Fatalf("replayed abort result = %#v, found=%t, err=%v", result, found, err)
	}
	after, err = os.ReadFile(filepath.Join(replayRun, "original-prompt.txt"))
	if err != nil || string(after) != string(before) {
		t.Fatalf("prompt changed across abort recovery: %q/%v", after, err)
	}
}

func TestPreApplyReservationAbortReportsMissingAndMalformedStores(t *testing.T) {
	t.Parallel()
	empty := t.TempDir()
	resolution := wbhome.Resolution{Write: wbhome.Layout{Home: empty}}
	if result, found, err := abortPreApplyRenameReservations(resolution, "destination", AbortOptions{Apply: true}); err != nil || found || result != nil {
		t.Fatalf("absent reservation = %#v, found=%t, err=%v", result, found, err)
	}
	if err := os.WriteFile(filepath.Join(empty, "worklogs"), []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result, found, err := abortPreApplyRenameReservations(resolution, "destination", AbortOptions{Apply: true}); err == nil || found || result != nil {
		t.Fatalf("malformed Work Log root = %#v, found=%t, err=%v", result, found, err)
	}

	home, _ := newPreApplyReservationFixture(t)
	resolution = wbhome.Resolution{Write: wbhome.Layout{Home: home}}
	if err := os.MkdirAll(filepath.Join(home, "worktrees"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "worktrees", "destination"), []byte("not a task shell"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, found, err := abortPreApplyRenameReservations(resolution, "destination", AbortOptions{Apply: true})
	if err == nil || !found || len(result) != 1 || result[0].Applied {
		t.Fatalf("non-directory task shell = %#v, found=%t, err=%v", result, found, err)
	}
}

func TestPreApplyReservationLockAndShellRefusals(t *testing.T) {
	t.Parallel()
	if err := preApplyReservationShellOnly(nil); err == nil {
		t.Fatal("nil held task shell was accepted")
	}
	home, _ := newPreApplyReservationFixture(t)
	root := filepath.Join(home, "worktrees")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	held, err := acquireCleanupTaskAtOrCreate(root, "destination")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(held.close)
	resolution := wbhome.Resolution{Write: wbhome.Layout{Home: home}}
	if second, err := acquirePreApplyReservationTask(resolution, "destination"); err == nil || second != nil {
		if second != nil {
			second.close()
		}
		t.Fatalf("live competing task lock = %#v, %v", second, err)
	}
	blockedHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(blockedHome, "worktrees"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if task, err := acquirePreApplyReservationTask(wbhome.Resolution{Write: wbhome.Layout{Home: blockedHome}}, "destination"); err == nil || task != nil {
		t.Fatalf("unreadable task namespace = %#v, %v", task, err)
	}
}

func TestPreApplyReservationPublicationRefusesMissingDigestAndCorruptReceipt(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	missing := WorkLogOptions{EffortID: "destination", RunID: "run", Model: "unknown", OriginalPrompt: filepath.Join(home, "missing-prompt.txt"), RequireOriginalPrompt: true}
	if err := reservePreApplyRenameWorkLog(home, "source", "destination", missing); err == nil {
		t.Fatal("missing original prompt was reserved")
	}
	prompt := filepath.Join(home, "prompt.txt")
	if err := os.WriteFile(prompt, []byte("prompt from path\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	noSnapshot := WorkLogOptions{EffortID: "destination", RunID: "run", Model: "unknown", OriginalPrompt: prompt, RequireOriginalPrompt: true}
	if err := reservePreApplyRenameWorkLog(home, "source", "destination", noSnapshot); err == nil || !strings.Contains(err.Error(), "no immutable prompt digest") {
		t.Fatalf("unsnapshotted prompt reservation = %v", err)
	}
	archive := filepath.Join(home, "worklogs", "destination", "runs", "run", "original-prompt.txt")
	if content, err := os.ReadFile(archive); err != nil || string(content) != "prompt from path\n" {
		t.Fatalf("retained immutable archive = %q, %v", content, err)
	}
	validHome, run := newPreApplyReservationFixture(t)
	if err := os.WriteFile(filepath.Join(run, preApplyRenameReservationName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	options, err := (WorkLogOptions{EffortID: "destination", RunID: "run", Model: "unknown"}).WithOriginalPromptFromStdin([]byte("exact recycle prompt\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := reservePreApplyRenameWorkLog(validHome, "source", "destination", options); err == nil || !strings.Contains(err.Error(), "inspect existing") {
		t.Fatalf("malformed prior reservation = %v", err)
	}
}

func TestPreApplyReservationLegacyMetadataAndTerminalOpenRefusals(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	runDir, _, err := openWorkLogRun(home, "destination", "legacy", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeBytesImmutableAt(runDir, "original-prompt.txt", []byte("legacy prompt\n"), 0o600, false); err != nil {
		_ = runDir.Close()
		t.Fatal(err)
	}
	_ = runDir.Close()
	if found, err := findPreApplyRenameReservations(home, "destination"); err != nil || len(found) != 0 {
		t.Fatalf("legacy prompt without metadata = %#v, %v", found, err)
	}
	candidate := preApplyRenameReservationCandidate{preApplyRenameReservation: preApplyRenameReservation{
		Version: 1, OldTask: "source", NewTask: "destination", EffortID: "absent", RunID: "run", PromptSHA256: strings.Repeat("a", 64),
	}}
	if err := terminalizePreApplyRenameReservation(home, candidate); err == nil {
		t.Fatal("terminalized a missing private run")
	}
}
