// Package hooks manages declarative, user-owned Git hook templates.
package hooks

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const PolicyVersion = 1

const (
	BuiltinPreCommit = "builtin:pre-commit"
	BuiltinPrePush   = "builtin:pre-push"
)

var validHookName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// HookConfig selects a script template for one Git hook. Relative template
// paths are resolved from the YAML file that declares them.
type HookConfig struct {
	Template string `yaml:"template" json:"template,omitempty"`
	Disabled bool   `yaml:"disabled" json:"disabled,omitempty"`
}

// MetricsConfig controls local hook-event collection. Enabled is a pointer so
// a repository policy can explicitly override a global true/false value.
type MetricsConfig struct {
	Enabled *bool             `yaml:"enabled" json:"enabled,omitempty"`
	Path    string            `yaml:"path" json:"path,omitempty"`
	Labels  map[string]string `yaml:"labels" json:"labels,omitempty"`
}

type fileConfig struct {
	Version  int                   `yaml:"version"`
	Hooks    map[string]HookConfig `yaml:"hooks"`
	Profiles ProfilesConfig        `yaml:"profiles"`
	Metrics  MetricsConfig         `yaml:"metrics"`
}

// ResolvedHook is a validated hook entry ready to execute.
type ResolvedHook struct {
	Name       string
	Template   string
	Builtin    bool
	Disabled   bool
	ConfigPath string
}

// Policy is the effective configuration after built-ins, the user's global
// policy, and the repository policy have been layered in that order.
type Policy struct {
	RepoRoot           string
	ConfigPaths        []string
	Hooks              map[string]ResolvedHook
	ProfilesAuto       bool
	ProfileSelections  map[string]bool
	ProfileDefinitions map[string]ProfileDefinition
	ActiveProfiles     []ActiveProfile
	Metrics            MetricsPolicy
	ExplicitPath       string
}

type MetricsPolicy struct {
	Enabled bool
	Path    string
	Labels  map[string]string
}

// LoadPolicy loads ~/.config/wb/hooks.yaml and .wb/hooks.yaml when present.
// An explicit path replaces those discovery locations but still layers on top
// of WB's conservative built-in templates.
func LoadPolicy(repoPath, explicitPath string) (Policy, error) {
	repoRoot, err := RepositoryRoot(repoPath)
	if err != nil {
		return Policy{}, err
	}
	policy := defaultPolicy(repoRoot)
	policy.ExplicitPath = explicitPath

	paths := []string{}
	if explicitPath != "" {
		paths = append(paths, expandPath(explicitPath))
	} else {
		if global := defaultGlobalConfigPath(); global != "" {
			paths = append(paths, global)
		}
		paths = append(paths, filepath.Join(repoRoot, ".wb", "hooks.yaml"))
	}

	for _, path := range paths {
		cfg, found, err := loadFile(path, explicitPath != "")
		if err != nil {
			return Policy{}, err
		}
		if !found {
			continue
		}
		policy.ConfigPaths = append(policy.ConfigPaths, path)
		if err := applyFile(&policy, path, cfg); err != nil {
			return Policy{}, err
		}
	}
	if policy.Metrics.Path == "" {
		policy.Metrics.Path = defaultMetricsPath()
	}
	policy.Metrics.Path = expandPath(policy.Metrics.Path)
	if err := resolveProfiles(&policy); err != nil {
		return Policy{}, err
	}
	if err := validatePolicy(policy); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

func defaultPolicy(repoRoot string) Policy {
	return Policy{
		RepoRoot: repoRoot,
		Hooks: map[string]ResolvedHook{
			"pre-commit": {Name: "pre-commit", Template: BuiltinPreCommit, Builtin: true},
			"pre-push":   {Name: "pre-push", Template: BuiltinPrePush, Builtin: true},
		},
		// Worktree admission is WB's safety boundary, not an optional language
		// profile. Every `wb hooks install` therefore installs the guard at
		// checkout, commit, and push unless a policy explicitly excludes it.
		// The explicit exclusion remains available for repositories where WB
		// cannot own checkout policy, and is visible in `wb hooks check`.
		ProfileSelections:  map[string]bool{"worktree": true},
		ProfileDefinitions: builtinProfileDefinitions(),
		Metrics:            MetricsPolicy{Enabled: true, Path: defaultMetricsPath(), Labels: map[string]string{}},
	}
}

func defaultGlobalConfigPath() string {
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "wb", "hooks.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "wb", "hooks.yaml")
}

func defaultMetricsPath() string {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "wb", "hook-events.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".wb", "hook-events.jsonl")
	}
	return filepath.Join(home, ".local", "state", "wb", "hook-events.jsonl")
}

func loadFile(path string, required bool) (fileConfig, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return fileConfig{}, false, nil
		}
		return fileConfig{}, false, fmt.Errorf("read hooks config %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)
	var cfg fileConfig
	if err := decoder.Decode(&cfg); err != nil {
		return fileConfig{}, false, fmt.Errorf("parse hooks config %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return fileConfig{}, false, fmt.Errorf("parse hooks config %s: %w", path, err)
	} else if err == nil {
		return fileConfig{}, false, fmt.Errorf("parse hooks config %s: multiple YAML documents are not supported", path)
	}
	if cfg.Version != PolicyVersion {
		return fileConfig{}, false, fmt.Errorf("hooks config %s has version %d; supported version is %d", path, cfg.Version, PolicyVersion)
	}
	return cfg, true, nil
}

func applyFile(policy *Policy, configPath string, cfg fileConfig) error {
	base := filepath.Dir(configPath)
	names := make([]string, 0, len(cfg.Hooks))
	for name := range cfg.Hooks {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := cfg.Hooks[name]
		if !validHookName.MatchString(name) {
			return fmt.Errorf("hooks config %s: invalid hook name %q", configPath, name)
		}
		resolved := ResolvedHook{Name: name, Disabled: entry.Disabled, ConfigPath: configPath}
		if !entry.Disabled {
			if strings.TrimSpace(entry.Template) == "" {
				return fmt.Errorf("hooks config %s: hook %q requires template or disabled: true", configPath, name)
			}
			resolved.Template = resolveTemplatePath(base, entry.Template)
			resolved.Builtin = strings.HasPrefix(resolved.Template, "builtin:")
		}
		policy.Hooks[name] = resolved
	}
	if err := applyProfiles(policy, configPath, cfg.Profiles); err != nil {
		return err
	}
	if cfg.Metrics.Enabled != nil {
		policy.Metrics.Enabled = *cfg.Metrics.Enabled
	}
	if cfg.Metrics.Path != "" {
		path := expandPath(cfg.Metrics.Path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(base, path)
		}
		policy.Metrics.Path = filepath.Clean(path)
	}
	for key, value := range cfg.Metrics.Labels {
		if !validMetricLabel(key) {
			return fmt.Errorf("hooks config %s: invalid metrics label %q", configPath, key)
		}
		policy.Metrics.Labels[key] = value
	}
	return nil
}

func validMetricLabel(label string) bool {
	return validHookName.MatchString(label)
}

func resolveTemplatePath(base, template string) string {
	template = expandPath(template)
	if strings.HasPrefix(template, "builtin:") || filepath.IsAbs(template) {
		return template
	}
	return filepath.Clean(filepath.Join(base, template))
}

func validatePolicy(policy Policy) error {
	for name, hook := range policy.Hooks {
		if err := validateResolvedHook(fmt.Sprintf("hook %q", name), hook); err != nil {
			return err
		}
	}
	for name, profile := range policy.ProfileDefinitions {
		for _, pattern := range append(append([]string(nil), profile.Detection.AnyFiles...), profile.Detection.AllFiles...) {
			if _, _, err := matchRepositoryPath(policy.RepoRoot, pattern); err != nil {
				return fmt.Errorf("profile %q: %w", name, err)
			}
		}
		for hookName, hook := range profile.Hooks {
			if err := validateResolvedHook(fmt.Sprintf("profile %q hook %q", name, hookName), hook); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateResolvedHook(subject string, hook ResolvedHook) error {
	if hook.Disabled {
		return nil
	}
	if hook.Builtin {
		if _, ok := builtinTemplate(hook.Template); !ok {
			return fmt.Errorf("%s refers to unknown template %q", subject, hook.Template)
		}
		return nil
	}
	info, err := os.Stat(hook.Template)
	if err != nil {
		return fmt.Errorf("%s template %s: %w", subject, hook.Template, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s template %s is not a regular file", subject, hook.Template)
	}
	return nil
}

func builtinTemplate(name string) (string, bool) {
	switch name {
	case BuiltinPreCommit:
		return "#!/bin/sh\nset -eu\ngit diff --cached --check\n", true
	case BuiltinPrePush:
		return "#!/bin/sh\nset -eu\ngit diff --check\n", true
	// A commit is a save point, not a release gate: the commit hook runs
	// formatting and static checks over the files changed in THIS commit and
	// is measured in seconds. It never runs a test suite. See
	// `commit-hook-is-fast-and-scoped`.
	case BuiltinNodePreCommit:
		return `#!/bin/sh
set -eu
if [ ! -f package.json ]; then
    exit 0
fi
if ! command -v node >/dev/null 2>&1; then
    exit 0
fi
changed="$(git diff --cached --name-only --diff-filter=ACMR -z -- \
    '*.js' '*.jsx' '*.mjs' '*.cjs' '*.ts' '*.tsx' '*.json' '*.css' '*.scss' '*.html' '*.md' \
    | tr '\0' '\n')"
if [ -z "$changed" ]; then
    exit 0
fi
if [ -f pnpm-lock.yaml ]; then
    runner=pnpm
elif [ -f yarn.lock ]; then
    runner=yarn
elif [ -f bun.lock ] || [ -f bun.lockb ]; then
    runner=bun
else
    runner=npx
fi
run_tool() {
    tool="$1"
    shift
    case "$runner" in
        pnpm) pnpm exec "$tool" "$@" ;;
        # yarn <tool> resolves a package SCRIPT before a binary, so a
        # repository with a script named "eslint" would run that instead.
        # yarn exec (Berry) addresses the binary; fall back to yarn run.
        yarn) yarn exec "$tool" "$@" 2>/dev/null || yarn run "$tool" "$@" ;;
        bun)  bun x "$tool" "$@" ;;
        *)    npx --no-install "$tool" "$@" ;;
    esac
}
# Formatting first: it is the cheapest check and the one whose failure is
# mechanical. Both tools are optional; a repository without them is not
# failed for lacking a dependency this hook did not install.
if [ -f .prettierrc ] || [ -f .prettierrc.json ] || [ -f .prettierrc.js ] || \
   [ -f .prettierrc.cjs ] || [ -f .prettierrc.yaml ] || [ -f .prettierrc.yml ] || \
   [ -f prettier.config.js ] || [ -f prettier.config.cjs ]; then
    if ! printf '%s\n' "$changed" | xargs run_tool prettier --check; then
        echo "WB hook: formatting failed on the files in this commit. Run your formatter and re-stage." >&2
        exit 1
    fi
fi
if [ -f eslint.config.js ] || [ -f eslint.config.mjs ] || [ -f eslint.config.cjs ] || \
   [ -f .eslintrc ] || [ -f .eslintrc.js ] || [ -f .eslintrc.cjs ] || \
   [ -f .eslintrc.json ] || [ -f .eslintrc.yaml ] || [ -f .eslintrc.yml ]; then
    lintable="$(printf '%s\n' "$changed" | grep -E '\.(js|jsx|mjs|cjs|ts|tsx)$' || true)"
    if [ -n "$lintable" ]; then
        if ! printf '%s\n' "$lintable" | xargs run_tool eslint --no-error-on-unmatched-pattern; then
            echo "WB hook: static checks failed on the files in this commit." >&2
            exit 1
        fi
    fi
fi
`, true
	case BuiltinGoPreCommit:
		// Format AND static checks, both scoped to the files in THIS commit.
		// gofmt alone met only half the requirement; `go vet` is the static
		// half, and it is run over the changed PACKAGES rather than the whole
		// module so a commit stays a save point measured in seconds.
		return `#!/bin/sh
set -eu
changed="$(git diff --cached --name-only --diff-filter=ACMR -- '*.go')"
if [ -z "$changed" ]; then
    exit 0
fi
unformatted="$(printf '%s\n' "$changed" | while IFS= read -r file; do
    if [ -f "$file" ]; then
        gofmt -l "$file"
    fi
done)"
if [ -n "$unformatted" ]; then
    echo "Go files need gofmt:" >&2
    echo "$unformatted" >&2
    exit 1
fi
if [ ! -f go.mod ]; then
    exit 0
fi
if ! command -v go >/dev/null 2>&1; then
    exit 0
fi
# Vet only the packages the commit touches. Vetting ./... would compile the
# whole module and turn a save point into a release gate.
packages="$(printf '%s\n' "$changed" | while IFS= read -r file; do
    # Avoid nested command-substitution/case parsing differences in macOS
    # /bin/sh while preserving paths that contain spaces.
    dir=${file%/*}
    [ "$dir" != "$file" ] || dir=.
    if [ "$dir" = "." ]; then
        printf '%s\n' "."
    else
        printf './%s\n' "$dir"
    fi
done | sort -u)"
if [ -z "$packages" ]; then
    exit 0
fi
# A deleted or moved package no longer resolves; vet reports that as an error,
# which is not what this commit did wrong. Only vet packages that still exist.
existing=""
for package in $packages; do
    if [ -d "$package" ]; then
        existing="$existing $package"
    fi
done
if [ -z "$existing" ]; then
    exit 0
fi
# shellcheck disable=SC2086
if ! go vet $existing; then
    echo "WB hook: go vet failed on the packages in this commit." >&2
    exit 1
fi
`, true
	case BuiltinGoPrePush:
		// profiles.include forces a profile on unconditionally for every
		// repository a policy governs — it never consults the profile's own
		// Detection rule the way auto-detection does. A fleet-wide policy
		// naming "go" once, to cover its Go repositories, forces this on for
		// every other repository too, so it must tolerate running somewhere
		// with no go.mod rather than assume Detection already screened it out.
		//
		// Tiering: `wb hooks push-tier` reads the same pushed-ref list Git
		// streams on stdin and decides whether this exact push needs static
		// validation, printing its decision and reason
		// on stdout before this script acts on it. Its exit code is the fixed
		// contract: 0 skips both (a deletion-only or WB checkpoint-ref push),
		// 1 runs vet (a feature branch with no open pull request), and 2 also
		// runs vet while marking a publication push for telemetry and downstream
		// policy. Tests and coverage belong to landing/CI. Tier 0 — the
		// base diff-check, worktree admission, and canonical-clone guard
		// blocks — is a different, always-on layer this script never touches.
		return `#!/bin/sh
set -eu
if [ ! -f go.mod ]; then
    exit 0
fi
tier=2
set +e
"$WB_EXECUTABLE" hooks push-tier
tier=$?
set -e
case "$tier" in
    0|1|2) ;;
    *)
        echo "WB hook: push-tier classifier exited $tier unexpectedly; running go vet as a safe default." >&2
        tier=2
        ;;
esac
if [ "$tier" -eq 0 ]; then
    exit 0
fi
if "$WB_EXECUTABLE" run --help 2>/dev/null | grep -q 'run -- <command>'; then
    "$WB_EXECUTABLE" --projects-root "$WB_PROJECTS_ROOT" run -- go vet ./...
else
    # One-release bootstrap: an installed WB predating the command gateway
    # must still be able to push the release that introduces it.
    go vet ./...
fi
`, true
	case BuiltinNodePrePush:
		return `#!/bin/sh
set -eu
if [ -f pnpm-lock.yaml ]; then
    package_manager=pnpm
elif [ -f yarn.lock ]; then
    package_manager=yarn
elif [ -f bun.lock ] || [ -f bun.lockb ]; then
    package_manager=bun
else
    package_manager=npm
fi
if ! command -v "$package_manager" >/dev/null 2>&1; then
    echo "Required Node package manager not found: $package_manager" >&2
    exit 1
fi
if ! command -v node >/dev/null 2>&1; then
    echo "Required Node runtime not found: node" >&2
    exit 1
fi
run_if_present() {
    script_name="$1"
    if node -e 'const p=require("./package.json"); process.exit(p.scripts && p.scripts[process.argv[1]] ? 0 : 1)' "$script_name"; then
        if "$WB_EXECUTABLE" run --help 2>/dev/null | grep -q 'run -- <command>'; then
            "$WB_EXECUTABLE" --projects-root "$WB_PROJECTS_ROOT" run -- "$package_manager" run "$script_name"
        else
            "$package_manager" run "$script_name"
        fi
    fi
}
# See the go-pre-push template for the full tiering contract: 'wb hooks
# push-tier' reads the pushed-ref list from stdin and exits 0 (skip lint),
# 1 (feature push), or 2 (publication push). Both nonzero tiers run static
# checks only. Tests and builds belong to landing/CI. Tier 0 is the separate,
# always-on base/worktree-admission layer.
tier=2
set +e
"$WB_EXECUTABLE" hooks push-tier
tier=$?
set -e
case "$tier" in
    0|1|2) ;;
    *)
        echo "WB hook: push-tier classifier exited $tier unexpectedly; running lint as a safe default." >&2
        tier=2
        ;;
esac
if [ "$tier" -eq 0 ]; then
    exit 0
fi
run_if_present lint
`, true
	case BuiltinWorktreeGuard:
		return `#!/bin/sh
set -eu
: "${WB_EXECUTABLE:?WB_EXECUTABLE is required for the worktree guard}"
: "${WB_PROJECTS_ROOT:?WB_PROJECTS_ROOT is required for the worktree guard}"

# Commit admission requires a managed worktree to carry its own record: a
# manifest and at least one recorded instruction. It applies only to commits —
# a checkout or push is not where an instruction gets recorded.
#
# The default is enforce. A commit with no record of who asked for it is the
# thing this exists to prevent, so declining it is the correct default rather
# than an opt-in. Set WB_ADMISSION=warn to report without refusing, or off to
# disable; both remain available for a fleet still adopting.
wb_admission="${WB_ADMISSION:-enforce}"
if [ "$WB_HOOK" != "pre-commit" ]; then
    wb_admission=off
fi

if "$WB_EXECUTABLE" --projects-root "$WB_PROJECTS_ROOT" worktree guard --quiet \
    --admission "$wb_admission" "$WB_REPO_ROOT"; then
    exit 0
else
    guard_status=$?
fi

# Git offers no pre-checkout hook. A non-zero post-checkout hook makes common
# 'git checkout && next-step' flows stop after Git has already changed the
# checkout, leaving misleading half-failed state. Report the violation loudly
# and preserve the checkout for explicit recovery; commit and push remain the
# hard enforcement boundaries.
if [ "$WB_HOOK" = "post-checkout" ]; then
    printf '%s\n' "WB warning: checkout already happened outside WB's managed worktree hierarchy; do not edit or commit here." >&2
    printf '%s\n' "Run 'wb worktree guard .' for details, and 'wb worktree rescue <path>' to move any uncommitted work onto a branch before anything discards it." >&2
    exit 0
fi
exit "$guard_status"
`, true
	default:
		return "", false
	}
}

func expandPath(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, path[2:])
}
