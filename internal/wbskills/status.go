package wbskills

import (
	"fmt"

	"github.com/strongo/cli-helpers/skillsync"
)

// DriftMessage is printed by the SessionStart hook. Ordinary WB
// invocations stay quiet: repeating one warning on every tool call consumed
// more agent context than the warning protected and made private feature builds
// particularly noisy. A verified self-update still synchronizes skills
// immediately, while SessionStart reports drift once when it can affect work.
func DriftMessage(dir string, status skillsync.Status, currentVersion string) string {
	syncedVersion, installed := SyncedWBVersion(status)
	if !installed {
		return fmt.Sprintf("wb: Agent Skills are not installed in %s -- run `wb skills sync`", dir)
	}
	return fmt.Sprintf("wb: Agent Skills in %s were synced by wb %s, this is wb %s -- run `wb skills sync`", dir, syncedVersion, currentVersion)
}

func SyncedWBVersion(status skillsync.Status) (string, bool) {
	plugin := PluginIdentity().String()
	if !status.Installed {
		return "", false
	}
	if _, ok := status.Plugins[plugin]; !ok {
		return "", false
	}
	version := status.SupplierCLIVersions[plugin][CLIIdentity().String()]
	return version, version != ""
}
