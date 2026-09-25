package main

import (
	"testing"

	"github.com/strongo/cli-helpers/skillsync"

	"github.com/sneat-dev/wb/internal/session"
)

// AC: cov-rwi-03 unit03 seam list, cmd/wb/session_register.go sessionLabel.
// sessionLabel picks the most specific human-readable name it can from a
// Record: Runtime+native ID, Runtime alone, native ID alone (falling back
// from NativeHarnessID to the legacy AgentID), or a fixed placeholder when
// the record carries neither.
func TestSessionLabelPrefersRuntimeAndNativeHarnessID(t *testing.T) {
	t.Parallel()
	got := sessionLabel(session.Record{Runtime: "claude-code", NativeHarnessID: "abc123"})
	if want := "claude-code/abc123"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
}

func TestSessionLabelFallsBackToLegacyAgentIDWhenNativeHarnessIDIsEmpty(t *testing.T) {
	t.Parallel()
	got := sessionLabel(session.Record{Runtime: "codex", AgentID: "legacy-9"})
	if want := "codex/legacy-9"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
}

func TestSessionLabelUsesRuntimeAloneWhenNoNativeIDIsKnown(t *testing.T) {
	t.Parallel()
	got := sessionLabel(session.Record{Runtime: "copilot"})
	if want := "copilot"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
}

func TestSessionLabelUsesNativeIDAloneWhenRuntimeIsEmpty(t *testing.T) {
	t.Parallel()
	got := sessionLabel(session.Record{NativeHarnessID: "xyz"})
	if want := "xyz"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
}

func TestSessionLabelIsAnUnnamedSessionWithNeitherRuntimeNorID(t *testing.T) {
	t.Parallel()
	got := sessionLabel(session.Record{})
	if want := "an unnamed session"; got != want {
		t.Errorf("sessionLabel = %q, want %q", got, want)
	}
}

// AC: cov-rwi-03 unit03 seam list, cmd/wb/skills.go syncedSkillsWBVersion.
// It reports the wb version that last synced a harness's skills directory,
// reading it out of the plugin-scoped supplier-version map skillsync.Status
// carries, and reports "not installed" whenever any layer of that lookup is
// missing.
func TestSyncedSkillsWBVersionReadsThePluginSuppliedVersion(t *testing.T) {
	t.Parallel()
	plugin := wbSkillsPlugin.String()
	cli := wbSkillsCLI.String()
	status := skillsync.Status{
		Installed: true,
		Plugins:   map[string]skillsync.Source{plugin: {}},
		SupplierCLIVersions: map[string]map[string]string{
			plugin: {cli: "0.150.2"},
		},
	}
	version, installed := syncedSkillsWBVersion(status)
	if !installed || version != "0.150.2" {
		t.Errorf("syncedSkillsWBVersion = (%q, %v), want (\"0.150.2\", true)", version, installed)
	}
}

func TestSyncedSkillsWBVersionReportsNotInstalledWhenStatusSaysSo(t *testing.T) {
	t.Parallel()
	version, installed := syncedSkillsWBVersion(skillsync.Status{Installed: false})
	if installed || version != "" {
		t.Errorf("syncedSkillsWBVersion = (%q, %v), want (\"\", false)", version, installed)
	}
}

func TestSyncedSkillsWBVersionReportsNotInstalledWhenPluginIsAbsent(t *testing.T) {
	t.Parallel()
	status := skillsync.Status{Installed: true, Plugins: map[string]skillsync.Source{}}
	version, installed := syncedSkillsWBVersion(status)
	if installed || version != "" {
		t.Errorf("syncedSkillsWBVersion = (%q, %v), want (\"\", false)", version, installed)
	}
}

func TestSyncedSkillsWBVersionReportsNotInstalledWhenTheCLIVersionIsEmpty(t *testing.T) {
	t.Parallel()
	plugin := wbSkillsPlugin.String()
	status := skillsync.Status{
		Installed:           true,
		Plugins:             map[string]skillsync.Source{plugin: {}},
		SupplierCLIVersions: map[string]map[string]string{plugin: {}},
	}
	version, installed := syncedSkillsWBVersion(status)
	if installed || version != "" {
		t.Errorf("syncedSkillsWBVersion = (%q, %v), want (\"\", false)", version, installed)
	}
}
