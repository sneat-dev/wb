package cmdquality

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"gopkg.in/yaml.v3"
	"io"
)

type verificationIndex = qualityrun.VerificationIndex

func coverageFailed(report quality.CoverageReport) bool {
	for _, repository := range report.Repositories {
		if repository.Status == quality.StatusFailed {
			return true
		}
	}
	return false
}

func coverageGateError(runtime shared.Runtime, report quality.CoverageReport, minimum float64) error {
	if coverageFailed(report) {
		return runtime.ExitError(shared.ExitFindings, "coverage could not be measured in one or more repositories; see the `error` column above, then rerun just those with --resume --report-dir")
	}
	if minimum >= 0 && report.Percentage < minimum {
		return runtime.ExitError(shared.ExitFindings, fmt.Sprintf("coverage %.2f%% is below required %.2f%%", report.Percentage, minimum))
	}
	return nil
}

func verificationFailed(report verificationIndex) bool {
	for _, repository := range report.Repositories {
		if repository.Status == quality.StatusFailed {
			return true
		}
	}
	return false
}

func writeCoverageOutputTo(out io.Writer, report quality.CoverageReport, format string, artifacts qualityrun.CoverageArtifacts) error {

	switch format {
	case "markdown":
		_, err := io.WriteString(out, qualityrun.CoverageMarkdown(report))
		return err
	case "yaml":
		// The concrete report graph contains scalars, slices and time.Time only;
		// none of its domain types implements MarshalYAML. Encoding cannot fail.
		raw, _ := yaml.Marshal(report)
		_, err := out.Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	case "summary":
		if artifacts.Report.Path == "" {
			return fmt.Errorf("--format summary requires --report-dir so the full report has a durable reference")
		}
		status := "passed"
		if coverageFailed(report) {
			status = "failed"
		}
		if _, err := fmt.Fprintf(out, "WB coverage %s: %.2f%% (%d/%d statements); report=%s; sha256=%s",
			status, report.Percentage, report.Covered, report.Statements, artifacts.Report.Path, artifacts.Report.SHA256); err != nil {
			return err
		}
		if artifacts.Diagnostics != nil {
			if _, err := fmt.Fprintf(out, "; diagnostics=%s; diagnostics-sha256=%s", artifacts.Diagnostics.Path, artifacts.Diagnostics.SHA256); err != nil {
				return err
			}
		}
		_, err := io.WriteString(out, "\n")
		return err
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, json, or summary)", format)
	}
}
func writeVerificationOutput(out io.Writer, report verificationIndex, format string) error {

	switch format {
	case "markdown":
		_, err := io.WriteString(out, qualityrun.VerificationMarkdown(report))
		return err
	case "yaml":
		// The concrete report graph contains scalars, slices and time.Time only;
		// none of its domain types implements MarshalYAML. Encoding cannot fail.
		raw, _ := yaml.Marshal(report)
		_, err := out.Write(raw)
		return err
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}
