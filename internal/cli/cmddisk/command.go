// Package cmddisk binds disk-report arguments to the disk collection operation.
package cmddisk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/disk"
)

type diskOptions struct {
	format    string
	skipSizes bool
	minimum   float64
}

// Dependencies contains the disk-report operation supplied by composition.
type Dependencies struct {
	Collect func(context.Context, disk.Options) (disk.Report, error)
}

// New constructs a fresh disk command with invocation policy read at execution.
func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	options := diskOptions{format: "text"}
	command := &cobra.Command{
		Use:   "disk",
		Short: "Report where WB's bytes are and how much headroom is left",
		Long: strings.TrimSpace(`
Report where the bytes WB causes to exist actually are, and how much room is
left for the next one.

On 2026-09-17 this fleet's workstation reached 101 MB free of 150 GB. The first
symptom was not a warning but a Go build failing in the linker with "no space
left on device". Nothing reported the growth because nothing measured it: WB
counted repositories, worktrees and attention, and no bytes at all.

The split matters more than the total. On that machine worktrees held 340 MB
across the entire fleet, one shared Go build cache held 22 GB, and per-task
scratch under the temporary directory held 46 GB across roughly 2,500
directories whose owning tasks had long finished. Anyone reasoning from
"worktrees are the big thing WB creates" would have cleaned the wrong 0.2%.

Two figures are reported per category, because one would mislead:

  RECLAIM   what removing it would actually give back
  APPARENT  what the trees look like measured on their own

They differ sharply. Git worktrees share objects with their canonical clone and
pnpm hard-links every store entry into every consumer, so an apparent size
promises a reclaim that deleting cannot deliver. Every category is measured in
one accounting walk, so content linked into two of them is counted once.

This command only reports. Retiring a worktree is wb worktree gc, which knows
what holds unlanded work; nothing here deletes anything.

Exit codes: 0 nothing to flag, 1 findings, 2 usage.
`),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch options.format {
			case "text", "json", "yaml":
			default:
				return runtime.ExitError(shared.ExitUsage, fmt.Sprintf("unsupported --format %q: use text, json, or yaml", options.format))
			}
			if options.minimum < 0 || options.minimum > 1 {
				return runtime.ExitError(shared.ExitUsage, "--minimum-available must be between 0 and 1")
			}

			report, err := deps.Collect(cmd.Context(), disk.Options{
				ProjectsRoot:          runtime.Flags().ProjectsRoot,
				SkipSizes:             options.skipSizes,
				MinimumAvailableRatio: options.minimum,
			})
			if err != nil {
				return err
			}

			if err := writeReport(cmd.OutOrStdout(), options.format, report); err != nil {
				return err
			}

			if len(report.Findings) > 0 {
				return runtime.ExitError(shared.ExitFindings, "wb disk reported findings; see the report above")
			}
			return nil
		},
	}

	command.Flags().StringVar(&options.format, "format", "text", "Stdout format: text, json, or yaml")
	command.Flags().BoolVar(&options.skipSizes, "skip-sizes", false, "Report roots and filesystem headroom without walking trees")
	command.Flags().Float64Var(&options.minimum, "minimum-available", disk.DefaultMinimumAvailableRatio,
		"Raise a finding when available space falls below this share of the volume")
	return command
}

func writeReport(out io.Writer, format string, report disk.Report) error {
	switch format {
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	case "yaml":
		encoder := yaml.NewEncoder(out)
		encodeErr := encoder.Encode(report)
		closeErr := encoder.Close()
		if encodeErr != nil {
			return encodeErr
		}
		return closeErr
	default:
		_, err := fmt.Fprint(out, disk.Render(report))
		return err
	}
}
