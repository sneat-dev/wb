// Package recipe implements wb's config-driven fleet operations: a Recipe
// describes how to detect and mutate matching repos, and how to land the
// result via the same worktree/commit/push-or-PR flow used everywhere else
// in wb.
package recipe

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kind is a recipe's mutation strategy.
type Kind string

const (
	KindTemplateSection Kind = "template-section"
	KindCommand         Kind = "command"
)

// Recipe describes one fleet-wide operation, as configured in wb.yaml.
type Recipe struct {
	Name string `yaml:"-"` // set from the config map key, not the YAML body

	Type      Kind   `yaml:"type"`
	AppliesIf string `yaml:"applies_if"`

	// template-section fields
	Target   string `yaml:"target"`
	Template string `yaml:"template"`
	Marker   string `yaml:"marker"`

	// command fields
	Command       string `yaml:"command"`
	DryRunCommand string `yaml:"dry_run_command"`
	CountRegex    string `yaml:"count_regex"`

	// landing fields (shared, all optional — defaulted from Name)
	CommitMessage string `yaml:"commit_message"`
	PRBranch      string `yaml:"pr_branch"`
	PRTitle       string `yaml:"pr_title"`
	PRBody        string `yaml:"pr_body"`
}

// Config is the parsed wb.yaml.
type Config struct {
	Recipes map[string]Recipe `yaml:"recipes"`
}

// LoadConfig reads, parses, and validates the recipe config at path.
func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(expandPath(path))
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	for name, r := range cfg.Recipes {
		r.Name = name
		if err := r.applyDefaults(); err != nil {
			return Config{}, fmt.Errorf("recipe %q: %w", name, err)
		}
		cfg.Recipes[name] = r
	}
	return cfg, nil
}

// applyDefaults fills in derived defaults and validates the fields required
// by r.Type. Fields already set (e.g. by a caller constructing a Recipe
// directly, not via LoadConfig) are left untouched.
func (r *Recipe) applyDefaults() error {
	switch r.Type {
	case KindTemplateSection:
		if r.Target == "" {
			r.Target = "README.md"
		}
		if r.Template == "" {
			return fmt.Errorf("template-section recipe requires 'template'")
		}
		if r.Marker == "" {
			r.Marker = r.Name
		}
	case KindCommand:
		if r.Command == "" {
			return fmt.Errorf("command recipe requires 'command'")
		}
	default:
		return fmt.Errorf("unknown type %q (want %q or %q)", r.Type, KindTemplateSection, KindCommand)
	}
	if r.AppliesIf == "" {
		r.AppliesIf = "always"
	}
	if r.CommitMessage == "" {
		r.CommitMessage = "chore: apply " + r.Name + " recipe"
	}
	if r.PRBranch == "" {
		r.PRBranch = "wb/" + r.Name
	}
	if r.PRTitle == "" {
		r.PRTitle = r.CommitMessage
	}
	if r.PRBody == "" {
		r.PRBody = "Automated by `wb run " + r.Name + " --apply`."
	}
	return nil
}

// expandPath expands a leading "~/" to the user's home directory. Paths
// without that prefix are returned unchanged.
func expandPath(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}
