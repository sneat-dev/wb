// Package migrate plans and applies declarative source migrations.
package migrate

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Spec is a versioned, language-neutral migration definition.
//
// The core owns discovery, planning, application, and reporting. Individual
// adapters own structural edits for their language.
type Spec struct {
	ID      string       `yaml:"id"`
	Title   string       `yaml:"title"`
	Version int          `yaml:"version"`
	Scope   Scope        `yaml:"scope"`
	Steps   []Step       `yaml:"steps"`
	Review  []ReviewRule `yaml:"review"`
}

// Scope limits the files to which a migration applies. Empty Languages means
// every language known to the runner.
type Scope struct {
	Languages []string `yaml:"languages"`
	Include   []string `yaml:"include"`
	Exclude   []string `yaml:"exclude"`
}

// Step is one declarative edit. Kinds are intentionally small:
//
//   - text.replace applies an exact replacement to a selected file;
//   - import.replace delegates an import-path rewrite to a language adapter;
//   - selector.rewrite delegates a qualified-symbol rewrite to an adapter.
//
// More language capabilities can be added without changing the runner's plan
// and apply protocol.
type Step struct {
	Kind     string `yaml:"kind"`
	Language string `yaml:"language"`

	From string `yaml:"from"`
	To   string `yaml:"to"`

	Import      string            `yaml:"import"`
	AddImport   string            `yaml:"add_import"`
	AddImportAs string            `yaml:"add_import_as"`
	Rewrites    map[string]string `yaml:"rewrites"`
}

// ReviewRule identifies a source pattern that needs semantic review after the
// mechanical plan. It is reported with affected files and line numbers, but
// never changes source code itself.
type ReviewRule struct {
	ID       string `yaml:"id"`
	Language string `yaml:"language"`
	Pattern  string `yaml:"pattern"`
	Message  string `yaml:"message"`
}

// Load reads and validates a migration specification.
func Load(path string) (Spec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("read migration %s: %w", path, err)
	}
	var spec Spec
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return Spec{}, fmt.Errorf("parse migration %s: %w", path, err)
	}
	if err := spec.Validate(); err != nil {
		return Spec{}, fmt.Errorf("migration %s: %w", filepath.Base(path), err)
	}
	return spec, nil
}

// Validate rejects unsupported or ambiguous migration definitions before any
// source tree is inspected or changed.
func (s Spec) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("missing id")
	}
	if s.Version < 1 {
		return fmt.Errorf("version must be at least 1")
	}
	for _, language := range s.Scope.Languages {
		if !knownLanguage(language) {
			return fmt.Errorf("unknown scope language %q", language)
		}
	}
	if len(s.Steps) == 0 {
		return fmt.Errorf("requires at least one step")
	}
	for i, step := range s.Steps {
		if err := step.Validate(); err != nil {
			return fmt.Errorf("step %d: %w", i+1, err)
		}
	}
	for i, rule := range s.Review {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("review rule %d: %w", i+1, err)
		}
	}
	return nil
}

func (s Step) Validate() error {
	switch s.Kind {
	case "text.replace":
		if s.From == "" {
			return fmt.Errorf("text.replace requires from")
		}
		if s.To == "" {
			return fmt.Errorf("text.replace requires to")
		}
		if s.Language != "" && !knownLanguage(s.Language) {
			return fmt.Errorf("unknown language %q", s.Language)
		}
	case "import.replace":
		if !knownLanguage(s.Language) {
			return fmt.Errorf("import.replace requires a known language")
		}
		if s.From == "" || s.To == "" {
			return fmt.Errorf("import.replace requires from and to")
		}
	case "selector.rewrite":
		if !knownLanguage(s.Language) {
			return fmt.Errorf("selector.rewrite requires a known language")
		}
		if s.Import == "" || s.AddImport == "" || len(s.Rewrites) == 0 {
			return fmt.Errorf("selector.rewrite requires import, add_import, and rewrites")
		}
	default:
		return fmt.Errorf("unknown kind %q", s.Kind)
	}
	return nil
}

func (r ReviewRule) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return fmt.Errorf("missing id")
	}
	if r.Language != "" && !knownLanguage(r.Language) {
		return fmt.Errorf("unknown language %q", r.Language)
	}
	if r.Pattern == "" {
		return fmt.Errorf("missing pattern")
	}
	if _, err := regexp.Compile(r.Pattern); err != nil {
		return fmt.Errorf("invalid pattern %q: %w", r.Pattern, err)
	}
	if strings.TrimSpace(r.Message) == "" {
		return fmt.Errorf("missing message")
	}
	return nil
}

func knownLanguage(language string) bool {
	switch language {
	case "go", "python", "typescript":
		return true
	default:
		return false
	}
}
