package migrate

import "fmt"

// The hcl* types are the HCL document schema decoded by HashiCorp's official
// hclsimple package. They intentionally stay separate from Spec: Spec is the
// execution model while these types preserve a concise, repeatable HCL shape.
type hclDocument struct {
	Format     string         `hcl:"format"`
	Migrations []hclMigration `hcl:"migration,block"`
}

type hclMigration struct {
	ID               string               `hcl:"id,label"`
	Title            string               `hcl:"title,optional"`
	Scopes           []hclScope           `hcl:"scope,block"`
	TextReplaces     []hclTextReplace     `hcl:"text_replace,block"`
	ImportReplaces   []hclImportReplace   `hcl:"import_replace,block"`
	SelectorRewrites []hclSelectorRewrite `hcl:"selector_rewrite,block"`
	SelectorRenames  []hclSelectorRename  `hcl:"selector_rename,block"`
	CompositeFields  []hclCompositeField  `hcl:"composite_field_rename,block"`
	GoModuleRequires []hclGoModuleRequire `hcl:"go_module_require,block"`
	GoModuleReleases []hclGoModuleRelease `hcl:"go_module_release,block"`
	Reviews          []hclReview          `hcl:"review,block"`
}

type hclScope struct {
	Languages []string `hcl:"languages,optional"`
	Include   []string `hcl:"include,optional"`
	Exclude   []string `hcl:"exclude,optional"`
}

type hclTextReplace struct {
	Language string `hcl:"language,label"`
	From     string `hcl:"from"`
	To       string `hcl:"to"`
}

type hclImportReplace struct {
	Language string `hcl:"language,label"`
	From     string `hcl:"from"`
	To       string `hcl:"to"`
}

type hclSelectorRewrite struct {
	Language    string            `hcl:"language,label"`
	Import      string            `hcl:"import"`
	AddImport   string            `hcl:"add_import"`
	AddImportAs string            `hcl:"add_import_as,optional"`
	Rewrites    map[string]string `hcl:"rewrites"`
}

// hclSelectorRename is intentionally repeatable. HCL blocks, unlike map
// attributes, can occur more than once with the same "go" language label.
type hclSelectorRename struct {
	Language string `hcl:"language,label"`
	Import   string `hcl:"import"`
	From     string `hcl:"from"`
	To       string `hcl:"to"`
}

// hclCompositeField is intentionally syntax-scoped: it renames only a keyed
// field in an explicitly typed Go composite literal. It does not claim to
// resolve the owning type across packages.
type hclCompositeField struct {
	Language string `hcl:"language,label"`
	From     string `hcl:"from"`
	To       string `hcl:"to"`
}

type hclGoModuleRequire struct {
	Path    string `hcl:"path,label"`
	Version string `hcl:"version"`
}

type hclGoModuleRelease struct {
	Path    string `hcl:"path,label"`
	Version string `hcl:"version"`
}

type hclReview struct {
	ID             string `hcl:"id,label"`
	Language       string `hcl:"language"`
	Pattern        string `hcl:"pattern"`
	ExcludePattern string `hcl:"exclude_pattern,optional"`
	Message        string `hcl:"message"`
}

func (d hclDocument) spec() (Spec, error) {
	if len(d.Migrations) != 1 {
		return Spec{}, fmt.Errorf("requires exactly one migration block, got %d", len(d.Migrations))
	}
	migration := d.Migrations[0]
	if len(migration.Scopes) > 1 {
		return Spec{}, fmt.Errorf("migration %q has %d scope blocks; at most one is allowed", migration.ID, len(migration.Scopes))
	}

	spec := Spec{Format: d.Format, ID: migration.ID, Title: migration.Title}
	if len(migration.Scopes) == 1 {
		spec.Scope = Scope{
			Languages: migration.Scopes[0].Languages,
			Include:   migration.Scopes[0].Include,
			Exclude:   migration.Scopes[0].Exclude,
		}
	}
	for _, step := range migration.TextReplaces {
		spec.Steps = append(spec.Steps, Step{Kind: "text.replace", Language: step.Language, From: step.From, To: step.To})
	}
	for _, step := range migration.ImportReplaces {
		spec.Steps = append(spec.Steps, Step{Kind: "import.replace", Language: step.Language, From: step.From, To: step.To})
	}
	for _, step := range migration.SelectorRewrites {
		spec.Steps = append(spec.Steps, Step{
			Kind: "selector.rewrite", Language: step.Language, Import: step.Import,
			AddImport: step.AddImport, AddImportAs: step.AddImportAs, Rewrites: step.Rewrites,
		})
	}
	for _, step := range migration.SelectorRenames {
		spec.Steps = append(spec.Steps, Step{
			Kind: "selector.rename", Language: step.Language, Import: step.Import, From: step.From, To: step.To,
		})
	}
	for _, step := range migration.CompositeFields {
		spec.Steps = append(spec.Steps, Step{
			Kind: "composite_field.rename", Language: step.Language, From: step.From, To: step.To,
		})
	}
	for _, requirement := range migration.GoModuleRequires {
		spec.GoModuleRequires = append(spec.GoModuleRequires, GoModuleRequire{
			Path: requirement.Path, Version: requirement.Version,
		})
	}
	for _, release := range migration.GoModuleReleases {
		spec.GoModuleReleases = append(spec.GoModuleReleases, GoModuleRelease{
			Path: release.Path, Version: release.Version,
		})
	}
	for _, rule := range migration.Reviews {
		spec.Review = append(spec.Review, ReviewRule{
			ID: rule.ID, Language: rule.Language, Pattern: rule.Pattern,
			ExcludePattern: rule.ExcludePattern, Message: rule.Message,
		})
	}
	return spec, nil
}
