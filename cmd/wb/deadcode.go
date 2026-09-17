package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/quality"
)

type deadcodeOptions struct {
	baseline       string
	updateBaseline bool
	filter         string
	generated      bool
	format         string
	patterns       []string
	timeout        time.Duration
	noBaseline     bool
}

func newDeadcodeCmd() *cobra.Command {
	options := deadcodeOptions{timeout: 10 * time.Minute}
	command := &cobra.Command{
		Use:   "deadcode [repository-path]",
		Short: "Report functions no path from main can reach, against a committed baseline",
		Long: strings.TrimSpace(`
Report functions that no path from any main package can reach.

A missing mechanism is loud: nothing compiles, a test fails. A mechanism that
is present but unreachable is silent — it can be added without ever being
wired in, or quietly unwired later, and every other signal stays green. Tests
do not catch it, because the test is then the only caller.

The analysis is golang.org/x/tools/cmd/deadcode, which builds a call graph
from each main package using Rapid Type Analysis. RTA over-approximates what
is reachable, so a function it reports is genuinely unreachable, apart from
reflection and //go:linkname. Errors fall on the side of reporting too little,
which is what makes this safe to gate on.

Existing unreachable code is tolerated through a baseline, so the gate can be
switched on in a repository that is not already clean. Only findings absent
from the baseline fail. Removing dead code, or giving it a caller, shrinks the
baseline and can never fail.

  wb deadcode                        # gate against ` + quality.DefaultDeadcodeBaseline + `
  wb deadcode --update-baseline      # record today's findings as tolerated
  wb deadcode --no-baseline          # report everything, gate on nothing

Exit codes: 0 nothing new, 1 new unreachable functions, 2 bad invocation.
`),
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			repositoryPath := "."
			if len(args) == 1 {
				repositoryPath = args[0]
			}
			absolute, err := filepath.Abs(repositoryPath)
			if err != nil {
				return &exitError{code: exitUsage, message: fmt.Sprintf("resolve repository path %q: %v", repositoryPath, err)}
			}
			if options.noBaseline && options.updateBaseline {
				return &exitError{code: exitUsage, message: "--no-baseline and --update-baseline are mutually exclusive"}
			}
			switch options.format {
			case "text", "json", "yaml":
			default:
				return &exitError{code: exitUsage, message: fmt.Sprintf("unsupported --format %q: use text, json, or yaml", options.format)}
			}

			baseline := options.baseline
			if options.noBaseline {
				baseline = ""
			}
			report, err := quality.Deadcode(cmd.Context(), absolute, quality.DeadcodeOptions{
				Patterns:         options.patterns,
				BaselinePath:     baseline,
				Filter:           options.filter,
				IncludeGenerated: options.generated,
				Timeout:          options.timeout,
			})
			if err != nil {
				return err
			}

			if options.updateBaseline {
				path := options.baseline
				if !filepath.IsAbs(path) {
					path = filepath.Join(absolute, path)
				}
				if err := quality.WriteDeadcodeBaseline(path, report.Findings); err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "recorded %d unreachable function(s) in %s\n", len(report.Findings), options.baseline)
				return nil
			}

			if err := writeDeadcodeReport(cmd, options.format, report); err != nil {
				return err
			}
			if len(report.New) > 0 {
				return &exitError{code: exitFindings, message: fmt.Sprintf(
					"%d function(s) are unreachable from main and are not in %s; wire them up, delete them, or record them with --update-baseline",
					len(report.New), options.baseline)}
			}
			return nil
		},
	}

	command.Flags().StringVar(&options.baseline, "baseline", quality.DefaultDeadcodeBaseline, "Repository-relative baseline of tolerated findings")
	command.Flags().BoolVar(&options.updateBaseline, "update-baseline", false, "Rewrite the baseline from this run instead of gating")
	command.Flags().BoolVar(&options.noBaseline, "no-baseline", false, "Report every finding and never fail")
	command.Flags().StringVar(&options.filter, "filter", "", "Only report packages matching this regular expression (default: the analyzed module)")
	command.Flags().BoolVar(&options.generated, "generated", false, "Include dead functions in generated files")
	command.Flags().StringVar(&options.format, "format", "text", "Stdout format: text, json, or yaml")
	command.Flags().StringSliceVar(&options.patterns, "packages", nil, "Main packages to analyze (default ./...)")
	command.Flags().DurationVar(&options.timeout, "timeout", 10*time.Minute, "Maximum analysis duration (0 disables)")
	return command
}

// writeDeadcodeReport renders the verdict. Text mode leads with what fails the
// gate, because that is the only part a caller must act on.
func writeDeadcodeReport(cmd *cobra.Command, format string, report quality.DeadcodeReport) error {
	out := cmd.OutOrStdout()
	switch format {
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	case "yaml":
		encoder := yaml.NewEncoder(out)
		defer func() { _ = encoder.Close() }()
		return encoder.Encode(report)
	}

	if len(report.New) > 0 {
		fmt.Fprintf(out, "New unreachable functions (%d):\n", len(report.New))
		for _, finding := range report.New {
			fmt.Fprintf(out, "  %s:%d: %s\n", finding.File, finding.Line, finding.Identity)
		}
	}
	if len(report.Fixed) > 0 {
		fmt.Fprintf(out, "\nBaseline entries now reachable or gone (%d) — rerun with --update-baseline to drop them:\n", len(report.Fixed))
		for _, identity := range report.Fixed {
			fmt.Fprintf(out, "  %s\n", identity)
		}
	}
	if report.BaselineMissing && report.BaselinePath != "" {
		fmt.Fprintf(out, "\nNo baseline at %s: every finding counts as new. Record the starting point with --update-baseline.\n", report.BaselinePath)
	}
	if len(report.New) == 0 && len(report.Fixed) == 0 {
		fmt.Fprintf(out, "%d unreachable function(s), all baselined; nothing new.\n", len(report.Findings))
	}
	return nil
}
