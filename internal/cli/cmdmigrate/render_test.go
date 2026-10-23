package cmdmigrate

import (
	"bytes"
	"errors"
	"github.com/sneat-dev/wb/internal/migrate"
	"strings"
	"testing"
)

func TestConcreteReportFormatsPreserveBytesAndWriterFailures(t *testing.T) {
	t.Parallel()
	report := migrate.Report{SchemaVersion: 1, Migration: migrate.ReportMigration{ID: "migration", Format: "https://sneat.dev/workbench/formats/migration/v1"}, Status: "planned"}
	campaign := migrate.CampaignReport{SchemaVersion: 1, Migration: report.Migration, Status: "completed", SourceRoot: "/tmp/src", GitHubDir: "/tmp/github", BaseRef: "main", Parallel: 1}
	for _, format := range []string{"markdown", "yaml", "json", "toml"} {
		t.Run(format, func(t *testing.T) {
			t.Parallel()
			for _, isCampaign := range []bool{false, true} {
				var out bytes.Buffer
				var err error
				var expected []byte
				if isCampaign {
					err = writeCampaignReport(&out, campaign, format)
					switch format {
					case "markdown":
						expected = []byte(campaign.Markdown())
					case "yaml":
						expected, _ = campaign.YAML()
					case "json":
						expected, _ = campaign.JSON()
					}
				} else {
					err = writeMigrationReport(&out, report, format)
					switch format {
					case "markdown":
						expected = []byte(report.Markdown())
					case "yaml":
						expected, _ = report.YAML()
					case "json":
						expected, _ = report.JSON()
					}
				}
				if format == "toml" {
					if err == nil || !strings.Contains(err.Error(), "unknown --format") {
						t.Fatal(err)
					}
					continue
				}
				if err != nil || out.Len() == 0 || !bytes.Equal(out.Bytes(), expected) {
					t.Fatal(out.String(), string(expected), err)
				}
				failure := errors.New("output failed")
				if isCampaign {
					err = writeCampaignReport(failingWriter{failure}, campaign, format)
				} else {
					err = writeMigrationReport(failingWriter{failure}, report, format)
				}
				if err != failure {
					t.Fatal(err)
				}
			}
		})
	}
}
