package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	BuiltinGoPreCommit  = "builtin:go-pre-commit"
	BuiltinGoPrePush    = "builtin:go-pre-push"
	BuiltinNodePrePush  = "builtin:node-pre-push"
	defaultProfileOrder = 200
)

// ProfilesConfig controls automatic detection and profile composition. Each
// active profile contributes at most one block to each configured hook type.
type ProfilesConfig struct {
	Auto        *bool                              `yaml:"auto"`
	Include     []string                           `yaml:"include"`
	Exclude     []string                           `yaml:"exclude"`
	Definitions map[string]ProfileDefinitionConfig `yaml:"definitions"`
}

// ProfileDefinitionConfig describes a language, toolchain, product, or other
// repository profile. Detection paths are repository-relative and may contain
// filepath.Glob patterns.
type ProfileDefinitionConfig struct {
	Order  *int                  `yaml:"order"`
	Detect *ProfileDetection     `yaml:"detect"`
	Hooks  map[string]HookConfig `yaml:"hooks"`
}

type ProfileDetection struct {
	AnyFiles []string `yaml:"any_files" json:"any_files,omitempty"`
	AllFiles []string `yaml:"all_files" json:"all_files,omitempty"`
}

type ProfileDefinition struct {
	Name       string
	Order      int
	Detection  ProfileDetection
	Hooks      map[string]ResolvedHook
	ConfigPath string
}

type ActiveProfile struct {
	Name   string `json:"name"`
	Order  int    `json:"order"`
	Reason string `json:"reason"`
}

type HookBlock struct {
	ID      string
	Profile string
	Hook    ResolvedHook
}

func builtinProfileDefinitions() map[string]ProfileDefinition {
	return map[string]ProfileDefinition{
		"go": {
			Name:      "go",
			Order:     100,
			Detection: ProfileDetection{AllFiles: []string{"go.mod"}},
			Hooks: map[string]ResolvedHook{
				"pre-commit": builtinProfileHook("pre-commit", BuiltinGoPreCommit, "go"),
				"pre-push":   builtinProfileHook("pre-push", BuiltinGoPrePush, "go"),
			},
		},
		"node": {
			Name:      "node",
			Order:     100,
			Detection: ProfileDetection{AllFiles: []string{"package.json"}},
			Hooks: map[string]ResolvedHook{
				"pre-push": builtinProfileHook("pre-push", BuiltinNodePrePush, "node"),
			},
		},
	}
}

func builtinProfileHook(name, template, profile string) ResolvedHook {
	return ResolvedHook{
		Name:       name,
		Template:   template,
		Builtin:    true,
		ConfigPath: "builtin:profile/" + profile,
	}
}

func applyProfiles(policy *Policy, configPath string, config ProfilesConfig) error {
	if config.Auto != nil {
		policy.ProfilesAuto = *config.Auto
	}
	for _, name := range config.Include {
		if err := validateProfileName(configPath, name); err != nil {
			return err
		}
		policy.ProfileSelections[name] = true
	}
	for _, name := range config.Exclude {
		if err := validateProfileName(configPath, name); err != nil {
			return err
		}
		policy.ProfileSelections[name] = false
	}

	names := make([]string, 0, len(config.Definitions))
	for name := range config.Definitions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := validateProfileName(configPath, name); err != nil {
			return err
		}
		entry := config.Definitions[name]
		definition, exists := policy.ProfileDefinitions[name]
		if !exists {
			definition = ProfileDefinition{Name: name, Order: defaultProfileOrder, Hooks: map[string]ResolvedHook{}}
		}
		definition.ConfigPath = configPath
		if entry.Order != nil {
			definition.Order = *entry.Order
		}
		if entry.Detect != nil {
			definition.Detection = cloneDetection(*entry.Detect)
		}
		if definition.Hooks == nil {
			definition.Hooks = map[string]ResolvedHook{}
		}
		if err := mergeProfileHooks(&definition, filepath.Dir(configPath), configPath, entry.Hooks); err != nil {
			return err
		}
		policy.ProfileDefinitions[name] = definition
	}
	return nil
}

func validateProfileName(configPath, name string) error {
	if !validHookName.MatchString(name) {
		return fmt.Errorf("hooks config %s: invalid profile name %q", configPath, name)
	}
	return nil
}

func mergeProfileHooks(definition *ProfileDefinition, base, configPath string, configured map[string]HookConfig) error {
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !validHookName.MatchString(name) {
			return fmt.Errorf("hooks config %s: profile %q has invalid hook name %q", configPath, definition.Name, name)
		}
		entry := configured[name]
		resolved := ResolvedHook{Name: name, Disabled: entry.Disabled, ConfigPath: configPath}
		if !entry.Disabled {
			if strings.TrimSpace(entry.Template) == "" {
				return fmt.Errorf("hooks config %s: profile %q hook %q requires template or disabled: true", configPath, definition.Name, name)
			}
			resolved.Template = resolveTemplatePath(base, entry.Template)
			resolved.Builtin = strings.HasPrefix(resolved.Template, "builtin:")
		}
		definition.Hooks[name] = resolved
	}
	return nil
}

func resolveProfiles(policy *Policy) error {
	for name := range policy.ProfileSelections {
		if _, exists := policy.ProfileDefinitions[name]; !exists {
			return fmt.Errorf("profile %q is included or excluded but has no built-in or configured definition", name)
		}
	}

	active := make([]ActiveProfile, 0, len(policy.ProfileDefinitions))
	for name, definition := range policy.ProfileDefinitions {
		selected, explicitlySelected := policy.ProfileSelections[name]
		if explicitlySelected && !selected {
			continue
		}
		reason := ""
		if explicitlySelected && selected {
			reason = "included by policy"
		} else if policy.ProfilesAuto {
			matched, matches, err := matchProfile(policy.RepoRoot, definition.Detection)
			if err != nil {
				return fmt.Errorf("detect profile %q: %w", name, err)
			}
			if !matched {
				continue
			}
			reason = "matched " + strings.Join(matches, ", ")
		} else {
			continue
		}
		active = append(active, ActiveProfile{Name: name, Order: definition.Order, Reason: reason})
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].Order == active[j].Order {
			return active[i].Name < active[j].Name
		}
		return active[i].Order < active[j].Order
	})
	policy.ActiveProfiles = active
	return nil
}

func matchProfile(repoRoot string, detection ProfileDetection) (bool, []string, error) {
	if len(detection.AnyFiles) == 0 && len(detection.AllFiles) == 0 {
		return false, nil, nil
	}
	matches := make([]string, 0, len(detection.AnyFiles)+len(detection.AllFiles))
	for _, pattern := range detection.AllFiles {
		matched, path, err := matchRepositoryPath(repoRoot, pattern)
		if err != nil {
			return false, nil, err
		}
		if !matched {
			return false, nil, nil
		}
		matches = append(matches, path)
	}
	if len(detection.AnyFiles) > 0 {
		anyMatched := false
		for _, pattern := range detection.AnyFiles {
			matched, path, err := matchRepositoryPath(repoRoot, pattern)
			if err != nil {
				return false, nil, err
			}
			if matched {
				anyMatched = true
				matches = append(matches, path)
			}
		}
		if !anyMatched {
			return false, nil, nil
		}
	}
	return true, matches, nil
}

func matchRepositoryPath(repoRoot, pattern string) (bool, string, error) {
	pattern = filepath.Clean(strings.TrimSpace(pattern))
	if pattern == "." || filepath.IsAbs(pattern) || pattern == ".." || strings.HasPrefix(pattern, ".."+string(filepath.Separator)) {
		return false, "", fmt.Errorf("detection path %q must be a non-empty repository-relative path", pattern)
	}
	if _, err := filepath.Match(pattern, ""); err != nil {
		return false, "", fmt.Errorf("invalid detection pattern %q: %w", pattern, err)
	}
	if !hasGlobMeta(pattern) {
		if _, err := os.Stat(filepath.Join(repoRoot, pattern)); err == nil {
			return true, filepath.ToSlash(pattern), nil
		} else if os.IsNotExist(err) {
			return false, "", nil
		} else {
			return false, "", err
		}
	}
	matches, err := filepath.Glob(filepath.Join(repoRoot, pattern))
	if err != nil {
		return false, "", err
	}
	if len(matches) == 0 {
		return false, "", nil
	}
	relative, err := filepath.Rel(repoRoot, matches[0])
	if err != nil {
		return false, "", err
	}
	return true, filepath.ToSlash(relative), nil
}

func hasGlobMeta(value string) bool {
	return strings.ContainsAny(value, "*?[")
}

func cloneDetection(detection ProfileDetection) ProfileDetection {
	return ProfileDetection{
		AnyFiles: append([]string(nil), detection.AnyFiles...),
		AllFiles: append([]string(nil), detection.AllFiles...),
	}
}

func hookBlocks(policy Policy, hookName string) []HookBlock {
	base, exists := policy.Hooks[hookName]
	if exists && base.Disabled {
		return nil
	}
	blocks := make([]HookBlock, 0, 1+len(policy.ActiveProfiles))
	if exists && base.Template != "" {
		blocks = append(blocks, HookBlock{ID: "base/" + hookName, Profile: "base", Hook: base})
	}
	for _, active := range policy.ActiveProfiles {
		definition := policy.ProfileDefinitions[active.Name]
		hook, exists := definition.Hooks[hookName]
		if !exists || hook.Disabled || hook.Template == "" {
			continue
		}
		blocks = append(blocks, HookBlock{ID: active.Name + "/" + hookName, Profile: active.Name, Hook: hook})
	}
	return blocks
}

func profileBlockMap(policy Policy) map[string][]string {
	result := map[string][]string{}
	for _, hookName := range expectedHookNames(policy) {
		for _, block := range hookBlocks(policy, hookName) {
			result[hookName] = append(result[hookName], block.ID)
		}
	}
	return result
}
