package recipe

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigTemplateSectionDefaults(t *testing.T) {
	path := writeConfig(t, `
recipes:
  dev-approach:
    type: template-section
    template: /tmp/dev-approach.md
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	r, ok := cfg.Recipes["dev-approach"]
	if !ok {
		t.Fatal("recipe \"dev-approach\" not found")
	}
	if r.Name != "dev-approach" {
		t.Errorf("Name = %q, want dev-approach", r.Name)
	}
	if r.Target != "README.md" {
		t.Errorf("Target default = %q, want README.md", r.Target)
	}
	if r.Marker != "dev-approach" {
		t.Errorf("Marker default = %q, want dev-approach (the recipe name)", r.Marker)
	}
	if r.AppliesIf != "always" {
		t.Errorf("AppliesIf default = %q, want always", r.AppliesIf)
	}
	if r.CommitMessage != "chore: apply dev-approach recipe" {
		t.Errorf("CommitMessage default = %q", r.CommitMessage)
	}
	if r.PRBranch != "wb/dev-approach" {
		t.Errorf("PRBranch default = %q", r.PRBranch)
	}
	if r.PRTitle != r.CommitMessage {
		t.Errorf("PRTitle default = %q, want same as CommitMessage %q", r.PRTitle, r.CommitMessage)
	}
	if r.PRBody != "Automated by `wb run dev-approach --apply`." {
		t.Errorf("PRBody default = %q", r.PRBody)
	}
}

func TestLoadConfigCommandExplicitOverrides(t *testing.T) {
	path := writeConfig(t, `
recipes:
  lint:
    type: command
    command: "specscore spec lint --fix"
    dry_run_command: "specscore spec lint"
    count_regex: '(\d+)\s+violation'
    applies_if: has_file:specscore.yaml
    commit_message: "chore: lint"
    pr_branch: "chore/lint"
    pr_title: "Lint fix"
    pr_body: "custom body"
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	r := cfg.Recipes["lint"]
	if r.Command != "specscore spec lint --fix" || r.DryRunCommand != "specscore spec lint" {
		t.Errorf("command fields = %+v", r)
	}
	if r.AppliesIf != "has_file:specscore.yaml" {
		t.Errorf("AppliesIf = %q, want explicit value preserved", r.AppliesIf)
	}
	if r.CommitMessage != "chore: lint" || r.PRBranch != "chore/lint" || r.PRTitle != "Lint fix" || r.PRBody != "custom body" {
		t.Errorf("landing fields should keep explicit overrides, got %+v", r)
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	if _, err := LoadConfig("/nonexistent/wb.yaml"); err == nil {
		t.Error("expected error for missing config file, got nil")
	}
}

func TestLoadConfigInvalidYAML(t *testing.T) {
	path := writeConfig(t, "recipes: [this is not a map")
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error for invalid YAML, got nil")
	}
}

func TestLoadConfigTemplateSectionMissingTemplate(t *testing.T) {
	path := writeConfig(t, `
recipes:
  bad:
    type: template-section
`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error when template-section recipe has no 'template', got nil")
	}
}

func TestLoadConfigCommandMissingCommand(t *testing.T) {
	path := writeConfig(t, `
recipes:
  bad:
    type: command
`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error when command recipe has no 'command', got nil")
	}
}

func TestLoadConfigUnknownType(t *testing.T) {
	path := writeConfig(t, `
recipes:
  bad:
    type: not-a-real-type
`)
	if _, err := LoadConfig(path); err == nil {
		t.Error("expected error for unknown recipe type, got nil")
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir available")
	}
	if got := expandPath("~/foo/bar"); got != filepath.Join(home, "foo/bar") {
		t.Errorf("expandPath(~/foo/bar) = %q, want %q", got, filepath.Join(home, "foo/bar"))
	}
	if got := expandPath("/abs/path"); got != "/abs/path" {
		t.Errorf("expandPath should leave absolute paths unchanged, got %q", got)
	}
}
