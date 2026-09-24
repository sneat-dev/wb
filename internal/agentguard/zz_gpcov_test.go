package agentguard

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// This file adds behavior-asserting coverage for the guard's smaller decision
// helpers. Every test drives a real input and checks the decision, the parsed
// words, the resolved path, or the file the guard wrote (or deliberately did
// not write). Helpers are prefixed gpCov to stay clear of the package's
// existing test helpers.

// gpCovWords returns the first segment's command words.
func gpCovWords(t *testing.T, command string) []string {
	t.Helper()
	segments := splitSegments(command)
	if len(segments) == 0 {
		t.Fatalf("splitSegments(%q) produced no segments", command)
	}
	return segments[0].Words
}

func gpCovTargets(t *testing.T, command string) []string {
	t.Helper()
	segments := splitSegments(command)
	if len(segments) == 0 {
		t.Fatalf("splitSegments(%q) produced no segments", command)
	}
	return segments[0].RedirectTargets
}

func gpCovEqualStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

// TestGpCovShellReaderQuotingAndEscapes pins how the reader treats escapes,
// quoting, redirection targets and here-strings, because every recogniser
// above it sees the command only through these words.
func TestGpCovShellReaderQuotingAndEscapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		command     string
		wantWords   []string
		wantTargets []string
	}{
		{"backslash escape inside a word", `echo a\ b`, []string{"echo", "a b"}, nil},
		{"line continuation joins words", "echo a\\\nb", []string{"echo", "ab"}, nil},
		{"trailing backslash is dropped", `echo a\`, []string{"echo", "a"}, nil},
		{"escaped quote in a double-quoted word", `echo "a\"b"`, []string{"echo", `a"b`}, nil},
		{"single-quoted redirect target", `echo > 'out file'`, []string{"echo"}, []string{"out file"}},
		{"double-quoted redirect target", `echo > "out file"`, []string{"echo"}, []string{"out file"}},
		{"escaped space in a redirect target", `echo > out\ file`, []string{"echo"}, []string{"out file"}},
		{"unterminated single-quoted target", `echo > 'out`, []string{"echo"}, []string{"out"}},
		{"unterminated double-quoted target", `echo > "out`, []string{"echo"}, []string{"out"}},
		{"here-string operand is data, not a command", `cat <<< "hello world"`, []string{"cat"}, nil},
		{"digit-prefixed word is not a file descriptor", `echo 2a>out`, []string{"echo", "2a"}, []string{"out"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := gpCovWords(t, testCase.command); !gpCovEqualStrings(got, testCase.wantWords) {
				t.Fatalf("splitSegments(%q) words = %q, want %q", testCase.command, got, testCase.wantWords)
			}
			gotTargets := gpCovTargets(t, testCase.command)
			if !gpCovEqualStrings(gotTargets, testCase.wantTargets) {
				t.Fatalf("splitSegments(%q) redirect targets = %q, want %q", testCase.command, gotTargets, testCase.wantTargets)
			}
		})
	}
}

// TestGpCovShellReaderHeredocBodies proves a heredoc body — including the
// `<<-` dash form — is skipped whole, so text inside it is never read as a
// command.
func TestGpCovShellReaderHeredocBodies(t *testing.T) {
	t.Parallel()
	command := "cat <<-EOF\nrm -rf everything\nEOF\necho done\n"
	segments := splitSegments(command)
	if len(segments) != 2 {
		t.Fatalf("splitSegments produced %d segments, want 2: %+v", len(segments), segments)
	}
	if !gpCovEqualStrings(segments[0].Words, []string{"cat"}) {
		t.Fatalf("first segment words = %q, want [cat]", segments[0].Words)
	}
	if !gpCovEqualStrings(segments[1].Words, []string{"echo", "done"}) {
		t.Fatalf("second segment words = %q, want [echo done]", segments[1].Words)
	}
}

// TestGpCovIsAllDigitsRejectsNonDigits pins the file-descriptor test the
// redirect reader uses to drop a `2>` descriptor without treating `2a>` as one.
func TestGpCovIsAllDigitsRejectsNonDigits(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]bool{"": false, "2": true, "2a": false, "12": true} {
		if got := isAllDigits(input); got != want {
			t.Fatalf("isAllDigits(%q) = %v, want %v", input, got, want)
		}
	}
}

// TestGpCovLocationSlugAndUnresolvableProjectsRoot covers the two degenerate
// inputs Classify has to answer without guessing: a Location that names no
// repository, and a projects root it cannot compare against a checkout root.
func TestGpCovLocationSlugAndUnresolvableProjectsRoot(t *testing.T) {
	t.Parallel()
	if got := (Location{}).Slug(); got != "" {
		t.Fatalf("Location{}.Slug() = %q, want empty", got)
	}
	if got := (Location{Owner: "sneat-co"}).Slug(); got != "" {
		t.Fatalf("Location with an owner but no repository = %q, want empty", got)
	}
	if got := (Location{Repository: "wb"}).Slug(); got != "" {
		t.Fatalf("Location with a repository but no owner = %q, want empty", got)
	}
	if got := (Location{Owner: "sneat-co", Repository: "wb"}).Slug(); got != "sneat-co/wb" {
		t.Fatalf("Slug() = %q, want sneat-co/wb", got)
	}

	repositories := newFixture(t)
	// A relative projects root cannot be compared with an absolute checkout
	// root: the guard must answer "not managed" rather than refuse work.
	location := Classify("relative/projects", repositories.Canonical)
	if location.Kind != KindForeign {
		t.Fatalf("Classify with a relative projects root = %q, want %q", location.Kind, KindForeign)
	}
}

// TestGpCovAbsolutePathTildeExpansion pins ~ handling, including a host whose
// home directory cannot be resolved — which must make the answer unknown.
func TestGpCovAbsolutePathTildeExpansion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, ok := absolutePath("~/notes/plan.md")
	if !ok || got != filepath.Join(home, "notes", "plan.md") {
		t.Fatalf("absolutePath(~/notes/plan.md) = (%q, %v), want (%q, true)", got, ok, filepath.Join(home, "notes", "plan.md"))
	}
	if got, ok := absolutePath("~"); !ok || got != filepath.Clean(home) {
		t.Fatalf("absolutePath(~) = (%q, %v), want (%q, true)", got, ok, filepath.Clean(home))
	}

	t.Setenv("HOME", "")
	if got, ok := absolutePath("~/notes/plan.md"); ok {
		t.Fatalf("absolutePath with no home directory returned (%q, true), want (\"\", false)", got)
	}
}

// gpCovRepoLayout is a claimed repository with one valid worktree and a live
// local claim, reaching the live-claim policy through real files only.
type gpCovRepoLayout struct {
	ProjectsRoot string
	Worktrees    string
	Worktree     string
}

const gpCovOwner, gpCovRepository, gpCovTask = "acme", "widget", "task-1"

func gpCovClaimedRepository(t *testing.T) gpCovRepoLayout {
	t.Helper()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	worktree := filepath.Join(projectsRoot, gpCovOwner, gpCovRepository, ".worktrees", gpCovTask)
	worktrees := filepath.Dir(worktree)

	writeFile(t, filepath.Join(worktree, ".wb", "local", "manifest.yaml"),
		"repository: "+gpCovOwner+"/"+gpCovRepository+"\neffort_id: "+gpCovTask+"\nworktree: "+worktree+"\n")

	configHome := filepath.Join(root, "config")
	writeFile(t, filepath.Join(configHome, "wb", "wb.yaml"), "remote:\n  provider: git\n  repo: acme/wb-state\n")
	t.Setenv("XDG_CONFIG_HOME", configHome)

	writeFile(t, filepath.Join(projectsRoot, gpCovOwner, "wb-state", "claims", gpCovTask+".yaml"),
		"login: someone\nmachine: theirlaptop\n")

	return gpCovRepoLayout{ProjectsRoot: projectsRoot, Worktrees: worktrees, Worktree: worktree}
}

func gpCovClaimPrompt() string {
	return "Fix the bug in /srv/projects/" + gpCovOwner + "/" + gpCovRepository + " described above."
}

// TestGpCovLiveClaimBaselineAndFalsePositives proves the policy still refuses
// a real live claim, and that each malformed worktree entry is skipped rather
// than allowed to crash or to refuse on its own.
func TestGpCovLiveClaimBaselineAndFalsePositives(t *testing.T) {
	t.Run("a live claim is refused", func(t *testing.T) {
		layout := gpCovClaimedRepository(t)
		finding := inspectDispatchIntoLiveClaim(toolInput{Prompt: gpCovClaimPrompt()}, t.TempDir(), layout.ProjectsRoot)
		if finding == nil {
			t.Fatal("inspectDispatchIntoLiveClaim allowed a dispatch into a live claim")
		}
		for _, expected := range []string{gpCovTask, "someone/theirlaptop", layout.Worktree} {
			if !strings.Contains(finding.Message, expected) {
				t.Fatalf("refusal is missing %q:\n%s", expected, finding.Message)
			}
		}
	})

	t.Run("the same lane dispatching from inside the claim is allowed", func(t *testing.T) {
		layout := gpCovClaimedRepository(t)
		if finding := inspectDispatchIntoLiveClaim(toolInput{Prompt: gpCovClaimPrompt()}, layout.Worktree, layout.ProjectsRoot); finding != nil {
			t.Fatalf("refused a dispatch from inside its own worktree:\n%s", finding.Message)
		}
	})

	t.Run("a brief naming the claimed worktree path is allowed", func(t *testing.T) {
		layout := gpCovClaimedRepository(t)
		prompt := gpCovClaimPrompt() + " Work in " + layout.Worktree + "."
		if finding := inspectDispatchIntoLiveClaim(toolInput{Prompt: prompt}, t.TempDir(), layout.ProjectsRoot); finding != nil {
			t.Fatalf("refused a brief that names the claimed worktree:\n%s", finding.Message)
		}
	})

	t.Run("a released claim is not live", func(t *testing.T) {
		layout := gpCovClaimedRepository(t)
		if err := os.Remove(filepath.Join(layout.ProjectsRoot, gpCovOwner, "wb-state", "claims", gpCovTask+".yaml")); err != nil {
			t.Fatal(err)
		}
		if finding := inspectDispatchIntoLiveClaim(toolInput{Prompt: gpCovClaimPrompt()}, t.TempDir(), layout.ProjectsRoot); finding != nil {
			t.Fatalf("refused a released claim:\n%s", finding.Message)
		}
	})

	t.Run("an empty projects root refuses nothing", func(t *testing.T) {
		t.Parallel()
		if finding := inspectDispatchIntoLiveClaim(toolInput{Prompt: gpCovClaimPrompt()}, t.TempDir(), ""); finding != nil {
			t.Fatalf("refused with no projects root:\n%s", finding.Message)
		}
	})

	t.Run("no repository named in the prompt", func(t *testing.T) {
		layout := gpCovClaimedRepository(t)
		if finding := inspectDispatchIntoLiveClaim(toolInput{Prompt: "Investigate the failing test."}, t.TempDir(), layout.ProjectsRoot); finding != nil {
			t.Fatalf("refused a prompt naming no repository:\n%s", finding.Message)
		}
	})
}

// TestGpCovLiveClaimMalformedWorktreeEntries drives every "skip this entry"
// branch: a non-directory entry, a missing manifest, an unparsable manifest,
// a repository mismatch, a missing effort ID, and a relative worktree path.
// Each layout holds a claim file, so skipping the malformed entry is the only
// reason the call is allowed.
func TestGpCovLiveClaimMalformedWorktreeEntries(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, layout gpCovRepoLayout)
	}{
		{"a plain file in .worktrees is not a worktree", func(t *testing.T, layout gpCovRepoLayout) {
			writeFile(t, filepath.Join(layout.Worktrees, "notes.txt"), "not a worktree\n")
		}},
		{"a worktree directory with no manifest", func(t *testing.T, layout gpCovRepoLayout) {
			if err := os.MkdirAll(filepath.Join(layout.Worktrees, "unmanaged"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"a manifest that is not valid YAML", func(t *testing.T, layout gpCovRepoLayout) {
			writeFile(t, filepath.Join(layout.Worktrees, "bad", ".wb", "local", "manifest.yaml"), "{not: [valid\n")
		}},
		{"a manifest for a different repository", func(t *testing.T, layout gpCovRepoLayout) {
			writeFile(t, filepath.Join(layout.Worktrees, "other", ".wb", "local", "manifest.yaml"),
				"repository: acme/other\neffort_id: "+gpCovTask+"\nworktree: "+layout.Worktree+"\n")
		}},
		{"a manifest with no effort ID", func(t *testing.T, layout gpCovRepoLayout) {
			writeFile(t, filepath.Join(layout.Worktrees, "noid", ".wb", "local", "manifest.yaml"),
				"repository: "+gpCovOwner+"/"+gpCovRepository+"\nworktree: "+layout.Worktree+"\n")
		}},
		{"a manifest whose worktree path is relative", func(t *testing.T, layout gpCovRepoLayout) {
			writeFile(t, filepath.Join(layout.Worktrees, "relative", ".wb", "local", "manifest.yaml"),
				"repository: "+gpCovOwner+"/"+gpCovRepository+"\neffort_id: "+gpCovTask+"\nworktree: some/relative/path\n")
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			layout := gpCovClaimedRepository(t)
			// Remove the valid entry so only the malformed one remains: if the
			// malformed entry were misread as a claim, the call would be refused.
			if err := os.RemoveAll(layout.Worktree); err != nil {
				t.Fatal(err)
			}
			testCase.build(t, layout)
			if finding := inspectDispatchIntoLiveClaim(toolInput{Prompt: gpCovClaimPrompt()}, t.TempDir(), layout.ProjectsRoot); finding != nil {
				t.Fatalf("a malformed worktree entry produced a refusal:\n%s", finding.Message)
			}
		})
	}
}

// TestGpCovCandidateRepositories pins both prompt shapes the policy reads,
// including the duplicate and `wb/...` tokens that must not become candidates.
func TestGpCovCandidateRepositories(t *testing.T) {
	t.Parallel()
	// The same repository named twice must not produce two candidates.
	repeated := "Touch /srv/projects/acme/widget and again /srv/projects/acme/widget here"
	if got := candidateRepositories(repeated); len(got) != 1 || got[0] != [2]string{"acme", "widget"} {
		t.Fatalf("candidateRepositories(%q) = %v, want one acme/widget", repeated, got)
	}

	beside := "Run: wb worktree create release wb/tooling acme/widget"
	got := candidateRepositories(beside)
	if len(got) != 1 || got[0] != [2]string{"acme", "widget"} {
		t.Fatalf("candidateRepositories(%q) = %v, want only acme/widget (the wb/ token is the tool itself)", beside, got)
	}

	if got := candidateRepositories("Nothing to see here."); len(got) != 0 {
		t.Fatalf("candidateRepositories with no repository = %v, want none", got)
	}
}

// TestGpCovWithinDirectory pins the containment test, including the two paths
// that cannot be compared at all.
func TestGpCovWithinDirectory(t *testing.T) {
	t.Parallel()
	cases := []struct {
		candidate string
		directory string
		want      bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b/c", "/a/b", true},
		{"/a/b", "/a/b/c", false},
		{"relative", "/absolute", false},
	}
	for _, testCase := range cases {
		if got := withinDirectory(testCase.candidate, testCase.directory); got != testCase.want {
			t.Fatalf("withinDirectory(%q, %q) = %v, want %v", testCase.candidate, testCase.directory, got, testCase.want)
		}
	}
}

// TestGpCovWBStateRepositoryResolution walks every shape of the wb.yaml
// remote-state config the live-claim policy reads, asserting which one counts
// as a readable git mirror and which resolves to "unknown" (not live).
func TestGpCovWBStateRepositoryResolution(t *testing.T) {
	stub := t.TempDir()
	claimsRoot := filepath.Join(stub, "projects")

	writeConfig := func(t *testing.T, content string) {
		t.Helper()
		writeFile(t, filepath.Join(stub, "config", "wb", "wb.yaml"), content)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(stub, "config"))
	}

	t.Run("a missing config resolves to unknown", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(stub, "absent"))
		if _, _, ok := wbStateRepository(); ok {
			t.Fatal("wbStateRepository accepted a missing config")
		}
		if claimOwner(claimsRoot, "t") != "" {
			t.Fatal("claimOwner named an owner with no readable config")
		}
		if claimLive(claimsRoot, "t") {
			t.Fatal("claimLive reported a claim live with no readable config")
		}
	})

	t.Run("config that is not valid YAML resolves to unknown", func(t *testing.T) {
		writeConfig(t, "{not: [valid\n")
		if _, _, ok := wbStateRepository(); ok {
			t.Fatal("wbStateRepository accepted an unparsable config")
		}
	})

	t.Run("a non-git provider has no local mirror", func(t *testing.T) {
		writeConfig(t, "remote:\n  provider: hub\n  url: https://example.test\n")
		if _, _, ok := wbStateRepository(); ok {
			t.Fatal("wbStateRepository accepted a hub provider")
		}
	})

	t.Run("a repo without an owner/name slash is rejected", func(t *testing.T) {
		writeConfig(t, "remote:\n  provider: git\n  repo: justaname\n")
		if _, _, ok := wbStateRepository(); ok {
			t.Fatal("wbStateRepository accepted a repo with no owner")
		}
	})

	t.Run("a valid git remote names the mirror", func(t *testing.T) {
		writeConfig(t, "remote:\n  provider: git\n  repo: acme/wb-state\n")
		owner, name, ok := wbStateRepository()
		if !ok || owner != "acme" || name != "wb-state" {
			t.Fatalf("wbStateRepository = (%q, %q, %v), want (acme, wb-state, true)", owner, name, ok)
		}

		if claimLive(claimsRoot, "t") {
			t.Fatal("claimLive reported a live claim with no claim file")
		}
		if claimOwner(claimsRoot, "t") != "" {
			t.Fatal("claimOwner named an owner with no claim file")
		}

		claimFile := filepath.Join(claimsRoot, owner, name, "claims", "t.yaml")
		writeFile(t, claimFile, "login: someone\nmachine: theirlaptop\n")
		if !claimLive(claimsRoot, "t") {
			t.Fatal("claimLive did not see the claim file")
		}
		if got := claimOwner(claimsRoot, "t"); got != "someone/theirlaptop" {
			t.Fatalf("claimOwner = %q, want someone/theirlaptop", got)
		}

		writeFile(t, claimFile, "login: someone\n")
		if got := claimOwner(claimsRoot, "t"); got != "" {
			t.Fatalf("claimOwner with a machine-less claim = %q, want empty", got)
		}

		writeFile(t, claimFile, "{not: [valid\n")
		if got := claimOwner(claimsRoot, "t"); got != "" {
			t.Fatalf("claimOwner with an unparsable claim = %q, want empty", got)
		}

		// A directory where the claim file should be is not a claim.
		if err := os.Remove(claimFile); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(claimFile, 0o755); err != nil {
			t.Fatal(err)
		}
		if claimLive(claimsRoot, "t") {
			t.Fatal("claimLive treated a directory as a live claim")
		}
	})

	t.Run("no config location at all resolves to unknown", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("HOME", "")
		if _, _, ok := wbStateRepository(); ok {
			t.Fatal("wbStateRepository accepted an unresolvable config home")
		}
	})
}

// TestGpCovUnresolvableHomePaths pins the HOME fallback both config-path
// resolvers share, including the host where no home directory exists.
func TestGpCovUnresolvableHomePaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".config", "wb", "wb.yaml")
	if got := wbConfigPath(); got != want {
		t.Fatalf("wbConfigPath() = %q, want %q", got, want)
	}
	if got := globalHooksConfigPath(); got != want {
		t.Fatalf("globalHooksConfigPath() = %q, want %q", got, want)
	}

	t.Setenv("HOME", "")
	if got := wbConfigPath(); got != "" {
		t.Fatalf("wbConfigPath() with no home = %q, want empty", got)
	}
	if got := globalHooksConfigPath(); got != "" {
		t.Fatalf("globalHooksConfigPath() with no home = %q, want empty", got)
	}
}

// TestGpCovReadAutoTagsFlags pins the two flag readers, including the
// distinguish-between "absent", "false" and "true" answers that decide whether
// the guard refuses a hand tag.
func TestGpCovReadAutoTagsFlags(t *testing.T) {
	t.Parallel()
	if value, ok := readAutoTagsFlag(""); ok || value {
		t.Fatal("readAutoTagsFlag(\"\") reported a value")
	}
	if value, ok := readGlobalAutoTagsFlag(""); ok || value {
		t.Fatal("readGlobalAutoTagsFlag(\"\") reported a value")
	}

	cases := []struct {
		name    string
		content string
		want    *bool
	}{
		{"absent file", "", nil},
		{"unparsable file", "{not: [valid\n", nil},
		{"autoTags absent", "agent: {}\n", nil},
		{"autoTags true", "agent:\n  autoTags: true\n", boolPointer(true)},
		{"autoTags false", "agent:\n  autoTags: false\n", boolPointer(false)},
	}
	for _, testCase := range cases {
		t.Run("repository config: "+testCase.name, func(t *testing.T) {
			t.Parallel()
			// Each subtest gets its own directory and config path: all five
			// run in parallel with each other, and a shared path written and
			// removed from concurrently is a logic race -race cannot see
			// (it is file I/O, not a memory access).
			repoConfig := filepath.Join(t.TempDir(), "hooks.yaml")
			if testCase.content != "" {
				writeFile(t, repoConfig, testCase.content)
			}
			value, ok := readAutoTagsFlag(repoConfig)
			gpCovAssertOptionalBool(t, "readAutoTagsFlag", value, ok, testCase.want)
		})
	}

	globalCases := []struct {
		name    string
		content string
		want    *bool
	}{
		{"unparsable file", "{not: [valid\n", nil},
		{"autoTags absent", "git_hooks: {}\n", nil},
		{"autoTags true", "git_hooks:\n  agent:\n    autoTags: true\n", boolPointer(true)},
		{"autoTags false", "git_hooks:\n  agent:\n    autoTags: false\n", boolPointer(false)},
	}
	for _, testCase := range globalCases {
		t.Run("global policy: "+testCase.name, func(t *testing.T) {
			t.Parallel()
			// Each subtest gets its own directory and config path for the
			// same reason as the repository-config cases above.
			globalConfig := filepath.Join(t.TempDir(), "wb.yaml")
			writeFile(t, globalConfig, testCase.content)
			value, ok := readGlobalAutoTagsFlag(globalConfig)
			gpCovAssertOptionalBool(t, "readGlobalAutoTagsFlag", value, ok, testCase.want)
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func gpCovAssertOptionalBool(t *testing.T, name string, value, ok bool, want *bool) {
	t.Helper()
	if want == nil {
		if ok {
			t.Fatalf("%s reported (%v, true), want no value", name, value)
		}
		return
	}
	if !ok || value != *want {
		t.Fatalf("%s = (%v, %v), want (%v, true)", name, value, ok, *want)
	}
}

// TestGpCovAutoTaggingSignals walks every signal the auto-tagging policy
// reads: the repository config (both values), the global policy, the workflow
// heuristic, and no signal at all.
func TestGpCovAutoTaggingSignals(t *testing.T) {
	t.Run("repository agent.autoTags: true wins", func(t *testing.T) {
		repo := newTagRepoFixture(t)
		repo.writeHooksConfig(t, true)
		reason, autoTagging := autoTaggingRepository(repo.Repo, repo.ProjectsRoot)
		if !autoTagging || !strings.Contains(reason, ".wb/hooks.yaml") {
			t.Fatalf("autoTaggingRepository = (%q, %v), want the repository config signal", reason, autoTagging)
		}
	})

	t.Run("an explicit repository false overrides the global policy", func(t *testing.T) {
		repo := newTagRepoFixture(t)
		repo.writeHooksConfig(t, false)
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wb", "wb.yaml"),
			"git_hooks:\n  agent:\n    autoTags: true\n")
		if reason, autoTagging := autoTaggingRepository(repo.Repo, repo.ProjectsRoot); autoTagging {
			t.Fatalf("repository autoTags:false was overridden by the global policy: %q", reason)
		}
	})

	t.Run("the global policy is read when the repository declares nothing", func(t *testing.T) {
		repo := newTagRepoFixture(t)
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wb", "wb.yaml"),
			"git_hooks:\n  agent:\n    autoTags: true\n")
		reason, autoTagging := autoTaggingRepository(repo.Repo, repo.ProjectsRoot)
		if !autoTagging || !strings.Contains(reason, "global hooks policy") {
			t.Fatalf("autoTaggingRepository = (%q, %v), want the global hooks policy signal", reason, autoTagging)
		}
	})

	t.Run("a global policy with no autoTags falls through to the workflow", func(t *testing.T) {
		repo := newTagRepoFixture(t)
		writeFile(t, filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wb", "wb.yaml"), "git_hooks: {}\n")
		repo.writeWorkflow(t, "ci.yml", autoTaggingWorkflow)
		reason, autoTagging := autoTaggingRepository(repo.Repo, repo.ProjectsRoot)
		if !autoTagging || !strings.Contains(reason, "strongo/cicd") {
			t.Fatalf("autoTaggingRepository = (%q, %v), want the workflow signal", reason, autoTagging)
		}
	})

	t.Run("no signal at all", func(t *testing.T) {
		repo := newTagRepoFixture(t)
		if reason, autoTagging := autoTaggingRepository(repo.Repo, repo.ProjectsRoot); autoTagging {
			t.Fatalf("autoTaggingRepository invented a signal: %q", reason)
		}
	})
}

// TestGpCovWorkflowHeuristicSkipsUnreadableEntries proves the heuristic walks
// past directories, non-workflow files, and files it cannot read instead of
// stopping at the first one.
func TestGpCovWorkflowHeuristicSkipsUnreadableEntries(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	workflows := filepath.Join(repo, ".github", "workflows")
	if err := os.MkdirAll(filepath.Join(workflows, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(workflows, "readme.txt"), "not a workflow\n")
	if err := os.Symlink(filepath.Join(repo, "missing-target.yml"), filepath.Join(workflows, "broken.yml")); err != nil {
		t.Logf("symlinks unavailable, unreadable entry not exercised: %v", err)
	}
	writeFile(t, filepath.Join(workflows, "zz-ci.yml"), autoTaggingWorkflow)

	reason, hit := autoTaggingWorkflowHeuristic(repo)
	if !hit || !strings.Contains(reason, "zz-ci.yml") {
		t.Fatalf("autoTaggingWorkflowHeuristic = (%q, %v), want the real workflow named", reason, hit)
	}
}

// TestGpCovInspectGitTaggingFailsOpenOutsideManagedCheckouts pins the two
// locations the tagging policy declines to judge.
func TestGpCovInspectGitTaggingFailsOpenOutsideManagedCheckouts(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	if finding := inspectGitTagging("tag", []string{"v1.2.3"}, repositories.Foreign, repositories.ProjectsRoot); finding != nil {
		t.Fatalf("tag policy judged a foreign checkout:\n%s", finding.Message)
	}
	// A linked worktree has a root but no owner/repository coordinates, so
	// there is no `<owner>/<repository>` to read repository files against.
	if finding := inspectGitTagging("tag", []string{"v1.2.3"}, repositories.Worktree, repositories.ProjectsRoot); finding != nil {
		t.Fatalf("tag policy judged a linked worktree with no repository slug:\n%s", finding.Message)
	}
}

// TestGpCovLooksLikeTagCreation pins which invocations create or move a tag.
func TestGpCovLooksLikeTagCreation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		subcommand string
		arguments  []string
		want       bool
	}{
		{"tag", nil, true},
		{"tag", []string{"-n5"}, false},
		{"tag", []string{"--list"}, false},
		{"push", []string{"--tags"}, true},
		{"push", []string{"origin", "v1.2.3"}, true},
		{"push", []string{"origin", "main"}, false},
		{"push", nil, false},
		{"commit", []string{"-m", "x"}, false},
	}
	for _, testCase := range cases {
		if got := looksLikeTagCreation(testCase.subcommand, testCase.arguments); got != testCase.want {
			t.Fatalf("looksLikeTagCreation(%q, %q) = %v, want %v", testCase.subcommand, testCase.arguments, got, testCase.want)
		}
	}
}

// TestGpCovManagedWorktreeWalks pins the metadata-only manifest walk: an
// unresolvable directory, a directory deep enough to exhaust the bounded walk,
// and a real manifest.
func TestGpCovManagedWorktreeWalks(t *testing.T) {
	t.Parallel()
	if managedWorktree("relative/path") {
		t.Fatal("managedWorktree resolved a relative directory")
	}

	deep := t.TempDir()
	for depth := 0; depth < maxAncestorWalk+6; depth++ {
		deep = filepath.Join(deep, "level")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if managedWorktree(deep) {
		t.Fatal("managedWorktree found a manifest that does not exist")
	}

	withManifest := t.TempDir()
	writeFile(t, filepath.Join(withManifest, "nested", ".wb", "local", "manifest.yaml"), "version: 1\n")
	if !managedWorktree(filepath.Join(withManifest, "nested", "child")) {
		t.Fatal("managedWorktree missed an enclosing manifest")
	}
}

// TestGpCovGovernedValidationDecisions pins the validation classifier's
// edge shapes, including an npx with no nested command, a package manager
// command that names no script, and a package manager command that names an
// ordinary (non-validation) subcommand such as "install".
func TestGpCovGovernedValidationDecisions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		words []string
		want  bool
	}{
		{"npx with no nested command", []string{"npx", "-y"}, false},
		{"npx running a governed tool", []string{"npx", "go", "test", "./..."}, true},
		{"cargo clippy", []string{"cargo", "clippy"}, true},
		{"pnpm exec running a governed tool", []string{"pnpm", "exec", "go", "test"}, true},
		{"pnpm with no script", []string{"pnpm", "-y"}, false},
		{"npm running an unrecognized, non-validation subcommand", []string{"npm", "install"}, false},
		{"an ordinary program", []string{"echo", "hi"}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := isGovernedValidation(testCase.words[0], testCase.words); got != testCase.want {
				t.Fatalf("isGovernedValidation(%q) = %v, want %v", testCase.words, got, testCase.want)
			}
		})
	}
}

// TestGpCovHelpAndFlagHelpers pins the small flag classifiers the help-bypass
// logic is built from.
func TestGpCovHelpAndFlagHelpers(t *testing.T) {
	t.Parallel()
	if cobraStyleHelp(nil) {
		t.Fatal("cobraStyleHelp accepted an empty invocation")
	}
	if !cobraStyleHelp([]string{"feature", "change-status", "--help"}) {
		t.Fatal("cobraStyleHelp missed a trailing --help")
	}
	if got := firstNonFlag([]string{"-x", "--long", "build"}); !gpCovEqualStrings(got, []string{"build"}) {
		t.Fatalf("firstNonFlag = %q, want [build]", got)
	}
	if !isEnvironmentAssignment("WB_HOME=/tmp/x") {
		t.Fatal("isEnvironmentAssignment rejected a plain assignment")
	}
	if isEnvironmentAssignment("1WB=/tmp/x") {
		t.Fatal("isEnvironmentAssignment accepted a name starting with a digit")
	}
	if isEnvironmentAssignment("WB-HOME=/tmp/x") {
		t.Fatal("isEnvironmentAssignment accepted a name with a dash")
	}
}

// TestGpCovBashPathResolutionHelpers pins the path helpers that decide whether
// a redirect or formatter target lands in a canonical clone.
func TestGpCovBashPathResolutionHelpers(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)

	if got := applyChangeDirectory("/base", []string{"-P", repositories.Canonical}); got != repositories.Canonical {
		t.Fatalf("applyChangeDirectory skipped -P and resolved %q, want %q", got, repositories.Canonical)
	}
	if got := applyChangeDirectory("", []string{"relative"}); got != "" {
		t.Fatalf("applyChangeDirectory with no base = %q, want empty", got)
	}
	if _, ok := canonicalTarget("/base", "$HOME/x", repositories.ProjectsRoot); ok {
		t.Fatal("canonicalTarget expanded a shell variable")
	}
	if location, ok := canonicalTarget("/base", repositories.Canonical, repositories.ProjectsRoot); !ok || location.Kind != KindCanonical {
		t.Fatalf("canonicalTarget missed a canonical clone: (%+v, %v)", location, ok)
	}
}

// TestGpCovInPlaceEditorAndFormatterDecisions pins the "read, do not write"
// shapes: an in-place editor with no canonical target and a formatter with no
// rewrite flag must both be allowed.
func TestGpCovInPlaceEditorAndFormatterDecisions(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	foreign := repositories.Foreign

	if finding := inspectInPlaceEditor("sed", []string{"sed", "-i", "file.txt"}, foreign, repositories.ProjectsRoot); finding != nil {
		t.Fatalf("in-place editor with no canonical target was refused:\n%s", finding.Message)
	}
	if finding := inspectInPlaceEditor("sed", []string{"sed", "-i", repositories.Canonical}, foreign, repositories.ProjectsRoot); finding == nil {
		t.Fatal("in-place editor targeting a canonical clone was allowed")
	}

	if finding := inspectFormatter("gofmt", []string{"gofmt", "-l", "."}, repositories.Canonical, repositories.ProjectsRoot); finding != nil {
		t.Fatalf("read-only formatter was refused:\n%s", finding.Message)
	}
	finding := inspectFormatter("gofmt", []string{"gofmt", "-w"}, repositories.Canonical, repositories.ProjectsRoot)
	if finding == nil {
		t.Fatal("formatter rewriting the canonical working directory was allowed")
	}
	if !strings.Contains(finding.Detail, "rewriting files in the clone") {
		t.Fatalf("formatter refusal detail = %q, want the working-directory wording", finding.Detail)
	}
	if finding := inspectFormatter("gofmt", []string{"gofmt", "-w", repositories.Canonical}, foreign, repositories.ProjectsRoot); finding == nil {
		t.Fatal("formatter rewriting a named canonical file was allowed")
	}
}

// TestGpCovGeneratorExtraVerbCallback covers the generic write-verb callback
// inspectGenerator accepts: when a tool has no verb in the named set, the
// callback names the verb the refusal reports.
func TestGpCovGeneratorExtraVerbCallback(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	alwaysWrite := func([]string) bool { return true }
	finding := inspectGenerator("sometool", []string{"sometool", "publish"}, repositories.Canonical, repositories.ProjectsRoot, map[string]bool{}, alwaysWrite)
	if finding == nil {
		t.Fatal("inspectGenerator ignored a write-verb callback that fired")
	}
	if !strings.Contains(finding.Detail, "sometool publish") {
		t.Fatalf("generator refusal detail = %q, want the callback-named verb", finding.Detail)
	}
}

// TestGpCovGitGlobalsParsing pins how Git's own global options are consumed
// before a subcommand is read.
func TestGpCovGitGlobalsParsing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name              string
		arguments         []string
		workingDirectory  string
		wantSubcommand    string
		wantDirectory     string
		wantHooksOverride bool
	}{
		{"attached -c naming hooksPath", []string{"-ccore.hooksPath=/dev/null", "commit"}, "/base", "commit", "/base", true},
		{"attached -c naming something else", []string{"-cuser.name=alex", "commit"}, "/base", "commit", "/base", false},
		{"--work-tree moves the directory", []string{"--work-tree=/other", "status"}, "/base", "status", "/other", false},
		{"--work-tree that cannot resolve", []string{"--work-tree=relative", "status"}, "", "status", "", false},
		{"--git-dir pointing at a .git directory", []string{"--git-dir=/other/.git", "status"}, "/base", "status", "/other", false},
		{"--git-dir that is not named .git", []string{"--git-dir=/other/repo", "status"}, "/base", "status", "/base", false},
		{"a two-word global option", []string{"--exec-path", "/usr/lib/git-core", "status"}, "/base", "status", "/base", false},
		{"an unknown flag is skipped", []string{"--dry-run", "status"}, "/base", "status", "/base", false},
		{"an unresolved -C clears the directory", []string{"-C", "relative", "status"}, "", "status", "", false},
		{"only global options, no subcommand", []string{"-C"}, "/base", "", "/base", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			invocation := parseGitGlobals(testCase.arguments, testCase.workingDirectory)
			if invocation.Subcommand != testCase.wantSubcommand {
				t.Fatalf("Subcommand = %q, want %q", invocation.Subcommand, testCase.wantSubcommand)
			}
			if invocation.Directory != testCase.wantDirectory {
				t.Fatalf("Directory = %q, want %q", invocation.Directory, testCase.wantDirectory)
			}
			if invocation.HooksPathOverride != testCase.wantHooksOverride {
				t.Fatalf("HooksPathOverride = %v, want %v", invocation.HooksPathOverride, testCase.wantHooksOverride)
			}
		})
	}
}

// TestGpCovGitDecisionHelpers pins the small decisions inspectGit makes.
func TestGpCovGitDecisionHelpers(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)

	if finding := inspectGit([]string{"-C"}, repositories.Canonical, repositories.ProjectsRoot); finding != nil {
		t.Fatalf("an invocation with no subcommand was refused:\n%s", finding.Message)
	}
	if _, ok := managedGitLocation("", repositories.ProjectsRoot); ok {
		t.Fatal("managedGitLocation accepted an empty directory")
	}
	if configNamesHooksPath([]string{"-l"}) {
		t.Fatal("configNamesHooksPath treated a bare flag as a hooksPath name")
	}
	if !configNamesHooksPath([]string{"-l", "core.hooksPath"}) {
		t.Fatal("configNamesHooksPath missed core.hooksPath after a flag")
	}

	message := hookBypassRefusal(Location{}, gitInvocation{Subcommand: "commit"})
	if !strings.Contains(message, "this checkout") {
		t.Fatalf("hookBypassRefusal with no root = %q, want the generic wording", message)
	}

	switchCases := []struct {
		subcommand string
		arguments  []string
		want       bool
	}{
		{"switch", []string{"-c", "feature"}, true},
		{"switch", []string{"main"}, false},
		{"bisect", []string{"start"}, true},
		{"bisect", []string{"log"}, false},
		{"bisect", nil, false},
	}
	for _, testCase := range switchCases {
		if got := gitSubcommandWrites(testCase.subcommand, testCase.arguments); got != testCase.want {
			t.Fatalf("gitSubcommandWrites(%q, %q) = %v, want %v", testCase.subcommand, testCase.arguments, got, testCase.want)
		}
	}
}

// TestGpCovGhApiMergeParsing pins how `gh api` merge calls are recognised
// without a network call.
func TestGpCovGhApiMergeParsing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		words []string
		want  bool
	}{
		{"a PUT to the merge endpoint", []string{"gh", "api", "-X", "PUT", "/repos/o/r/pulls/1/merge"}, true},
		{"the endpoint after -- is data", []string{"gh", "api", "--", "/repos/o/r/pulls/1/merge"}, false},
		{"a value-taking flag eats its value", []string{"gh", "api", "--input", "payload.json", "graphql", "-f", "query=mergePullRequest"}, true},
		{"a graphql call with no mutation", []string{"gh", "api", "graphql", "-f", "query=viewer"}, false},
		{"a GET to an unrelated endpoint", []string{"gh", "api", "/repos/o/r/pulls/1"}, false},
		{"not an api call", []string{"gh", "pr", "view", "1"}, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := isGhAPIMerge(testCase.words); got != testCase.want {
				t.Fatalf("isGhAPIMerge(%q) = %v, want %v", testCase.words, got, testCase.want)
			}
		})
	}
}

// TestGpCovGhFlagClusters pins the short-flag cluster reader, including the
// unknown-letter case pflag rejects outright.
func TestGpCovGhFlagClusters(t *testing.T) {
	t.Parallel()
	cases := []struct {
		letters      string
		wantHelp     bool
		wantConsumes bool
	}{
		{"h", true, false},
		{"sh", true, false},
		{"Rt", false, false},
		{"t", false, true},
		{"x", false, false},
	}
	for _, testCase := range cases {
		help, consumes := ghShortFlagCluster(testCase.letters)
		if help != testCase.wantHelp || consumes != testCase.wantConsumes {
			t.Fatalf("ghShortFlagCluster(%q) = (%v, %v), want (%v, %v)", testCase.letters, help, consumes, testCase.wantHelp, testCase.wantConsumes)
		}
	}

	if ghRequestsHelp([]string{"gh", "pr", "merge", "-x", "1"}) {
		t.Fatal("ghRequestsHelp treated an unknown flag as a help request")
	}
	if !ghRequestsHelp([]string{"gh", "pr", "merge", "--help"}) {
		t.Fatal("ghRequestsHelp missed --help")
	}
}

// TestGpCovBraceExpansionLimit pins the bound that keeps a pathological brace
// list from expanding without end.
func TestGpCovBraceExpansionLimit(t *testing.T) {
	t.Parallel()
	got := braceExpansions("{a,b,c,d}", 1)
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("braceExpansions with limit 1 = %q, want [a]", got)
	}
	got = braceExpansions("{a,b}", 64)
	if !gpCovEqualStrings(got, []string{"a", "b"}) {
		t.Fatalf("braceExpansions = %q, want [a b]", got)
	}
	if got := braceExpansions("m{erge,}", 64); !gpCovEqualStrings(got, []string{"merge", "m"}) {
		t.Fatalf("braceExpansions(m{erge,}) = %q, want [merge m]", got)
	}
}

// TestGpCovGhOverrideRecordingIsBestEffort proves the override audit is
// fail-open: an allowed call stays allowed even when the record cannot be
// written, and the record is written when it can.
func TestGpCovGhOverrideRecordingIsBestEffort(t *testing.T) {
	merge := []string{"gh", "pr", "merge", "1"}

	t.Run("a record is written when the state home is writable", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if finding := inspectGh(merge, root, "operator asked"); finding != nil {
			t.Fatalf("override did not allow the call:\n%s", finding.Message)
		}
		home, err := wbhome.Root(root)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(filepath.Join(home, "agentguard", "gh-pr-merge-overrides.jsonl"))
		if err != nil {
			t.Fatalf("read override log: %v", err)
		}
		if !strings.Contains(string(raw), "operator asked") {
			t.Fatalf("override log = %q, want the recorded reason", raw)
		}
	})

	t.Run("an unresolvable projects root still allows the call", func(t *testing.T) {
		t.Setenv(wbhome.EnvOverride, "")
		t.Setenv("HOME", "")
		if finding := inspectGh(merge, "", "operator asked"); finding != nil {
			t.Fatalf("bookkeeping failure blocked an allowed call:\n%s", finding.Message)
		}
	})

	t.Run("a projects root that is a file still allows the call", func(t *testing.T) {
		t.Parallel()
		blocker := filepath.Join(t.TempDir(), "blocker")
		writeFile(t, blocker, "not a directory\n")
		if finding := inspectGh(merge, blocker, "operator asked"); finding != nil {
			t.Fatalf("an uncreatable audit directory blocked an allowed call:\n%s", finding.Message)
		}
	})

	t.Run("an unwritable audit file still allows the call", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, ".wb", "agentguard", "gh-pr-merge-overrides.jsonl"), 0o755); err != nil {
			t.Fatal(err)
		}
		if finding := inspectGh(merge, root, "operator asked"); finding != nil {
			t.Fatalf("an unwritable audit file blocked an allowed call:\n%s", finding.Message)
		}
	})
}

// TestGpCovShellDashCPayloadBoundaries pins where a shell's -c payload ends:
// an option terminator before -c, or nothing after it, means there is no
// payload to recurse into.
func TestGpCovShellDashCPayloadBoundaries(t *testing.T) {
	t.Parallel()
	readings := []shellOptionReading{bashOptionReading}
	cases := []struct {
		name  string
		words []string
		want  []string
	}{
		{"an option terminator before -c", []string{"bash", "--", "echo", "hi"}, nil},
		{"-c with no word after it", []string{"bash", "-c"}, nil},
		{"-c consumed as an option terminator's operand", []string{"bash", "-c", "--"}, nil},
		{"a script file before -c runs the script", []string{"bash", "script.sh", "-c", "gh pr merge 1"}, nil},
		{"a real -c payload", []string{"bash", "-c", "gh pr merge 1"}, []string{"gh pr merge 1"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := shellDashCPayloads(testCase.words, readings)
			if !gpCovEqualStrings(got, testCase.want) {
				t.Fatalf("shellDashCPayloads(%q) = %q, want %q", testCase.words, got, testCase.want)
			}
		})
	}
}

// TestGpCovEnclosingCheckoutWalkIsBounded pins the bound on the ancestor walk:
// a file deeper than the walk limit with no checkout anywhere above it must
// answer "unknown" rather than keep walking.
func TestGpCovEnclosingCheckoutWalkIsBounded(t *testing.T) {
	t.Parallel()
	deep := t.TempDir()
	for depth := 0; depth < maxAncestorWalk+6; depth++ {
		deep = filepath.Join(deep, "level")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	location := Classify(t.TempDir(), filepath.Join(deep, "note.md"))
	if location.Kind != KindUnknown {
		t.Fatalf("Classify below the walk limit = %q, want %q", location.Kind, KindUnknown)
	}
}

// TestGpCovRefusalWordingFallbacks pins the two fallbacks in the canonical
// refusal wording: a missing slug and a missing root.
func TestGpCovRefusalWordingFallbacks(t *testing.T) {
	t.Parallel()
	message := refusal(finding{Location: Location{Root: "/projects/acme/widget"}, Detail: "writing a file"})
	for _, expected := range []string{"<owner/repository>", "wb worktree create <task>", "/projects/acme/widget"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("refusal is missing %q:\n%s", expected, message)
		}
	}

	// A finding carrying GovernedCommand reaches refusal() whenever
	// inspectBashCall could not rewrite it (wb#645 review): deny-wins found a
	// real deny elsewhere on the line, or the command did not match the
	// narrow "simple command" shape. Either way it renders the pre-PR
	// governed-command wording, naming the command to submit through
	// `wb run --` directly.
	governed := refusal(finding{Detail: "go test ./...", GovernedCommand: []string{"go", "test", "./..."}})
	for _, expected := range []string{"wb run -- go test ./...", "durable ID"} {
		if !strings.Contains(governed, expected) {
			t.Fatalf("governed refusal is missing %q:\n%s", expected, governed)
		}
	}
}

// TestGpCovFileToolIgnoresUnresolvablePaths pins that a Write naming a path
// the guard cannot make absolute is allowed rather than guessed at.
func TestGpCovFileToolIgnoresUnresolvablePaths(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	call := ToolCall{
		HookEventName: "PreToolUse",
		ToolName:      "Write",
		ToolInput:     []byte(`{"file_path":"relative/notes.md"}`),
	}
	if decision := Inspect(call, Options{ProjectsRoot: repositories.ProjectsRoot}); decision.Deny {
		t.Fatalf("a relative Write path was refused:\n%s", decision.Reason)
	}
}

// gpCovFailingWriter fails every write, standing in for a closed stdout.
type gpCovFailingWriter struct{}

func (gpCovFailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("gpCov: write failed")
}

// TestGpCovWriteDecisionReportsWriterFailure pins that a deny whose JSON
// cannot be delivered is reported as an error rather than silently lost.
func TestGpCovWriteDecisionReportsWriterFailure(t *testing.T) {
	t.Parallel()
	written, err := WriteDecision(gpCovFailingWriter{}, Decision{Deny: true, Reason: "refused"}, nil)
	if err == nil {
		t.Fatal("WriteDecision hid a writer failure")
	}
	if written {
		t.Fatal("WriteDecision reported a write that failed")
	}
}
