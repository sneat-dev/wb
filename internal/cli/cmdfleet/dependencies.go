// Package cmdfleet owns fleet command arguments and presentation.
package cmdfleet

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/cli/statusview"
	"github.com/sneat-dev/wb/internal/defaultbranch"
	"github.com/sneat-dev/wb/internal/fleetinspect"
	"github.com/sneat-dev/wb/internal/mergepolicy"
	"github.com/sneat-dev/wb/internal/prinventory"
	"github.com/spf13/cobra"
)

type Dependencies struct {
	Overview        func(context.Context, fleetinspect.Request) (fleetinspect.OverviewReport, error)
	Stats           func(context.Context, fleetinspect.Request) (fleetinspect.StatsReport, error)
	Inventory       func(context.Context, prinventory.Options) prinventory.Report
	InventoryOwners func([]string) ([]prinventory.Owner, []prinventory.Diagnostic)
	MergePolicy     func(context.Context, mergepolicy.Request, io.Writer) (mergepolicy.Report, error)
	DefaultBranch   func(context.Context, defaultbranch.Request, io.Writer) (defaultbranch.Report, error)
	Status          statusview.Dependencies
	Budget          func() int
	MkdirAll        func(string, os.FileMode) error
	WriteFile       func(string, []byte, os.FileMode) error
}

func requestedOwners(runtime shared.Runtime, command *cobra.Command, owners []string) []string {
	selected := append([]string(nil), owners...)
	if org := command.Root().PersistentFlags().Lookup("org"); org != nil && org.Changed {
		selected = append(selected, runtime.Flags().ExtraOrgs...)
	}
	return selected
}
func usage(runtime shared.Runtime, message string) error {
	return runtime.ExitError(shared.ExitUsage, message)
}
func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
