package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/hooks"
)

func writeHookEvents(t *testing.T, path string, events []hooks.Event) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := hooks.AppendEvents(path, events); err != nil {
		t.Fatal(err)
	}
}

// AC: hooks-are-cheap-on-a-stream-branch — `wb hooks measure` shows the
// recorded durations for the stream-branch push and the other-branch push, and
// prices the saving from the measured average.
func TestHooksMeasureShowsTheStreamProfileDelta(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	writeHookEvents(t, path, []hooks.Event{
		{SchemaVersion: hooks.EventSchemaVersion, Timestamp: now, Repository: "acme/app",
			Hook: "pre-commit", Action: "commit-check", Outcome: "passed", DurationMS: 800, Branch: "stream/x"},
		{SchemaVersion: hooks.EventSchemaVersion, Timestamp: now, Repository: "acme/app",
			Hook: "pre-push", Action: "push-attempt", Outcome: "passed", DurationMS: 20, Branch: "stream/x"},
		{SchemaVersion: hooks.EventSchemaVersion, Timestamp: now, Repository: "acme/app",
			Hook: "pre-push", Action: "push-attempt", Outcome: "passed", DurationMS: 60000, Branch: "feature/y"},
	})

	var stdout, stderr bytes.Buffer
	if code := run([]string{"hooks", "measure", ".", "--file", path, "--json", "--non-interactive"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}
	var delta hooks.ProfileDelta
	if err := json.Unmarshal(stdout.Bytes(), &delta); err != nil {
		t.Fatalf("parse %q: %v", stdout.String(), err)
	}
	if delta.Commit.Runs != 1 || delta.Commit.MaxDurationMS != 800 {
		t.Errorf("commit = %#v", delta.Commit)
	}
	if delta.StreamPush.Runs != 1 || delta.OtherPush.Runs != 1 {
		t.Fatalf("stream=%#v other=%#v", delta.StreamPush, delta.OtherPush)
	}
	if delta.SavedRuns != 1 || delta.SavedBasisMS != 60000 || delta.SavedDurationMS != 60000 {
		t.Errorf("saving = %d × %d = %d", delta.SavedRuns, delta.SavedBasisMS, delta.SavedDurationMS)
	}

	var textOut, textErr bytes.Buffer
	if code := run([]string{"hooks", "measure", ".", "--file", path, "--non-interactive"}, &textOut, &textErr); code != exitOK {
		t.Fatalf("text exit code = %d; stderr=%s", code, textErr.String())
	}
	for _, want := range []string{"stream push", "other push", "budget ms", "ran no local verification"} {
		if !strings.Contains(textOut.String(), want) {
			t.Errorf("text report does not contain %q:\n%s", want, textOut.String())
		}
	}
}

// Hook reports are read-only. Pending receipt replay belongs to a maintenance
// path because even preparing its runtime changes filesystem state.
func TestHookReportsDoNotCreateRuntimeState(t *testing.T) {
	repo := t.TempDir()
	command := exec.Command("git", "init", "-b", "main")
	command.Dir = repo
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, output)
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	wbHome := filepath.Join(t.TempDir(), "wb-home")
	t.Setenv("WB_HOME", wbHome)

	for _, name := range []string{"metrics", "measure"} {
		t.Run(name, func(t *testing.T) {
			cmd := map[string]func() *cobra.Command{
				"metrics": newHooksMetricsCmd,
				"measure": newHooksMeasureCmd,
			}[name]()
			cmd.SetArgs([]string{repo, "--json"})
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(wbHome); !os.IsNotExist(err) {
				t.Fatalf("read-only report created WB_HOME state: %v", err)
			}
		})
	}
}

// The hidden classifier the hook templates dispatch on must exit 0 for a
// stream branch, which is the whole mechanism behind the cheap push.
func TestHooksPushTierExitsSkipForAStreamBranch(t *testing.T) {
	classification := hooks.ClassifyPushTier([]hooks.RefUpdate{{
		LocalRef: "refs/heads/stream/x", LocalSHA: "a",
		RemoteRef: "refs/heads/stream/x", RemoteSHA: "b",
	}}, "main", nil)
	if classification.ExitCode() != 0 {
		t.Fatalf("exit code = %d, want 0 so the hook template skips both blocks", classification.ExitCode())
	}
}
