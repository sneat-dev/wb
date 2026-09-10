package agentguard

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// inspectGitTagging judges a `git tag` or a tag-pushing `git push` against
// lesson:l3-check-existing-tags-before-tagging-a-lower-version-with-newer-code-hides-the-fix
// and lesson:l11-find-out-whether-the-repo-auto-tags-before-you-hand-tag-it: a
// repository whose release automation already owns tagging must not also
// receive a hand-pushed tag, because a stale local view can hand-tag a lower
// version onto newer code and hide the fix that already shipped.
//
// It denies only when the repository is recognisably auto-tagging — an
// explicit `.wb/hooks.yaml` (or global hooks policy) `agent: autoTags: true`,
// or the workflow heuristic lesson l11 itself names: a `strongo/cicd`
// reusable-workflow tag/release job present with no
// `disable-version-bumping: true` beside it. No config and no heuristic hit
// means allow — this is deliberately a narrow, high-confidence check, not a
// guess about every repository's release convention.
func inspectGitTagging(subcommand string, arguments []string, workingDirectory, projectsRoot string) *finding {
	if !looksLikeTagCreation(subcommand, arguments) {
		return nil
	}
	location, ok := managedGitLocation(workingDirectory, projectsRoot)
	if !ok || location.Slug() == "" {
		// The heuristic reads repository-local files (.wb/hooks.yaml,
		// .github/workflows/*.yml); outside a recognised canonical/linked
		// checkout there is nothing to read, so this fails open.
		return nil
	}
	reason, autoTagging := autoTaggingRepository(location.Root, projectsRoot)
	if !autoTagging {
		return nil
	}
	return &finding{Message: autoTaggingRefusal(location.Slug(), subcommand, reason)}
}

// looksLikeTagCreation reports whether the invocation would create or move a
// tag (`git tag <name>`) or push one (`git push --tags`, `git push origin
// <tag>`). Read-only tag inspection — `git tag -l`, `--list`, `--contains`,
// `-n<N>` (annotation lines), and deletion (`-d`/`--delete`, which removes
// rather than creates a tag and is not the construct either lesson is
// about) — is left alone.
func looksLikeTagCreation(subcommand string, arguments []string) bool {
	switch subcommand {
	case "tag":
		for _, argument := range arguments {
			switch {
			case argument == "-l", argument == "--list",
				argument == "-d", argument == "--delete",
				argument == "--contains", argument == "--no-contains",
				argument == "--points-at", argument == "--merged", argument == "--no-merged":
				return false
			case strings.HasPrefix(argument, "-n") && argument != "-n":
				// `-n<N>` (e.g. -n5) shows N lines of annotation; it is not
				// the bare `-n` shorthand that exists nowhere in `git tag`.
				return false
			}
		}
		return true
	case "push":
		if containsWord(arguments, "--tags") {
			return true
		}
		for _, argument := range arguments {
			if strings.HasPrefix(argument, "-") {
				continue
			}
			if strings.HasPrefix(argument, "refs/tags/") || looksLikeVersionTag(argument) {
				return true
			}
		}
		return false
	}
	return false
}

// versionTagPattern recognises the fleet's dominant tag shape (see
// ~/.claude/projects/…/memory: "pin a tag, not a pseudo-version",
// strongo/cicd's own `tags: 'v[0-9]+\.[0-9]+\.[0-9]+'` push trigger): an
// optional module-path prefix followed by a semver, with or without a
// leading v. This is a heuristic, not a ref-name parser — it exists to catch
// the concrete `git push origin <tag>` shape the brief names, not to
// classify every possible ref.
var versionTagPattern = regexp.MustCompile(`^([A-Za-z0-9._-]+/)*v?[0-9]+\.[0-9]+(\.[0-9]+)?$`)

func looksLikeVersionTag(ref string) bool {
	return versionTagPattern.MatchString(ref)
}

// autoTaggingConfig is the minimal shape read from `.wb/hooks.yaml` (or the
// global hooks policy) — a lightweight, self-contained decode rather than a
// dependency on internal/hooks.LoadPolicy, which resolves a repository root
// via `git rev-parse` and this package's whole design deliberately spawns no
// process (see checkout.go's package doc).
type autoTaggingConfig struct {
	Agent struct {
		AutoTags *bool `yaml:"autoTags"`
	} `yaml:"agent"`
}

// autoTaggingRepository reports whether repoRoot is recognisably
// auto-tagging, and names which signal decided it for the refusal message.
func autoTaggingRepository(repoRoot, projectsRoot string) (reason string, autoTagging bool) {
	if value, ok := readAutoTagsFlag(filepath.Join(repoRoot, ".wb", "hooks.yaml")); ok {
		if value {
			return "`.wb/hooks.yaml` declares `agent.autoTags: true`", true
		}
		// An explicit `false` at the repository is a deliberate override:
		// stop here rather than falling through to the global policy or the
		// workflow heuristic.
		return "", false
	}
	if value, ok := readAutoTagsFlag(globalHooksConfigPath()); ok && value {
		return "the global hooks policy declares `agent.autoTags: true`", true
	}
	if reason, hit := autoTaggingWorkflowHeuristic(repoRoot); hit {
		return reason, true
	}
	return "", false
}

func readAutoTagsFlag(path string) (value bool, ok bool) {
	if path == "" {
		return false, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, false
	}
	var config autoTaggingConfig
	if yaml.Unmarshal(raw, &config) != nil || config.Agent.AutoTags == nil {
		return false, false
	}
	return *config.Agent.AutoTags, true
}

func globalHooksConfigPath() string {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "wb", "hooks.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "wb", "hooks.yaml")
}

// disableVersionBumping matches the exact key strongo/cicd's reusable Go
// workflow reads to opt a repository OUT of auto-tagging (verified across
// the fleet, e.g. dal-go/dalgo2postgres's `disable-version-bumping: true`).
var disableVersionBumping = regexp.MustCompile(`(?i)disable-version-bumping\s*:\s*true`)

// strongoCICDReusableWorkflow matches the `uses:` line of strongo/cicd's
// shared workflow (verified across the fleet, e.g.
// dal-go/dalgo's `uses: strongo/cicd/.github/workflows/workflow.yml@main`).
var strongoCICDReusableWorkflow = regexp.MustCompile(`(?i)uses:\s*strongo/cicd/\.github/workflows/\S+\.ya?ml@\S+`)

// autoTaggingWorkflowHeuristic implements
// lesson:l11-find-out-whether-the-repo-auto-tags-before-you-hand-tag-it's own
// proposed mechanism verbatim: "grep the repo's workflows for
// disable-version-bumping. If it is absent, the repo tags itself." A
// strongo/cicd reusable workflow with no `disable-version-bumping: true`
// beside it means the repository's release automation owns tagging.
func autoTaggingWorkflowHeuristic(repoRoot string) (reason string, hit bool) {
	workflowsDir := filepath.Join(repoRoot, ".github", "workflows")
	entries, err := os.ReadDir(workflowsDir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		path := filepath.Join(workflowsDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(raw)
		if !strongoCICDReusableWorkflow.MatchString(content) {
			continue
		}
		if disableVersionBumping.MatchString(content) {
			continue
		}
		return ".github/workflows/" + entry.Name() + " calls the strongo/cicd reusable workflow with no `disable-version-bumping: true`", true
	}
	return "", false
}

func autoTaggingRefusal(slug, subcommand, reason string) string {
	var message strings.Builder
	message.WriteString("git " + subcommand + " would hand-tag " + slug + ", which auto-tags on its own CI.\n\n")
	message.WriteString("rule: check the repo's release convention before tagging it by hand\n")
	message.WriteString("(lesson l3-check-existing-tags-before-tagging-a-lower-version-with-newer-code-hides-the-fix,\n")
	message.WriteString(" lesson l11-find-out-whether-the-repo-auto-tags-before-you-hand-tag-it)\n\n")
	message.WriteString("Signal: " + reason + ".\n\n")
	message.WriteString("A stale local view can hand-tag a lower version onto newer code and hide the\n")
	message.WriteString("fix that already shipped through automation, or race the same evening's other\n")
	message.WriteString("agents tagging the same repository.\n\n")
	message.WriteString("Remedy: merge to main and wait for the release run instead; report the\n")
	message.WriteString("version it produced.\n")
	return message.String()
}
