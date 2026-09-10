package ciaudit

import (
	"regexp"
	"strings"
)

// unpinnedToolFindings implements
// lesson:pin-the-toolchains-your-lint-gates-install and
// lesson:a-coverage-floor-is-bound-to-the-toolchain-that-measured-it: a
// required CI step that installs a tool with no pinned version means the
// tool vendor's next release silently changes what the gate expects, with no
// commit to the repository itself — the exact shape that reddened 21 of 22
// sneat-co repositories the same day specscore v0.38.3 shipped.
//
// Four constructs are recognised, each read from the workflow's own
// lowercased content (see scan() in audit.go):
//
//  1. `curl ... | sh` (or `| bash`) with no version marker anywhere in the
//     same step.
//  2. `go install <pkg>@latest`.
//  3. `npm`/`pnpm`/`yarn` global install with no `@<version>` suffix.
//  4. A GitHub Action `uses:` pin with a floating major (`@v1`, not
//     `@v1.2.3` and not a commit SHA) and no trailing comment explaining or
//     pinning it further.
func unpinnedToolFindings(workflows []workflowFile) []Finding {
	var findings []Finding
	for _, workflow := range workflows {
		steps := splitWorkflowSteps(workflow.content)
		for _, step := range steps {
			if match := curlPipeShellCommand.FindString(step); match != "" && !hasVersionMarker(step) {
				findings = append(findings, Finding{
					Code:    "unpinned-tool-install",
					Message: "installer step pipes to a shell with no pinned version: " + strings.TrimSpace(firstLine(match)),
					File:    workflow.path,
				})
			}
			if match := goInstallLatest.FindString(step); match != "" {
				findings = append(findings, Finding{
					Code:    "unpinned-tool-install",
					Message: "go install step pins no version: " + strings.TrimSpace(match),
					File:    workflow.path,
				})
			}
			for _, match := range globalPackageInstall.FindAllStringSubmatch(step, -1) {
				// match[2] is the optional `@<version>` suffix; empty means
				// unpinned. `npx`/`dlx` one-shot invocations are a different
				// construct (they resolve fresh every run rather than
				// installing a persistent binary the gate depends on later)
				// and are not matched by this pattern at all.
				if match[2] == "" {
					findings = append(findings, Finding{
						Code:    "unpinned-tool-install",
						Message: "global package install pins no version: " + strings.TrimSpace(match[0]),
						File:    workflow.path,
					})
				}
			}
		}
		for _, line := range strings.Split(workflow.content, "\n") {
			if match := floatingMajorActionUse.FindString(line); match != "" && !strings.Contains(line, "#") {
				findings = append(findings, Finding{
					Code:    "unpinned-tool-install",
					Message: "action reference pins only a floating major with no comment: " + strings.TrimSpace(match),
					File:    workflow.path,
				})
			}
		}
	}
	return findings
}

// splitWorkflowSteps breaks workflow content at each `- name:`/`- uses:`/
// `- run:` boundary (the same marker hasPositiveWBCoverageGate uses), so a
// version marker declared for one step is never credited to an unrelated
// step later in the same file.
func splitWorkflowSteps(content string) []string {
	indexes := workflowStepBoundary.FindAllStringIndex(content, -1)
	if len(indexes) == 0 {
		return []string{content}
	}
	steps := make([]string, 0, len(indexes)+1)
	if indexes[0][0] > 0 {
		steps = append(steps, content[:indexes[0][0]])
	}
	for i, index := range indexes {
		end := len(content)
		if i+1 < len(indexes) {
			end = indexes[i+1][0]
		}
		steps = append(steps, content[index[0]:end])
	}
	return steps
}

func firstLine(text string) string {
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		return text[:index]
	}
	return text
}

var (
	curlPipeShellCommand = regexp.MustCompile(`(?mi)\bcurl\b[^\n|]*\|\s*(?:sudo\s+)?(?:sh|bash)\b`)
	// versionMarker is deliberately broad: an env assignment
	// (`TOOL_VERSION=`, `SPECSCORE_VERSION:`), a pinned tag in the URL itself
	// (`/v1.2.3/`, `@v1.2.3`), or the bare word "version" covers every pinned
	// installer this fleet actually uses (see
	// lesson:pin-the-toolchains-your-lint-gates-install's own fix:
	// `SPECSCORE_VERSION: v0.38.3`).
	versionMarker   = regexp.MustCompile(`(?i)version|[/@]v?[0-9]+\.[0-9]+(\.[0-9]+)?\b`)
	goInstallLatest = regexp.MustCompile(`(?mi)\bgo\s+install\s+\S+@latest\b`)
	// globalPackageInstall matches an npm/pnpm/yarn global install; group 1 is
	// the package name, group 2 is the optional `@<version>` pin.
	globalPackageInstall   = regexp.MustCompile(`(?mi)\b(?:npm|pnpm|yarn)\s+(?:i|install|add|global add)\s+(?:-g|--global)\s+(@?[a-z0-9][a-z0-9._\/-]*)(@[a-z0-9._-]+)?`)
	floatingMajorActionUse = regexp.MustCompile(`(?mi)^\s*-?\s*uses:\s*[a-z0-9_.\/-]+@(v[0-9]+)(?:[^0-9.]|$)`)
)

func hasVersionMarker(step string) bool {
	return versionMarker.MatchString(step)
}
