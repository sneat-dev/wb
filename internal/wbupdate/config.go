package wbupdate

import (
	"fmt"
	"time"

	"github.com/strongo/cli-helpers/cliinstall"
	"github.com/strongo/cli-helpers/selfupdate"
)

// CatalogID is wb's own id in github.com/strongo/cli-helpers/cliinstall's
// compiled-in fleet catalog (cli-install#req:host-identity-from-catalog).
// newSelfUpdateConfig and newInstallCmd both resolve this SAME entry, so
// `wb self-update` and every other fleet CLI's `install wb` agree on how wb
// is released, by construction rather than by two copies staying in sync
// (cli-install#req:catalog-identity-single-source).
const CatalogID = "wb"

// wb binds the shared github.com/strongo/cli-helpers/selfupdate library rather than
// reimplementing any of it: install-method detection, release resolution,
// checksum verification, atomic replacement, and every failure rule are
// that library's Feature, not wb's. This file supplies only what is
// genuinely wb's own — its release identity, the one package manager it
// ships through, and the mapping from the library's outcomes onto wb's own
// exit-code contract. See spec/features/self-update/README.md for the
// Feature spec that draws this boundary, and REQ: library-provided-behavior
// in particular.

// HomebrewUpgradeCommand is the exact command printed for a
// Homebrew-managed install. wb ships as a cask, not a formula, so this must
// carry --cask (REQ: wb-homebrew-cask).
const HomebrewUpgradeCommand = "brew update && brew upgrade --yes --cask -- wb"

// HomebrewInstallCommand is named alongside elevated permissions
// in the permission-failure remedy. It only ever fires on the manual-install
// path — a Homebrew-managed install is redirected long before any write is
// attempted — so a user who hit a permission error is pointed at wb's
// supported install channel instead of being left with only "run as sudo"
// (REQ: permission-remedy-names-brew).
const HomebrewInstallCommand = "brew install --cask sneat-dev/tap/wb"

// newSelfUpdateConfig returns wb's own selfupdate.Config, built from its
// compiled-in cliinstall catalog entry rather than restated by hand
// (cli-install#req:catalog-identity-single-source; REQ: wb-release-identity,
// REQ: wb-homebrew-cask, REQ: wb-version-identity). It is a plain function,
// not inlined into newSelfUpdateCmd, purely so selfupdate_test.go can assert
// its fields directly without constructing a command or touching any I/O.
//
// The catalog entry (github.com/strongo/cli-helpers/cliinstall's own
// catalog_wb.go) already reproduces wb's release identity field for field —
// the GitHub repository, the Homebrew cask manager
// (selfupdate.HomebrewCask("wb"), whose UpgradeCommand is byte-identical to
// HomebrewUpgradeCommand below), the supported darwin/linux
// platforms .goreleaser.yml publishes, the {"version","--json"} probe args,
// and the "unknown"/"(devel)" undetermined placeholders — so binding it here
// is not a behavior change, only a single source of truth: a future drift
// between wb's own self-update and any other fleet CLI's `install wb` now
// fails cli-helpers' own catalog-snapshot tests instead of silently
// diverging between two hand-copied literals.
func Config(version string) selfupdate.Config { return ConfigFor(CatalogID, version) }

func ConfigFor(id, version string) selfupdate.Config {
	entry, ok := cliinstall.ByID(id)
	if !ok {
		// A host id absent from the compiled catalog is a programming error
		// caught by TestNewSelfUpdateConfigIdentity, never a runtime state a
		// user can trigger (cli-install#req:host-identity-from-catalog).
		panic(fmt.Sprintf("cliinstall: no catalog entry for %q", id))
	}
	return entry.Config(version)
}

const HandoffMargin = 5 * time.Second
