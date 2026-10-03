package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/cmdskills"
	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/skillsync"
)

func TestMaintenanceFactoriesPreserveRootCodedErrorIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		command func() *cobra.Command
		args    []string
		code    int
	}{{"install", newInstallCmd, []string{"not-a-catalog-target"}, exitUsage}, {"upgrade", newUpgradeCmd, []string{"not-a-catalog-target"}, exitUsage}, {"skills", newSkillsCmd, []string{"sync", "--dir", "/no-work", "--harness", "cursor"}, exitUsage}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			command := tc.command()
			command.SetArgs(tc.args)
			var out bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&out)
			err := command.Execute()
			var coded *exitError
			if !errors.As(err, &coded) || coded.code != tc.code || coded.message == "" {
				t.Fatalf("err=%v coded=%+v", err, coded)
			}
		})
	}
}

func TestMaintenanceHookWiringPreservesLegacyQuoteBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, quoted string }{{"/path with spaces/wb", "'/path with spaces/wb'"}, {"/path/é/wb", "/path/é/wb"}, {"/path/a'b/wb", `'/path/a'\''b/wb'`}} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			deps := maintenanceHookDependencies()
			if actual := deps.Quote(tc.path); actual != tc.quoted {
				t.Fatalf("quote=%q want=%q", actual, tc.quoted)
			}
			deps.Executable = func() string { return tc.path }
			command := cmdskills.New(newCLIRuntime(&invocation{}), cmdskills.SyncDependencies{Config: func() (skillsync.Config, error) { return skillsync.Config{}, nil }}, deps)
			command.SetArgs([]string{"hook", "print"})
			var out bytes.Buffer
			command.SetOut(&out)
			if err := command.Execute(); err != nil {
				t.Fatal(err)
			}
			var doc map[string]any
			if err := json.NewDecoder(&out).Decode(&doc); err != nil {
				t.Fatal(err)
			}
			events := doc["hooks"].(map[string]any)["SessionStart"].([]any)
			handler := events[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
			want := fmt.Sprintf("%s skills hook run 2>/dev/null; exit 0", tc.quoted)
			if handler["command"] != want {
				t.Fatalf("handler=%v want=%q", handler, want)
			}
		})
	}
}

func TestMaintenanceSessionStartFailsOpenWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")
	command := newSkillsCmd()
	command.SetArgs([]string{"hook", "run"})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wb session register") {
		t.Fatal(out.String())
	}
}

func TestMaintenanceSessionStartUsesActualUserSettingsBinding(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	command := newSkillsCmd()
	command.SetArgs([]string{"hook", "run"})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wb session register") {
		t.Fatal(out.String())
	}
}
