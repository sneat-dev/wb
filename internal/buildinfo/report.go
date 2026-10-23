package buildinfo

import "runtime"

// Report captures the WB and fleet version metadata without performing I/O.
type Report struct {
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
	// JSONVersion preserves the fleet placeholder independently of text output.
	JSONVersion string `json:"-" yaml:"-"`
}

// Snapshot resolves a report using the existing build metadata conventions.
func Snapshot() Report {
	fleetJSON := JSON()
	return Report{
		Version: Version(), Revision: Revision(), Built: Date(), Modified: Modified(),
		Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Name: fleetJSON.Name, Commit: fleetJSON.Commit, Date: fleetJSON.Date, DateSource: fleetJSON.DateSource,
		JSONVersion: fleetJSON.Version,
	}
}
