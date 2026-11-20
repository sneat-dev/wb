// Package cmdversion renders version metadata supplied by the build domain.
package cmdversion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/stalewb"
	"github.com/spf13/cobra"
)

// New creates an independent version command with a lazy snapshot operation.
func New(snapshot func() buildinfo.Report) *cobra.Command { return NewWithStale(snapshot, nil) }

// NewWithStale is New plus a scan for older wb copies on PATH. scan receives the
// running version; nil skips it.
func NewWithStale(snapshot func() buildinfo.Report, scan func(context.Context, string) []stalewb.Binary) *cobra.Command {
	var asJSON bool
	command := &cobra.Command{Use: "version", Short: "Print the wb version, build revision, and toolchain", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			info := snapshot()
			var stale []stalewb.Binary
			if scan != nil {
				stale = scan(command.Context(), info.JSONVersion)
			}
			return WriteWithStale(command.OutOrStdout(), command.ErrOrStderr(), info, stale, asJSON)
		},
	}
	shared.AddJSONFormatFlags(command, &asJSON)
	return command
}

// Write preserves the successful output contract and returns writer failures.
func Write(out io.Writer, info buildinfo.Report, asJSON bool) error {
	return WriteWithStale(out, io.Discard, info, nil, asJSON)
}

// jsonReport is the fleet `version --json` document plus the stale copies, an
// additional key the contract permits; it is absent when there are none.
type jsonReport struct {
	buildinfo.Report
	StaleBinaries []stalewb.Binary `json:"stale_binaries,omitempty"`
}

// WriteWithStale writes the version document. Stale wb copies are a field of
// the JSON document and a warning on warn after the human text.
func WriteWithStale(out, warn io.Writer, info buildinfo.Report, stale []stalewb.Binary, asJSON bool) error {
	if asJSON {
		info.Version = info.JSONVersion
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(jsonReport{Report: info, StaleBinaries: stale})
	}
	if err := writeHuman(out, info); err != nil {
		return err
	}
	_, err := io.WriteString(warn, stalewb.Warning(info.JSONVersion, stale))
	return err
}

func writeHuman(out io.Writer, info buildinfo.Report) error {
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
