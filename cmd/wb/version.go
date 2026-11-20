package main

import (
	"io"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/cli/cmdversion"
	"github.com/sneat-dev/wb/internal/stalewb"
	"github.com/spf13/cobra"
)

type versionInfo = buildinfo.Report

func newVersionCmd() *cobra.Command {
	return cmdversion.NewWithStale(buildinfo.Snapshot, stalewb.Scan)
}

// Bare version requests bypass Cobra and all invocation side effects.
func printBareVersion(out io.Writer) int {
	if err := cmdversion.WriteBare(out, buildinfo.Version()); err != nil {
		return exitFindings
	}
	return exitOK
}

func collectVersion() versionInfo { return buildinfo.Snapshot() }
