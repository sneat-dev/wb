package mechanicalchange

import (
	"strings"
	"testing"
)

// classifyFile runs one manifest through the classifier and returns the refusal
// summary, or "" when it is mechanical.
func classifyFile(file ChangedFile) string {
	verdict := ClassifyMechanical([]ChangedFile{file})
	if verdict.Mechanical {
		return ""
	}
	return verdict.Summary()
}

func TestNewDirectDependenciesNeedAReviewAndBumpsDoNot(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		file  string
		patch string
		want  string // "" = mechanical, else a fragment of the refusal
	}{
		// package.json
		{name: "package.json replaced version", file: "package.json",
			patch: "@@ -5,3 +5,3 @@\n   \"dependencies\": {\n-    \"a\": \"^1.0.0\",\n+    \"a\": \"^1.1.0\",\n     \"b\": \"2.0.0\"\n"},
		{name: "package.json new dependency", file: "package.json",
			patch: "@@ -5,3 +5,4 @@\n   \"dependencies\": {\n     \"a\": \"^1.0.0\",\n+    \"left-pad\": \"^1.3.0\",\n     \"b\": \"2.0.0\"\n",
			want:  "adds new direct dependency left-pad; needs a recorded review"},
		{name: "package.json new last dependency also touches the comma of the line before", file: "package.json",
			patch: "@@ -5,3 +5,4 @@\n   \"dependencies\": {\n-    \"a\": \"^1.0.0\"\n+    \"a\": \"^1.0.0\",\n+    \"left-pad\": \"^1.3.0\"\n   }\n",
			want:  "adds new direct dependency left-pad"},
		{name: "package.json new devDependency", file: "package.json",
			patch: "@@ -5,3 +5,4 @@\n   \"devDependencies\": {\n+    \"vitest\": \"^2.0.0\",\n     \"a\": \"1.0.0\"\n",
			want:  "adds new direct dependency vitest"},
		{name: "package.json new peerDependency", file: "package.json",
			patch: "@@ -5,3 +5,4 @@\n   \"peerDependencies\": {\n+    \"react\": \"^18.0.0\",\n     \"a\": \"1.0.0\"\n",
			want:  "adds new direct dependency react"},
		{name: "package.json new optionalDependency", file: "package.json",
			patch: "@@ -5,3 +5,4 @@\n   \"optionalDependencies\": {\n+    \"fsevents\": \"^2.0.0\",\n     \"a\": \"1.0.0\"\n",
			want:  "adds new direct dependency fsevents"},
		{name: "package.json moved to another section is a new direct dependency there", file: "package.json",
			patch: "@@ -5,4 +5,4 @@\n   \"dependencies\": {\n-    \"a\": \"1.0.0\"\n   },\n   \"devDependencies\": {\n+    \"a\": \"1.0.0\"\n",
			want:  "adds new direct dependency a"},
		{name: "package.json re-sort across two hunks is a replacement", file: "package.json",
			patch: "@@ -5,3 +5,2 @@\n   \"dependencies\": {\n-    \"z\": \"1.0.0\",\n     \"a\": \"1.0.0\",\n@@ -20,3 +19,4 @@\n   \"dependencies\": {\n+    \"z\": \"1.0.0\",\n     \"m\": \"1.0.0\",\n"},
		{name: "package.json new override stays refused", file: "package.json",
			patch: "@@ -5,3 +5,4 @@\n   \"overrides\": {\n+    \"semver\": \"7.6.0\",\n     \"a\": \"1.0.0\"\n",
			want:  "adds, removes or renames the pin"},

		// pnpm-workspace.yaml catalogs
		{name: "catalog replaced version", file: "pnpm-workspace.yaml",
			patch: "@@ -1,4 +1,4 @@\n catalog:\n-  react: ^18.2.0\n+  react: ^18.3.0\n"},
		{name: "catalog new entry", file: "pnpm-workspace.yaml",
			patch: "@@ -1,3 +1,4 @@\n catalog:\n   react: ^18.2.0\n+  left-pad: ^1.3.0\n",
			want:  "adds new direct dependency left-pad; needs a recorded review"},
		{name: "named catalog new entry", file: "pnpm-workspace.yaml",
			patch: "@@ -1,4 +1,5 @@\n catalogs:\n   legacy:\n     react: ^17.0.0\n+    left-pad: ^1.3.0\n",
			want:  "adds new direct dependency left-pad"},
		{name: "catalog re-sort across two hunks is a replacement", file: "pnpm-workspace.yaml",
			patch: "@@ -1,3 +1,2 @@\n catalog:\n-  zod: ^3.0.0\n   react: ^18.2.0\n@@ -9,3 +8,4 @@\n catalog:\n+  zod: ^3.0.0\n   vue: ^3.0.0\n"},
		{name: "catalog removed entry is refused", file: "pnpm-workspace.yaml",
			patch: "@@ -1,3 +1,2 @@\n catalog:\n-  zod: ^3.0.0\n   react: ^18.2.0\n",
			want:  "removes the catalog entry zod"},

		// go.mod
		{name: "go.mod replaced single-line require", file: "go.mod",
			patch: "@@ -5,1 +5,1 @@\n-require github.com/a/b v1.0.0\n+require github.com/a/b v1.1.0\n"},
		{name: "go.mod new single-line require", file: "go.mod",
			patch: "@@ -5,0 +6,1 @@\n+require github.com/new/dep v1.0.0\n",
			want:  "adds new direct dependency github.com/new/dep; needs a recorded review"},
		{name: "go.mod new single-line indirect require", file: "go.mod",
			patch: "@@ -5,0 +6,1 @@\n+require github.com/new/dep v1.0.0 // indirect\n"},
		{name: "go.mod new block require", file: "go.mod",
			patch: "@@ -5,3 +5,4 @@ require (\n \tgithub.com/a/b v1.0.0\n+\tgithub.com/new/dep v1.0.0\n )\n",
			want:  "adds new direct dependency github.com/new/dep"},
		{name: "go.mod new block indirect require", file: "go.mod",
			patch: "@@ -5,3 +5,4 @@ require (\n \tgithub.com/a/b v1.0.0\n+\tgithub.com/new/dep v1.0.0 // indirect\n )\n"},
		{name: "go.mod new require block", file: "go.mod",
			patch: "@@ -5,0 +6,4 @@\n+require (\n+\tgithub.com/new/dep v1.0.0\n+\tgithub.com/new/two v1.0.0 // indirect\n+)\n",
			want:  "adds new direct dependency github.com/new/dep"},
		{name: "go.mod re-sort within the block is a replacement", file: "go.mod",
			patch: "@@ -5,3 +5,2 @@ require (\n-\tgithub.com/z/z v1.0.0\n \tgithub.com/a/b v1.0.0\n@@ -9,3 +8,4 @@ require (\n+\tgithub.com/z/z v1.0.0\n \tgithub.com/m/m v1.0.0\n"},
		{name: "go.mod indirect turned direct is not a bump", file: "go.mod",
			patch: "@@ -5,1 +5,1 @@\n-\tgithub.com/a/b v1.0.0 // indirect\n+\tgithub.com/a/b v1.0.0\n",
			want:  "adds new direct dependency github.com/a/b"},
		{name: "go.mod block replace entry", file: "go.mod",
			patch: "@@ -5,1 +5,2 @@ replace (\n+\tgithub.com/a/b => ../evil\n",
			want:  "replace directive"},
		{name: "go.mod module directive", file: "go.mod",
			patch: "@@ -1,1 +1,1 @@\n-module github.com/a/b\n+module github.com/a/c\n",
			want:  "module directive"},
		{name: "go.mod comment and blank lines are not requirements", file: "go.mod",
			patch: "@@ -1,3 +1,4 @@\n+// a note\n+\n+)\n"},
		{name: "go.sum new lines", file: "go.sum",
			patch: "@@ -1,0 +2,2 @@\n+github.com/new/dep v1.0.0 h1:x=\n+github.com/new/dep v1.0.0/go.mod h1:y=\n"},
		{name: "pnpm-lock new transitive entry", file: "pnpm-lock.yaml",
			patch: "@@ -1,0 +2,2 @@\n+  /left-pad@1.3.0:\n+    resolution: {integrity: sha512-x}\n"},
		{name: "package-lock new transitive entry", file: "package-lock.json",
			patch: "@@ -1,0 +2,2 @@\n+    \"node_modules/left-pad\": {\n+      \"version\": \"1.3.0\"\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := classifyFile(ChangedFile{Filename: test.file, Patch: test.patch})
			if test.want == "" {
				if got != "" {
					t.Fatalf("refused: %s", got)
				}
				return
			}
			if !strings.Contains(got, test.want) {
				t.Fatalf("summary = %q, want it to contain %q", got, test.want)
			}
		})
	}
}

// A Renovate Go bump as the bot writes it: the module is bumped and `go mod
// tidy` brings in and drops transitive `// indirect` requirements.
const renovateGoBumpPatch = "@@ -5,13 +5,14 @@ go 1.24\n" +
	" require (\n" +
	"-\tgithub.com/dal-go/dalgo v0.50.0\n" +
	"+\tgithub.com/dal-go/dalgo v0.51.0\n" +
	" \tgithub.com/stretchr/testify v1.10.0\n" +
	" )\n" +
	" \n" +
	" require (\n" +
	" \tgithub.com/davecgh/go-spew v1.1.1 // indirect\n" +
	"+\tgithub.com/strongo/random v0.0.1 // indirect\n" +
	"+\tgithub.com/strongo/validation v0.0.7 // indirect\n" +
	" \tgopkg.in/yaml.v3 v3.0.1 // indirect\n" +
	" )\n"

func TestRealisticRenovateGoBumps(t *testing.T) {
	t.Parallel()
	sum := ChangedFile{Filename: "go.sum", Patch: "@@ -1,2 +1,4 @@\n-github.com/dal-go/dalgo v0.50.0 h1:a=\n-github.com/dal-go/dalgo v0.50.0/go.mod h1:b=\n+github.com/dal-go/dalgo v0.51.0 h1:c=\n+github.com/dal-go/dalgo v0.51.0/go.mod h1:d=\n+github.com/strongo/random v0.0.1 h1:e=\n+github.com/strongo/random v0.0.1/go.mod h1:f=\n"}
	if got := classifyFile(ChangedFile{Filename: "go.mod", Patch: renovateGoBumpPatch}); got != "" {
		t.Fatalf("a bump that adds // indirect lines was refused: %s", got)
	}
	if verdict := ClassifyMechanical([]ChangedFile{{Filename: "go.mod", Patch: renovateGoBumpPatch}, sum}); !verdict.Mechanical {
		t.Fatalf("go.mod with go.sum was refused: %s", verdict.Summary())
	}
	direct := strings.Replace(renovateGoBumpPatch, "+\tgithub.com/strongo/random v0.0.1 // indirect\n", "+\tgithub.com/strongo/random v0.0.1\n", 1)
	got := classifyFile(ChangedFile{Filename: "go.mod", Patch: direct})
	if !strings.Contains(got, "adds new direct dependency github.com/strongo/random; needs a recorded review") {
		t.Fatalf("a bump that adds a direct require = %q", got)
	}
}

func TestWorkspaceProtocolValuesPointOnlyAtAVersionOfThisWorkspace(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]bool{
		"workspace:*":                 true,
		"workspace:^":                 true,
		"workspace:~":                 true,
		"workspace:1.2.3":             true,
		"workspace:^1.2.3":            true,
		"workspace:~1.2":              true,
		"workspace:>=1.0.0":           true,
		"workspace:":                  false,
		"workspace:../../evil":        false,
		"workspace:./pkg":             false,
		"workspace:/abs":              false,
		"workspace:@scope/other@^1.0": false,
		"workspace:evil":              false,
		"workspace:*/x":               false,
		"catalog:":                    true,
	} {
		if got := looksLikeDependencyEntry(`"` + value + `"`); got != want {
			t.Errorf("%s: accepted = %t, want %t", value, got, want)
		}
	}
	patch := "@@ -5,3 +5,3 @@\n   \"dependencies\": {\n-    \"a\": \"workspace:*\",\n+    \"a\": \"workspace:../../evil\",\n"
	if got := classifyFile(ChangedFile{Filename: "package.json", Patch: patch}); !strings.Contains(got, "not a version range") {
		t.Fatalf("summary = %q", got)
	}
}

func TestBotWorkflowUsesBumpsNeverGoBackwards(t *testing.T) {
	t.Parallel()
	sha := func(char string) string { return strings.Repeat(char, 40) }
	for _, test := range []struct {
		name   string
		title  string
		before string
		after  string
		want   string
	}{
		{name: "tag upgrade", title: "update actions/checkout to v5", before: "uses: actions/checkout@v4", after: "uses: actions/checkout@v5"},
		{name: "tag minor upgrade", title: "update actions/checkout to v4.2.0", before: "uses: actions/checkout@v4.1.9", after: "uses: actions/checkout@v4.2.0"},
		{name: "tag with more components upgrade", title: "update actions/checkout to v4.0.1", before: "uses: actions/checkout@v4", after: "uses: actions/checkout@v4.0.1"},
		{name: "tag major downgrade", title: "update actions/checkout to v3", before: "uses: actions/checkout@v4", after: "uses: actions/checkout@v3", want: "downgrade"},
		{name: "tag minor downgrade", title: "update actions/checkout to v4.1", before: "uses: actions/checkout@v4.2.0", after: "uses: actions/checkout@v4.1", want: "downgrade"},
		{name: "tag patch downgrade", title: "update actions/checkout", before: "uses: actions/checkout@v4.2.2", after: "uses: actions/checkout@v4.2.1", want: "downgrade"},
		{name: "minor up but patch down is an upgrade", title: "update actions/checkout", before: "uses: actions/checkout@v4.1.9", after: "uses: actions/checkout@v4.2.0"},
		{name: "sha upgrade by comments", title: "update actions/checkout to v4.2.2", before: "uses: actions/checkout@" + sha("a") + " # v4.2.1", after: "uses: actions/checkout@" + sha("b") + " # v4.2.2"},
		{name: "sha downgrade by comments", title: "update actions/checkout to v4.2.0", before: "uses: actions/checkout@" + sha("a") + " # v4.2.1", after: "uses: actions/checkout@" + sha("b") + " # v4.2.0", want: "downgrade"},
		{name: "tag pinned to a sha of the same version", title: "pin actions/checkout to v4", before: "uses: actions/checkout@v4", after: "uses: actions/checkout@" + sha("b") + " # v4"},
		{name: "tag pinned to a sha of an older version", title: "update actions/checkout to v3.9.9", before: "uses: actions/checkout@v4", after: "uses: actions/checkout@" + sha("b") + " # v3.9.9", want: "downgrade"},
		{name: "old sha has no comment", title: "update actions/checkout to v4.2.2", before: "uses: actions/checkout@" + sha("a"), after: "uses: actions/checkout@" + sha("b") + " # v4.2.2", want: "cannot be compared"},
		{name: "old sha comment is not a version", title: "update actions/checkout to v4.2.2", before: "uses: actions/checkout@" + sha("a") + " # main", after: "uses: actions/checkout@" + sha("b") + " # v4.2.2", want: "cannot be compared"},
		{name: "new pre-release tag cannot be compared", title: "update actions/checkout to v5.0.0-beta.1", before: "uses: actions/checkout@v4", after: "uses: actions/checkout@v5.0.0-beta.1", want: "cannot be compared"},
		{name: "old build-suffixed tag cannot be compared", title: "update actions/checkout to v5", before: "uses: actions/checkout@v4+x", after: "uses: actions/checkout@v5", want: "cannot be compared"},
		{name: "five-part version cannot be compared", title: "update actions/checkout to v1.2.3.4", before: "uses: actions/checkout@" + sha("a") + " # v1.2.3.4.5", after: "uses: actions/checkout@" + sha("b") + " # v1.2.3.4", want: "cannot be compared"},
		{name: "leading-zero part cannot be compared", title: "update actions/checkout", before: "uses: actions/checkout@v04", after: "uses: actions/checkout@v5", want: "cannot be compared"},
		{name: "a tag that is a bare sha-shaped number", title: "update actions/checkout to 5", before: "uses: actions/checkout@4", after: "uses: actions/checkout@5"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			pr := PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: test.title}
			patch := workflowPatch("      - "+test.before, "      - "+test.after)
			verdict := ClassifyMechanicalPullRequest([]ChangedFile{{Filename: ".github/workflows/ci.yml", Patch: patch}}, pr)
			if test.want == "" {
				if !verdict.Mechanical {
					t.Fatalf("refused: %s", verdict.Summary())
				}
				return
			}
			if verdict.Mechanical || !strings.Contains(verdict.Summary(), test.want) {
				t.Fatalf("mechanical = %t, summary = %q, want a refusal containing %q", verdict.Mechanical, verdict.Summary(), test.want)
			}
		})
	}
}

func TestAFileWithoutAContentsURLCannotBeProvedToBeReadAtTheHead(t *testing.T) {
	t.Parallel()
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	file := ChangedFile{Filename: "go.sum", Patch: "@@\n-a v1 h1:x\n+a v2 h1:y"}
	if got := classifyFileAt(file, head); !strings.Contains(got, "different commit") {
		t.Fatalf("empty contents_url: %q", got)
	}
	file.ContentsURL = "https://api.github.com/repos/o/r/contents/go.sum?other=" + head
	if got := classifyFileAt(file, head); !strings.Contains(got, "different commit") {
		t.Fatalf("contents_url without ref=: %q", got)
	}
	file.ContentsURL = "https://api.github.com/repos/o/r/contents/go.sum?ref=" + head
	if got := classifyFileAt(file, head); got != "" {
		t.Fatalf("contents_url at the head: %q", got)
	}
}

func classifyFileAt(file ChangedFile, head string) string {
	verdict := ClassifyMechanicalPullRequest([]ChangedFile{file}, PullRequest{HeadSHA: head})
	if verdict.Mechanical {
		return ""
	}
	return verdict.Summary()
}

func TestSignedPatchLinesKeepTheMarkerAndSkipFileHeaders(t *testing.T) {
	t.Parallel()
	got := signedPatchLines("--- a/go.mod\n+++ b/go.mod\n@@ -1 +1 @@\n context\n-\told v1\n+\tnew v2\n\n")
	if len(got) != 2 || got[0] != "-old v1" || got[1] != "+new v2" {
		t.Fatalf("lines = %q", got)
	}
}
