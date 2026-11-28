package main

import (
	"fmt"

	"github.com/sneat-dev/wb/internal/cli/cmdworktree"
	"github.com/sneat-dev/wb/internal/disk"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
	"github.com/spf13/cobra"
)

func newCleanCmd(inv *invocation) *cobra.Command {
	command := cmdworktree.NewClean(newCLIRuntime(inv), cmdworktree.CleanDependencies{Run: worktrees.Clean, Progress: func(command *cobra.Command, verbose bool) cmdworktree.InventoryProgress {
		p := newInventoryProgress(inv, command.ErrOrStderr(), verbose)
		return cmdworktree.InventoryProgress{Report: p.report, Detail: p.detail, Finish: p.finish}
	}, FloorNotice: func(projectsRoot string) string {
		return diskFloorNotice(projectsRoot, wbconfig.DefaultPath(), wbhome.Resolve, disk.Probe)
	}})
	setDiscoveryTerms(command, "clean clean up cleanup tidy garbage collect gc retire remove delete finished landed merged squash worktrees branches stale abandoned orphans orphaned what is safe to delete hygiene disk reclaim sweep audit")
	return command
}

// diskFloorNotice is the line wb clean prints when the projects root's
// filesystem is below disk.minimum_available: clean frees finished
// worktrees, and the caches and scratch are wb disk prune's to plan. Any
// failure to measure prints nothing, because this is advice, not a check.
func diskFloorNotice(projectsRoot, configPath string, resolve func(string) (wbhome.Resolution, error), probe func(string) (disk.Filesystem, error)) string {
	config, err := wbconfig.LoadDisk(configPath)
	if err != nil {
		return ""
	}
	resolution, err := resolve(projectsRoot)
	if err != nil {
		return ""
	}
	fs, err := probe(resolution.Root)
	if err != nil || fs.TotalBytes == 0 || fs.AvailableRatio() >= config.MinimumAvailable {
		return ""
	}
	return fmt.Sprintf("wb clean: %s is below the %.0f%% free-space floor (%.1f%% free); run wb disk prune to plan reclaiming caches and scratch",
		resolution.Root, config.MinimumAvailable*100, fs.AvailableRatio()*100)
}
