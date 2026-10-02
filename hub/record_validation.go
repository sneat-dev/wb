package hub

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/sneat-dev/wb/internal/quality"
)

// errUnsafeRecord marks a metric or coverage record refused for holding a value
// outside the form its field has. The pages that show these records share an
// origin with Cockpit, so a record is a place a client could plant markup; the
// pages are built to be safe against any stored value, and this is the second
// layer: such a value is not stored in the first place.
var errUnsafeRecord = errors.New("record holds a value outside the form of its field")

// The bounds of a record. They are far above what a real report holds and
// exist so that a field cannot carry a document.
const (
	maxRecordTextBytes   = 256
	maxRecordNameBytes   = 512
	maxRecordMapEntries  = 64
	maxRecordDimensions  = 20000
	maxFormattedValueLen = 48
)

var (
	// A repository in its canonical form: the forge and owner/name.
	recordRepositoryPattern = regexp.MustCompile(`^github\.com/[a-z0-9][a-z0-9._-]{0,99}/[a-z0-9][a-z0-9._-]{0,99}$`)
	// An owner or a repository name on its own.
	recordSlugPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)
	// A metric type and a metadata key: a short identifier.
	recordTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)
	// A Git ref, and a commit id (a short identifier: a hub's own tests and
	// other forges name commits that are not forty hexadecimal digits).
	recordRefPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+@-]{0,254}$`)
	recordSHAPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// A formatted value: a number with its unit, such as "85.3%" or "1.2 commits/day".
	recordFormattedPattern = regexp.MustCompile(`^[A-Za-z0-9 .,%+/:_-]*$`)
)

// plainRecordText reports whether value is short text with nothing that could
// open markup or leave an attribute: no angle bracket, quote, backtick,
// backslash or control character.
func plainRecordText(value string, limit int) bool {
	if len(value) > limit || !utf8.ValidString(value) {
		return false
	}
	return !strings.ContainsFunc(value, func(r rune) bool {
		return r < 0x20 || r == 0x7f || strings.ContainsRune("<>\"'`\\", r)
	})
}

// httpsRecordURL reports whether value is empty or an https address.
func httpsRecordURL(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && plainRecordText(value, maxRecordNameBytes)
}

func optional(pattern *regexp.Regexp, value string) bool {
	return value == "" || pattern.MatchString(value)
}

// recordValues reports whether values is a small map of short identifiers to
// numbers, booleans or short plain text: no nested value, which a page would
// have to render as a document.
func recordValues(values map[string]any) bool {
	if len(values) > maxRecordMapEntries {
		return false
	}
	for key, value := range values {
		if !recordTokenPattern.MatchString(key) {
			return false
		}
		switch typed := value.(type) {
		case nil, bool, float64, float32, int, int32, int64:
		case string:
			if !plainRecordText(typed, maxRecordTextBytes) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func metricStatus(status MetricStatus) bool {
	switch status {
	case "", StatusPassed, StatusWarning, StatusFailed, StatusNeutral:
		return true
	}
	return false
}

// recordIdentity checks the fields a metric and a coverage record share. An
// empty repository is left to the store, which names it as required.
func recordIdentity(repository, owner, name, ref, sha string) string {
	switch {
	case strings.TrimSpace(repository) != "" && !recordRepositoryPattern.MatchString(canonicalRepository(repository)):
		return "repository"
	case !optional(recordSlugPattern, owner):
		return "owner"
	case !optional(recordSlugPattern, name):
		return "name"
	case !optional(recordRefPattern, ref):
		return "ref"
	case !optional(recordSHAPattern, sha):
		return "sha"
	}
	return ""
}

func unsafeField(field string) error {
	return fmt.Errorf("%w: %s", errUnsafeRecord, field)
}

// validateMetricRecord refuses a metric whose fields are outside their forms:
// the repository is owner/name, the status one of the closed set, the type a
// short identifier, and the metadata and each dimension's details numbers or
// short plain text. The error names the field and never its value.
func validateMetricRecord(metric RepositoryMetric) error {
	if field := recordIdentity(metric.Repository, metric.Owner, metric.Name, metric.Ref, metric.SHA); field != "" {
		return unsafeField(field)
	}
	switch {
	case !optional(recordTokenPattern, metric.MetricType):
		return unsafeField("metric_type")
	case !metricStatus(metric.Status):
		return unsafeField("status")
	case len(metric.FormattedValue) > maxFormattedValueLen || !recordFormattedPattern.MatchString(metric.FormattedValue):
		return unsafeField("formatted_value")
	case !recordValues(metric.Metadata):
		return unsafeField("metadata")
	case len(metric.Dimensions) > maxRecordDimensions:
		return unsafeField("dimensions")
	}
	if address, isText := metric.Metadata["workflow_run_url"].(string); isText && !httpsRecordURL(address) {
		return unsafeField("metadata.workflow_run_url")
	}
	for _, dimension := range metric.Dimensions {
		switch {
		case dimension.Name == "" || !plainRecordText(dimension.Name, maxRecordNameBytes):
			return unsafeField("dimensions.name")
		case !metricStatus(dimension.Status):
			return unsafeField("dimensions.status")
		case len(dimension.FormattedValue) > maxFormattedValueLen || !recordFormattedPattern.MatchString(dimension.FormattedValue):
			return unsafeField("dimensions.formatted_value")
		case !recordValues(dimension.Details):
			return unsafeField("dimensions.details")
		}
	}
	return nil
}

// validateCoverageRecord is validateMetricRecord for a coverage report: its
// status is one of the quality statuses, its workflow run address is https, and
// the names of its modules and packages are plain text.
func validateCoverageRecord(record StoredRepositoryCoverage) error {
	if field := recordIdentity(record.Repository, record.Owner, record.Name, record.Ref, record.SHA); field != "" {
		return unsafeField(field)
	}
	switch record.Status {
	case "", quality.StatusPassed, quality.StatusFailed, quality.StatusSkipped:
	default:
		return unsafeField("status")
	}
	if !httpsRecordURL(record.WorkflowRunURL) {
		return unsafeField("workflow_run_url")
	}
	if len(record.Modules) > maxRecordDimensions || len(record.Packages) > maxRecordDimensions {
		return unsafeField("modules")
	}
	for _, module := range record.Modules {
		if module.Path == "" || !plainRecordText(module.Path, maxRecordNameBytes) {
			return unsafeField("modules.path")
		}
	}
	for name := range record.Packages {
		if name == "" || !plainRecordText(name, maxRecordNameBytes) {
			return unsafeField("packages")
		}
	}
	return nil
}
