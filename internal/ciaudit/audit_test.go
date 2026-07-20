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
