package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/discover"
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

func newFleetPRsCmd() *cobra.Command {
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
			owners, diagnostics := resolvePRInventoryOwners(extraOrgs)
			report := prinventory.Inventory(cmd.Context(), prinventory.Options{Owners: owners,
				ExcludeArchived: options.excludeArchived, CreatedBefore: options.createdBefore,
				Parallel: options.parallel})
			report.Diagnostics = append(report.Diagnostics, diagnostics...)
			if len(diagnostics) > 0 {
				report.Complete = false
			}
			if err := writePRInventoryOutput(cmd, report, options.format, options.reportDir); err != nil {
				return err
			}
			if !report.Complete {
				return &exitError{code: exitFindings, message: "pull-request inventory is partial; see diagnostics"}
			}
			return nil
		},
	}
	command.Flags().StringVar(&options.format, "format", options.format, "output format: markdown or json")
	command.Flags().StringVar(&options.reportDir, "report-dir", "", "write pull-request-inventory.md and .json")
	command.Flags().StringVar(&options.createdBefore, "created-before", "", "immutable RFC3339 cutoff; include PRs created strictly before it")
	command.Flags().BoolVar(&options.excludeArchived, "exclude-archived", false, "explicitly exclude archived repositories (included by default)")
	command.Flags().IntVar(&options.parallel, "parallel", options.parallel, "maximum owners queried concurrently")
	setDiscoveryTerms(command, "fleet remote pull request PR GitHub inventory owner archived archive cutoff JSON Markdown")
	return command
}

func resolvePRInventoryOwners(extra []string) ([]prinventory.Owner, []prinventory.Diagnostic) {
	seen := map[string]bool{}
	owners := make([]prinventory.Owner, 0)
	diagnostics := make([]prinventory.Diagnostic, 0)
	add := func(login, qualifier string) {
		login = strings.TrimSpace(login)
		if login == "" {
			return
		}
		key := qualifier + ":" + strings.ToLower(login)
		if seen[key] {
			return
		}
		seen[key] = true
		owners = append(owners, prinventory.Owner{Login: login, Qualifier: qualifier})
	}
	if user, err := discover.AuthUser(); err == nil {
		add(user, "user")
	} else {
		diagnostics = append(diagnostics, prinventory.Diagnostic{Severity: "error", Message: "could not discover authenticated GitHub user: " + err.Error()})
	}
	if orgs, err := discover.MemberOrgs(); err == nil {
		for _, org := range orgs {
			add(org, "org")
		}
	} else {
		diagnostics = append(diagnostics, prinventory.Diagnostic{Severity: "error", Message: "could not discover GitHub organizations: " + err.Error()})
	}
	for _, org := range extra {
		add(org, "org")
	}
	result := make([]prinventory.Owner, 0, len(owners))
	for _, owner := range owners {
		if owner.Login != "" {
			result = append(result, owner)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].String() < result[j].String() })
	return result, diagnostics
}

func writePRInventoryOutput(cmd *cobra.Command, report prinventory.Report, format, reportDir string) error {
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
		if err := os.MkdirAll(reportDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "pull-request-inventory.json"), jsonBytes, 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(reportDir, "pull-request-inventory.md"), markdown, 0o644); err != nil {
			return err
		}
	}
	if format == "json" {
		_, err = cmd.OutOrStdout().Write(jsonBytes)
	} else {
		_, err = cmd.OutOrStdout().Write(markdown)
	}
	return err
}
