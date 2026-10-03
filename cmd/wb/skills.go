package main

import (
	"os"

	"github.com/sneat-dev/wb/ai"
	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/claudesettings"
	"github.com/sneat-dev/wb/internal/cli/cmdskills"
	"github.com/sneat-dev/wb/internal/wbskills"
	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/skillsync"
	skillscmd "github.com/strongo/cli-helpers/skillsync/cobracmd"
)

func newSkillsCmd() *cobra.Command {
	return cmdskills.New(newCLIErrorRuntime(), cmdskills.SyncDependencies{Config: func() (skillsync.Config, error) { return wbskills.Config(ai.SkillsFS, buildinfo.Snapshot()) }, Home: os.UserHomeDir, Getenv: os.Getenv}, maintenanceHookDependencies())
}

func maintenanceHookDependencies() cmdskills.HookDependencies {
	return cmdskills.HookDependencies{Executable: hookExecutable, Quote: shellQuote, Home: os.UserHomeDir, MergeSettings: claudesettings.MergeSessionStart, WriteSettings: claudesettings.WriteAtomically, Announcement: func() string {
		return wbskills.Announcement(wbskills.AnnouncementDependencies{SkillsDir: func() (string, error) {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			return skillscmd.DefaultHarnesses[0].SkillsDir(home, os.Getenv), nil
		}, ReadStatus: skillsync.ReadStatus, Version: buildinfo.Version})
	}}
}
