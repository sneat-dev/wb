package ciaudit

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestGpCovAuditPropagatesReadErrorsFromEveryScannedFileKind pins that a tree
// Audit cannot read is reported as an error rather than silently audited as if
// the file were absent: an unreadable manifest, coverage config, Playwright
// test source, and workflow must each surface.
func TestGpCovAuditPropagatesReadErrorsFromEveryScannedFileKind(t *testing.T) {
	t.Parallel()
	const goSource = "package main\nfunc main() {}\n"
	cases := []struct {
		name string
		file string
	}{
		{name: "package manifest", file: "package.json"},
		{name: "coverage config", file: "vitest.config.ts"},
		{name: "Playwright test source", file: "tests/e2e/runtime.spec.js"},
		{name: "workflow file", file: ".github/workflows/ci.yml"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write(t, root, "main.go", goSource)
			write(t, root, testCase.file, "placeholder\n")
			gpCovMakeUnreadable(t, filepath.Join(root, filepath.FromSlash(testCase.file)))

			report, err := Audit(root)
			if err == nil {
				t.Fatalf("Audit accepted an unreadable %s: %+v", testCase.file, report)
			}
			if !strings.Contains(err.Error(), filepath.FromSlash(testCase.file)) {
				t.Fatalf("error %q does not name the unreadable file %s", err, testCase.file)
			}
		})
	}
}

func TestGpCovAuditOnAMissingRootReportsTheWalkError(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "absent-root")
	report, err := Audit(missing)
	if err == nil {
		t.Fatalf("Audit on a missing root returned %+v, want an error", report)
	}
	if !strings.Contains(err.Error(), "absent-root") {
		t.Fatalf("error %q does not name the missing root", err)
	}
}

// TestGpCovAuditDoesNotCountSourcesUnderIgnoredDirectories pins that a checked
// out dependency tree or build output never makes a repository look like it
// ships Go source, so no coverage gate is demanded of it.
func TestGpCovAuditDoesNotCountSourcesUnderIgnoredDirectories(t *testing.T) {
	t.Parallel()
	for _, directory := range []string{".git", ".worktrees", "node_modules", "vendor", "dist", "coverage", ".nx", ".cache"} {
		t.Run(directory, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write(t, root, filepath.Join(directory, "main.go"), "package main\n")
			report, err := Audit(root)
			if err != nil {
				t.Fatal(err)
			}
			if report.HasGo {
				t.Fatalf("a .go file under %s was counted as repository source: %+v", directory, report)
			}
			if len(report.Findings) != 0 {
				t.Fatalf("an ignored directory produced findings: %+v", report.Findings)
			}
		})
	}

	t.Run("a source file outside them is counted", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, "main.go", "package main\n")
		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if !report.HasGo || !hasFinding(report, "go-coverage-threshold") {
			t.Fatalf("a top-level Go source file was not counted: %+v", report)
		}
	})
}

// TestGpCovAuditReportsAnArtifactConsumerThatChecksNoProvenance covers the
// middle of the promotion triangle: CI publishes an artifact and the deploy
// downloads it, but nothing ties the bytes back to the source revision.
func TestGpCovAuditReportsAnArtifactConsumerThatChecksNoProvenance(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, ".github/workflows/ci.yml", `
jobs:
  build:
    steps:
      - run: echo build
      - uses: actions/upload-artifact@v4.1.7
`)
	write(t, root, ".github/workflows/deploy.yml", `
jobs:
  deploy:
    steps:
      - uses: actions/download-artifact@v4.1.7
      - run: firebase deploy
`)

	report, err := Audit(root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.HasDeploy {
		t.Fatalf("the deploy workflow was not recognized: %+v", report)
	}
	finding := gpCovFinding(t, report, "artifact-missing-provenance-check")
	if finding.File != ".github/workflows/deploy.yml" {
		t.Fatalf("finding names %q, want the deploy workflow", finding.File)
	}
	if report.ArtifactPromotion {
		t.Fatalf("promotion accepted without a provenance check: %+v", report)
	}
}

// TestGpCovAuditOrdersFindingsByCodeThenFile pins the deterministic report
// order a caller can diff against: codes sort first, and findings sharing a
// code sort by their file.
func TestGpCovAuditOrdersFindingsByCodeThenFile(t *testing.T) {
	t.Parallel()
	t.Run("findings sharing a code are ordered by file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, "main.go", "package main\n")
		write(t, root, ".github/workflows/a.yml", "jobs:\n  a:\n    steps:\n      - uses: actions/checkout@v4\n")
		write(t, root, ".github/workflows/z.yml", "jobs:\n  z:\n    steps:\n      - uses: actions/setup-go@v5\n")

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		gpCovAssertFindings(t, report.Findings, []string{
			"go-coverage-threshold@",
			"unpinned-tool-install@.github/workflows/a.yml",
			"unpinned-tool-install@.github/workflows/z.yml",
		})
	})

	// The deploy loop appends "deploy-rebuilds-source" for a.yml before
	// "deploy-missing-artifact" for b.yml; only the sort can put them in code
	// order, so this asserts a real reorder rather than a no-op pass.
	t.Run("a later finding is moved ahead of an earlier one", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, ".github/workflows/a.yml", `
jobs:
  deploy:
    steps:
      - run: go build ./cmd/app
      - uses: actions/upload-artifact@v4.1.7
      - uses: actions/download-artifact@v4.1.7
      - run: echo "source-sha=$SHA sha256sum=$SUM"
      - run: firebase deploy
`)
		write(t, root, ".github/workflows/b.yml", `
jobs:
  deploy:
    steps:
      - run: firebase deploy
`)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		gpCovAssertFindings(t, report.Findings, []string{
			"deploy-missing-artifact@.github/workflows/b.yml",
			"deploy-rebuilds-source@.github/workflows/a.yml",
		})
		if report.ArtifactPromotion {
			t.Fatalf("a deployment that rebuilds source was accepted as promotion: %+v", report)
		}
	})

	// A finding that is neither artifact- nor deploy-shaped must not veto
	// promotion: hasArtifactFinding has to walk findings that do not match.
	t.Run("an unrelated finding does not veto promotion", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, ".github/workflows/ci.yml", `
jobs:
  build:
    steps:
      - uses: actions/upload-artifact@v4.1.7
`)
		write(t, root, ".github/workflows/deploy.yml", `
jobs:
  deploy:
    steps:
      - uses: actions/download-artifact@v4.1.7
      - run: echo "source-sha=$SHA sha256sum=$SUM"
      - run: firebase deploy
`)
		write(t, root, ".github/workflows/e2e.yml", `
jobs:
  one:
    uses: sneat-co/cicd/.github/workflows/playwright-e2e.yml@main
  two:
    uses: sneat-co/cicd/.github/workflows/playwright-e2e.yml@main
`)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		gpCovFinding(t, report, "duplicate-e2e-setup")
		if !report.ArtifactPromotion {
			t.Fatalf("an unrelated finding vetoed artifact promotion: %+v", report)
		}
	})
}

// TestGpCovAuditRecognizesC8GateDeclaredInTheWorkflowItself covers the shape
// where the coverage floor lives in the workflow's own c8 invocation rather
// than in a package script.
func TestGpCovAuditRecognizesC8GateDeclaredInTheWorkflowItself(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	write(t, root, "src/pages/index.astro", "<main>Surpriseless</main>")
	write(t, root, ".github/workflows/ci.yml", `
jobs:
  unit:
    steps:
      - run: npx c8 --check-coverage --lines 85 node --test
`)

	report, err := Audit(root)
	if err != nil {
		t.Fatal(err)
	}
	if !report.HasFrontend {
		t.Fatalf("Astro source was not recognized as frontend: %+v", report)
	}
	if !report.FrontendCoverageThreshold {
		t.Fatalf("a workflow-level c8 floor was not recognized: %+v", report)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("a workflow-level c8 floor produced findings: %+v", report.Findings)
	}
}

// TestGpCovAuditDoesNotReadACoverageGateFromDocumentation pins that a
// Playwright V8 gate quoted inside docs/ is documentation, not evidence that
// CI enforces a floor.
func TestGpCovAuditDoesNotReadACoverageGateFromDocumentation(t *testing.T) {
	t.Parallel()
	const manifest = `{
  "scripts": {"test:coverage": "pnpm run build && playwright test"},
  "devDependencies": {"@playwright/test": "1", "astro": "7"}
}`
	const workflow = `
jobs:
  landing:
    steps:
      - run: pnpm run test:coverage
`
	const source = `import { expect, test } from "@playwright/test";
test("built landing runtime", async ({ page }) => {
  await page.coverage.startJSCoverage();
  const entries = await page.coverage.stopJSCoverage();
  const percentage = executableLineCoverage(entries);
  expect(percentage).toBeGreaterThanOrEqual(90);
});
`

	t.Run("a gate quoted under docs/", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, "package.json", manifest)
		write(t, root, "src/pages/index.astro", "<main>Surpriseless</main>")
		write(t, root, "docs/tests/e2e/runtime.spec.js", source)
		write(t, root, ".github/workflows/ci.yml", workflow)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if !report.HasFrontend {
			t.Fatalf("Astro source was not recognized as frontend: %+v", report)
		}
		if report.FrontendCoverageThreshold {
			t.Fatalf("a documentation example was accepted as a CI gate: %+v", report)
		}
		gpCovFinding(t, report, "frontend-coverage-threshold")
	})

	t.Run("the same source outside docs/", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, "package.json", manifest)
		write(t, root, "src/pages/index.astro", "<main>Surpriseless</main>")
		write(t, root, "tests/e2e/runtime.spec.js", source)
		write(t, root, ".github/workflows/ci.yml", workflow)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if !report.FrontendCoverageThreshold {
			t.Fatalf("an enforced Playwright V8 gate was not recognized: %+v", report)
		}
	})
}

// TestGpCovAuditReadsCoverageThresholdsFromTestRunnerConfigs covers the
// vitest/jest config read and the file it feeds jsConfigThreshold.
func TestGpCovAuditReadsCoverageThresholdsFromTestRunnerConfigs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		config string
		body   string
	}{
		{name: "vitest", config: "vitest.config.ts", body: "export default { test: { coverage: { thresholds: { lines: 80 } } } };\n"},
		{name: "jest", config: "jest.config.js", body: "module.exports = { coverageThreshold: { global: { lines: 80 } } };\n"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			write(t, root, "src/pages/index.astro", "<main>Surpriseless</main>")
			write(t, root, testCase.config, testCase.body)
			write(t, root, ".github/workflows/ci.yml", "jobs:\n  unit:\n    steps:\n      - run: pnpm test\n")

			report, err := Audit(root)
			if err != nil {
				t.Fatal(err)
			}
			if !report.FrontendCoverageThreshold {
				t.Fatalf("%s threshold was not recognized: %+v", testCase.name, report)
			}
			if len(report.Findings) != 0 {
				t.Fatalf("a %s threshold produced findings: %+v", testCase.name, report.Findings)
			}
		})
	}
}

// TestGpCovAuditCreditsAWBCoverageMinimumOnlyWithinItsStep pins the step
// boundary truncation: a --minimum belonging to the next step must not be read
// as the gate of the wb coverage step.
func TestGpCovAuditCreditsAWBCoverageMinimumOnlyWithinItsStep(t *testing.T) {
	t.Parallel()
	t.Run("a minimum in a later step is not credited", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, "main.go", "package main\n")
		write(t, root, ".github/workflows/ci.yml", `
jobs:
  coverage:
    steps:
      - run: wb coverage .
      - run: echo --minimum=58
`)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if report.GoCoverageThreshold {
			t.Fatalf("another step's --minimum was credited to wb coverage: %+v", report)
		}
		gpCovFinding(t, report, "go-coverage-threshold")
	})

	t.Run("a minimum in the same step is credited", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, "main.go", "package main\n")
		write(t, root, ".github/workflows/ci.yml", `
jobs:
  coverage:
    steps:
      - run: wb coverage . --minimum=58
`)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if !report.GoCoverageThreshold {
			t.Fatalf("the wb coverage step's own --minimum was not credited: %+v", report)
		}
		if hasFinding(report, "go-coverage-threshold") {
			t.Fatalf("a positive wb coverage gate produced a finding: %+v", report.Findings)
		}
	})
}

// TestGpCovAuditBoundsTheWindowItSearchesForAWBCoverageMinimum pins the
// 4096-byte cap that keeps a marker from scanning an entire workflow file.
func TestGpCovAuditBoundsTheWindowItSearchesForAWBCoverageMinimum(t *testing.T) {
	t.Parallel()
	t.Run("a minimum beyond the window is not credited", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, "main.go", "package main\n")
		write(t, root, ".github/workflows/ci.yml",
			"jobs:\n  coverage:\n    steps:\n      - run: |\n          wb coverage .\n          "+
				strings.Repeat("x", 5000)+"\n          --minimum=58\n")

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if report.GoCoverageThreshold {
			t.Fatalf("a --minimum beyond the window was credited: %+v", report)
		}
		gpCovFinding(t, report, "go-coverage-threshold")
	})

	t.Run("a minimum inside the window is credited", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		write(t, root, "main.go", "package main\n")
		write(t, root, ".github/workflows/ci.yml",
			"jobs:\n  coverage:\n    steps:\n      - run: |\n          wb coverage . --minimum=58\n          "+
				strings.Repeat("x", 5000)+"\n")

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if !report.GoCoverageThreshold {
			t.Fatalf("a --minimum inside the window was not credited: %+v", report)
		}
	})
}
