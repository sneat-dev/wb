package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/buildinfo"
)

func TestSkillsHookShellCommandForcesExitZero(t *testing.T) {
	command := skillsHookShellCommand("/opt/homebrew/bin/wb")
	for _, expected := range []string{"/opt/homebrew/bin/wb", skillsHookInvocation, "2>/dev/null", "exit 0"} {
		if !strings.Contains(command, expected) {
			t.Fatalf("the hook command is missing %q: %s", expected, command)
		}
	}
	if quoted := skillsHookShellCommand("/path with spaces/wb"); !strings.Contains(quoted, `'/path with spaces/wb'`) {
		t.Fatalf("an executable path with spaces was not quoted: %s", quoted)
	}
}

func TestSkillsHookSettingsSnippetHasNoMatcherSoItRunsForEverySource(t *testing.T) {
	snippet := skillsHookSettingsSnippet("/usr/local/bin/wb")
	encoded, err := json.Marshal(snippet)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "matcher") {
		t.Fatalf("SessionStart snippet must omit matcher to run for every session-start source: %s", encoded)
	}
	if !strings.Contains(string(encoded), "SessionStart") {
		t.Fatalf("snippet does not register a SessionStart hook: %s", encoded)
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
	shellCommand := skillsHookShellCommand("/usr/local/bin/wb")

	document, changed, err := mergeSkillsHookSettings(path, shellCommand)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !changed {
		t.Fatal("the first merge reported no change")
	}
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatalf("persist the merged document: %v", err)
	}

	second, changed, err := mergeSkillsHookSettings(path, shellCommand)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if changed {
		t.Fatal("a second merge with the entry already present reported a change")
	}
	if string(second) != string(document) {
		t.Fatalf("a no-op merge changed the document:\nfirst:  %s\nsecond: %s", document, second)
	}

	for _, want := range []string{`"model": "opus"`, "other-greeter", "wb hooks agent pre-tool-use", skillsHookInvocation} {
		if !strings.Contains(string(document), want) {
			t.Errorf("merged document is missing %q:\n%s", want, document)
		}
	}
}

func TestNewSkillsHookInstallCmdWritesThenReportsAlreadyRegistered(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")

	first := newSkillsHookInstallCmd()
	first.SetArgs([]string{"--settings", settings})
	var firstOut bytes.Buffer
	first.SetOut(&firstOut)
	if err := first.Execute(); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !strings.Contains(firstOut.String(), "registered") {
		t.Errorf("first install output = %q, want it to say the hook was registered", firstOut.String())
	}
	raw, err := os.ReadFile(settings)
	if err != nil {
		t.Fatalf("settings file was not written: %v", err)
	}
	if !strings.Contains(string(raw), "SessionStart") {
		t.Fatalf("written settings do not contain SessionStart: %s", raw)
	}

	second := newSkillsHookInstallCmd()
	second.SetArgs([]string{"--settings", settings})
	var secondOut bytes.Buffer
	second.SetOut(&secondOut)
	if err := second.Execute(); err != nil {
		t.Fatalf("second install: %v", err)
	}
	if !strings.Contains(secondOut.String(), "already registered") {
		t.Errorf("second install output = %q, want it to say the hook was already registered", secondOut.String())
	}
}

func TestNewSkillsHookInstallCmdDryRunNeverWrites(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")

	command := newSkillsHookInstallCmd()
	command.SetArgs([]string{"--settings", settings, "--dry-run"})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SessionStart") {
		t.Errorf("dry-run output = %q, want the merged document printed", out.String())
	}
	if _, err := os.Stat(settings); !os.IsNotExist(err) {
		t.Fatalf("--dry-run wrote %s: err=%v", settings, err)
	}
}

func TestNewSkillsHookPrintCmdPrintsAPasteableSnippet(t *testing.T) {
	command := newSkillsHookPrintCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SessionStart", skillsHookInvocation, "wb skills hook install"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("print output is missing %q:\n%s", want, out.String())
		}
	}
}

// TestSessionStartAnnouncementAlwaysRemindsRegistration covers a fresh home
// with no marker at all. A SessionStart hook only ever runs because Claude
// Code itself is launching a session, so -- unlike the general drift banner
// in main.go, which must stay silent on a machine with no harness present
// at all -- this announcement always reports "never synced" as exactly the
// drift it is: the reminder this whole feature exists to give.
func TestSessionStartAnnouncementAlwaysRemindsRegistration(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.claude/skills at all
	buildinfo.Set("1.2.3")        // a go test binary otherwise reports an undetermined version, which never counts as drifted
	t.Cleanup(func() { buildinfo.Set("") })
	announcement := sessionStartAnnouncement()
	if !strings.Contains(announcement, "wb session register") {
		t.Errorf("announcement = %q, want the registration reminder", announcement)
	}
	if !strings.Contains(announcement, "wb skills sync") {
		t.Errorf("announcement = %q, want a drift warning: skills were never synced under this home", announcement)
	}
}

func TestSessionStartAnnouncementWarnsWhenSkillsAreStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	buildinfo.Set("1.2.3")
	t.Cleanup(func() { buildinfo.Set("") })
	skillsDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, ".wb-skills-sync.json"),
		[]byte(`{"schema_version":1,"wb_version":"0.0.1-old","skills":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	announcement := sessionStartAnnouncement()
	if !strings.Contains(announcement, "wb skills sync") {
		t.Errorf("announcement = %q, want a drift warning when the marker predates the running wb", announcement)
	}
}

func TestNewSkillsHookRunCmdIsHiddenAndAlwaysSucceeds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	command := newSkillsHookRunCmd()
	if !command.Hidden {
		t.Error("skills hook run must be Hidden: it is invoked by the installed hook, not by hand")
	}
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatalf("skills hook run failed: %v", err)
	}
	if strings.TrimSpace(out.String()) == "" {
		t.Error("skills hook run printed nothing")
	}
}
