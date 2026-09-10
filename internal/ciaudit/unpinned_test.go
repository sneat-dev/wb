package ciaudit

import (
	"strings"
	"testing"
)

// findingCodes counts findings by code, for assertions that care only about
// how many of one kind fired.
func findingCodes(findings []Finding, code string) int {
	count := 0
	for _, finding := range findings {
		if finding.Code == code {
			count++
		}
	}
	return count
}

// TestAuditReportsUnpinnedToolInstalls pins
// lesson:pin-the-toolchains-your-lint-gates-install: a required CI step that
// installs a tool with no pinned version reddens the gate on a tree nobody
// touched the next time the vendor ships a change.
func TestAuditReportsUnpinnedToolInstalls(t *testing.T) {
	cases := []struct {
		name     string
		workflow string
	}{
		{
			name: "curl piped to sh with no version env",
			workflow: `
jobs:
  install:
    steps:
      - name: install specscore
        run: curl -fsSL https://specscore.md/install/get-cli | sh
`,
		},
		{
			name: "go install @latest",
			workflow: `
jobs:
  install:
    steps:
      - run: go install golang.org/x/tools/cmd/goimports@latest
`,
		},
		{
			name: "npm global install with no version",
			workflow: `
jobs:
  install:
    steps:
      - run: npm install -g eslint
`,
		},
		{
			name: "pnpm global add with no version",
			workflow: `
jobs:
  install:
    steps:
      - run: pnpm add -g @angular/cli
`,
		},
		{
			name: "action uses a floating major with no comment",
			workflow: `
jobs:
  build:
    steps:
      - uses: actions/checkout@v4
`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, ".github/workflows/ci.yml", testCase.workflow)
			report, err := Audit(root)
			if err != nil {
				t.Fatal(err)
			}
			if count := findingCodes(report.Findings, "unpinned-tool-install"); count == 0 {
				t.Fatalf("expected an unpinned-tool-install finding, got none: %+v", report.Findings)
			}
		})
	}
}

// TestAuditAllowsPinnedToolInstalls pins the false-positive cases: every
// construct above, done the pinned way, and the constructs that merely look
// similar.
func TestAuditAllowsPinnedToolInstalls(t *testing.T) {
	cases := []struct {
		name     string
		workflow string
	}{
		{
			name: "curl piped to sh with a pinned version env beside it",
			workflow: `
jobs:
  install:
    steps:
      - name: install specscore
        env:
          SPECSCORE_VERSION: v0.38.4
        run: curl -fsSL https://specscore.md/install/get-cli | sh
`,
		},
		{
			name: "curl piped to sh with the version in the URL itself",
			workflow: `
jobs:
  install:
    steps:
      - run: curl -fsSL https://example.test/install/v1.2.3/get.sh | sh
`,
		},
		{
			name: "go install pinned to an exact version",
			workflow: `
jobs:
  install:
    steps:
      - run: go install golang.org/x/tools/cmd/goimports@v0.24.0
`,
		},
		{
			name: "npm global install pinned to an exact version",
			workflow: `
jobs:
  install:
    steps:
      - run: npm install -g eslint@8.57.0
`,
		},
		{
			name: "pnpm global add pinned, scoped package",
			workflow: `
jobs:
  install:
    steps:
      - run: pnpm add -g @angular/cli@16.2.0
`,
		},
		{
			name: "action pinned to an exact semver",
			workflow: `
jobs:
  build:
    steps:
      - uses: actions/checkout@v4.1.7
`,
		},
		{
			name: "action pinned to a commit SHA",
			workflow: `
jobs:
  build:
    steps:
      - uses: actions/checkout@8f4b7f84864484a7bf31766abe9204da3cbe65b3
`,
		},
		{
			name: "action floating major with a why-comment",
			workflow: `
jobs:
  build:
    steps:
      - uses: actions/checkout@v4 # pinned by Renovate; floating major is intentional here
`,
		},
		{
			name: "curl not piped to a shell at all",
			workflow: `
jobs:
  install:
    steps:
      - run: curl -fsSL https://example.test/asset.tar.gz -o asset.tar.gz
`,
		},
		{
			name: "an npx one-shot invocation is not a persistent global install",
			workflow: `
jobs:
  build:
    steps:
      - run: npx cowsay hello
`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, ".github/workflows/ci.yml", testCase.workflow)
			report, err := Audit(root)
			if err != nil {
				t.Fatal(err)
			}
			if count := findingCodes(report.Findings, "unpinned-tool-install"); count != 0 {
				var messages []string
				for _, finding := range report.Findings {
					if finding.Code == "unpinned-tool-install" {
						messages = append(messages, finding.Message)
					}
				}
				t.Fatalf("unexpected unpinned-tool-install findings: %s", strings.Join(messages, "; "))
			}
		})
	}
}
