package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTemplate writes a template file with the given marker and body to a
// temp dir and returns a Recipe pointing at it.
func writeTemplate(t *testing.T, marker, body string) Recipe {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "template.md")
	content := "<!-- " + marker + ":v1 -->\n" + body + "\n<!-- /" + marker + " -->\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return Recipe{Type: KindTemplateSection, Marker: marker, Template: path}
}

func TestLoadTemplateParses(t *testing.T) {
	r := writeTemplate(t, "test-approach", "## Our approach\n\nBody text.")
	tmpl, err := r.loadTemplate()
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	if tmpl.Version != 1 {
		t.Errorf("Version = %d, want 1", tmpl.Version)
	}
	if !strings.Contains(tmpl.Block, "Body text.") {
		t.Errorf("Block missing body: %q", tmpl.Block)
	}
	if strings.HasSuffix(tmpl.Block, "\n") {
		t.Errorf("Block should be trimmed, got trailing newline")
	}
}

func TestLoadTemplateErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"no start marker": "## heading\n<!-- /m -->",
		"no end marker":   "<!-- m:v1 -->\n## heading",
		"bad version":     "<!-- m:vX -->\n<!-- /m -->",
	}
	for name, content := range cases {
		path := filepath.Join(dir, name+".md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		r := Recipe{Marker: "m", Template: path}
		if _, err := r.loadTemplate(); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestLoadTemplateMissingFile(t *testing.T) {
	r := Recipe{Marker: "m", Template: "/nonexistent/template.md"}
	if _, err := r.loadTemplate(); err == nil {
		t.Error("expected error for missing template file, got nil")
	}
}

func TestMarkerRegexEscaping(t *testing.T) {
	// A marker with regex-special characters must not corrupt matching —
	// regexp.QuoteMeta makes it literal.
	r := writeTemplate(t, "my.marker+v2", "body")
	tmpl, err := r.loadTemplate()
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	action := planTemplateSection("# T\n\n"+tmpl.Block+"\n", tmpl, r.blockRe())
	if action != ActionNoop {
		t.Errorf("action = %v, want noop for exact current block", action)
	}
}

func TestApplyTemplateSectionInsertBeforeFirstH2(t *testing.T) {
	r := writeTemplate(t, "m", "block body")
	tmpl, _ := r.loadTemplate()
	content := "# My Project\n\nA great project.\n\n## Usage\n\nRun it.\n"
	out, action := applyTemplateSection(content, tmpl, r.blockRe())
	if action != ActionInsert {
		t.Fatalf("action = %v, want insert", action)
	}
	idxBlock := strings.Index(out, "<!-- m:v1")
	idxIntro := strings.Index(out, "A great project.")
	idxUsage := strings.Index(out, "## Usage")
	if !(idxIntro < idxBlock && idxBlock < idxUsage) {
		t.Errorf("block not placed after intro and before ## Usage:\n%s", out)
	}
}

func TestApplyTemplateSectionAppendsWhenNoH2(t *testing.T) {
	r := writeTemplate(t, "m", "block body")
	tmpl, _ := r.loadTemplate()
	content := "# Only a title\n\nSome text with no second-level heading.\n"
	out, action := applyTemplateSection(content, tmpl, r.blockRe())
	if action != ActionInsert {
		t.Fatalf("action = %v, want insert", action)
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "<!-- /m -->") {
		t.Errorf("block should be appended at end:\n%s", out)
	}
}

func TestApplyTemplateSectionInsertWhenStartsWithH2(t *testing.T) {
	r := writeTemplate(t, "m", "block body")
	tmpl, _ := r.loadTemplate()
	content := "## First Section\n\nbody\n"
	out, action := applyTemplateSection(content, tmpl, r.blockRe())
	if action != ActionInsert {
		t.Fatalf("action = %v, want insert", action)
	}
	if !strings.HasPrefix(out, tmpl.Block) {
		t.Errorf("block should lead when content starts with H2:\n%s", out)
	}
	if !strings.Contains(out, "## First Section") {
		t.Errorf("original H2 must be preserved:\n%s", out)
	}
}

func TestApplyTemplateSectionInsertEmptyContent(t *testing.T) {
	r := writeTemplate(t, "m", "block body")
	tmpl, _ := r.loadTemplate()
	out, action := applyTemplateSection("", tmpl, r.blockRe())
	if action != ActionInsert {
		t.Fatalf("action = %v, want insert", action)
	}
	if strings.TrimSpace(out) != tmpl.Block {
		t.Errorf("empty content should yield just the block, got:\n%s", out)
	}
}

func TestApplyTemplateSectionNoopWhenCurrent(t *testing.T) {
	r := writeTemplate(t, "m", "block body")
	tmpl, _ := r.loadTemplate()
	content := "# Title\n\n" + tmpl.Block + "\n\n## More\n"
	out, action := applyTemplateSection(content, tmpl, r.blockRe())
	if action != ActionNoop {
		t.Fatalf("action = %v, want noop", action)
	}
	if out != content {
		t.Errorf("noop must not modify content")
	}
}

func TestApplyTemplateSectionNoopWhenNewerPresent(t *testing.T) {
	r := writeTemplate(t, "m", "block body")
	tmpl, _ := r.loadTemplate()
	newer := "<!-- m:v999 -->\nFUTURE\n<!-- /m -->"
	content := "# T\n\n" + newer + "\n"
	out, action := applyTemplateSection(content, tmpl, r.blockRe())
	if action != ActionNoop {
		t.Fatalf("action = %v, want noop (existing newer)", action)
	}
	if out != content {
		t.Errorf("noop must not modify content")
	}
}

func TestApplyTemplateSectionReplaceOlder(t *testing.T) {
	r := writeTemplate(t, "m", "new body")
	tmpl, _ := r.loadTemplate()
	old := "<!-- m:v0 -->\nold body\n<!-- /m -->"
	content := "# T\n\nintro\n\n" + old + "\n\n## Tail\n"
	out, action := applyTemplateSection(content, tmpl, r.blockRe())
	if action != ActionReplace {
		t.Fatalf("action = %v, want replace", action)
	}
	if strings.Contains(out, "old body") {
		t.Errorf("old body should be gone:\n%s", out)
	}
	if !strings.Contains(out, tmpl.Block) {
		t.Errorf("new block missing:\n%s", out)
	}
	if !strings.Contains(out, "## Tail") || !strings.Contains(out, "intro") {
		t.Errorf("surrounding content must be preserved:\n%s", out)
	}
}

func TestPlanTemplateSection(t *testing.T) {
	r := writeTemplate(t, "m", "body")
	tmpl, _ := r.loadTemplate()
	if a := planTemplateSection("# x\n## y\n", tmpl, r.blockRe()); a != ActionInsert {
		t.Errorf("missing: got %v want insert", a)
	}
	if a := planTemplateSection("# x\n\n"+tmpl.Block+"\n", tmpl, r.blockRe()); a != ActionNoop {
		t.Errorf("current: got %v want noop", a)
	}
	old := "<!-- m:v0 -->\nx\n<!-- /m -->"
	if a := planTemplateSection(old, tmpl, r.blockRe()); a != ActionReplace {
		t.Errorf("older: got %v want replace", a)
	}
}

func TestActionString(t *testing.T) {
	for a, want := range map[Action]string{ActionNoop: "noop", ActionInsert: "insert", ActionReplace: "replace"} {
		if got := a.String(); got != want {
			t.Errorf("Action(%d).String() = %q, want %q", a, got, want)
		}
	}
}
