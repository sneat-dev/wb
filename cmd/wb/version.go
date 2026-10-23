package main

import (
	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/cli/cmdversion"
	"github.com/spf13/cobra"
	"io"
)

type versionInfo = buildinfo.Report

func newVersionCmd() *cobra.Command { return cmdversion.New(buildinfo.Snapshot) }

// Bare version requests bypass Cobra and all invocation side effects.
func printBareVersion(out io.Writer) int {
	if err := cmdversion.WriteBare(out, buildinfo.Version()); err != nil {
		return exitFindings
	}
	return exitOK
}

func collectVersion() versionInfo { return buildinfo.Snapshot() }
