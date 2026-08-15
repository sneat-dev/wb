// Package ciaudit checks repository CI/CD files for explicit coverage gates and
// build-once artifact promotion. It is deliberately read-only so the same audit
// can run locally, in CI, or across a workstation fleet.
package ciaudit

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Finding struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	File    string `json:"file,omitempty"`
}

type Report struct {
	Path                      string    `json:"path"`
	HasGo                     bool      `json:"has_go"`
	HasFrontend               bool      `json:"has_frontend"`
	HasDeploy                 bool      `json:"has_deploy"`
	GoCoverageThreshold       bool      `json:"go_coverage_threshold"`
	FrontendCoverageThreshold bool      `json:"frontend_coverage_threshold"`
	ArtifactPromotion         bool      `json:"artifact_promotion"`
	Findings                  []Finding `json:"findings"`
}

type workflowFile struct {
	path    string
	content string
	deploy  bool
}

var (
	positiveGoThreshold         = regexp.MustCompile(`(?mi)min_test_coverage_percent\s*:\s*["']?(?:[1-9][0-9]*(?:\.[0-9]+)?|0\.[0-9]*[1-9])`)
	positiveJSWorkflow          = regexp.MustCompile(`(?mi)(?:minimum-coverage|coverage-threshold)\s*:\s*["']?(?:[1-9][0-9]*(?:\.[0-9]+)?|0\.[0-9]*[1-9])`)
	jsConfigThreshold           = regexp.MustCompile(`(?mi)(?:coverageThreshold|thresholds)\s*[:=]`)
	positiveC8Threshold         = regexp.MustCompile(`(?mi)\bc8\b[^\n]*--check-coverage[^\n]*--(?:lines|statements|functions|branches)(?:=|\s+)(?:[1-9][0-9]*(?:\.[0-9]+)?|0\.[0-9]*[1-9])`)
	coverageScriptRun           = regexp.MustCompile(`(?mi)\b(?:pnpm|npm|yarn)\s+(?:run\s+)?[a-z0-9:_-]*coverage[a-z0-9:_-]*\b`)
	playwrightTestRun           = regexp.MustCompile(`(?mi)\b(?:(?:pnpm|npm|yarn)\s+(?:exec\s+)?|npx\s+)?playwright\s+test\b`)
	playwrightV8Start           = regexp.MustCompile(`(?mi)\.\s*coverage\s*\.\s*startjscoverage\s*\(`)
	playwrightV8Stop            = regexp.MustCompile(`(?mi)\.\s*coverage\s*\.\s*stopjscoverage\s*\(`)
	positiveCoverageExpectFloor = regexp.MustCompile(`(?mis)\bexpect\s*\(\s*\b[a-z0-9_$]*(?:coverage|percentage|percent)[a-z0-9_$]*\b(?:\s*,.{0,500}?)?\)\s*\.\s*tobegreaterthanorequal\s*\(\s*(?:[1-9][0-9]*(?:\.[0-9]+)?|0\.[0-9]*[1-9])\s*\)`)
	deployCommand               = regexp.MustCompile(`(?mi)(firebase(?:-tools[^\n]*)?\s+deploy|wrangler(?:-action|[^\n]*)?\s+deploy|gcloud\s+(?:run|app)\s+deploy|kubectl\s+apply|helm\s+(?:install|upgrade))`)
	sourceBuildCommand          = regexp.MustCompile(`(?mi)(pnpm\s+(?:exec\s+nx\s+|run\s+)?build|npm\s+(?:run\s+)?build|yarn\s+build|\bnx\s+build|\bgo\s+build|\bcargo\s+build)`)
)

func Audit(root string) (Report, error) {
	report := Report{Path: root}
	workflows, configText, packageCoverageScripts, playwrightV8Gate, err := scan(root, &report)
	if err != nil {
		return Report{}, err
	}

	allWorkflowText := strings.Builder{}
	for _, workflow := range workflows {
		allWorkflowText.WriteString(workflow.content)
		allWorkflowText.WriteByte('\n')
	}
	workflowText := allWorkflowText.String()
	report.GoCoverageThreshold = positiveGoThreshold.MatchString(workflowText) || hasGoCoverageComparison(workflowText)
	report.FrontendCoverageThreshold = report.HasFrontend && (positiveJSWorkflow.MatchString(workflowText) ||
		jsConfigThreshold.MatchString(configText) ||
		hasPositiveC8Coverage(workflowText, packageCoverageScripts) ||
		hasPositivePlaywrightV8Coverage(workflowText, packageCoverageScripts, playwrightV8Gate))
	pathScoped := strings.Contains(workflowText, "git diff --name-only") ||
		strings.Contains(workflowText, "paths-filter") || strings.Contains(workflowText, "nx affected")

	if report.HasGo && !report.GoCoverageThreshold {
		report.Findings = append(report.Findings, Finding{
			Code:    "go-coverage-threshold",
			Message: "Go sources require an explicit, positive CI coverage threshold",
		})
	}
	if report.HasFrontend && !report.FrontendCoverageThreshold {
		report.Findings = append(report.Findings, Finding{
			Code:    "frontend-coverage-threshold",
			Message: "frontend sources require an explicit, positive CI coverage threshold",
		})
	}
	if report.HasGo && report.HasFrontend && !pathScoped {
		report.Findings = append(report.Findings, Finding{
			Code:    "monorepo-unscoped-ci",
			Message: "mixed Go/frontend repository CI does not select jobs from changed paths",
		})
	}
	legacyE2ECalls := strings.Count(workflowText, "playwright-e2e.yml@")
	playwrightInstalls := strings.Count(workflowText, "playwright install")
	if legacyE2ECalls > 1 || playwrightInstalls > 1 {
		report.Findings = append(report.Findings, Finding{
			Code:    "duplicate-e2e-setup",
			Message: "multiple E2E jobs repeat Playwright/dependency setup; consolidate suites around one build artifact",
		})
	}

	artifactProducer := strings.Contains(workflowText, "upload-artifact") ||
		(strings.Contains(workflowText, "artifact-name:") && strings.Contains(workflowText, "artifact-path"))
	verifiedConsumers := 0
	for _, workflow := range workflows {
		if !workflow.deploy {
			continue
		}
		report.HasDeploy = true
		if sourceBuildCommand.MatchString(workflow.content) {
			report.Findings = append(report.Findings, Finding{
				Code:    "deploy-rebuilds-source",
				Message: "deployment builds source instead of promoting CI output",
				File:    workflow.path,
			})
		}
		consumer := strings.Contains(workflow.content, "download-artifact") ||
			strings.Contains(workflow.content, "restore-build-artifact") ||
			strings.Contains(workflow.content, "firebase-deploy.yml")
		verified := strings.Contains(workflow.content, "restore-build-artifact") ||
			(strings.Contains(workflow.content, "source-sha") &&
				(strings.Contains(workflow.content, "sha256sum") || strings.Contains(workflow.content, "checksum"))) ||
			(strings.Contains(workflow.content, "firebase-deploy.yml") && strings.Contains(workflow.content, "source-sha"))
		if !consumer {
			report.Findings = append(report.Findings, Finding{
				Code:    "deploy-missing-artifact",
				Message: "deployment does not consume a CI build artifact",
				File:    workflow.path,
			})
		} else if !verified {
			report.Findings = append(report.Findings, Finding{
				Code:    "artifact-missing-provenance-check",
				Message: "downloaded artifact is not verified against its source SHA and checksum",
				File:    workflow.path,
			})
		} else {
			verifiedConsumers++
		}
	}
	if report.HasDeploy && !artifactProducer {
		report.Findings = append(report.Findings, Finding{
			Code:    "artifact-missing-producer",
			Message: "repository deploys but CI does not publish a build artifact",
		})
	}
	report.ArtifactPromotion = report.HasDeploy && artifactProducer && verifiedConsumers > 0 && !hasArtifactFinding(report.Findings)
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Code == report.Findings[j].Code {
			return report.Findings[i].File < report.Findings[j].File
		}
		return report.Findings[i].Code < report.Findings[j].Code
	})
	return report, nil
}

func scan(root string, report *Report) ([]workflowFile, string, string, bool, error) {
	var workflows []workflowFile
	var config strings.Builder
	var packageCoverageScripts strings.Builder
	playwrightV8Gate := false
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != root && ignoredDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		lower := strings.ToLower(entry.Name())
		switch {
		case strings.HasSuffix(lower, ".go") && !strings.HasSuffix(lower, "_test.go"):
			report.HasGo = true
		case strings.HasSuffix(lower, ".astro"):
			// A real Astro source file is frontend code. A package manifest merely
			// mentioning Astro is not enough: documentation-only repositories often
			// contain those dependency names without shipping a frontend.
			report.HasFrontend = true
		case lower == "package.json":
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := strings.ToLower(string(data))
			if strings.Contains(text, "vitest") || strings.Contains(text, "jest") || strings.Contains(text, "@angular/") || strings.Contains(text, "@nx/") {
				report.HasFrontend = true
			}
			var manifest struct {
				Scripts map[string]string `json:"scripts"`
			}
			if json.Unmarshal(data, &manifest) == nil {
				for name, script := range manifest.Scripts {
					if strings.Contains(strings.ToLower(name), "coverage") {
						packageCoverageScripts.WriteString(strings.ToLower(script))
						packageCoverageScripts.WriteByte('\n')
					}
				}
			}
		case strings.Contains(lower, "vitest.config") || strings.Contains(lower, "jest.config"):
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			config.Write(data)
			config.WriteByte('\n')
		case isPlaywrightTestSource(rel, lower):
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if hasPositivePlaywrightV8GateSource(string(data)) {
				playwrightV8Gate = true
			}
		}
		if strings.HasPrefix(filepath.ToSlash(rel), ".github/workflows/") && (strings.HasSuffix(lower, ".yml") || strings.HasSuffix(lower, ".yaml")) {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			content := strings.ToLower(string(data))
			deploy := strings.Contains(lower, "deploy") || deployCommand.MatchString(content)
			workflows = append(workflows, workflowFile{path: filepath.ToSlash(rel), content: content, deploy: deploy})
		}
		return nil
	})
	return workflows, config.String(), packageCoverageScripts.String(), playwrightV8Gate, err
}

func hasPositiveC8Coverage(workflowText, packageCoverageScripts string) bool {
	if positiveC8Threshold.MatchString(workflowText) {
		return true
	}
	return positiveC8Threshold.MatchString(packageCoverageScripts) && coverageScriptRun.MatchString(workflowText)
}

func hasPositivePlaywrightV8Coverage(workflowText, packageCoverageScripts string, sourceGate bool) bool {
	if !sourceGate {
		return false
	}
	return playwrightTestRun.MatchString(workflowText) ||
		(playwrightTestRun.MatchString(packageCoverageScripts) && coverageScriptRun.MatchString(workflowText))
}

func hasPositivePlaywrightV8GateSource(content string) bool {
	return playwrightV8Start.MatchString(content) &&
		playwrightV8Stop.MatchString(content) &&
		positiveCoverageExpectFloor.MatchString(content)
}

func isPlaywrightTestSource(rel, lowerName string) bool {
	switch filepath.Ext(lowerName) {
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts":
	default:
		return false
	}

	path := "/" + strings.ToLower(filepath.ToSlash(rel))
	if strings.Contains(path, "/docs/") || strings.Contains(path, "/documentation/") {
		return false
	}
	return strings.Contains(path, "/test/") ||
		strings.Contains(path, "/tests/") ||
		strings.Contains(lowerName, ".spec.") ||
		strings.Contains(lowerName, ".test.")
}

func ignoredDirectory(name string) bool {
	switch name {
	case ".git", ".worktrees", "node_modules", "vendor", "dist", "coverage", ".nx", ".cache":
		return true
	default:
		return false
	}
}

func hasGoCoverageComparison(content string) bool {
	return strings.Contains(content, "go tool cover") &&
		(strings.Contains(content, "total_coverage") || strings.Contains(content, "minimum") || strings.Contains(content, "threshold"))
}

func hasArtifactFinding(findings []Finding) bool {
	for _, finding := range findings {
		if strings.HasPrefix(finding.Code, "artifact-") || strings.HasPrefix(finding.Code, "deploy-") {
			return true
		}
	}
	return false
}
