package cmdskills

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
)

func TestSkillsHookInstallWithoutSettingsFlagDerivesPathFromHome(t *testing.T) {
	t.Parallel()
	deps := testHookDependencies()
	deps.MergeSettings = func(path, command string) ([]byte, bool, error) {
		if path != "/private/home/.claude/settings.json" {
			t.Fatal(path)
		}
		return []byte("SessionStart"), true, nil
	}
	command := newSkillsHookInstallCmd(deps)
	command.SetArgs([]string{"--dry-run"})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "SessionStart") {
		t.Fatalf("dry-run document = %q, want the merged SessionStart hook", out.String())
	}
}
func TestSkillsHookShellCommandForcesExitZero(t *testing.T) {
	t.Parallel()
	command := skillsHookShellCommand("/opt/homebrew/bin/wb", shared.ShellQuoteArg)
	for _, expected := range []string{"/opt/homebrew/bin/wb", skillsHookInvocation, "2>/dev/null", "exit 0"} {
		if !strings.Contains(command, expected) {
			t.Fatalf("the hook command is missing %q: %s", expected, command)
		}
	}
	if quoted := skillsHookShellCommand("/path with spaces/wb", shared.ShellQuoteArg); !strings.Contains(quoted, `'/path with spaces/wb'`) {
		t.Fatalf("an executable path with spaces was not quoted: %s", quoted)
	}
}

func TestSkillsHookSettingsSnippetHasNoMatcherSoItRunsForEverySource(t *testing.T) {
	t.Parallel()
	snippet := skillsHookSettingsSnippet("/usr/local/bin/wb", shared.ShellQuoteArg)
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

func TestNewSkillsHookPrintCmdPrintsAPasteableSnippet(t *testing.T) {
	t.Parallel()
	command := newSkillsHookPrintCmd(testHookDependencies())
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

func TestNewSkillsHookRunCmdIsHiddenAndAlwaysSucceeds(t *testing.T) {
	t.Parallel()
	command := newSkillsHookRunCmd(testHookDependencies())
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
