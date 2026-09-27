package hub

import (
	"context"
	"time"
)

// FormatType defines formatting rules for a metric value.
type FormatType string

const (
	FormatPercentage FormatType = "percentage"
	FormatInteger    FormatType = "integer"
	FormatFloat      FormatType = "float"
	FormatDuration   FormatType = "duration"
)

// MetricStatus represents the health evaluation for a metric.
type MetricStatus string

const (
	StatusPassed  MetricStatus = "passed"
	StatusWarning MetricStatus = "warning"
	StatusFailed  MetricStatus = "failed"
	StatusNeutral MetricStatus = "neutral"
)

const (
	MetricTypeCoverage      = "test_coverage"
	MetricTypeCommitsPerDay = "commits_per_day"
)

// MetricTypeDefinition defines the presentation and evaluation rules for a metric category.
type MetricTypeDefinition struct {
	Type           string     `json:"type"`
	Title          string     `json:"title"`
	Description    string     `json:"description"`
	Unit           string     `json:"unit"`
	Format         FormatType `json:"format"`
	DimensionLabel string     `json:"dimension_label"`
	GoodThreshold  float64    `json:"good_threshold"`
	WarnThreshold  float64    `json:"warn_threshold"`
	HigherIsBetter bool       `json:"higher_is_better"`
}

// RepositoryMetric represents a point-in-time multi-dimensional metric for a repository.
type RepositoryMetric struct {
	Repository     string            `json:"repository"`
	Owner          string            `json:"owner"`
	Name           string            `json:"name"`
	MetricType     string            `json:"metric_type"`
	ReportedAt     time.Time         `json:"reported_at"`
	Ref            string            `json:"ref,omitempty"`
	SHA            string            `json:"sha,omitempty"`
	Value          float64           `json:"value"`
	FormattedValue string            `json:"formatted_value"`
	Status         MetricStatus      `json:"status"`
	Metadata       map[string]any    `json:"metadata,omitempty"`
	Dimensions     []MetricDimension `json:"dimensions,omitempty"`
}

// MetricDimension represents a single slice along the primary dimension (e.g. package, date).
type MetricDimension struct {
	Name           string         `json:"name"`
	Value          float64        `json:"value"`
	FormattedValue string         `json:"formatted_value"`
	Status         MetricStatus   `json:"status"`
	Details        map[string]any `json:"details,omitempty"`
}

// RepositoryMetricsStore provides persistent storage and querying for repository metrics.
type RepositoryMetricsStore interface {
	SaveMetric(ctx context.Context, metric RepositoryMetric) error
	GetMetric(ctx context.Context, repository, metricType string) (RepositoryMetric, bool, error)
	ListMetrics(ctx context.Context, metricType string) ([]RepositoryMetric, error)
	ListMetricTypes(ctx context.Context) ([]MetricTypeDefinition, error)
}
