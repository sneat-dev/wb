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
	Version int                   `yaml:"version"`
	Hooks   map[string]HookConfig `yaml:"hooks"`
	Metrics MetricsConfig         `yaml:"metrics"`
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
	RepoRoot     string
	ConfigPaths  []string
	Hooks        map[string]ResolvedHook
	Metrics      MetricsPolicy
	ExplicitPath string
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
		Metrics: MetricsPolicy{Enabled: true, Path: defaultMetricsPath(), Labels: map[string]string{}},
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
		if hook.Disabled {
			continue
		}
		if hook.Builtin {
			if _, ok := builtinTemplate(hook.Template); !ok {
				return fmt.Errorf("hook %q refers to unknown template %q", name, hook.Template)
			}
			continue
		}
		info, err := os.Stat(hook.Template)
		if err != nil {
			return fmt.Errorf("hook %q template %s: %w", name, hook.Template, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("hook %q template %s is not a regular file", name, hook.Template)
		}
	}
	return nil
}

func builtinTemplate(name string) (string, bool) {
	switch name {
	case BuiltinPreCommit:
		return "#!/bin/sh\nset -eu\ngit diff --cached --check\n", true
	case BuiltinPrePush:
		return "#!/bin/sh\nset -eu\ngit diff --check\n", true
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
