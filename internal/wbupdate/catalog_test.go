package wbupdate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/cliinstall"
)

// TestCatalogWBEntryMatchesGoReleaserConfig checks compiled catalog and release packaging agree.
func TestCatalogWBEntryMatchesGoReleaserConfig(t *testing.T) {
	t.Parallel()
	entry, ok := cliinstall.ByID(CatalogID)
	if !ok {
		t.Fatalf("no catalog entry for %q", CatalogID)
	}
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	contents, err := os.ReadFile(filepath.Join(repoRoot, ".goreleaser.yml"))
	if err != nil {
		t.Fatal(err)
	}
	goreleaser := string(contents)

	if entry.Repository != "sneat-dev/wb" {
		t.Errorf("catalog Repository = %q, want sneat-dev/wb (this repository, GoReleaser's implicit release target: no release.github override in .goreleaser.yml)", entry.Repository)
	}
	if entry.TagPrefix != "" {
		t.Errorf("catalog TagPrefix = %q, want empty: .goreleaser.yml publishes plain vX.Y.Z tags with no prefix", entry.TagPrefix)
	}
	if !strings.Contains(goreleaser, `name_template: "wb_{{ .Version }}_{{ .Os }}_{{ .Arch }}"`) {
		t.Error(".goreleaser.yml archive name_template drifted from wb_<version>_<os>_<arch>, which the catalog entry's nil AssetName (the library's GoReleaser-shaped default) assumes")
	}
	if !strings.Contains(goreleaser, `name_template: "wb_{{ .Version }}_checksums.txt"`) {
		t.Error(".goreleaser.yml checksum name_template drifted from wb_<version>_checksums.txt, which the catalog entry's nil ChecksumsName assumes")
	}
	for _, platform := range []string{"linux", "darwin"} {
		if !strings.Contains(goreleaser, "\n      - "+platform+"\n") {
			t.Errorf(".goreleaser.yml builds.goos is missing %q, present in the catalog's SupportedPlatforms", platform)
		}
	}
	if entry.CaskToken != "sneat-dev/tap/wb" {
		t.Errorf("catalog CaskToken = %q, want sneat-dev/tap/wb (sneat-dev/homebrew-tap's cask named wb)", entry.CaskToken)
	}
	if !strings.Contains(goreleaser, "name: homebrew-tap") || !strings.Contains(goreleaser, "owner: sneat-dev") {
		t.Error(".goreleaser.yml homebrew_casks.repository drifted from sneat-dev/homebrew-tap, which CaskToken sneat-dev/tap/wb assumes")
	}
	wantCaskOS := map[string]bool{"darwin": true, "linux": true}
	if len(entry.CaskOS) != len(wantCaskOS) {
		t.Fatalf("CaskOS = %v, want exactly %v", entry.CaskOS, wantCaskOS)
	}
	for _, caskOS := range entry.CaskOS {
		if !wantCaskOS[caskOS] {
			t.Errorf("unexpected CaskOS entry %q", caskOS)
		}
	}
}
