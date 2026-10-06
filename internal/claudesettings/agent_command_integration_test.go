package claudesettings_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/claudesettings"
	"github.com/sneat-dev/wb/internal/cli/cmdhooks"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/wbexec"
	"github.com/spf13/cobra"
)

func newHooksCmd(inv *testInvocation) *cobra.Command {
	return cmdhooks.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: inv.projectsRoot, Filter: inv.filterFlag} }}, cmdhooks.GitOperations{}, cmdhooks.LifecycleOperations{}, cmdhooks.AgentOperations{Home: os.UserHomeDir, Executable: wbexec.HookExecutable, MergeSettings: claudesettings.MergeAgentHook, WriteSettings: claudesettings.WriteAtomically, Quote: wbexec.QuoteShellWord})
}

type testInvocation struct{ projectsRoot, filterFlag string }

func selected(inv *testInvocation, path ...string) *cobra.Command {
	cmd := newHooksCmd(inv)
	for _, name := range path {
		for _, child := range cmd.Commands() {
			if child.Name() == name {
				cmd = child
				break
			}
		}
	}
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if parent := cmd.Parent(); parent != nil {
		parent.RemoveCommand(cmd)
	}
	return cmd
}

func newHooksAgentInstallCmd() *cobra.Command { return selected(&testInvocation{}, "agent", "install") }

func TestAgentInstallPreviewThenApplyPreservesOtherSettingsAndIsIdempotent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark","hooks":{"SessionStart":[{"matcher":"Bash"}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	preview := newHooksAgentInstallCmd()
	var previewOut bytes.Buffer
	preview.SetOut(&previewOut)
	preview.SetArgs([]string{"--settings", path, "--dry-run"})
	if err := preview.Execute(); err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(previewOut.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["theme"] != "dark" {
		t.Fatalf("preview lost unrelated setting: %s", previewOut.String())
	}
	if _, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "PreToolUse") {
		t.Fatal("dry run changed settings")
	}

	apply := newHooksAgentInstallCmd()
	var appliedOut bytes.Buffer
	apply.SetOut(&appliedOut)
	apply.SetArgs([]string{"--settings", path})
	if err := apply.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(appliedOut.String(), "registered") {
		t.Fatalf("apply output = %q", appliedOut.String())
	}
	installed, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(installed), bytes.TrimSpace(previewOut.Bytes())) {
		t.Fatalf("applied settings differ from preview\n%s\n%s", installed, previewOut.Bytes())
	}

	again := newHooksAgentInstallCmd()
	var againOut bytes.Buffer
	again.SetOut(&againOut)
	again.SetArgs([]string{"--settings", path})
	if err := again.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(againOut.String(), "already registered") {
		t.Fatalf("repeat output = %q", againOut.String())
	}
	repeated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(installed, repeated) {
		t.Fatal("repeat install modified settings")
	}
}
func TestAgentInstallRejectsMalformedSettingsWithoutReplacingThem(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	invalid := []byte(`{"hooks":`)
	if err := os.WriteFile(path, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newHooksAgentInstallCmd()
	cmd.SetArgs([]string{"--settings", path})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "parse "+path) {
		t.Fatalf("malformed settings error = %v", err)
	}
	still, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(still, invalid) {
		t.Fatalf("malformed settings replaced with %q", still)
	}
}
