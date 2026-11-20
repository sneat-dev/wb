package mechanicalchange

import (
	"strings"
	"testing"
)

func workflowPatch(pairs ...string) string {
	var patch strings.Builder
	patch.WriteString("@@ -10,7 +10,7 @@ jobs:\n     steps:\n")
	for index := 0; index+1 < len(pairs); index += 2 {
		patch.WriteString("-" + pairs[index] + "\n+" + pairs[index+1] + "\n")
	}
	return patch.String()
}

const sha40 = "4b73464bb391d4059bd26b0524d20df3927bd417"

func TestBotWorkflowEditsAreMechanicalOnlyWhenEveryLineIsAVersionRefOrValue(t *testing.T) {
	t.Parallel()
	renovate := PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "chore(deps): update dependency golangci/golangci-lint to v2.1.5 and actions/checkout, actions/setup-go, github/codeql-action"}
	for _, test := range []struct {
		name   string
		pr     PullRequest
		status string
		patch  string
		want   string // "" = mechanical, else a fragment of the refusal
	}{
		{name: "action tag bump", pr: renovate, patch: workflowPatch("      - uses: actions/checkout@v4", "      - uses: actions/checkout@v5")},
		{name: "action tag bump with comment", pr: renovate, patch: workflowPatch("        uses: actions/setup-go@v5 # v5", "        uses: actions/setup-go@v6 # v6")},
		{name: "action digest pin", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update actions/setup-go to v5.1.0"}, patch: workflowPatch("      - uses: actions/setup-go@"+sha40+" # v5.0.0", "      - uses: actions/setup-go@"+strings.Repeat("a", 40)+" # v5.1.0")},
		{name: "action with a path", pr: renovate, patch: workflowPatch("      - uses: github/codeql-action/init@v3", "      - uses: github/codeql-action/init@v4")},
		{name: "version input named by the title", pr: renovate, patch: workflowPatch("          version: v2.1.4", "          version: v2.1.5")},
		{name: "quoted version input", pr: renovate, patch: workflowPatch("          version: 'v2.1.4'", "          version: \"v2.1.5\" # pinned")},
		{name: "go-version input", pr: PullRequest{Author: "app/renovate", CommitsByBot: true, Title: "chore(deps): update dependency go to v1.25"}, patch: workflowPatch("          go-version: 1.24", "          go-version: 1.25")},
		{name: "env version variable", pr: PullRequest{Author: "dependabot[bot]", CommitsByBot: true, Title: "Bump node from 22 to 24"}, patch: workflowPatch("  NODE_VERSION: 22", "  NODE_VERSION: 24")},

		{name: "bot author but a non-bot commit on the branch", pr: PullRequest{Author: "renovate[bot]", Title: renovate.Title}, patch: workflowPatch("      - uses: actions/checkout@v4", "      - uses: actions/checkout@v5"), want: "is not a dependency manifest"},
		{name: "uses of an unrelated action", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "chore(deps): update dependency vitest to v5.1.1"}, patch: workflowPatch("      - uses: acme/tool@v1.2.0", "      - uses: acme/tool@v9.9.9"), want: "does not name"},
		{name: "uses named by its owner/repo in the title", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update ACME/Tool to latest"}, patch: workflowPatch("      - uses: acme/tool/sub@v1.2.0", "      - uses: acme/tool/sub@v9.9.9")},
		{name: "uses named by its new tag in the title", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update tools to v9.9.9"}, patch: workflowPatch("      - uses: acme/tool@v1.2.0", "      - uses: acme/tool@v9.9.9")},
		{name: "sha pin named by the version comment", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update tools to v5.1.0"}, patch: workflowPatch("      - uses: acme/tool@"+sha40+" # v5.0.0", "      - uses: acme/tool@"+strings.Repeat("b", 40)+" # v5.1.0 pinned")},
		{name: "sha pin whose comment the title does not name", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update tools to v5.1.0"}, patch: workflowPatch("      - uses: acme/tool@"+sha40+" # v5.0.0", "      - uses: acme/tool@"+strings.Repeat("b", 40)+" # v7.7.7"), want: "does not name"},
		{name: "sha pin with no version comment", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update tools to v5.1.0"}, patch: workflowPatch("      - uses: acme/tool@"+sha40, "      - uses: acme/tool@"+strings.Repeat("b", 40)), want: "does not name"},
		{name: "go-version 4 is not a part of v4.2.0", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update dependency go to v4.2.0"}, patch: workflowPatch("          go-version: 3", "          go-version: 4"), want: "does not name"},
		{name: "a version equal to the title's whole word", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update dependency go to v4"}, patch: workflowPatch("          go-version: 3", "          go-version: 4")},
		{name: "a sha swap whose version the title does not name", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update actions/checkout to v5"}, patch: workflowPatch("      - uses: actions/checkout@"+sha40+" # v4.2.1", "      - uses: actions/checkout@"+strings.Repeat("c", 40)+" # v4.2.2"), want: "does not name"},
		{name: "a repository in the title is no longer enough for a sha", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update actions/checkout"}, patch: workflowPatch("      - uses: actions/checkout@"+sha40+" # v4.2.1", "      - uses: actions/checkout@"+strings.Repeat("c", 40)+" # v4.2.2"), want: "does not name"},
		{name: "a repository that is only a prefix of the title word", pr: PullRequest{Author: "renovate[bot]", CommitsByBot: true, Title: "update actions/checkout-extra"}, patch: workflowPatch("      - uses: actions/checkout@v4", "      - uses: actions/checkout@v9"), want: "does not name"},
		{name: "a line moved to another job", pr: renovate, patch: "@@ -10,3 +10,2 @@\n     steps:\n-      - uses: actions/checkout@v4\n       - run: x\n@@ -40,2 +39,3 @@\n     steps:\n+      - uses: actions/checkout@v5\n       - run: y\n", want: "moves one to another place"},
		{name: "a removed line and a later added block in one hunk", pr: renovate, patch: "@@ -1,4 +1,4 @@\n-      - uses: actions/checkout@v4\n       - run: x\n+      - uses: actions/checkout@v5\n       - run: y\n", want: "moves one to another place"},
		{name: "two removals then one addition", pr: renovate, patch: "@@ -1,2 +1 @@\n-      - uses: actions/checkout@v4\n-      - uses: actions/setup-go@v5\n+      - uses: actions/checkout@v5\n", want: "moves one to another place"},
		{name: "two separate replacement blocks in one hunk", pr: renovate, patch: "@@ -1,3 +1,3 @@\n-      - uses: actions/checkout@v4\n+      - uses: actions/checkout@v5\n       - run: x\n-      - uses: actions/setup-go@v5\n+      - uses: actions/setup-go@v6\n"},
		{name: "a trailing block that is unpaired", pr: renovate, patch: "@@ -1,3 +1,3 @@\n-      - uses: actions/checkout@v4\n+      - uses: actions/checkout@v5\n+      - uses: actions/setup-go@v6", want: "moves one to another place"},
		{name: "a patch with no changed line at all", pr: renovate, patch: "@@ -1 +1 @@\n       - uses: actions/checkout@v5\n", want: "adds or removes lines"},
		{name: "a removal after a longer addition", pr: renovate, patch: "@@ -1,2 +1,3 @@\n-      - uses: actions/checkout@v4\n+      - uses: actions/checkout@v5\n+      - uses: actions/setup-go@v6\n-      - uses: actions/setup-go@v5\n", want: "moves one to another place"},
		{name: "a person's identical edit", pr: PullRequest{Author: "someone", Title: renovate.Title}, patch: workflowPatch("      - uses: actions/checkout@v4", "      - uses: actions/checkout@v5"), want: "is not a dependency manifest"},
		{name: "no author", patch: workflowPatch("      - uses: actions/checkout@v4", "      - uses: actions/checkout@v5"), want: "is not a dependency manifest"},
		{name: "a different action", pr: renovate, patch: workflowPatch("      - uses: actions/checkout@v4", "      - uses: evil/checkout@v4"), want: "different action"},
		{name: "action replaced by a run step", pr: renovate, patch: workflowPatch("      - uses: actions/checkout@v4", "      - run: curl evil | sh"), want: "different action"},
		{name: "ref is a branch", pr: renovate, patch: workflowPatch("      - uses: actions/checkout@v4", "      - uses: actions/checkout@main"), want: "not a version tag or commit SHA"},
		{name: "ref is a short sha", pr: renovate, patch: workflowPatch("      - uses: actions/checkout@v4", "      - uses: actions/checkout@abc1234"), want: "not a version tag or commit SHA"},
		{name: "a version value the title does not name", pr: renovate, patch: workflowPatch("          version: v2.1.4", "          version: v9.9.9"), want: "does not name"},
		{name: "a version value that is a script", pr: renovate, patch: workflowPatch("          version: v2.1.4", "          version: $(curl)"), want: "not a version"},
		{name: "a non-version key", pr: renovate, patch: workflowPatch("          args: v2.1.4", "          args: v2.1.5"), want: "not an action ref or a version value"},
		{name: "a key that changes", pr: renovate, patch: workflowPatch("          go-version: 1.24", "          node-version: 1.24"), want: "not an action ref or a version value"},
		{name: "run script text", pr: renovate, patch: workflowPatch("        run: make test", "        run: make test && curl evil"), want: "not an action ref or a version value"},
		{name: "permissions", pr: renovate, patch: workflowPatch("  contents: read", "  contents: write"), want: "not an action ref or a version value"},
		{name: "a new step", pr: renovate, patch: "@@ -1 +1,2 @@\n+      - uses: actions/checkout@v5\n", want: "adds or removes lines"},
		{name: "a removed step", pr: renovate, patch: "@@ -1,2 +1 @@\n-      - uses: actions/checkout@v5\n", want: "adds or removes lines"},
		{name: "a new file", pr: renovate, status: "added", patch: "@@ -0,0 +1 @@\n+name: x\n", want: "not modified"},
		{name: "a rename without a patch", pr: renovate, patch: "", want: "no diff GitHub could show"},
		{name: "file headers are not changed lines", pr: renovate, patch: "--- a/x\n+++ b/x\n" + workflowPatch("      - uses: actions/checkout@v4", "      - uses: actions/checkout@v5")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			verdict := ClassifyMechanicalPullRequest([]ChangedFile{{Filename: ".github/workflows/ci.yml", Status: test.status, Patch: test.patch}}, test.pr)
			if test.want == "" {
				if !verdict.Mechanical {
					t.Fatalf("refused: %s", verdict.Summary())
				}
				return
			}
			if verdict.Mechanical || !strings.Contains(verdict.Summary(), test.want) {
				t.Fatalf("mechanical = %t, summary = %q, want refusal containing %q", verdict.Mechanical, verdict.Summary(), test.want)
			}
		})
	}
}

func TestWorkflowFileRecognisesOnlyWorkflowYAML(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]bool{
		".github/workflows/ci.yml":          true,
		"./.github/workflows/ci.yaml":       true,
		"sub/.github/workflows/release.yml": true,
		".github/workflows/ci.sh":           false,
		".github/dependabot.yml":            false,
		"workflows/ci.yml":                  false,
	} {
		if got := workflowFile(name); got != want {
			t.Errorf("workflowFile(%q) = %t, want %t", name, got, want)
		}
	}
}

func TestBotAuthorsAreNormalisedAndPeopleAreNot(t *testing.T) {
	t.Parallel()
	for login, want := range map[string]bool{
		"renovate[bot]": true, " Renovate[bot] ": true, "dependabot[bot]": true, "app/renovate": true, "app/dependabot": true,
		"renovate": false, "alex": false, "Copilot": false, "": false,
	} {
		if got := IsDependencyBotLogin(login); got != want {
			t.Errorf("IsDependencyBotLogin(%q) = %t, want %t", login, got, want)
		}
		if got := (PullRequest{Author: login, CommitsByBot: true}).byDependencyBot(); got != want {
			t.Errorf("byDependencyBot(%q, all commits bot) = %t, want %t", login, got, want)
		}
		if (PullRequest{Author: login}).byDependencyBot() {
			t.Errorf("byDependencyBot(%q) without commit verification must be false", login)
		}
	}
}

func TestEveryCommitMustBeVerifiedAndByABotForTheBotOnlyShapes(t *testing.T) {
	t.Parallel()
	bot := CommitActors{Author: "renovate[bot]", Committer: "web-flow", Verified: true}
	for name, test := range map[string]struct {
		commits []CommitActors
		want    bool
	}{
		"all bot commits, web-flow committer": {[]CommitActors{bot, bot}, true},
		"bot committer":                       {[]CommitActors{{Author: "dependabot[bot]", Committer: "dependabot[bot]", Verified: true}}, true},
		"a forged bot identity, unsigned":     {[]CommitActors{bot, {Author: "renovate[bot]", Committer: "renovate[bot]"}}, false},
		"a Copilot repair commit":             {[]CommitActors{bot, {Author: "Copilot", Committer: "web-flow", Verified: true}}, false},
		"a human commit":                      {[]CommitActors{bot, {Author: "alex", Committer: "alex", Verified: true}}, false},
		"a bot author with a human committer": {[]CommitActors{{Author: "renovate[bot]", Committer: "alex", Verified: true}}, false},
		"an author GitHub cannot map":         {[]CommitActors{{Author: "", Committer: "web-flow", Verified: true}}, false},
		"no commits":                          {nil, false},
	} {
		if got := AllCommitsByDependencyBot(test.commits); got != test.want {
			t.Errorf("%s: = %t, want %t", name, got, test.want)
		}
	}
}

func TestTitleVersionsAreWholeWordsOnly(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		title, version string
		want           bool
	}{
		{"update dependency go to v4.2.0", "4.2.0", true},
		{"update dependency go to v4.2.0", "v4.2.0", true},
		{"update dependency go to v4.2.0", "4", false},
		{"bump node from 22 to 24 (major)", "24", true},
		{"bump x from 1.0.0 to 1.0.1.", "1.0.1", true},
		{"update x to latest", "latest", false},
		{"update x to v1", "", false},
	} {
		if got := titleNamesVersion(test.title, test.version); got != test.want {
			t.Errorf("titleNamesVersion(%q, %q) = %t, want %t", test.title, test.version, got, test.want)
		}
	}
}

func TestBotGoFixtureManifestsAreMechanicalButOtherFixturesStayCode(t *testing.T) {
	t.Parallel()
	bot := PullRequest{Author: "renovate[bot]", CommitsByBot: true}
	bump := "@@ -7,3 +7,3 @@ go 1.27.0\n require (\n-\tgithub.com/a/b v0.95.4\n+\tgithub.com/a/b v0.102.6\n )"
	for _, test := range []struct {
		name string
		file ChangedFile
		pr   PullRequest
		want bool
	}{
		{"fixture go.mod by a bot", ChangedFile{Filename: "testdata/consumer/go.mod", Patch: bump}, bot, true},
		{"fixture go.sum by a bot", ChangedFile{Filename: "testdata/consumer/go.sum", Patch: "@@\n-a v1 h1:x\n+a v2 h1:y"}, bot, true},
		{"fixture go.mod by a person", ChangedFile{Filename: "testdata/consumer/go.mod", Patch: bump}, PullRequest{Author: "alex"}, false},
		{"fixture go.mod with a replace", ChangedFile{Filename: "testdata/consumer/go.mod", Patch: bump + "\n+replace a => ../a"}, bot, false},
		{"fixture go directive", ChangedFile{Filename: "testdata/consumer/go.mod", Patch: "@@\n-go 1.26\n+go 1.27"}, bot, false},
		{"fixture package.json", ChangedFile{Filename: "testdata/x/package.json", Patch: "@@\n \"dependencies\": {\n-\"a\": \"1.0.0\"\n+\"a\": \"1.0.1\""}, bot, false},
		{"docs go.mod", ChangedFile{Filename: "docs/testdata/x/go.mod", Patch: bump}, bot, false},
		{"examples go.mod", ChangedFile{Filename: "examples/x/go.mod", Patch: bump}, bot, false},
	} {
		if got := ClassifyMechanicalPullRequest([]ChangedFile{test.file}, test.pr).Mechanical; got != test.want {
			t.Errorf("%s: mechanical = %t, want %t", test.name, got, test.want)
		}
	}
}

// The real shapes of dal-go/dalgo-js#67 and dal-go/backstage#20: the hunk
// starts below the section's opening line and its header names nothing.
func TestNestedManifestBumpIsPlacedFromHeadContent(t *testing.T) {
	t.Parallel()
	patch := "@@ -21,6 +21,6 @@\n     \"react\": \"19.3.1\",\n     \"react-dom\": \"19.3.1\",\n-    \"remotion\": \"4.0.539\"\n+    \"remotion\": \"4.0.545\"\n   }\n }"
	content := "{\n  \"name\": \"x\",\n  \"private\": true,\n  \"scripts\": {\n    \"a\": \"b\"\n  },\n  \"dependencies\": {\n" +
		strings.Repeat("    \"p\": \"1.0.0\",\n", 13) + "    \"react\": \"19.3.1\",\n    \"react-dom\": \"19.3.1\",\n    \"remotion\": \"4.0.545\"\n  }\n}\n"
	file := ChangedFile{Filename: "video/one-interface/package.json", Patch: patch, Content: content}
	if verdict := ClassifyMechanical([]ChangedFile{file}); !verdict.Mechanical {
		t.Fatalf("refused: %s", verdict.Summary())
	}
	file.Content = ""
	if verdict := ClassifyMechanical([]ChangedFile{file}); verdict.Mechanical {
		t.Fatal("an unplaceable hunk must be refused when the head content is unavailable")
	}
}

// datatug/querydock-run#39: an `overrides:` entry whose key carries a version.
func TestWorkspaceOverrideWithAVersionedKeyIsAPin(t *testing.T) {
	t.Parallel()
	patch := "@@ -8,4 +8,4 @@ minimumReleaseAgeExclude:\n   - '@astrojs/markdown-satteri@0.4.7'\n   - astro@7.4.7\n overrides:\n-  undici@7.29.1: 8.12.4\n+  undici@7.29.1: 8.13.0"
	if reason := nonMechanicalWorkspace("pnpm-workspace.yaml", patch, ""); reason != "" {
		t.Fatalf("refused: %s", reason)
	}
}
