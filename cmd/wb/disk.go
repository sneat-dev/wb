package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/disk"
)

type diskOptions struct {
	format    string
	skipSizes bool
	minimum   float64
}

func newDiskCmd(inv *invocation) *cobra.Command {
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
				return &exitError{code: exitUsage, message: fmt.Sprintf("unsupported --format %q: use text, json, or yaml", options.format)}
			}
			if options.minimum < 0 || options.minimum > 1 {
				return &exitError{code: exitUsage, message: "--minimum-available must be between 0 and 1"}
			}

			report, err := disk.Collect(cmd.Context(), disk.Options{
				ProjectsRoot:          inv.projectsRoot,
				SkipSizes:             options.skipSizes,
				MinimumAvailableRatio: options.minimum,
			})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			switch options.format {
			case "json":
				encoder := json.NewEncoder(out)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(report); err != nil {
					return err
				}
			case "yaml":
				encoder := yaml.NewEncoder(out)
				if err := encoder.Encode(report); err != nil {
					_ = encoder.Close()
					return err
				}
				if err := encoder.Close(); err != nil {
					return err
				}
			default:
				if _, err := fmt.Fprint(out, disk.Render(report)); err != nil {
					return err
				}
			}

			if len(report.Findings) > 0 {
				return &exitError{code: exitFindings, message: "wb disk reported findings; see the report above"}
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
