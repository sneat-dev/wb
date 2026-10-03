package claudesettings_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/wbskills"
	"github.com/strongo/cli-helpers/skillsync"

	"github.com/sneat-dev/wb/internal/claudesettings"
	"github.com/sneat-dev/wb/internal/cli/cmdskills"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func settingsHookCommand() *cobra.Command {
	command := cmdskills.New(shared.Runtime{}, cmdskills.SyncDependencies{Config: func() (skillsync.Config, error) {
		return wbskills.Config(fstest.MapFS{"skills/fixture/SKILL.md": {Data: []byte("fixture")}}, buildinfo.Report{Version: "1.2.3"})
	}}, cmdskills.HookDependencies{
		Quote: shared.ShellQuoteArg, Executable: func() string { return "/verified/wb" },
		MergeSettings: claudesettings.MergeSessionStart, WriteSettings: claudesettings.WriteAtomically,
	})
	command.SetArgs([]string{"hook", "install"})
	return command
}
func TestNewSkillsHookInstallCmdWritesThenReportsAlreadyRegistered(t *testing.T) {
	settings := filepath.Join(t.TempDir(), "settings.json")

	first := settingsHookCommand()
	first.SetArgs([]string{"hook", "install", "--settings", settings})
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

	second := settingsHookCommand()
	second.SetArgs([]string{"hook", "install", "--settings", settings})
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

	command := settingsHookCommand()
	command.SetArgs([]string{"hook", "install", "--settings", settings, "--dry-run"})
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
