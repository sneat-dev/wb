package cmdfleet

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/prinventory"
	"github.com/spf13/cobra"
)

type fleetPRsOptions struct {
	format          string
	reportDir       string
	createdBefore   string
	excludeArchived bool
	parallel        int
}

func NewPRs(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	options := fleetPRsOptions{format: "markdown", parallel: 8}
	command := &cobra.Command{
		Use:     "prs",
		Aliases: []string{"pr", "pull-requests"},
		Short:   "Inventory open GitHub pull requests across fleet owners",
		Long: `Inventory every open pull request visible to the authenticated
GitHub account and its organizations. Each owner is queried independently, so
the provider's ownership-filter limit cannot silently narrow the snapshot.
Archived repositories are included unless --exclude-archived is explicit.
Partial owner/API results remain visible and return exit code 1.

This command inventories remote pull requests; use wb worktree list for local
WB-managed worktrees.`,
		Example: `wb fleet prs --format json --created-before 2026-08-11T00:00:00Z
wb fleet prs --org sneat-dev --exclude-archived --report-dir reports`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			owners, diagnostics := deps.InventoryOwners(runtime.Flags().ExtraOrgs)
			report := deps.Inventory(cmd.Context(), prinventory.Options{Owners: owners,
				ExcludeArchived: options.excludeArchived, CreatedBefore: options.createdBefore,
				Parallel: options.parallel})
			report.Diagnostics = append(report.Diagnostics, diagnostics...)
			if len(diagnostics) > 0 {
				report.Complete = false
			}
			if err := writePRInventoryOutput(cmd.OutOrStdout(), deps, report, options.format, options.reportDir); err != nil {
				return err
			}
			if !report.Complete {
				return runtime.ExitError(shared.ExitFindings, "pull-request inventory is partial; see diagnostics")
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.format, "format", options.format, "output format: markdown or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write pull-request-inventory.md and .json")
	command.Flags().StringVar(&options.createdBefore, "created-before", "", "immutable RFC3339 cutoff; include PRs created strictly before it")
	command.Flags().BoolVar(&options.excludeArchived, "exclude-archived", false, "explicitly exclude archived repositories (included by default)")
	command.Flags().IntVar(&options.parallel, "parallel", options.parallel, "maximum owners queried concurrently")
	command.Annotations = map[string]string{"wb.dev/discovery-terms": "fleet remote pull request PR GitHub inventory owner archived archive cutoff JSON Markdown"}
	return command
}

func writePRInventoryOutput(out io.Writer, deps Dependencies, report prinventory.Report, format, reportDir string) error {
	if format != "markdown" && format != "json" {
		return fmt.Errorf("unsupported --format %q; use markdown or json", format)
	}
	jsonBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	jsonBytes = append(jsonBytes, '\n')
	markdown := []byte(prinventory.RenderMarkdown(report))
	if reportDir != "" {
		if err := deps.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := deps.WriteFile(filepath.Join(reportDir, "pull-request-inventory.json"), jsonBytes, 0o644); err != nil {
			return err
		}
		if err := deps.WriteFile(filepath.Join(reportDir, "pull-request-inventory.md"), markdown, 0o644); err != nil {
			return err
		}
	}
	if format == "json" {
		_, err = out.Write(jsonBytes)
	} else {
		_, err = out.Write(markdown)
	}
	return err
}
