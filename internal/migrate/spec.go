// Package migrate plans and applies declarative source migrations.
package migrate

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsimple"
)

// MigrationFormatV1 is the format contract for HCL migration specifications.
// It is deliberately a stable public URL so it can become human-readable
// documentation without teaching the runner a second, implicit versioning
// system.
const MigrationFormatV1 = "https://sneat.dev/workbench/formats/migration/v1"

// Spec is a format-versioned, language-neutral migration definition.
//
// The core owns discovery, planning, application, and reporting. Individual
// adapters own structural edits for their language.
type Spec struct {
	Format           string
	ID               string
	Title            string
	Scope            Scope
	Steps            []Step
	Review           []ReviewRule
	GoModuleRequires []GoModuleRequire
	GoModuleReleases []GoModuleRelease
}

// Scope limits the files to which a migration applies. Empty Languages means
// every language known to the runner.
type Scope struct {
	Languages []string
	Include   []string
	Exclude   []string
}

// Step is one declarative edit. Kinds are intentionally small:
//
//   - text.replace applies an exact replacement to a selected file;
//   - import.replace delegates an import-path rewrite to a language adapter;
//   - selector.rewrite delegates a qualified-symbol rewrite to an adapter.
//   - selector.rename renames one qualified symbol without changing imports.
//   - composite_field.rename renames a keyed field in typed composite literals.
//
// More language capabilities can be added without changing the runner's plan
// and apply protocol.
type Step struct {
	Kind     string
	Language string

	From string
	To   string

	Import      string
	AddImport   string
	AddImportAs string
	Rewrites    map[string]string
}

// ReviewRule identifies a source pattern that needs semantic review after the
// mechanical plan. It is reported with affected files and line numbers, but
// never changes source code itself.
type ReviewRule struct {
	ID             string
	Language       string
	Pattern        string
	ExcludePattern string
	Message        string
}

// GoModuleRequire declares a module made newly necessary by the migration.
// It is acted on only by a hierarchical Go campaign; ordinary source-only
// migration deliberately leaves package manifests alone.
type GoModuleRequire struct {
	Path    string
	Version string
}

// GoModuleRelease declares the published version that replaces a temporary
// campaign worktree replacement before a branch may become a pull request.
// It is intentionally separate from GoModuleRequire: an existing dependency
// may need an upgrade without being newly introduced by source edits.
type GoModuleRelease struct {
	Path    string
	Version string
}

// Load reads and validates an HCL migration specification with HashiCorp's
// official hclsimple decoder. WB defines the document schema below but never
// parses HCL itself.
func Load(path string) (Spec, error) {
	var document hclDocument
	if err := hclsimple.DecodeFile(path, nil, &document); err != nil {
		return Spec{}, fmt.Errorf("parse migration %s: %w", path, err)
	}
	spec, err := document.spec()
	if err != nil {
		return Spec{}, fmt.Errorf("migration %s: %w", filepath.Base(path), err)
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
	if s.Format != MigrationFormatV1 {
		return fmt.Errorf("unsupported format %q (want %q)", s.Format, MigrationFormatV1)
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
	for i, requirement := range s.GoModuleRequires {
		if strings.TrimSpace(requirement.Path) == "" || strings.TrimSpace(requirement.Version) == "" {
			return fmt.Errorf("go module requirement %d requires path and version", i+1)
		}
	}
	releases := map[string]bool{}
	for i, release := range s.GoModuleReleases {
		if strings.TrimSpace(release.Path) == "" || strings.TrimSpace(release.Version) == "" {
			return fmt.Errorf("go module release %d requires path and version", i+1)
		}
		if releases[release.Path] {
			return fmt.Errorf("duplicate go module release for %q", release.Path)
		}
		releases[release.Path] = true
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
	case "selector.rename":
		if !knownLanguage(s.Language) {
			return fmt.Errorf("selector.rename requires a known language")
		}
		if s.Import == "" || s.From == "" || s.To == "" {
			return fmt.Errorf("selector.rename requires import, from, and to")
		}
	case "composite_field.rename":
		if !knownLanguage(s.Language) {
			return fmt.Errorf("composite_field.rename requires a known language")
		}
		if s.From == "" || s.To == "" {
			return fmt.Errorf("composite_field.rename requires from and to")
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
	if r.ExcludePattern != "" {
		if _, err := regexp.Compile(r.ExcludePattern); err != nil {
			return fmt.Errorf("invalid exclude_pattern %q: %w", r.ExcludePattern, err)
		}
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
