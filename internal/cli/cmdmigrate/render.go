package cmdmigrate

import (
	"fmt"
	"github.com/sneat-dev/wb/internal/migrate"
	"io"
)

// Both concrete report DTOs consist solely of primitive fields, slices and
// pointers to primitive values/slices. They have no custom marshalers, interfaces, maps or cycles.
// Their YAML and YAML-derived JSON encoding cannot fail; writer errors can.
func writeMigrationReport(out io.Writer, report migrate.Report, format string) error {
	switch format {
	case "markdown":
		_, err := fmt.Fprint(out, report.Markdown())
		return err
	case "yaml":
		raw, _ := report.YAML()
		_, err := out.Write(raw)
		return err
	case "json":
		raw, _ := report.JSON()
		_, err := out.Write(raw)
		return err
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}

func writeCampaignReport(out io.Writer, report migrate.CampaignReport, format string) error {
	switch format {
	case "markdown":
		_, err := fmt.Fprint(out, report.Markdown())
		return err
	case "yaml":
		raw, _ := report.YAML()
		_, err := out.Write(raw)
		return err
	case "json":
		raw, _ := report.JSON()
		_, err := out.Write(raw)
		return err
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}
