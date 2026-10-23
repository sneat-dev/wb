package cmddeps

import (
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/sneat-dev/wb/internal/encode"
	"gopkg.in/yaml.v3"
)

func writeNpmPublishOutput(out io.Writer, output depsrun.PublicationOutput, format string) error {
	// Keep the machine-readable composite output on the same report generation
	// contract as the durable YAML/JSON receipt. The closed composite DTO tree
	// is acyclic supported YAML data without fallible custom marshalers; JSON
	// normalizes that YAML document. A type-tree regression protects this invariant.
	output.Publication = output.Publication.WithGeneration()
	switch format {
	case "json":
		raw, _ := encode.JSON(output)
		_, err := out.Write(raw)
		return err
	case "yaml":
		raw, _ := yaml.Marshal(output)
		_, err := out.Write(raw)
		return err
	case "markdown":
		return writeNpmPublishMarkdown(out, output)
	default:
		return fmt.Errorf("unknown --format %q (want markdown, yaml, or json)", format)
	}
}
func writeNpmPublishMarkdown(out io.Writer, output depsrun.PublicationOutput) error {
	var builder strings.Builder
	builder.WriteString("# WB npm publication and dependency waves\n\n")
	fmt.Fprintf(&builder, "- Operation: `%s`\n", output.Publication.Operation)
	fmt.Fprintf(&builder, "- Publication status: `%s`\n", output.Publication.Status)
	fmt.Fprintf(&builder, "- Ref: `%s`\n\n", output.Publication.Ref)
	builder.WriteString("## Workflow and registry receipts\n\n")
	builder.WriteString("| Repository | Workflow | Package | Version | Status | Head | Run | Registry | Reason |\n")
	builder.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, receipt := range output.Publication.Releases {
		run := receipt.RunID
		if receipt.RunURL != "" {
			run = "[" + receipt.RunID + "](" + receipt.RunURL + ")"
		}
		fmt.Fprintf(&builder, "| `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | %s | `%s` | %s |\n", receipt.Repository, receipt.Workflow, receipt.Package, receipt.Version, receipt.Status, receipt.HeadSHA, run, receipt.RegistryVersion, strings.ReplaceAll(receipt.Reason, "|", "\\|"))
	}
	if output.Propagation != nil {
		builder.WriteString("\n")
		builder.WriteString(output.Propagation.Markdown())
	}
	_, err := fmt.Fprint(out, builder.String())
	return err
}
