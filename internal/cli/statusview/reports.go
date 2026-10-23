package statusview

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/repostatus"
	"gopkg.in/yaml.v3"
)

func defaultTitle(title string) string {
	if title == "" {
		return "# WB local repository status\n\n"
	}
	return title
}

// yamlBytes serializes a closed DTO containing only primitive fields and slices.
// There are no user-defined marshalers or unsupported values in this report.
func yamlBytes(report repostatus.Index) []byte {
	raw, _ := yaml.Marshal(report)
	return raw
}

// WriteReports preserves Markdown-before-YAML file effects before stdout validation.
func WriteReports(report repostatus.Index, directory string, details bool, title string) error {
	return writeReportsWith(report, directory, details, title, os.MkdirAll, os.WriteFile)
}
func writeReportsWith(report repostatus.Index, directory string, details bool, title string,
	mkdir func(string, os.FileMode) error, write func(string, []byte, os.FileMode) error,
) error {
	if err := mkdir(directory, 0o755); err != nil {
		return err
	}
	if err := write(filepath.Join(directory, "status.md"), []byte(Markdown(report, details, defaultTitle(title))), 0o644); err != nil {
		return err
	}
	return write(filepath.Join(directory, "status.yaml"), yamlBytes(report), 0o644)
}

func writeOutput(out io.Writer, report repostatus.Index, format string, details bool, title string) error {
	switch format {
	case "markdown":
		_, err := fmt.Fprint(out, Markdown(report, details, defaultTitle(title)))
		return err
	case "yaml":
		_, err := out.Write(yamlBytes(report))
		return err
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}
