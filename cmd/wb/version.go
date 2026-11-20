package main

import (
	"context"
	"io"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/cli/cmdversion"
	"github.com/sneat-dev/wb/internal/stalewb"
	"github.com/spf13/cobra"
)

type versionInfo = buildinfo.Report

func newVersionCmd() *cobra.Command {
	return cmdversion.NewWithStale(buildinfo.Snapshot, func(ctx context.Context, current string) []stalewb.Binary {
		if stalewb.Disabled() {
			return nil
		}
		return stalewb.New().Stale(ctx, current)
	})
}

// Bare version requests bypass Cobra and all invocation side effects.
func printBareVersion(out io.Writer) int {
	if err := cmdversion.WriteBare(out, buildinfo.Version()); err != nil {
		return exitFindings
	}
	return exitOK
}

func collectVersion() versionInfo { return buildinfo.Snapshot() }
