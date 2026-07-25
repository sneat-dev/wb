package main

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is overridden at link time by the release build
// (-ldflags "-X main.version=v1.2.3"). Builds without that stamp fall back to
// the module version and VCS metadata the Go toolchain embeds, so `wb version`
// always identifies the binary rather than reporting nothing.
var version = ""

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

func collectVersion() versionInfo {
	info := versionInfo{
		Version:  version,
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "" && build.Main.Version != "" {
			info.Version = build.Main.Version
		}
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.Revision = setting.Value
			case "vcs.time":
				info.Built = setting.Value
			case "vcs.modified":
				info.Modified = setting.Value == "true"
			}
		}
	}
	if info.Version == "" {
		info.Version = "unknown"
	}
	return info
}
