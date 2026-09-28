package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/quality"
)

// newCoverageWorklistCmd implements `wb coverage worklist`: task-25
// (spec/plans/coverage-to-100/README.md) turns an already-measured Go
// coverage profile into the deterministic unit list a coverage-to-100 lane's
// brief carries instead of exploration. The coordinator regenerates the list
// from the latest cov/integration profile before cutting new lane units.
func newCoverageWorklistCmd() *cobra.Command {
	var (
		module   string
		unitSize int
		format   string
	)
	command := &cobra.Command{
		Use:   "worklist <coverage-profile>",
		Short: "Group a coverage profile's uncovered blocks into sized, function-whole worklist units",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return &exitError{code: exitUsage, message: fmt.Sprintf("--format must be text or json, not %q", format)}
			}
			modulePath, err := quality.ReadModulePath(module)
			if err != nil {
				return err
			}
			blocks, err := quality.ParseCoverageProfile(args[0])
			if err != nil {
				return err
			}
			// module (not an absolute form of it) is also the moduleRoot every
			// source lookup joins against: this process never changes its
			// working directory between ReadModulePath and here, so a relative
			// --module resolves the same way both times.
			worklist, err := quality.BuildWorklist(blocks, modulePath, module, unitSize)
			if err != nil {
				return err
			}
			return writeWorklistOutput(cmd.OutOrStdout(), worklist, format)
		},
	}
	command.Flags().StringVar(&module, "module", ".", "path to the Go module root (its go.mod names the module path the coverage profile uses, and its source maps blocks to functions)")
	command.Flags().IntVar(&unitSize, "unit-size", 300, "target statement count per worklist unit")
	command.Flags().StringVar(&format, "format", "text", "output format: text or json")
	return command
}

func writeWorklistOutput(out io.Writer, worklist quality.Worklist, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(worklist)
	}
	_, _ = fmt.Fprintf(out, "worklist: %d unit(s), %d uncovered statement(s) (target unit size %d)\n", len(worklist.Units), worklist.TotalUncoveredStatements, worklist.UnitSize)
	for _, unit := range worklist.Units {
		shares := ""
		if len(unit.SharesFileWith) > 0 {
			names := make([]string, len(unit.SharesFileWith))
			for i, index := range unit.SharesFileWith {
				names[i] = fmt.Sprintf("%d", index)
			}
			shares = fmt.Sprintf(" (shares a file with unit %s)", strings.Join(names, ", "))
		}
		_, _ = fmt.Fprintf(out, "  unit %d: %d statement(s), %d file(s)%s\n", unit.Index, unit.Statements, len(unit.Files), shares)
		for _, file := range unit.Files {
			_, _ = fmt.Fprintf(out, "    %s\n", file)
		}
		for _, block := range unit.Blocks {
			function := block.Function
			if function == "" {
				function = "(file-level)"
			}
			_, _ = fmt.Fprintf(out, "    %s:%d.%d,%d.%d %d stmt(s) %s\n", block.File, block.StartLine, block.StartCol, block.EndLine, block.EndCol, block.Statements, function)
		}
	}
	return nil
}
