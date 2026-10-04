package main

import (
	"github.com/sneat-dev/wb/internal/cli/cmdsync"
	"github.com/sneat-dev/wb/internal/syncreport"
	"github.com/sneat-dev/wb/internal/syncrun"
	"github.com/spf13/cobra"
)

func newSyncReportCmd(inv *invocation) *cobra.Command {
	return cmdsync.NewReport(newCLIRuntime(inv), cmdsync.ReportOperations{Load: syncreport.LoadDirectory, Validate: syncreport.ValidateInGitDB, Publish: syncrun.PublishSyncReport}, setDiscoveryTerms)
}
