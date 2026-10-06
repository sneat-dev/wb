// Package cmdversion renders version metadata supplied by the build domain.
package cmdversion

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

// New creates an independent version command with a lazy snapshot operation.
func New(snapshot func() buildinfo.Report) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{Use: "version", Short: "Print the wb version, build revision, and toolchain", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return Write(command.OutOrStdout(), snapshot(), asJSON)
		},
	}
	shared.AddJSONFormatFlags(command, &asJSON)
	return command
}

// Write preserves the successful output contract and returns writer failures.
func Write(out io.Writer, info buildinfo.Report, asJSON bool) error {
	if asJSON {
		info.Version = info.JSONVersion
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(info)
	}
	if _, err := fmt.Fprintf(out, "wb %s\n", info.Version); err != nil {
		return err
	}
	if info.Revision != "" {
		suffix := ""
		if info.Modified {
			suffix = " (modified)"
		}
		if _, err := fmt.Fprintf(out, "revision: %s%s\n", info.Revision, suffix); err != nil {
			return err
		}
	}
	if info.Built != "" {
		if _, err := fmt.Fprintf(out, "built:    %s\n", info.Built); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, "go:       %s\n", info.Go); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "platform: %s\n", info.Platform)
	return err
}

// WriteBare emits only the version for root-level pre-Cobra requests.
func WriteBare(out io.Writer, version string) error {
	_, err := fmt.Fprintln(out, version)
	return err
}
