package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseSignsAndNotarizesMacOSArtifacts(t *testing.T) {
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

	// A prior "containment" period disabled macOS code signing under a
	// diagnosis that has since been proven wrong. Exit-137 SIGKILLs were
	// blamed on Go 1.27 Mach-O output; the real cause was that the .p12
	// signing bundle had been rebuilt without the Apple Root CA. quill sorts
	// the cert chain root-first and emits `root` for whatever sits at index
	// 0; with only leaf + G2 intermediate present, the G2 CA landed at index
	// 0, so the designated requirement read
	// `certificate root[field.1.2.840.113635.100.6.2.6]` — an OID the actual
	// Apple Root CA (what macOS resolves `root` to) does not carry, so the
	// requirement was unsatisfiable and the binary was killed.
	//
	// The .p12 has been rebuilt with the full leaf + intermediate + root
	// chain and pushed to all org secret stores. Proof it works, on a real
	// published Go 1.27 artifact: ingitdb/ingitdb-cli v0.65.11 (2026-08-30)
	// satisfies its designated requirement, chains to the Apple Root CA,
	// passes `spctl --assess` as Notarized Developer ID, and executes
	// (rc=0). So signing and notarization must be wired up, not withheld.
	for _, required := range []string{"MACOS_SIGN_P12", "MACOS_SIGN_PASSWORD", "NOTARIZE_ISSUER_ID", "NOTARIZE_KEY_ID", "NOTARIZE_KEY"} {
		if !strings.Contains(string(releaseContents), required) {
			t.Errorf("%s must forward macOS signing/notarization secret %s", releasePath, required)
		}
	}
	if !strings.Contains(string(releaseContents), "require_notarized_macos: true") {
		t.Errorf("%s must set require_notarized_macos: true", releasePath)
	}
	if !strings.Contains(string(goreleaserContents), "notarize:") {
		t.Errorf("%s must enable the notarize: block for macOS artifacts", goreleaserPath)
	}
}
