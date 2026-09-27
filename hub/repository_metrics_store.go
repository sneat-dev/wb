package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/api/githubapp"
)

const repositoryMetricsCollection = "workbench_repository_metrics"

var errRepositoryMetricsStoreUnavailable = errors.New("workbench repository metrics store is unavailable")

// DefaultMetricTypes returns standard built-in metric type definitions.
func DefaultMetricTypes() []MetricTypeDefinition {
	return []MetricTypeDefinition{
		{
			Type:           MetricTypeCoverage,
			Title:          "Test Coverage",
			Description:    "Statement test coverage measured on default branch merge/push",
			Unit:           "%",
			Format:         FormatPercentage,
			DimensionLabel: "Package",
			GoodThreshold:  80.0,
			WarnThreshold:  60.0,
			HigherIsBetter: true,
		},
		{
			Type:           MetricTypeCommitsPerDay,
			Title:          "Commits Per Day",
			Description:    "Daily commit activity on default branch",
			Unit:           "commits/day",
			Format:         FormatFloat,
			DimensionLabel: "Day",
			GoodThreshold:  5.0,
			WarnThreshold:  1.0,
			HigherIsBetter: true,
		},
	}
}

type repositoryMetricsStore struct {
	backend       githubapp.DocumentStore
	coverageStore RepositoryCoverageStore
}

// NewRepositoryMetricsStore creates a new generic metrics store backed by DALgo and optional coverage store adapter.
func NewRepositoryMetricsStore(backend githubapp.DocumentStore, coverageStore RepositoryCoverageStore) RepositoryMetricsStore {
	return repositoryMetricsStore{
		backend:       backend,
		coverageStore: coverageStore,
	}
}

func metricDocumentID(repository, metricType string) string {
	raw := canonicalRepository(repository) + "::" + metricType
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func findMetricTypeDefinition(metricType string) (MetricTypeDefinition, bool) {
	for _, def := range DefaultMetricTypes() {
		if def.Type == metricType {
			return def, true
		}
	}
	return MetricTypeDefinition{}, false
}

func evaluateMetricStatus(val float64, def MetricTypeDefinition) MetricStatus {
	if def.HigherIsBetter {
		if val >= def.GoodThreshold {
			return StatusPassed
		}
		if val >= def.WarnThreshold {
			return StatusWarning
		}
		return StatusFailed
	}
	if val <= def.GoodThreshold {
		return StatusPassed
	}
	if val <= def.WarnThreshold {
		return StatusWarning
	}
	return StatusFailed
}

func formatMetricValue(val float64, format FormatType, unit string) string {
	switch format {
	case FormatPercentage:
		return fmt.Sprintf("%.1f%%", val)
	case FormatInteger:
		return fmt.Sprintf("%d", int64(val))
	case FormatDuration:
		return fmt.Sprintf("%.1fs", val/1000)
	default:
		if unit != "" {
			return fmt.Sprintf("%.1f %s", val, unit)
		}
		return fmt.Sprintf("%.1f", val)
	}
}

func (s repositoryMetricsStore) SaveMetric(ctx context.Context, metric RepositoryMetric) error {
	if s.backend == nil {
		return errRepositoryMetricsStoreUnavailable
	}
	canonical := canonicalRepository(metric.Repository)
	if canonical == "" || canonical == "github.com/" {
		return errors.New("repository is required for metric record")
	}
	if metric.MetricType == "" {
		return errors.New("metric_type is required for metric record")
	}
	metric.Repository = canonical
	parts := strings.Split(strings.TrimPrefix(canonical, "github.com/"), "/")
	if len(parts) == 2 {
		if metric.Owner == "" {
			metric.Owner = parts[0]
		}
		if metric.Name == "" {
			metric.Name = parts[1]
		}
	}

	def, hasDef := findMetricTypeDefinition(metric.MetricType)
	if metric.FormattedValue == "" {
		format := FormatFloat
		unit := ""
		if hasDef {
			format = def.Format
			unit = def.Unit
		}
		metric.FormattedValue = formatMetricValue(metric.Value, format, unit)
	}
	if metric.Status == "" && hasDef {
		metric.Status = evaluateMetricStatus(metric.Value, def)
	} else if metric.Status == "" {
		metric.Status = StatusNeutral
	}

	id := metricDocumentID(canonical, metric.MetricType)
	if err := s.backend.Set(ctx, repositoryMetricsCollection, id, metric); err != nil {
		return fmt.Errorf("save repository metric for %s (%s): %w", canonical, metric.MetricType, err)
	}
	return nil
}

func (s repositoryMetricsStore) GetMetric(ctx context.Context, repository, metricType string) (RepositoryMetric, bool, error) {
	if s.backend == nil {
		return RepositoryMetric{}, false, errRepositoryMetricsStoreUnavailable
	}
	if metricType == "" {
		metricType = MetricTypeCoverage
	}
	canonical := canonicalRepository(repository)
	id := metricDocumentID(canonical, metricType)
	var metric RepositoryMetric
	found, err := s.backend.Get(ctx, repositoryMetricsCollection, id, &metric)
	if err != nil {
		return RepositoryMetric{}, false, fmt.Errorf("read repository metric for %s (%s): %w", canonical, metricType, err)
	}
	if found {
		return metric, true, nil
	}

	// Fallback to coverageStore adapter if metricType is coverage
	if (metricType == MetricTypeCoverage || metricType == "coverage") && s.coverageStore != nil {
		cov, covFound, covErr := s.coverageStore.GetCoverage(ctx, canonical)
		if covErr != nil {
			return RepositoryMetric{}, false, fmt.Errorf("read coverage fallback for %s: %w", canonical, covErr)
		}
		if covFound {
			return CoverageToRepositoryMetric(cov), true, nil
		}
	}

	return RepositoryMetric{}, false, nil
}

func (s repositoryMetricsStore) ListMetrics(ctx context.Context, metricType string) ([]RepositoryMetric, error) {
	if s.backend == nil {
		return nil, errRepositoryMetricsStoreUnavailable
	}
	if metricType == "" {
		metricType = MetricTypeCoverage
	}
	var stored []RepositoryMetric
	if err := s.backend.Query(ctx, repositoryMetricsCollection, nil, 0, &stored); err != nil {
		return nil, fmt.Errorf("list repository metrics: %w", err)
	}

	resultMap := make(map[string]RepositoryMetric)
	for _, m := range stored {
		if m.MetricType == metricType {
			resultMap[m.Repository] = m
		}
	}

	// Merge coverage records if listing coverage
	if (metricType == MetricTypeCoverage || metricType == "coverage") && s.coverageStore != nil {
		covList, err := s.coverageStore.ListCoverage(ctx)
		if err != nil {
			return nil, fmt.Errorf("list coverage fallback: %w", err)
		}
		for _, cov := range covList {
			if _, exists := resultMap[cov.Repository]; !exists {
				resultMap[cov.Repository] = CoverageToRepositoryMetric(cov)
			}
		}
	}

	records := make([]RepositoryMetric, 0, len(resultMap))
	for _, m := range resultMap {
		records = append(records, m)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].Repository < records[j].Repository
	})
	return records, nil
}

func (s repositoryMetricsStore) ListMetricTypes(ctx context.Context) ([]MetricTypeDefinition, error) {
	return DefaultMetricTypes(), nil
}

// CoverageToRepositoryMetric converts a StoredRepositoryCoverage record into a generic RepositoryMetric.
func CoverageToRepositoryMetric(record StoredRepositoryCoverage) RepositoryMetric {
	status := StatusFailed
	if record.Percentage >= 80.0 {
		status = StatusPassed
	} else if record.Percentage >= 60.0 {
		status = StatusWarning
	}

	metric := RepositoryMetric{
		Repository:     record.Repository,
		Owner:          record.Owner,
		Name:           record.Name,
		MetricType:     MetricTypeCoverage,
		ReportedAt:     record.ReportedAt,
		Ref:            record.Ref,
		SHA:            record.SHA,
		Value:          record.Percentage,
		FormattedValue: fmt.Sprintf("%.1f%%", record.Percentage),
		Status:         status,
		Metadata: map[string]any{
			"workflow_run_id":  record.WorkflowRunID,
			"workflow_run_url": record.WorkflowRunURL,
			"statements":       record.Statements,
			"covered":          record.Covered,
		},
	}

	if len(record.Packages) > 0 {
		dimensions := make([]MetricDimension, 0, len(record.Packages))
		for pkgName, pkg := range record.Packages {
			pkgStatus := StatusFailed
			if pkg.Percentage >= 80.0 {
				pkgStatus = StatusPassed
			} else if pkg.Percentage >= 60.0 {
				pkgStatus = StatusWarning
			}
			dimensions = append(dimensions, MetricDimension{
				Name:           pkgName,
				Value:          pkg.Percentage,
				FormattedValue: fmt.Sprintf("%.1f%%", pkg.Percentage),
				Status:         pkgStatus,
				Details: map[string]any{
					"statements": pkg.Statements,
					"covered":    pkg.Covered,
				},
			})
		}
		sort.Slice(dimensions, func(i, j int) bool {
			return dimensions[i].Name < dimensions[j].Name
		})
		metric.Dimensions = dimensions
	} else if len(record.Modules) > 0 {
		dimensions := make([]MetricDimension, 0, len(record.Modules))
		for _, mod := range record.Modules {
			modStatus := StatusFailed
			if mod.Percentage >= 80.0 {
				modStatus = StatusPassed
			} else if mod.Percentage >= 60.0 {
				modStatus = StatusWarning
			}
			dimensions = append(dimensions, MetricDimension{
				Name:           mod.Path,
				Value:          mod.Percentage,
				FormattedValue: fmt.Sprintf("%.1f%%", mod.Percentage),
				Status:         modStatus,
				Details: map[string]any{
					"statements": mod.Statements,
					"covered":    mod.Covered,
				},
			})
		}
		sort.Slice(dimensions, func(i, j int) bool {
			return dimensions[i].Name < dimensions[j].Name
		})
		metric.Dimensions = dimensions
	}

	return metric
}

var _ RepositoryMetricsStore = repositoryMetricsStore{}
