package main

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/buildinfo"
)

type versionInfo struct {
	Version  string `json:"version"`
	Revision string `json:"revision,omitempty"`
	Built    string `json:"built,omitempty"`
	Modified bool   `json:"modified,omitempty"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

func newVersionCmd() *cobra.Command {
	var asJSON bool
	command := &cobra.Command{
		Use:   "version",
		Short: "Print the wb version, build revision, and toolchain",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			printVersion(command.OutOrStdout(), asJSON)
			return nil
		},
	}
	command.Flags().BoolVar(&asJSON, "json", false, "emit machine-readable JSON")
	return command
}

func printVersion(out io.Writer, asJSON bool) int {
	info := collectVersion()
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(info); err != nil {
			return exitFindings
		}
		return exitOK
	}
	_, _ = fmt.Fprintf(out, "wb %s\n", info.Version)
	if info.Revision != "" {
		suffix := ""
		if info.Modified {
			suffix = " (modified)"
		}
		_, _ = fmt.Fprintf(out, "revision: %s%s\n", info.Revision, suffix)
	}
	if info.Built != "" {
		_, _ = fmt.Fprintf(out, "built:    %s\n", info.Built)
	}
	_, _ = fmt.Fprintf(out, "go:       %s\n", info.Go)
	_, _ = fmt.Fprintf(out, "platform: %s\n", info.Platform)
	return exitOK
}

// printBareVersion prints just the resolved version — no "wb " name, no
// revision, no decoration, no trailing detail — for `wb --version`/`wb -v`,
// matching the bare-semver contract a script piping `$(wb --version)`
// expects. `wb version`'s richer multi-line form stays printVersion's job;
// both are sourced from the identical buildinfo.Version(), so the two
// surfaces can never disagree about which version this binary is.
func printBareVersion(out io.Writer) int {
	_, _ = fmt.Fprintln(out, buildinfo.Version())
	return exitOK
}

func collectVersion() versionInfo {
	return versionInfo{
		Version:  buildinfo.Version(),
		Revision: buildinfo.Revision(),
		Built:    buildinfo.Date(),
		Modified: buildinfo.Modified(),
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
}
