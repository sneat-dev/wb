package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseKeepsGo127MacOSArtifactsRunnable(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	releasePath := filepath.Join(repoRoot, ".github", "workflows", "release.yml")
	releaseContents, err := os.ReadFile(releasePath)
	if err != nil {
		t.Fatal(err)
	}
	goreleaserPath := filepath.Join(repoRoot, ".goreleaser.yml")
	goreleaserContents, err := os.ReadFile(goreleaserPath)
	if err != nil {
		t.Fatal(err)
	}

	// The cross-platform quill signer currently produces an invalid signature
	// for WB's Go 1.27 Mach-O binaries. A notarization attempt can still report
	// success, so the release artifact itself must be the authority. Keep the
	// proven ad-hoc-signed cask path until a Go 1.27 pilot passes codesign
	// verification and executes on a clean macOS host. The shared recovery
	// contract is tracked at https://github.com/strongo/cicd/issues/66.
	for _, forbidden := range []string{"MACOS_SIGN_P12", "NOTARIZE_ISSUER_ID", "NOTARIZE_KEY_ID", "NOTARIZE_KEY"} {
		if strings.Contains(string(releaseContents), forbidden) {
			t.Errorf("%s enables unproven Go 1.27 signing secret %s", releasePath, forbidden)
		}
	}
	if strings.Contains(string(goreleaserContents), "notarize:") {
		t.Errorf("%s enables the unproven Go 1.27 cross-platform notarizer", goreleaserPath)
	}
	if !strings.Contains(string(goreleaserContents), `xattr", args: ["-dr", "com.apple.quarantine"`) {
		t.Errorf("%s must retain the proven Homebrew-cask quarantine removal fallback", goreleaserPath)
	}
}
