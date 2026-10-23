package cmddeps

import (
	"fmt"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/spf13/cobra"
)

// dependencyReport is closed over the four concrete command reports. Their
// YAML encoders only visit concrete supported fields (time.Time uses yaml's
// time encoding). JSON is produced through the existing YAML normalization chain.
type dependencyReport interface {
	deps.DriftReport | deps.PeerReport | deps.Report | deps.BumpReport
	Markdown() string
	YAML() ([]byte, error)
	JSON() ([]byte, error)
}

func writeDependencyReport[T dependencyReport](command *cobra.Command, report T, format string) error {
	var raw []byte
	switch format {
	case "markdown":
		// Both the original fmt.Fprint(string) and Writer.Write([]byte) paths
		// make one Write call and return its error, including short nil writes.
		raw = []byte(report.Markdown())
	case "yaml":
		raw, _ = report.YAML()
	case "json":
		// The closed concrete report trees normalize to supported JSON values:
		// string-key maps, arrays, scalars and valid YAML timestamps. Arbitrary
		// values passed to encode.JSON remain fallible outside this constraint.
		raw, _ = report.JSON()
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
	_, err := command.OutOrStdout().Write(raw)
	return err
}
