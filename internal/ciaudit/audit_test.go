package ciaudit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuditAcceptsCoverageAndVerifiedArtifactPromotion(t *testing.T) {
	root := t.TempDir()
	write(t, root, "backend/main.go", "package main\nfunc main() {}\n")
	write(t, root, "frontend/package.json", `{"devDependencies":{"vitest":"1"}}`)
	write(t, root, ".github/workflows/ci.yml", `
jobs:
  changes:
    steps:
      - run: git diff --name-only "$BASE_SHA" "$GITHUB_SHA"
  backend:
    with:
      min_test_coverage_percent: "85.5"
  frontend:
    with:
      minimum-coverage: 53.5
      artifact-name: frontend-dist
      artifact-paths: frontend/dist
`)
	write(t, root, ".github/workflows/deploy.yml", `
jobs:
  deploy:
    uses: sneat-co/cicd/.github/workflows/firebase-deploy.yml@main
    with:
      artifact-name: frontend-dist
      artifact-run-id: 123
      source-sha: abc
`)

	report, err := Audit(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("unexpected findings: %+v", report.Findings)
	}
	if !report.GoCoverageThreshold || !report.FrontendCoverageThreshold || !report.ArtifactPromotion {
		t.Fatalf("policy not recognized: %+v", report)
	}
}

func TestAuditReportsMissingThresholdsAndDeployRebuild(t *testing.T) {
	root := t.TempDir()
	write(t, root, "main.go", "package main\n")
	write(t, root, "package.json", `{"devDependencies":{"vitest":"1"}}`)
	write(t, root, ".github/workflows/deploy.yml", `
jobs:
  deploy:
    steps:
      - run: pnpm exec nx build app
      - run: firebase deploy
`)

	report, err := Audit(root)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"artifact-missing-producer":   false,
		"deploy-missing-artifact":     false,
		"deploy-rebuilds-source":      false,
		"frontend-coverage-threshold": false,
		"go-coverage-threshold":       false,
	}
	for _, finding := range report.Findings {
		if _, ok := want[finding.Code]; ok {
			want[finding.Code] = true
		}
	}
	for code, found := range want {
		if !found {
			t.Errorf("missing finding %q: %+v", code, report.Findings)
		}
	}
}

func TestAuditReportsDuplicateE2ESetup(t *testing.T) {
	root := t.TempDir()
	write(t, root, "package.json", `{"devDependencies":{"vitest":"1"}}`)
	write(t, root, ".github/workflows/ci.yml", `
jobs:
  unit:
    with:
      minimum-coverage: 50
  e2e-one:
    uses: sneat-co/cicd/.github/workflows/playwright-e2e.yml@main
  e2e-two:
    uses: sneat-co/cicd/.github/workflows/playwright-e2e.yml@main
`)

	report, err := Audit(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range report.Findings {
		if finding.Code == "duplicate-e2e-setup" {
			return
		}
	}
	t.Fatalf("duplicate E2E finding missing: %+v", report.Findings)
}

func TestAuditRecognizesAstroRuntimeCoverageWithoutClassifyingManifestOnlyDocsAsFrontend(t *testing.T) {
	t.Run("Astro source with CI-invoked c8 coverage", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", `{
  "scripts": {
    "coverage": "c8 --check-coverage --lines 85 --functions 85 node --test"
  },
  "devDependencies": {"astro": "7", "c8": "12"}
}`)
		write(t, root, "src/components/Header.astro", `<header>Shared header</header>`)
		write(t, root, ".github/workflows/ci.yml", `
jobs:
  check:
    steps:
      - run: pnpm coverage
`)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if !report.HasFrontend || !report.FrontendCoverageThreshold || len(report.Findings) != 0 {
			t.Fatalf("Astro runtime CI policy not recognized: %+v", report)
		}
	})

	t.Run("manifest-only documentation", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", `{
  "scripts": {
    "coverage": "c8 --check-coverage --lines 85 node --test"
  },
  "devDependencies": {"astro": "7", "c8": "12"}
}`)
		write(t, root, "README.md", "Astro package installation notes only.\n")
		write(t, root, ".github/workflows/ci.yml", `
jobs:
  docs:
    steps:
      - run: pnpm coverage
`)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if report.HasFrontend || report.FrontendCoverageThreshold || len(report.Findings) != 0 {
			t.Fatalf("manifest-only documentation misclassified: %+v", report)
		}
	})
}

func TestAuditRecognizesOnlyEnforcedPlaywrightV8Coverage(t *testing.T) {
	const manifest = `{
  "scripts": {
    "test:coverage": "pnpm run build && playwright test"
  },
  "devDependencies": {"@playwright/test": "1", "astro": "7"}
}`
	const workflow = `
jobs:
  landing:
    steps:
      - run: pnpm run test:coverage
`
	const positiveGate = `
import { expect, test } from "@playwright/test";

test("built landing runtime", async ({ page }) => {
  await page.coverage.startJSCoverage({ reportAnonymousScripts: true });
  await page.goto("/");
  const entries = await page.coverage.stopJSCoverage();
  const percentage = executableLineCoverage(entries);
  expect(percentage).toBeGreaterThanOrEqual(90);
});
`

	t.Run("invoked coverage script with positive executable-line gate", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", manifest)
		write(t, root, "src/pages/index.astro", `<main>Surpriseless</main>`)
		write(t, root, "tests/e2e/runtime.spec.js", positiveGate)
		write(t, root, ".github/workflows/ci.yml", workflow)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if !report.HasFrontend || !report.FrontendCoverageThreshold || len(report.Findings) != 0 {
			t.Fatalf("Playwright V8 coverage gate not recognized: %+v", report)
		}
	})

	t.Run("coverage package script is not invoked", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", manifest)
		write(t, root, "src/pages/index.astro", `<main>Surpriseless</main>`)
		write(t, root, "tests/e2e/runtime.spec.js", positiveGate)
		write(t, root, ".github/workflows/ci.yml", `
jobs:
  landing:
    steps:
      - run: pnpm run build
`)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if report.FrontendCoverageThreshold || !hasFinding(report, "frontend-coverage-threshold") {
			t.Fatalf("uninvoked package script accepted as a coverage gate: %+v", report)
		}
	})

	t.Run("ordinary Playwright test", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", manifest)
		write(t, root, "src/pages/index.astro", `<main>Surpriseless</main>`)
		write(t, root, "tests/e2e/runtime.spec.js", `
import { expect, test } from "@playwright/test";
test("landing", async ({ page }) => {
  await page.goto("/");
  await expect(page.locator("main")).toBeVisible();
});
`)
		write(t, root, ".github/workflows/ci.yml", workflow)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if report.FrontendCoverageThreshold || !hasFinding(report, "frontend-coverage-threshold") {
			t.Fatalf("ordinary Playwright E2E accepted as a coverage gate: %+v", report)
		}
	})

	t.Run("zero threshold and diagnostic percentage", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", manifest)
		write(t, root, "src/pages/index.astro", `<main>Surpriseless</main>`)
		write(t, root, "tests/e2e/runtime.spec.js", `
import { expect, test } from "@playwright/test";
test("coverage diagnostic", async ({ page }) => {
  await page.coverage.startJSCoverage();
  const entries = await page.coverage.stopJSCoverage();
  const percentage = executableLineCoverage(entries);
  console.info("coverage threshold would be 90%", percentage);
  expect(entries.length).toBeGreaterThanOrEqual(1);
  expect(percentage).toBeGreaterThanOrEqual(0);
});
`)
		write(t, root, ".github/workflows/ci.yml", workflow)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if report.FrontendCoverageThreshold || !hasFinding(report, "frontend-coverage-threshold") {
			t.Fatalf("zero or diagnostic threshold accepted as a coverage gate: %+v", report)
		}
	})

	t.Run("documentation example", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "package.json", manifest)
		write(t, root, "README.md", positiveGate)
		write(t, root, ".github/workflows/ci.yml", workflow)

		report, err := Audit(root)
		if err != nil {
			t.Fatal(err)
		}
		if report.HasFrontend || report.FrontendCoverageThreshold || len(report.Findings) != 0 {
			t.Fatalf("documentation-only Playwright example misclassified: %+v", report)
		}
	})
}

func hasFinding(report Report, code string) bool {
	for _, finding := range report.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func write(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
