package mechanicalchange

import (
	"strings"
	"testing"
)

func TestOrchCovSectionFromHunkHeaderRejectsUnusableContext(t *testing.T) {
	t.Parallel()
	if got := sectionFromHunkHeader("@@ -1,3 +1,3 @@   \"dependencies\": {"); len(got) != 1 || got[0] != "dependencies" {
		t.Fatalf("hunk header section = %v", got)
	}
	for _, line := range []string{
		"@@ -1,3 +1,3 @@",
		"@@ no second marker",
		"@@ -1,3 +1,3 @@ not a json property",
		"@@ -1,3 +1,3 @@ \"dependencies\": []",
	} {
		if got := sectionFromHunkHeader(line); got != nil {
			t.Fatalf("sectionFromHunkHeader(%q) = %v, want nil", line, got)
		}
	}
}

func TestOrchCovNonMechanicalGoModuleRefusesGraphDirectives(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		patch string
		want  string
	}{
		{name: "require is a bump", patch: "@@ -1 +1 @@\n-require example.test/a v1.0.0\n+require example.test/a v1.1.0\n"},
		{name: "replace", patch: "@@ -1 +1 @@\n+replace example.test/a => ../a\n", want: "replace directive"},
		{name: "exclude", patch: "@@ -1 +1 @@\n+exclude example.test/a v1.0.0\n", want: "exclude directive"},
		{name: "go directive", patch: "@@ -1 +1 @@\n-go 1.22\n+go 1.23\n", want: "go directive"},
		{name: "toolchain", patch: "@@ -1 +1 @@\n+toolchain go1.23.1\n", want: "toolchain directive"},
		// A bare marker line survives as an empty entry, which carries no
		// directive to judge.
		{name: "empty patch line", patch: "+\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reason := nonMechanicalGoModule("go.mod", test.patch)
			if test.want == "" {
				if reason != "" {
					t.Fatalf("reason = %q, want none", reason)
				}
				return
			}
			if !strings.Contains(reason, test.want) {
				t.Fatalf("reason = %q, want it to name %q", reason, test.want)
			}
		})
	}
}

func TestOrchCovNonMechanicalWorkspaceRefusesGraphRewritingKeys(t *testing.T) {
	t.Parallel()
	clean := nonMechanicalWorkspace("pnpm-workspace.yaml", "packages:\n  - apps/*\n")
	if clean != "" {
		t.Fatalf("clean workspace reason = %q", clean)
	}
	reason := nonMechanicalWorkspace("pnpm-workspace.yaml", "@@ -1 +1 @@\n overrides:\n-  semver: 7.5.0\n+  semver: 7.6.0\n")
	if !strings.Contains(reason, "overrides") || !strings.Contains(reason, "rewrites what the workspace resolves") {
		t.Fatalf("overrides reason = %q", reason)
	}
}

func TestOrchCovChangedPatchLinesSkipsFileHeaders(t *testing.T) {
	t.Parallel()
	lines := changedPatchLines("--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n-require a v1\n+require a v2\nplain\n+\n")
	if len(lines) != 3 || lines[0] != "require a v1" || lines[1] != "require a v2" || lines[2] != "" {
		t.Fatalf("changed patch lines = %q", lines)
	}
}

func TestOrchCovNonMechanicalContentReasonsAboutAPackageManifest(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		file  string
		patch string
		want  string
	}{
		{
			name:  "dependency bump",
			patch: "--- a/package.json\n+++ b/package.json\n@@ -1,3 +1,3 @@\n \"dependencies\": {\n-  \"lodash\": \"^4.17.20\"\n+  \"lodash\": \"^4.17.21\"\n",
		},
		{
			name:  "blank diff line",
			patch: "--- a/package.json\n+++ b/package.json\n@@ -1,3 +1,3 @@\n \"dependencies\": {\n \n-  \"lodash\": \"^4.17.20\"\n+  \"lodash\": \"^4.17.21\"\n",
		},
		{
			name:  "changed section that is not a dependency section",
			patch: "@@ -1,3 +1,4 @@\n-\"scripts\": {\n+\"scripts\": {\n",
			want:  "which is not a dependency section",
		},
		{
			name:  "hunk header supplies the enclosing section",
			patch: "@@ -12,7 +12,7 @@   \"dependencies\": {\n-  \"lodash\": \"^4.17.20\"\n+  \"lodash\": \"^4.17.21\"\n",
		},
		{
			name:  "graph rewriting key inside a dependency section",
			patch: "@@ -1,3 +1,3 @@\n \"dependencies\": {\n+  \"catalog\": \"1.0.0\"\n",
			want:  "rewrites what the resolver produces",
		},
		{
			name:  "graph rewriting section",
			patch: "@@ -1,3 +1,3 @@   \"overrides\": {\n-  \"semver\": \"7.5.0\"\n+  \"semver\": \"7.6.0\"\n",
			want:  "rewrites what the resolver produces",
		},
		{
			name:  "outside any dependency section",
			patch: "@@ -1,3 +1,3 @@\n+  \"private\": true\n",
			want:  "which a dependency bump does not touch",
		},
		{
			name:  "a line that is not a single json property",
			patch: "@@ -1,3 +1,3 @@\n+  garbage without a colon\n",
			want:  "not a single JSON property",
		},
		{
			name:  "not a version range",
			patch: "@@ -1,3 +1,3 @@\n \"dependencies\": {\n+  \"build\": \"tsc && node x.js\"\n",
			want:  "not a version range",
		},
		{
			name:  "no diff at all",
			patch: "   \n",
			want:  "has no diff GitHub could show",
		},
		{
			name:  "lockfile carries no decision of its own",
			file:  "go.sum",
			patch: "--- a/go.sum\n+++ b/go.sum\n@@ -1 +1 @@\n-example.test/a v1.0.0 h1:x\n+example.test/a v1.1.0 h1:y\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			name := test.file
			if name == "" {
				name = "package.json"
			}
			reason := nonMechanicalContent(name, test.patch)
			if test.want == "" {
				if reason != "" {
					t.Fatalf("reason = %q, want none", reason)
				}
				return
			}
			if !strings.Contains(reason, test.want) {
				t.Fatalf("reason = %q, want it to contain %q", reason, test.want)
			}
		})
	}
}

func TestOrchCovJSONLineKeyRefusesUnparseableProperties(t *testing.T) {
	t.Parallel()
	key, value, ok := jsonLineKey(`"lodash": "^4.17.21",`)
	if !ok || key != "lodash" || value != `"^4.17.21"` {
		t.Fatalf("jsonLineKey = %q/%q/%t", key, value, ok)
	}
	for _, body := range []string{"not quoted", `"unterminated`, `"key" value`, ""} {
		if _, _, ok := jsonLineKey(body); ok {
			t.Fatalf("jsonLineKey(%q) accepted an unparseable property", body)
		}
	}
}

func TestOrchCovLooksLikeDependencyEntryAcceptsEveryVersionSpelling(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: `"^4.17.21"`, want: true},
		{value: `"~1.2.3"`, want: true},
		{value: `">=1.0.0"`, want: true},
		{value: `"*"`, want: true},
		{value: `"1.2.3"`, want: true},
		{value: `"workspace:*"`, want: true},
		{value: `"catalog:default"`, want: true},
		{value: `"npm:other@1.0.0"`, want: true},
		{value: `"file:../local"`, want: true},
		{value: `"git+https://example.test/a.git"`, want: true},
		{value: `not json`, want: false},
		{value: `""`, want: false},
		{value: `"   "`, want: false},
		{value: `"tsc && node x.js"`, want: false},
		{value: `"latest"`, want: false},
	} {
		if got := looksLikeDependencyEntry(test.value); got != test.want {
			t.Fatalf("looksLikeDependencyEntry(%s) = %t, want %t", test.value, got, test.want)
		}
	}
}

func TestOrchCovTrimForMessageBoundsLongValues(t *testing.T) {
	t.Parallel()
	if got := trimForMessage("short"); got != "short" {
		t.Fatalf("short value = %q", got)
	}
	long := strings.Repeat("x", 100)
	got := trimForMessage(long)
	if len([]rune(got)) != 61 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long value = %q", got)
	}
}

func TestOrchCovMechanicalVerdictSummaryExplainsItself(t *testing.T) {
	t.Parallel()
	if got := (MechanicalVerdict{Mechanical: true}).Summary(); !strings.Contains(got, "every changed line") {
		t.Fatalf("mechanical summary = %q", got)
	}
	if got := (MechanicalVerdict{}).Summary(); got != "the change is not a mechanical dependency bump" {
		t.Fatalf("reasonless summary = %q", got)
	}
	got := (MechanicalVerdict{Reasons: []string{"a.go is not a dependency manifest", "b.go is not a dependency manifest"}}).Summary()
	for _, want := range []string{"a.go", "b.go"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary %q is missing %q", got, want)
		}
	}
}
