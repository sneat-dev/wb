package readme

import (
	"strings"
	"testing"
)

func mustTemplate(t *testing.T) Template {
	t.Helper()
	tmpl, err := Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if tmpl.Version < 1 {
		t.Fatalf("expected version >= 1, got %d", tmpl.Version)
	}
	return tmpl
}

func TestCanonicalParses(t *testing.T) {
	tmpl := mustTemplate(t)
	if !strings.Contains(tmpl.Block, "Our approach to development") {
		t.Errorf("template block missing heading: %q", tmpl.Block)
	}
	if strings.HasSuffix(tmpl.Block, "\n") {
		t.Errorf("template block should be trimmed, got trailing newline")
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"no start marker": "## heading\n<!-- /dev-approach -->",
		"no end marker":   "<!-- dev-approach:v1 -->\n## heading",
		"bad version":     "<!-- dev-approach:vX -->\n<!-- /dev-approach -->", // regex won't match vX -> treated as missing start
	}
	for name, content := range cases {
		if _, err := Parse(content); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestApplyInsertBeforeFirstH2(t *testing.T) {
	tmpl := mustTemplate(t)
	content := "# My Project\n\nA great project.\n\n## Usage\n\nRun it.\n"
	out, action := Apply(content, tmpl)
	if action != ActionInsert {
		t.Fatalf("action = %v, want insert", action)
	}
	idxBlock := strings.Index(out, "<!-- dev-approach")
	idxIntro := strings.Index(out, "A great project.")
	idxUsage := strings.Index(out, "## Usage")
	if !(idxIntro < idxBlock && idxBlock < idxUsage) {
		t.Errorf("block not placed after intro and before ## Usage:\n%s", out)
	}
}

func TestApplyInsertAppendsWhenNoH2(t *testing.T) {
	tmpl := mustTemplate(t)
	content := "# Only a title\n\nSome text with no second-level heading.\n"
	out, action := Apply(content, tmpl)
	if action != ActionInsert {
		t.Fatalf("action = %v, want insert", action)
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), endMarker) {
		t.Errorf("block should be appended at end:\n%s", out)
	}
}

func TestParseVersionOverflow(t *testing.T) {
	// Digits match the marker regex but overflow int -> Atoi fails.
	huge := "<!-- dev-approach:v99999999999999999999 -->\nx\n<!-- /dev-approach -->"
	if _, err := Parse(huge); err == nil {
		t.Error("expected overflow version error, got nil")
	}
}

func TestApplyInsertWhenContentStartsWithH2(t *testing.T) {
	tmpl := mustTemplate(t)
	content := "## First Section\n\nbody\n"
	out, action := Apply(content, tmpl)
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

func TestApplyInsertEmptyContent(t *testing.T) {
	tmpl := mustTemplate(t)
	out, action := Apply("", tmpl)
	if action != ActionInsert {
		t.Fatalf("action = %v, want insert", action)
	}
	if strings.TrimSpace(out) != tmpl.Block {
		t.Errorf("empty content should yield just the block, got:\n%s", out)
	}
}

func TestApplyNoopWhenCurrent(t *testing.T) {
	tmpl := mustTemplate(t)
	content := "# Title\n\n" + tmpl.Block + "\n\n## More\n"
	out, action := Apply(content, tmpl)
	if action != ActionNoop {
		t.Fatalf("action = %v, want noop", action)
	}
	if out != content {
		t.Errorf("noop must not modify content")
	}
}

func TestApplyNoopWhenNewerPresent(t *testing.T) {
	tmpl := mustTemplate(t)
	newer := strings.Replace(tmpl.Block, "dev-approach:v", "dev-approach:v9", 1)
	// craft a v(version+9) by replacing the digit; simpler: force a high version
	newer = "<!-- dev-approach:v999 -->\nFUTURE\n<!-- /dev-approach -->"
	content := "# T\n\n" + newer + "\n"
	out, action := Apply(content, tmpl)
	if action != ActionNoop {
		t.Fatalf("action = %v, want noop (existing newer)", action)
	}
	if out != content {
		t.Errorf("noop must not modify content")
	}
	_ = newer
}

func TestApplyReplaceOlder(t *testing.T) {
	tmpl := mustTemplate(t)
	old := "<!-- dev-approach:v0 -->\nold body\n<!-- /dev-approach -->"
	content := "# T\n\nintro\n\n" + old + "\n\n## Tail\n"
	out, action := Apply(content, tmpl)
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

func TestPlan(t *testing.T) {
	tmpl := mustTemplate(t)
	if a, v := Plan("# x\n## y\n", tmpl); a != ActionInsert || v != 0 {
		t.Errorf("missing: got %v,%d want insert,0", a, v)
	}
	if a, _ := Plan("# x\n\n"+tmpl.Block+"\n", tmpl); a != ActionNoop {
		t.Errorf("current: got %v want noop", a)
	}
	old := "<!-- dev-approach:v0 -->\nx\n<!-- /dev-approach -->"
	if a, v := Plan(old, tmpl); a != ActionReplace || v != 0 {
		t.Errorf("older: got %v,%d want replace,0", a, v)
	}
}

func TestActionString(t *testing.T) {
	for a, want := range map[Action]string{ActionNoop: "noop", ActionInsert: "insert", ActionReplace: "replace"} {
		if got := a.String(); got != want {
			t.Errorf("Action(%d).String() = %q, want %q", a, got, want)
		}
	}
}
