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

	// Name, Commit, Date and DateSource complete the fleet-wide
	// `version --json` contract every catalog CLI's `version --json` must
	// print (cli-install#req:version-json-contract in
	// strongo/cli-helpers): the keys a cliinstall status prober decodes
	// through the shared github.com/strongo/buildinfo.VersionJSON type.
	// They sit alongside, not instead of, wb's own pre-existing Revision/
	// Built/Modified keys above (cli-install#req:version-json-contract:
	// "Additional keys are permitted"; existing keys are never removed).
	// Commit deliberately keeps any "+dirty" suffix the contract requires,
	// unlike Revision, which strips it and pairs with Modified instead; the
	// contract's four fields are never omitted, even when empty, so a
	// probing host always finds every key.
	Name       string `json:"name"`
	Commit     string `json:"commit"`
	Date       string `json:"date"`
	DateSource string `json:"date_source"`
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
	addJSONFormatFlags(command, &asJSON)
	return command
}

func printVersion(out io.Writer, asJSON bool) int {
	info := collectVersion()
	if asJSON {
		// cli-install#req:version-json-contract's own undetermined placeholder
		// ("dev") differs from wb's text-banner convention (buildinfo.Unknown,
		// "unknown") that info.Version otherwise carries for every other
		// caller (the text path below, and self-update's Config.CurrentVersion
		// via collectVersion().Version). Override only the JSON-encoded copy;
		// the plain `wb version` banner stays exactly as it prints today.
		info.Version = buildinfo.JSON().Version
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
	fleetJSON := buildinfo.JSON()
	return versionInfo{
		Version:  buildinfo.Version(),
		Revision: buildinfo.Revision(),
		Built:    buildinfo.Date(),
		Modified: buildinfo.Modified(),
		Go:       runtime.Version(),
		Platform: runtime.GOOS + "/" + runtime.GOARCH,

		Name:       fleetJSON.Name,
		Commit:     fleetJSON.Commit,
		Date:       fleetJSON.Date,
		DateSource: fleetJSON.DateSource,
	}
}
