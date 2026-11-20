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

func TestWorkspaceChangesAreDenyByDefaultAndOnlyAnExistingPinsVersionIsMechanical(t *testing.T) {
	t.Parallel()
	// head places the hunk at line 5 of this file inside `overrides:`, while
	// Git's own heading would name the earlier `minimumReleaseAgeExclude`.
	overridesHead := "packages:\n  - apps/*\nminimumReleaseAgeExclude:\n  - astro@7.4.7\noverrides:\n  semver: 7.5.0\n  other: 1.0.0\n"
	quotedKeyHead := "overrides:\n  semver: 7.5.0\n\"packageExtensions\":\n  semver:\n    dependencies:\n      x: 1\n"
	for _, test := range []struct {
		name    string
		patch   string
		content string
		want    string
	}{
		{name: "an existing pin's version, section in the hunk", patch: "@@ -1 +1 @@\n overrides:\n-  semver: 7.5.0\n+  semver: 7.6.0\n"},
		{name: "a versioned key pin", patch: "@@ -8,4 +8,4 @@ minimumReleaseAgeExclude:\n   - astro@7.4.7\n overrides:\n-  undici@7.29.1: 8.12.4\n+  undici@7.29.1: 8.13.0"},
		{name: "section placed by the head file", patch: "@@ -6,2 +6,2 @@ minimumReleaseAgeExclude:\n-  semver: 7.5.0\n+  semver: 7.6.0\n   other: 1.0.0", content: overridesHead},
		{name: "quoted and ranged values", patch: "@@ -1 +1 @@\n overrides:\n-  semver: \"^7.5.0\"\n+  semver: '>=7.6.0 <8'\n"},
		{name: "catalog pin", patch: "@@ -1 +1 @@\n catalog:\n-  react: ^18.0.0\n+  react: ^18.3.0\n"},
		{name: "catalogs pin under a named group", patch: "@@ -1 +1 @@\n catalogs:\n   react17:\n-    react: 17.0.1\n+    react: 17.0.2\n"},
		{name: "comments and blank lines are ignored", patch: "@@ -1 +1 @@\n overrides:\n \n-  # old\n+  # new\n"},
		{name: "file headers are skipped", patch: "--- a/x\n+++ b/x\n@@ -1 +1 @@\n overrides:\n-  semver: 7.5.0\n+  semver: 7.6.0\n"},
		{name: "a context line that is not a property is not judged", patch: "@@ -1 +1 @@\n overrides:\n   - note\n-  semver: 7.5.0\n+  semver: 7.6.0\n"},

		{name: "an added pin to another package", patch: "@@ -1 +1,2 @@\n overrides:\n+  lodash: github:evil/lodash\n", want: "not the version of a pin"},
		{name: "an added pin with a plain version", patch: "@@ -1 +1,2 @@\n overrides:\n+  lodash: 4.17.21\n", want: "adds, removes or renames the pin"},
		{name: "a removed pin", patch: "@@ -1,2 +1 @@\n overrides:\n-  lodash: 4.17.21\n", want: "adds, removes or renames the pin"},
		{name: "a renamed pin", patch: "@@ -1,2 +1,2 @@\n overrides:\n-  lodash: 4.17.21\n+  underscore: 4.17.21\n", want: "adds, removes or renames the pin"},
		{name: "a pin changed to an npm alias", patch: "@@ -1 +1 @@\n overrides:\n-  semver: 7.5.0\n+  semver: npm:evil-semver@1.0.0\n", want: "not the version of a pin"},
		{name: "a pin changed to a git url", patch: "@@ -1 +1 @@\n overrides:\n-  semver: 7.5.0\n+  semver: git+https://evil.test/s.git\n", want: "not the version of a pin"},
		{name: "a pin changed to a path", patch: "@@ -1 +1 @@\n overrides:\n-  semver: 7.5.0\n+  semver: file:../evil\n", want: "not the version of a pin"},
		{name: "a pin with no value", patch: "@@ -1 +1 @@\n overrides:\n-  semver: 7.5.0\n+  semver:\n", want: "not the version of a pin"},
		{name: "a pin that is not a property", patch: "@@ -1 +1 @@\n overrides:\n+  - semver\n", want: "not the version of a pin"},
		{name: "a pin indented too deep", patch: "@@ -1 +1 @@\n overrides:\n-    semver: 7.5.0\n+    semver: 7.6.0\n", want: "not the version of a pin"},
		{name: "a catalogs entry indented as a group", patch: "@@ -1 +1 @@\n catalogs:\n-  react: 7.5.0\n+  react: 7.6.0\n", want: "not the version of a pin"},
		{name: "a new catalogs group", patch: "@@ -1 +1 @@\n catalogs:\n+  react17:\n", want: "not the version of a pin"},
		{name: "the same key in two groups cannot be swapped silently", patch: "@@ -1 +1 @@\n catalogs:\n   a:\n-    react: 1.0.0\n   b:\n+    react: 1.0.1\n", want: "adds, removes or renames the pin"},
		{name: "a dangerous build setting", patch: "@@ -1 +1 @@\n-dangerouslyAllowAllBuilds: false\n+dangerouslyAllowAllBuilds: true\n", want: "top-level line"},
		{name: "a new onlyBuiltDependencies entry", patch: "@@ -1 +1,2 @@ onlyBuiltDependencies:\n   - esbuild\n+  - evil\n", want: "outside overrides"},
		{name: "a new workspace glob", patch: "@@ -1 +1,2 @@\n packages:\n+  - evil/*\n", want: "outside overrides"},
		{name: "a packageExtensions edit", patch: "@@ -1 +1 @@\n packageExtensions:\n-  semver: 7.5.0\n+  semver: 7.6.0\n", want: "outside overrides"},
		{name: "pins in two hunks are paired per hunk", patch: "@@ -1 +1 @@\n overrides:\n-  semver: 7.5.0\n@@ -9 +9 @@\n overrides:\n+  semver: 7.6.0\n", want: "adds, removes or renames the pin"},
		{name: "a header that names overrides is not trusted", patch: "@@ -3,2 +3,2 @@ overrides:\n-    dependencies: 7.5.0\n+    dependencies: 7.6.0\n", want: "outside overrides"},
		{name: "a quoted key the heading skipped, placed by the head file", patch: "@@ -5,2 +5,2 @@ overrides:\n-    x: 1\n+    x: 2\n", content: quotedKeyHead, want: "outside overrides"},
		{name: "a changed pin section opener", patch: "@@ -1 +1 @@\n-overrides:\n+overrides: {}\n", want: "top-level line"},
		{name: "no section known and no head file", patch: "@@ -4 +4 @@\n-  semver: 7.5.0\n+  semver: 7.6.0\n", want: "outside overrides"},
		{name: "a hunk starting past the end of the head file", patch: "@@ -90 +90 @@\n-  semver: 7.5.0\n+  semver: 7.6.0\n", content: overridesHead, want: "outside overrides"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reason := nonMechanicalWorkspace("pnpm-workspace.yaml", test.patch, test.content)
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

func TestStrictPinVersionAcceptsOnlyRegistryVersionsAndRanges(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]bool{
		"1.2.3": true, "v1.2.3": true, "^1.2.3": true, "~1.2": true, ">=1.0.0": true, "<2": true, "=1.0.0": true,
		"1.x": true, "1.2.*": true, "1.0.0-beta.1": true, "1.0.0+build.5": true, ">=1.0.0 <2.0.0": true, "^1.0.0 || ^2.0.0": true, " 1.2.3 ": true,
		"": false, "*": false, "latest": false, "npm:x@1.0.0": false, "github:a/b": false, "git+https://x/y.git": false,
		"git:x": false, "http://x/y.tgz": false, "https://x/y.tgz": false, "file:../x": false, "link:../x": false,
		"workspace:*": false, "patch:x@1.0.0#p": false, "portal:../x": false, "a/b": false, "1.0.0||npm:x": false, "1.0.0 github:a/b": false,
	} {
		if got := strictPinVersion(value); got != want {
			t.Errorf("strictPinVersion(%q) = %t, want %t", value, got, want)
		}
	}
}

func TestWorkspaceTopAtReadsTheEnclosingTopLevelKey(t *testing.T) {
	t.Parallel()
	content := "# c\npackages:\n  - a\n\n'overrides':\n\tx: 1\n  y: 2\n- note\n"
	for line, want := range map[int]string{1: "", 3: "packages", 6: "overrides", 9: "overrides", 99: ""} {
		if got := workspaceTopAt(content, line); got != want {
			t.Errorf("workspaceTopAt(line %d) = %q, want %q", line, got, want)
		}
	}
}

func TestUnquoteYAMLScalarDropsOneLevelOfQuotes(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{`'1.0'`: "1.0", `"1.0"`: "1.0", ` 1.0 `: "1.0", `'1.0"`: `'1.0"`, `'`: `'`} {
		if got := unquoteYAMLScalar(in); got != want {
			t.Errorf("unquoteYAMLScalar(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStaleHeadFilesAreRefused(t *testing.T) {
	t.Parallel()
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	manifest := ChangedFile{Filename: "go.sum", Patch: "@@\n-a v1 h1:x\n+a v2 h1:y"}
	for name, test := range map[string]struct {
		url  string
		want bool
	}{
		"the head commit":         {"https://api.github.com/repos/o/r/contents/go.sum?ref=" + head, true},
		"another commit":          {"https://api.github.com/repos/o/r/contents/go.sum?ref=bbbb", false},
		"no ref":                  {"https://api.github.com/repos/o/r/contents/go.sum", false},
		"an unparseable url":      {"https://api.github.com/%zz?ref=" + head, false},
		"no url to check against": {"", true},
	} {
		file := manifest
		file.ContentsURL = test.url
		if got := ClassifyMechanicalPullRequest([]ChangedFile{file}, PullRequest{HeadSHA: head}).Mechanical; got != test.want {
			t.Errorf("%s: mechanical = %t, want %t", name, got, test.want)
		}
	}
	file := manifest
	file.ContentsURL = "https://api.github.com/repos/o/r/contents/go.sum?ref=bbbb"
	if !ClassifyMechanical([]ChangedFile{file}).Mechanical {
		t.Error("without a pull request head there is nothing to compare against")
	}
}

func TestPinSectionsAcceptOnlyAReplacedPinWithAPlainRegistryVersion(t *testing.T) {
	t.Parallel()
	pin := func(sign, section, key, value string) string {
		return sign + "    \"" + key + "\": \"" + value + "\"\n"
	}
	opener := func(name string) string { return " \"" + name + "\": {\n" }
	for _, test := range []struct {
		name  string
		patch string
		want  string
	}{
		{name: "overrides pin replaced", patch: "@@ -1 +1 @@\n" + opener("overrides") + pin("-", "", "semver", "7.5.4") + pin("+", "", "semver", "7.6.0")},
		{name: "resolutions pin replaced", patch: "@@ -1 +1 @@\n" + opener("resolutions") + pin("-", "", "semver", "7.5.4") + pin("+", "", "semver", "^7.6.0")},
		{name: "pnpm overrides pin replaced", patch: "@@ -1 +1 @@\n \"pnpm\": {\n" + opener("overrides") + pin("-", "", "semver", "7.5.4") + pin("+", "", "semver", "7.6.0")},
		{name: "a new pin", patch: "@@ -1 +1 @@\n" + opener("overrides") + pin("+", "", "semver", "7.6.0"), want: "adds, removes or renames the pin"},
		{name: "a removed pin", patch: "@@ -1 +1 @@\n" + opener("overrides") + pin("-", "", "semver", "7.6.0"), want: "adds, removes or renames the pin"},
		{name: "a pin added under npm alias", patch: "@@ -1 +1 @@\n" + opener("overrides") + pin("+", "", "lodash", "npm:evil-lodash@1.0.0"), want: "plain registry version"},
		{name: "a pin added under github", patch: "@@ -1 +1 @@\n" + opener("overrides") + pin("+", "", "lodash", "github:evil/lodash"), want: "plain registry version"},
		{name: "a resolutions pin changed to a file", patch: "@@ -1 +1 @@\n" + opener("resolutions") + pin("-", "", "lodash", "4.0.0") + pin("+", "", "lodash", "file:../evil"), want: "plain registry version"},
		{name: "a pin whose value is not a string", patch: "@@ -1 +1 @@\n" + opener("overrides") + "-    \"lodash\": 1\n+    \"lodash\": 2\n", want: "plain registry version"},
		{name: "a new overrides block", patch: "@@ -1 +1 @@\n+  \"overrides\": {\n+    \"lodash\": \"github:evil/lodash\"\n+  }\n", want: "not a dependency section"},
		{name: "a rewritten overrides opener", patch: "@@ -1 +1 @@\n-  \"overrides\": {\n+  \"overrides\": {\n", want: "not a dependency section"},
		{name: "pins in two hunks are paired per hunk", patch: "@@ -1 +1 @@\n" + opener("overrides") + pin("-", "", "a", "1.0.0") + "@@ -9 +9 @@\n" + opener("overrides") + pin("+", "", "a", "1.0.1"), want: "adds, removes or renames the pin"},
		{name: "a new dependency entry with a plain version stays mechanical", patch: "@@ -1 +1 @@\n" + opener("dependencies") + pin("+", "", "lodash", "4.17.21")},
		{name: "a new dependency entry with github", patch: "@@ -1 +1 @@\n" + opener("dependencies") + pin("+", "", "lodash", "github:evil/lodash"), want: "not a version range"},
		{name: "a dependency changed to an npm alias", patch: "@@ -1 +1 @@\n" + opener("dependencies") + pin("-", "", "lodash", "4.0.0") + pin("+", "", "lodash", "npm:evil@1.0.0"), want: "not a version range"},
		{name: "a dependency range hiding a protocol", patch: "@@ -1 +1 @@\n" + opener("dependencies") + pin("+", "", "lodash", "1.0.0||npm:evil@1"), want: "not a version range"},
		{name: "a dependency to a git url", patch: "@@ -1 +1 @@\n" + opener("dependencies") + pin("+", "", "lodash", "git+https://evil.test/l.git"), want: "not a version range"},
		{name: "a dependency to a path", patch: "@@ -1 +1 @@\n" + opener("dependencies") + pin("+", "", "lodash", "link:../evil"), want: "not a version range"},
		{name: "a dependency to an http tarball", patch: "@@ -1 +1 @@\n" + opener("dependencies") + pin("+", "", "lodash", "https://evil.test/l.tgz"), want: "not a version range"},
		{name: "a dependency to a workspace package stays mechanical", patch: "@@ -1 +1 @@\n" + opener("dependencies") + pin("+", "", "lodash", "workspace:*")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			reason := nonMechanicalContent("package.json", test.patch, "")
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

func TestHunkNewStartReadsTheNewSideLineNumber(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		header string
		want   int
		ok     bool
	}{
		{"@@ -19,7 +21,7 @@", 21, true},
		{"@@ -19 +21 @@ x", 21, true},
		{"@@ -19,7 @@", 0, false},
		{"@@ -19,7 +x,7 @@", 0, false},
		{"@@ -19,7 +0,0 @@", 0, false},
	} {
		got, ok := hunkNewStart(test.header)
		if got != test.want || ok != test.ok {
			t.Fatalf("hunkNewStart(%q) = %d,%t want %d,%t", test.header, got, ok, test.want, test.ok)
		}
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
		// content is the head file, when the case supplies one.
		content string
		want    string
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
			name:  "overrides is a pin section",
			patch: "@@ -1,3 +1,3 @@   \"overrides\": {\n-  \"semver\": \"7.5.0\"\n+  \"semver\": \"7.6.0\"\n",
		},
		{
			name:  "a nested override object is a decision",
			patch: "@@ -1,3 +1,3 @@   \"overrides\": {\n   \"semver\": {\n-    \"x\": \"7.5.0\"\n+    \"x\": \"7.6.0\"\n",
			want:  "inside semver",
		},
		{
			name:  "graph rewriting section",
			patch: "@@ -1,3 +1,3 @@   \"packageExtensions\": {\n-  \"semver\": \"7.5.0\"\n+  \"semver\": \"7.6.0\"\n",
			want:  "rewrites what the resolver produces",
		},
		{
			name:    "head content places a hunk with an unreadable header",
			patch:   "@@ -4,3 +4,3 @@\n-    \"c\": \"1.0.0\",\n+    \"c\": \"1.0.1\",\n",
			content: "{\n  \"dependencies\": {\n    \"a\": \"1\",\n    \"c\": \"1.0.1\",\n  }\n}\n",
		},
		{
			name:    "head content places a hunk inside engines",
			patch:   "@@ -4,3 +4,3 @@\n-    \"node\": \"20.0.0\",\n+    \"node\": \"22.0.0\",\n",
			content: "{\n  \"engines\": {\n    \"npm\": \"1\",\n    \"node\": \"22.0.0\",\n  }\n}\n",
			want:    "inside engines",
		},
		{
			name:    "a bare brace inside head content cannot name a dependency section",
			patch:   "@@ -6,3 +6,3 @@\n-    \"c\": \"1.0.0\",\n+    \"c\": \"1.0.1\",\n",
			content: "{\n  \"files\": [\n    {\n      \"a\": \"1\"\n    },\n    \"c\": \"1.0.1\",\n}\n",
			want:    "which a dependency bump does not touch",
		},
		{
			name:  "outside any dependency section",
			patch: "@@ -1,3 +1,3 @@\n+  \"private\": true\n",
			want:  "which a dependency bump does not touch",
		},
		{
			name:  "a bare brace opens an unnamed object",
			patch: "@@ -1,3 +1,3 @@\n {\n+  \"private\": true\n",
			want:  "outside any dependency section",
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
			reason := nonMechanicalContent(name, test.patch, test.content)
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
		{value: `"npm:other@1.0.0"`, want: false},
		{value: `"file:../local"`, want: false},
		{value: `"link:../local"`, want: false},
		{value: `"github:a/b"`, want: false},
		{value: `"1.0.0||npm:x"`, want: false},
		{value: `"git+https://example.test/a.git"`, want: false},
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

func TestYAMLLineKeyReadsQuotedAndPlainKeys(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		in, key, value string
		ok             bool
	}{
		{"semver: 7.6.0", "semver", "7.6.0", true},
		{"'@a/b': 1", "@a/b", "1", true},
		{`"a": "b"`, "a", `"b"`, true},
		{"undici@7.29.1: 8.1", "undici@7.29.1", "8.1", true},
		{"", "", "", false},
		{"'unterminated: 1", "", "", false},
		{"'a' 1", "", "", false},
		{": 1", "", "", false},
		{"- item", "", "", false},
	} {
		key, value, ok := yamlLineKey(test.in)
		if key != test.key || value != test.value || ok != test.ok {
			t.Errorf("yamlLineKey(%q) = %q,%q,%t want %q,%q,%t", test.in, key, value, ok, test.key, test.value, test.ok)
		}
	}
}
