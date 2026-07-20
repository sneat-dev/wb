package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/ciaudit"
	"github.com/sneat-dev/wb/internal/discover"
)

func newCICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Inspect and validate CI/CD policy",
	}
	cmd.AddCommand(newCIAuditCmd())
	return cmd
}

func newCIAuditCmd() *cobra.Command {
	var (
		fleetMode bool
		strict    bool
		jsonOut   bool
	)
	cmd := &cobra.Command{
		Use:   "audit [repository-path]",
		Short: "Check coverage gates and build-artifact promotion",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "."
			if len(args) == 1 {
				path = args[0]
			}
			code, err := runCIAudit(path, projectsRoot, filterFlag, fleetMode, strict, jsonOut)
			if err != nil {
				return err
			}
			if code != 0 {
				return &exitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fleetMode, "fleet", false, "audit every local repository under --projects-root")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero when policy findings exist")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON")
	return cmd
}

type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("CI policy audit failed (exit %d)", e.code) }

func runCIAudit(path, root, filter string, fleetMode, strict, jsonOut bool) (int, error) {
	paths := []string{path}
	if fleetMode {
		repos, err := discover.ScanLocal(root)
		if err != nil {
			return 1, err
		}
		paths = paths[:0]
		for _, repo := range repos {
			if filter != "" && !strings.Contains(repo.Slug(), filter) {
				continue
			}
			paths = append(paths, repo.Path)
		}
	}

	reports := make([]ciaudit.Report, 0, len(paths))
	for _, repoPath := range paths {
		absolute, err := filepath.Abs(repoPath)
		if err != nil {
			return 1, err
		}
		report, err := ciaudit.Audit(absolute)
		if err != nil {
			return 1, err
		}
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].Path < reports[j].Path })

	if jsonOut {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(reports); err != nil {
			return 1, err
		}
	} else {
		printCIAudit(reports)
	}

	findings := 0
	for _, report := range reports {
		findings += len(report.Findings)
	}
	if strict && findings > 0 {
		return 1, nil
	}
	return 0, nil
}

func printCIAudit(reports []ciaudit.Report) {
	for _, report := range reports {
		fmt.Println(report.Path)
		if !report.HasGo && !report.HasFrontend && !report.HasDeploy {
			fmt.Println("  – no Go/frontend/deploy CI policy applies")
			continue
		}
		if report.HasGo && report.GoCoverageThreshold {
			fmt.Println("  ✓ Go coverage threshold")
		}
		if report.HasFrontend && report.FrontendCoverageThreshold {
			fmt.Println("  ✓ frontend coverage threshold")
		}
		if report.HasDeploy && report.ArtifactPromotion {
			fmt.Println("  ✓ deploys promote verified build artifacts")
		}
		for _, finding := range report.Findings {
			where := ""
			if finding.File != "" {
				where = " (" + finding.File + ")"
			}
			fmt.Printf("  ✗ %s: %s%s\n", finding.Code, finding.Message, where)
		}
	}
}
