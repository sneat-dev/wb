package deps

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/sneat-dev/wb/internal/encode"
)

// Markdown renders the verdict table.
//
// The table is the product. One row per published peer, the range the package
// demands, what the target actually resolves, where that came from, and the
// verdict — so "can I reuse this here" is answered by reading, not by running
// an install and interpreting its warnings.
func (report PeerReport) Markdown() string {
	var output strings.Builder
	output.WriteString("# WB npm peer compatibility\n\n")
	fmt.Fprintf(&output, "- Package: `%s`\n", report.Package)
	if report.Version != "" {
		fmt.Fprintf(&output, "- Published version: `%s`\n", report.Version)
	}
	fmt.Fprintf(&output, "- Against: `%s`", report.Against)
	if report.AgainstName != "" {
		fmt.Fprintf(&output, " (`%s`)", report.AgainstName)
	}
	output.WriteByte('\n')
	if report.Source != "" {
		fmt.Fprintf(&output, "- Source: `%s`\n", report.Source)
	}
	fmt.Fprintf(&output, "- Observed at: `%s`\n\n", report.ObservedAt.Format("2006-01-02T15:04:05Z07:00"))

	if len(report.Peers) == 0 {
		output.WriteString("This package declares no peer dependencies: it requires nothing of its host.\n")
		return output.String()
	}

	output.WriteString("| Peer | Required | Installed | Source | Verdict | Reason |\n")
	output.WriteString("|---|---|---|---|---|---|\n")
	for _, row := range report.Peers {
		peer := row.Peer
		if row.Optional {
			peer += " *(optional)*"
		}
		fmt.Fprintf(&output, "| `%s` | `%s` | %s | %s | `%s` | %s |\n",
			peer, row.Required, peerCell(row.Installed), peerCell(row.InstalledSource), row.Verdict, escapeTable(row.Reason))
	}

	fmt.Fprintf(&output, "\n%d peer(s): %d satisfied, %d unsatisfied, %d missing, %d optional missing, %d unevaluated.\n",
		report.Summary.Total, report.Summary.Satisfied, report.Summary.Unsatisfied,
		report.Summary.Missing, report.Summary.OptionalMissing, report.Summary.Unevaluated)

	if PeersFailed(report) {
		output.WriteString("\nThis package cannot be used in the target checkout as it stands. ")
		output.WriteString("Every `unsatisfied` and `missing` row above names exactly what would have to change first.\n")
	} else if report.Summary.Unevaluated > 0 {
		output.WriteString("\nNothing blocks reuse among the rows WB evaluated. ")
		output.WriteString("The `unevaluated` rows are not a pass: WB declined to judge those specifier shapes, which is not the same as judging them compatible.\n")
	} else {
		output.WriteString("\nEvery peer requirement is met by the target checkout.\n")
	}
	return output.String()
}

// YAML renders the peer report.
func (report PeerReport) YAML() ([]byte, error) {
	return yaml.Marshal(report)
}

// JSON renders the peer report.
func (report PeerReport) JSON() ([]byte, error) {
	return encode.JSON(report)
}

func peerCell(value string) string {
	if value == "" {
		return "—"
	}
	return "`" + value + "`"
}
