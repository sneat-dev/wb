package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

type coverageDeps struct {
	openStore func(context.Context) (hub.RepositoryCoverageStore, io.Closer, error)
	originURL func(string) (string, error)
}

var defaultCoverageDeps = coverageDeps{
	openStore: openDefaultCoverageStore,
	originURL: gitops.OriginURL,
}

func openDefaultCoverageStore(ctx context.Context) (hub.RepositoryCoverageStore, io.Closer, error) {
	cfg, found, err := hubconfig.Load(wbconfig.DefaultPath())
	if err == nil && found && cfg.Store.Engine != "" {
		db, closer, err := hubstore.Open(ctx, cfg.Store)
		if err == nil {
			return hub.NewRepositoryCoverageStore(db), closer, nil
		}
	}

	defaultInGitDBPath := hubconfig.DefaultStorePath()
	if defaultInGitDBPath != "" {
		if info, err := os.Stat(defaultInGitDBPath); err == nil && info.IsDir() {
			db, closer, err := hubstore.Open(ctx, hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: defaultInGitDBPath})
			if err == nil {
				return hub.NewRepositoryCoverageStore(db), closer, nil
			}
		}
	}

	return nil, nopCloser{}, errors.New("coverage store unavailable: configure hub in ~/.config/wb/wb.yaml or initialize inGitDB at ~/.wb/hub")
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

func newFleetCoverageCmd(inv *invocation) *cobra.Command {
	var (
		format    string
		reportDir string
		match     string
		regex     string
	)
	command := &cobra.Command{
		Use:   "coverage",
		Short: "Inspect latest test coverage across the fleet without running tests",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			options := qualityOptions{
				fleet:     true,
				format:    format,
				reportDir: reportDir,
				match:     match,
				regex:     regex,
			}
			return runCICoverage(cmd, "", options)
		},
	}
	command.Flags().StringVar(&format, "format", "markdown", "stdout format: markdown, yaml, or json")
	command.Flags().StringVar(&reportDir, "report-dir", "", "write coverage.md and coverage.yaml to this directory")
	command.Flags().StringVar(&match, "match", "", "fleet glob matched against org/repo, e.g. sneat-co/*")
	command.Flags().StringVar(&regex, "regex", "", "fleet regular expression matched against org/repo")
	return command
}

func resolveRepositorySlug(path string) string {
	target := strings.TrimSpace(path)
	if target == "" || target == "." {
		target = "."
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		originFn := defaultCoverageDeps.originURL
		if originFn == nil {
			originFn = gitops.OriginURL
		}
		if origin, err := originFn(target); err == nil {
			if parsed, err := gitremote.Parse(origin); err == nil && parsed.Identity.Repository != "" {
				return parsed.Identity.Repository
			}
		}
		if abs, err := filepath.Abs(target); err == nil {
			return filepath.Base(abs)
		}
	}
	return target
}

func runCICoverage(cmd *cobra.Command, path string, options qualityOptions) error {
	ctx := cmd.Context()
	store, closer, err := defaultCoverageDeps.openStore(ctx)
	if err != nil {
		return err
	}
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}

	out := cmd.OutOrStdout()

	if options.fleet {
		records, err := store.ListCoverage(ctx)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			_, _ = fmt.Fprintln(out, "no CI coverage reports collected yet")
			return nil
		}
		var expression *regexp.Regexp
		if options.regex != "" {
			compiled, err := regexp.Compile(options.regex)
			if err != nil {
				return fmt.Errorf("invalid --regex: %w", err)
			}
			expression = compiled
		}
		filtered := make([]hub.StoredRepositoryCoverage, 0, len(records))
		for _, r := range records {
			repoSlug := strings.TrimPrefix(r.Repository, "github.com/")
			if matchesQualityTarget(repoSlug, "", options.match, expression) || matchesQualityTarget(r.Repository, "", options.match, expression) {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("no CI coverage reports match the selected filters")
		}
		report := coverageReportFromStored(filtered, "")
		if err := writeCoverageOutputTo(out, report, options.format, options.reportDir); err != nil {
			return err
		}
		return coverageGateError(report, options.minimumCoverage)
	}

	targetRepo := resolveRepositorySlug(path)
	targetRepo = strings.TrimPrefix(targetRepo, "github.com/")
	record, found, err := store.GetCoverage(ctx, targetRepo)
	if err != nil {
		return err
	}
	if !found {
		if !strings.Contains(targetRepo, "/") {
			allRecords, listErr := store.ListCoverage(ctx)
			if listErr == nil {
				targetLower := strings.ToLower(targetRepo)
				for _, r := range allRecords {
					rLower := strings.ToLower(strings.TrimPrefix(r.Repository, "github.com/"))
					if rLower == targetLower || strings.HasSuffix(rLower, "/"+targetLower) {
						record = r
						found = true
						break
					}
				}
			}
		}
	}
	if !found {
		return fmt.Errorf("no CI coverage found for repository %s", targetRepo)
	}
	report := coverageReportFromStored([]hub.StoredRepositoryCoverage{record}, "")
	if err := writeCoverageOutputTo(out, report, options.format, options.reportDir); err != nil {
		return err
	}
	return coverageGateError(report, options.minimumCoverage)
}

func coverageReportFromStored(stored []hub.StoredRepositoryCoverage, filter string) quality.CoverageReport {
	repos := make([]quality.RepositoryCoverage, 0, len(stored))
	for _, s := range stored {
		repoName := strings.TrimPrefix(s.Repository, "github.com/")
		if filter != "" && !strings.Contains(repoName, filter) {
			continue
		}
		status := s.Status
		if status == "" {
			status = quality.StatusPassed
		}
		modules := make([]quality.ModuleCoverage, len(s.Modules))
		for i, m := range s.Modules {
			modules[i] = quality.ModuleCoverage{
				Path:       m.Path,
				Statements: m.Statements,
				Covered:    m.Covered,
				Percentage: m.Percentage,
			}
		}
		repos = append(repos, quality.RepositoryCoverage{
			Repository: repoName,
			Status:     status,
			Statements: s.Statements,
			Covered:    s.Covered,
			Percentage: s.Percentage,
			Modules:    modules,
		})
	}
	return quality.NewCoverageReport(repos)
}
