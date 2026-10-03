package main

import (
	"github.com/sneat-dev/wb/internal/cli/statusview"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/repostatus"
	"github.com/spf13/cobra"
)

// Remaining fleet rollups consume these exact domain DTOs during their cutover.
type statusIndex = repostatus.Index
type repositoryStatusInfo = repostatus.Row

func newStatusCmd(inv *invocation) *cobra.Command {
	return statusview.NewHistorical(newCLIRuntime(inv), statusCommandDependencies())
}

// runStatusTargets projects the remaining fleet's private selected targets once.
func runStatusTargets(targets []qualityTarget, parallel int) []repositoryStatusInfo {
	selected := make([]reposelection.Target, len(targets))
	for i, target := range targets {
		selected[i] = reposelection.Target{Repository: target.repository, Path: target.path}
	}
	return repostatus.InspectTargets(selected, parallel, nil)
}

func statusOptionsForFleet(options statusview.Options) qualityOptions {
	return qualityOptions{fleet: true, parallel: options.Parallel, match: options.Match,
		regex: options.Regex, format: options.Format, reportDir: options.ReportDir}
}
