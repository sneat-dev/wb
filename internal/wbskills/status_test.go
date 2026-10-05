package wbskills

import (
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/skillsync" // AC: cov-rwi-03 unit03 seam list, cmd/wb/skills.go SyncedWBVersion.
	// It reports the wb version that last synced a harness's skills directory,
	// reading it out of the plugin-scoped supplier-version map skillsync.Status
	// carries, and reports "not installed" whenever any layer of that lookup is
	// missing.
)

func TestSyncedSkillsWBVersionReadsThePluginSuppliedVersion(t *testing.T) {
	t.Parallel()
	plugin := PluginIdentity().String()
	cli := CLIIdentity().String()
	status := skillsync.Status{Installed: true, Plugins: map[string]skillsync.Source{plugin: {}}, SupplierCLIVersions: map[string]map[string]string{plugin: {cli: "0.150.2"}}}
	version, installed := SyncedWBVersion(status)
	if !installed || version != "0.150.2" {
		t.Errorf("SyncedWBVersion = (%q, %v), want (\"0.150.2\", true)", version, installed)
	}
}

func TestSyncedSkillsWBVersionReportsNotInstalledWhenStatusSaysSo(t *testing.T) {
	t.Parallel()
	version, installed := SyncedWBVersion(skillsync.Status{Installed: false})
	if installed || version != "" {
		t.Errorf("SyncedWBVersion = (%q, %v), want (\"\", false)", version, installed)
	}
}

func TestSyncedSkillsWBVersionReportsNotInstalledWhenPluginIsAbsent(t *testing.T) {
	t.Parallel()
	status := skillsync.Status{Installed: true, Plugins: map[string]skillsync.Source{}}
	version, installed := SyncedWBVersion(status)
	if installed || version != "" {
		t.Errorf("SyncedWBVersion = (%q, %v), want (\"\", false)", version, installed)
	}
}

func TestSyncedSkillsWBVersionReportsNotInstalledWhenTheCLIVersionIsEmpty(t *testing.T) {
	t.Parallel()
	plugin := PluginIdentity().String()
	status := skillsync.Status{
		Installed:           true,
		Plugins:             map[string]skillsync.Source{plugin: {}},
		SupplierCLIVersions: map[string]map[string]string{plugin: {}},
	}
	version, installed := SyncedWBVersion(status)
	if installed || version != "" {
		t.Errorf("SyncedWBVersion = (%q, %v), want (\"\", false)", version, installed)
	}
}

func TestSkillsDriftMessageNamesTheDirAndBothVersionsOrTheMissingInstall(t *testing.T) {
	t.Parallel()
	never := DriftMessage("/home/user/.claude/skills", skillsync.Status{}, "1.2.3")
	for _, want := range []string{"/home/user/.claude/skills", "wb skills sync"} {
		if !strings.Contains(never, want) {
			t.Errorf("never-installed message = %q, missing %q", never, want)
		}
	}

	plugin := PluginIdentity().String()
	drifted := DriftMessage("/home/user/.claude/skills", skillsync.Status{
		Installed: true,
		Plugins: map[string]skillsync.Source{
			plugin: {},
		},
		SupplierCLIVersions: map[string]map[string]string{
			plugin: {CLIIdentity().String(): "1.0.0"},
		},
	}, "1.2.3")
	for _, want := range []string{"1.0.0", "1.2.3", "wb skills sync"} {
		if !strings.Contains(drifted, want) {
			t.Errorf("drift message = %q, missing %q", drifted, want)
		}
	}
}
