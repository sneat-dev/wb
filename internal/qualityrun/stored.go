package qualityrun

import (
	"context"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/reposelection"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

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
func resolveRepositorySlug(path string, originFn func(string) (string, error)) string {
	target := strings.TrimSpace(path)
	if target == "" || target == "." {
		target = "."
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
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

type StoredRequest struct {
	Path, Match, Regex, ReportDir string
	Fleet                         bool
}
type StoredResult struct {
	Report    quality.CoverageReport
	Artifacts CoverageArtifacts
	NoRecords bool
}
type storeOperations struct {
	Open      func(context.Context) (hub.RepositoryCoverageStore, io.Closer, error)
	OriginURL func(string) (string, error)
}

func StoredCoverage(ctx context.Context, request StoredRequest) (StoredResult, error) {
	return storedWith(ctx, request, storeOperations{Open: openDefaultCoverageStore, OriginURL: gitops.OriginURL})
}
func storedWith(ctx context.Context, request StoredRequest, ops storeOperations) (StoredResult, error) {
	store, closer, err := ops.Open(ctx)
	if err != nil {
		return StoredResult{}, err
	}
	if closer != nil {
		defer func() { _ = closer.Close() }()
	}

	if request.Fleet {
		records, err := store.ListCoverage(ctx)
		if err != nil {
			return StoredResult{}, err
		}
		if len(records) == 0 {
			return StoredResult{NoRecords: true}, nil
		}
		var expression *regexp.Regexp
		if request.Regex != "" {
			compiled, err := regexp.Compile(request.Regex)
			if err != nil {
				return StoredResult{}, fmt.Errorf("invalid --regex: %w", err)
			}
			expression = compiled
		}
		filtered := make([]hub.StoredRepositoryCoverage, 0, len(records))
		for _, r := range records {
			repoSlug := strings.TrimPrefix(r.Repository, "github.com/")
			if reposelection.Match(repoSlug, "", request.Match, expression) || reposelection.Match(r.Repository, "", request.Match, expression) {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			return StoredResult{}, fmt.Errorf("no CI coverage reports match the selected filters")
		}
		report := coverageReportFromStored(filtered, "")
		artifacts, err := PersistCoverage(report, request.ReportDir)
		if err != nil {
			return StoredResult{}, err
		}
		return StoredResult{Report: report, Artifacts: artifacts}, nil
	}

	targetRepo := resolveRepositorySlug(request.Path, ops.OriginURL)
	targetRepo = strings.TrimPrefix(targetRepo, "github.com/")
	record, found, err := store.GetCoverage(ctx, targetRepo)
	if err != nil {
		return StoredResult{}, err
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
		return StoredResult{}, fmt.Errorf("no CI coverage found for repository %s", targetRepo)
	}
	report := coverageReportFromStored([]hub.StoredRepositoryCoverage{record}, "")
	artifacts, err := PersistCoverage(report, request.ReportDir)
	if err != nil {
		return StoredResult{}, err
	}
	return StoredResult{Report: report, Artifacts: artifacts}, nil
}
