package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/layout"
)

func newLayoutCmd() *cobra.Command {
	command := &cobra.Command{
		Use:   "layout",
		Short: "Audit and clean local clone placement under --projects-root",
		Long: `Inspect whether local clones follow {owner}/{repository} under --projects-root.

  wb layout audit   report top-level, misowned, and ok checkouts
  wb layout clean   remove safe top-level duplicates (dry-run by default)

Canonical fleet members are owner/repository directories with a real .git
directory. Linked worktrees are ignored.`,
	}
	command.AddCommand(newLayoutAuditCmd())
	command.AddCommand(newLayoutCleanCmd())
	return command
}

func newLayoutAuditCmd() *cobra.Command {
	var format, reportDir string
	command := &cobra.Command{
		Use:   "audit",
		Short: "Report non-canonical clone placement under --projects-root",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := layout.Audit(cmd.Context(), projectsRoot)
			if err != nil {
				return err
			}
			if reportDir != "" {
				if err := writeLayoutAuditReports(reportDir, report); err != nil {
					return err
				}
			}
			if err := writeLayoutOutput(cmd, format, report.Markdown(), report); err != nil {
				return err
			}
			if layout.Failed(report) {
				return &exitError{
					code:    exitFindings,
					message: "layout findings reported; see the audit above",
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&reportDir, "report-dir", "", "write layout-audit.md/.yaml/.json to this directory")
	return command
}

func newLayoutCleanCmd() *cobra.Command {
	var (
		format, reportDir     string
		apply                 bool
		allowMissingCanonical bool
	)
	command := &cobra.Command{
		Use:   "clean",
		Short: "Remove safe top-level clones under --projects-root",
		Long: `Remove Git checkouts that sit directly under --projects-root when it is safe.

Safety requires a usable origin, a clean working tree (no dirty/stash/unpushed
state), and a canonical {owner}/{repository} clone unless
--allow-missing-canonical is set. Default mode is dry-run; pass --apply to
delete.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			report, err := layout.Clean(cmd.Context(), projectsRoot, layout.CleanOptions{
				Apply:                 apply,
				AllowMissingCanonical: allowMissingCanonical,
			})
			if err != nil {
				return err
			}
			if reportDir != "" {
				if err := writeLayoutCleanReports(reportDir, report); err != nil {
					return err
				}
			}
			if err := writeLayoutOutput(cmd, format, report.Markdown(), report); err != nil {
				return err
			}
			if layout.CleanFailed(report) {
				return &exitError{
					code:    exitFindings,
					message: "layout clean reported errors; see the actions above",
				}
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "remove safe top-level clones (default is dry-run)")
	command.Flags().BoolVar(&allowMissingCanonical, "allow-missing-canonical", false, "allow removing a clean top-level clone even when the canonical path does not exist")
	command.Flags().StringVar(&format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&reportDir, "report-dir", "", "write layout-clean.md/.yaml/.json to this directory")
	return command
}

func writeLayoutOutput(cmd *cobra.Command, format, markdown string, value any) error {
	switch format {
	case "markdown":
		_, err := fmt.Fprint(cmd.OutOrStdout(), markdown)
		return err
	case "yaml":
		raw, err := yaml.Marshal(value)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func writeLayoutAuditReports(directory string, report layout.Report) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "layout-audit.md"), []byte(report.Markdown()), 0o644); err != nil {
		return err
	}
	raw, err := yaml.Marshal(report)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "layout-audit.yaml"), raw, 0o644); err != nil {
		return err
	}
	raw, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "layout-audit.json"), append(raw, '\n'), 0o644)
}

func writeLayoutCleanReports(directory string, report layout.CleanReport) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "layout-clean.md"), []byte(report.Markdown()), 0o644); err != nil {
		return err
	}
	raw, err := yaml.Marshal(report)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "layout-clean.yaml"), raw, 0o644); err != nil {
		return err
	}
	raw, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, "layout-clean.json"), append(raw, '\n'), 0o644)
}
