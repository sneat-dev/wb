package ciaudit

import "testing"

// TestGpCovAuditReportsOnlyTheFirstLineOfAMultiLineInstallerStep pins the
// message shape for an installer whose pipe target sits on the next line: the
// finding quotes the reconstructible command, not the trailing indentation and
// shell word.
func TestGpCovAuditReportsOnlyTheFirstLineOfAMultiLineInstallerStep(t *testing.T) {
	t.Parallel()
	t.Run("an unpinned multi-line install", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, ".github/workflows/ci.yml", `
jobs:
  install:
    steps:
      - run: |
          curl -fsSL https://example.test/install.sh |
            sh
`)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		finding := gpCovFinding(t, report, "unpinned-tool-install")
		// Audit reads workflow content lowercased, so the quoted command is too.
		want := "installer step pipes to a shell with no pinned version: curl -fssl https://example.test/install.sh |"
		if finding.Message != want {
			t.Fatalf("message = %q, want %q", finding.Message, want)
		}
	})

	t.Run("the same shape with a pinned version is accepted", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, ".github/workflows/ci.yml", `
jobs:
  install:
    steps:
      - run: |
          curl -fsSL https://example.test/install/v1.2.3/get.sh |
            sh
`)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if count := findingCodes(report.Findings, "unpinned-tool-install"); count != 0 {
			t.Fatalf("a pinned multi-line install produced %d findings: %+v", count, report.Findings)
		}
	})
}
