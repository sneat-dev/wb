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

	"github.com/sneat-dev/wb/internal/wbconfig"
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

// AgentConfig declares policy for `wb hooks agent pre-tool-use` — the
// PreToolUse guard, not a Git hook. It lives in the same file as the Git
// hooks policy because both are "how strict is this repository", but this
// package only carries the field through decode/validate so the guard's own
// package (internal/agentguard, which never spawns a process and reads this
// file itself) can read it; nothing here acts on it.
type AgentConfig struct {
	// AutoTags declares that this repository's own CI already tags it (see
	// lesson l3-check-existing-tags-before-tagging-a-lower-version-with-newer-code-hides-the-fix
	// and lesson l11-find-out-whether-the-repo-auto-tags-before-you-hand-tag-it),
	// so a hand-pushed `git tag`/`git push --tags` is refused. A pointer so an
	// explicit `false` at the repository can override a `true` global policy.
	AutoTags *bool `yaml:"autoTags" json:"autoTags,omitempty"`
}

type fileConfig struct {
	Version  int                   `yaml:"version"`
	Hooks    map[string]HookConfig `yaml:"hooks"`
	Profiles ProfilesConfig        `yaml:"profiles"`
	Metrics  MetricsConfig         `yaml:"metrics"`
	Agent    AgentConfig           `yaml:"agent"`
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
	Agent              AgentPolicy
	ExplicitPath       string
}

// AgentPolicy is the resolved (global-then-repository-layered) form of
// AgentConfig — see AgentConfig for what AutoTags means.
type AgentPolicy struct {
	AutoTags bool
}

type MetricsPolicy struct {
	Enabled bool
	Path    string
	Labels  map[string]string
}

// LoadPolicy loads the git_hooks section from ~/.config/wb/wb.yaml and the
// repository's .wb/hooks.yaml when present.
// An explicit path replaces those discovery locations but still layers on top
// of WB's conservative built-in templates.
func LoadPolicy(repoPath, explicitPath string) (Policy, error) {
	repoRoot, err := RepositoryRoot(repoPath)
	if err != nil {
		return Policy{}, err
	}
	policy := defaultPolicy(repoRoot)
	policy.ExplicitPath = explicitPath

	if explicitPath != "" {
		path := expandPath(explicitPath)
		cfg, found, err := loadFile(path, true)
		if err != nil {
			return Policy{}, err
		}
		if found {
			policy.ConfigPaths = append(policy.ConfigPaths, path)
			if err := applyFile(&policy, path, cfg); err != nil {
				return Policy{}, err
			}
		}
	} else {
		global := defaultGlobalConfigPath()
		if global != "" {
			cfg, found, err := loadWBConfigGitHooks(global)
			if err != nil {
				return Policy{}, err
			}
			if found {
				policy.ConfigPaths = append(policy.ConfigPaths, global)
				if err := applyFile(&policy, global, cfg); err != nil {
					return Policy{}, err
				}
			}
		}
		repositoryPath := filepath.Join(repoRoot, ".wb", "hooks.yaml")
		cfg, found, err := loadFile(repositoryPath, false)
		if err != nil {
			return Policy{}, err
		}
		if found {
			policy.ConfigPaths = append(policy.ConfigPaths, repositoryPath)
			if err := applyFile(&policy, repositoryPath, cfg); err != nil {
				return Policy{}, err
			}
		}
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
	return wbconfig.DefaultPath()
}

func loadWBConfigGitHooks(path string) (fileConfig, bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileConfig{}, false, nil
	}
	if err != nil {
		return fileConfig{}, false, fmt.Errorf("read hooks config %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	decoder := yaml.NewDecoder(file)
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return fileConfig{}, false, fmt.Errorf("parse hooks config %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != nil && !errors.Is(err, io.EOF) {
		return fileConfig{}, false, fmt.Errorf("parse hooks config %s: %w", path, err)
	} else if err == nil {
		return fileConfig{}, false, fmt.Errorf("parse hooks config %s: multiple YAML documents are not supported", path)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return fileConfig{}, false, fmt.Errorf("parse hooks config %s: top level must be a mapping", path)
	}
	root := document.Content[0]
	var section *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "git_hooks" {
			continue
		}
		if section != nil {
			return fileConfig{}, false, fmt.Errorf("parse hooks config %s: duplicate git_hooks section", path)
		}
		section = root.Content[i+1]
	}
	if section == nil || section.Tag == "!!null" {
		return fileConfig{}, false, nil
	}
	// This section is an unmodified node produced by the successful YAML parser.
	// Re-emitting its parsed node kinds and scalar bytes has no fallible Go value conversion.
	raw, _ := yaml.Marshal(section)
	strict := yaml.NewDecoder(strings.NewReader(string(raw)))
	strict.KnownFields(true)
	var cfg fileConfig
	if err := strict.Decode(&cfg); err != nil {
		return fileConfig{}, false, fmt.Errorf("parse hooks config %s: %w", path, err)
	}
	if cfg.Version != PolicyVersion {
		return fileConfig{}, false, fmt.Errorf("hooks config %s has version %d; supported version is %d", path, cfg.Version, PolicyVersion)
	}
	return cfg, true, nil
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
	if cfg.Agent.AutoTags != nil {
		policy.Agent.AutoTags = *cfg.Agent.AutoTags
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

// stagedDiffShell defines wb_staged_diff, the built-in pre-commit templates'
// `git diff --cached`. `git commit -a`, `git commit <paths>` and
// `git commit --only` stage the commit in a temporary index and point the hook
// at it with GIT_INDEX_FILE. hooks.Run clears GIT_INDEX_FILE from every block
// (see gitGeneratedEnvironmentVars) and hands the index over as
// WB_GIT_INDEX_FILE instead, so only this one git call reads it: without it
// the staged checks read the real index, see nothing staged, and pass. Nothing
// else the block starts -- go vet, a formatter, a linter -- inherits it;
// `wb hooks lint` takes it as --index-file. (wb#993)
const stagedDiffShell = `wb_staged_diff() {
    if [ -n "${WB_GIT_INDEX_FILE:-}" ]; then
        GIT_INDEX_FILE="$WB_GIT_INDEX_FILE" git diff --cached "$@"
    else
        git diff --cached "$@"
    fi
}
`

func builtinTemplate(name string) (string, bool) {
	switch name {
	case BuiltinPreCommit:
		return "#!/bin/sh\nset -eu\n" + stagedDiffShell + "wb_staged_diff --check\n", true
	case BuiltinPrePush:
		return "#!/bin/sh\nset -eu\ngit diff --check\n", true
	// A commit is a save point, not a release gate: the commit hook runs
	// formatting and static checks over the files changed in THIS commit and
	// is measured in seconds. It never runs a test suite. See
	// `commit-hook-is-fast-and-scoped`.
	case BuiltinNodePreCommit:
		return `#!/bin/sh
set -eu
` + stagedDiffShell + `if [ ! -f package.json ]; then
    exit 0
fi
if ! command -v node >/dev/null 2>&1; then
    exit 0
fi
changed="$(wb_staged_diff --name-only --diff-filter=ACMR -z -- \
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
` + stagedDiffShell + `changed="$(wb_staged_diff --name-only --diff-filter=ACMR -- '*.go')"
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
if ! command -v go >/dev/null 2>&1; then
    exit 0
fi
# Vet only the packages the commit touches. Vetting ./... would compile the
# whole module and turn a save point into a release gate.
#
# A package belongs to the module of its nearest enclosing go.mod. A nested
# module (its own go.mod, no go.work) is not part of the root module, so its
# packages cannot be loaded from the repository root: they are grouped by
# module and each group is vetted from its own module directory. A staged file
# under no go.mod at all is not vetted; a repository whose modules all live in
# subdirectories is vetted module by module. (WB-831)
nearest_module() {
    candidate="$1"
    while :; do
        if [ -f "$candidate/go.mod" ]; then
            printf '%s\n' "$candidate"
            return 0
        fi
        [ "$candidate" != "." ] || return 1
        parent=${candidate%/*}
        [ "$parent" != "$candidate" ] || parent=.
        candidate=$parent
    done
}
tab="$(printf '\t')"
# One "module<TAB>package" line per staged package, the package expressed
# relative to its module the way "go vet" expects it.
targets="$(printf '%s\n' "$changed" | while IFS= read -r file; do
    # Avoid nested command-substitution/case parsing differences in macOS
    # /bin/sh while preserving paths that contain spaces.
    dir=${file%/*}
    [ "$dir" != "$file" ] || dir=.
    module="$(nearest_module "$dir")" || continue
    if [ "$dir" = "$module" ]; then
        package="."
    elif [ "$module" = "." ]; then
        package="./$dir"
    else
        package="./${dir#"$module"/}"
    fi
    printf '%s\t%s\n' "$module" "$package"
done | sort -u)"
if [ -z "$targets" ]; then
    exit 0
fi
modules="$(printf '%s\n' "$targets" | while IFS="$tab" read -r module package; do
    printf '%s\n' "$module"
done | sort -u)"
failed=0
while IFS= read -r module; do
    # A deleted or moved package no longer resolves; vet reports that as an
    # error, which is not what this commit did wrong. Only vet packages that
    # still exist.
    set --
    while IFS="$tab" read -r target_module package; do
        if [ "$target_module" = "$module" ] && [ -d "$module/$package" ]; then
            set -- "$@" "$package"
        fi
    done <<EOF_TARGETS
$targets
EOF_TARGETS
    if [ "$#" -eq 0 ]; then
        continue
    fi
    # </dev/null: the loop's stdin is the list of remaining modules, which a
    # go wrapper that reads stdin must not be able to swallow.
    if ! ( cd "./$module" && go vet "$@" </dev/null ); then
        if [ "$module" = "." ]; then
            echo "WB hook: go vet failed on the packages in this commit." >&2
        else
            echo "WB hook: go vet failed on the packages in this commit, in module $module." >&2
        fi
        failed=1
    fi
done <<EOF_MODULES
$modules
EOF_MODULES
# Changed-line lint with the CI-pinned golangci-lint, over the same staged
# packages, through ` + "`wb run --`" + `. It reports only issues on new or changed
# lines, and never builds a linter (pre-push does), so it stays in seconds.
if ! "$WB_EXECUTABLE" --projects-root "$WB_PROJECTS_ROOT" hooks lint --hook pre-commit --language go --index-file "${WB_GIT_INDEX_FILE:-}"; then
    failed=1
fi
if [ "$failed" -ne 0 ]; then
    exit 1
fi
`, true
	case BuiltinGoPrePush:
		// profiles.include forces a profile on unconditionally for every
		// repository a policy governs — it never consults the profile's own
		// Detection rule the way auto-detection does — so `wb hooks lint`
		// must tolerate a repository with no Go module at all.
		//
		// `wb hooks lint` reads the pushed-ref list Git streams on stdin,
		// classifies the push with the same tier contract as `wb hooks
		// push-tier` (printing its decision), and for Tier 1 and Tier 2 runs
		// go vet and the CI-pinned golangci-lint over every Go module the
		// pushed commits touch, nested go.mod included, each through
		// `wb run --`. Tests and coverage belong to landing/CI. Tier 0 — the
		// base diff-check, worktree admission, and canonical-clone guard
		// blocks — is a different, always-on layer this script never touches.
		return `#!/bin/sh
set -eu
exec "$WB_EXECUTABLE" --projects-root "$WB_PROJECTS_ROOT" hooks lint --hook pre-push --language go
`, true
	case BuiltinNodePrePush:
		// Lint only, and only when the push changes JS/TS sources: an Nx
		// workspace runs `nx affected -t lint` against the push base, any
		// other package its own lint script. Builds and tests belong to
		// landing/CI. The tier contract is the go-pre-push one above.
		return `#!/bin/sh
set -eu
exec "$WB_EXECUTABLE" --projects-root "$WB_PROJECTS_ROOT" hooks lint --hook pre-push --language node
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

# A pre-push that only deletes remote refs (how WB retires a merged branch)
# sends nothing from this checkout, so the guard passes it without inspecting
# the checkout: the canonical clone may be on any branch, clean or dirty.
wb_push_stdin=""
if [ "$WB_HOOK" = "pre-push" ]; then
    wb_push_stdin="--pre-push-stdin"
fi

if "$WB_EXECUTABLE" --projects-root "$WB_PROJECTS_ROOT" worktree guard --quiet \
    --admission "$wb_admission" $wb_push_stdin "$WB_REPO_ROOT"; then
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
