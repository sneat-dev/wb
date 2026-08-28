package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"gopkg.in/yaml.v3"
)

type skillCommandCoverage struct {
	Version  int                 `json:"version"`
	Commands map[string][]string `json:"commands"`
}

type claudePluginManifest struct {
	Version string `json:"version"`
}

type codexPluginManifest struct {
	Version   string `json:"version"`
	Skills    string `json:"skills"`
	Interface struct {
		DefaultPrompt []string `json:"defaultPrompt"`
	} `json:"interface"`
}

type capabilityManifest struct {
	Schema        string       `json:"$schema"`
	SchemaVersion int          `json:"schema_version"`
	Binary        string       `json:"binary"`
	Capabilities  []capability `json:"capabilities"`
}

type capability struct {
	ID          string             `json:"id"`
	FeatureRefs []string           `json:"feature_refs"`
	Surfaces    capabilitySurfaces `json:"surfaces"`
	Notes       string             `json:"notes"`
}

type capabilitySurfaces struct {
	Runtime runtimeSurface `json:"runtime"`
	Help    helpSurface    `json:"help"`
	AISkill skillSurface   `json:"ai_skill"`
	Tests   testSurface    `json:"tests"`
}

type runtimeSurface struct {
	Status   string           `json:"status"`
	Commands []runtimeCommand `json:"commands"`
}

type runtimeCommand struct {
	Path  string   `json:"path"`
	Flags []string `json:"flags"`
}

type helpSurface struct {
	Status  string       `json:"status"`
	Anchors []helpAnchor `json:"anchors"`
}

type helpAnchor struct {
	Command  string   `json:"command"`
	Contains []string `json:"contains"`
}

type skillSurface struct {
	Status string          `json:"status"`
	Skills []skillEvidence `json:"skills"`
}

type skillEvidence struct {
	Path     string   `json:"path"`
	Marker   string   `json:"marker"`
	Examples []string `json:"examples"`
}

type testSurface struct {
	Status     string         `json:"status"`
	References []testEvidence `json:"references"`
}

type testEvidence struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

func TestAgentSkillsCoverPublicCommands(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	coveragePath := filepath.Join(repoRoot, "ai", "skills", "commands.json")
	coverageBytes, err := os.ReadFile(coveragePath)
	if err != nil {
		t.Fatal(err)
	}
	var coverage skillCommandCoverage
	if err := json.Unmarshal(coverageBytes, &coverage); err != nil {
		t.Fatalf("parse %s: %v", coveragePath, err)
	}
	if coverage.Version != 1 {
		t.Fatalf("commands.json version = %d, want 1", coverage.Version)
	}

	publicCommands := map[string]bool{}
	for _, command := range newRootCmd().Commands() {
		switch command.Name() {
		case "completion", "help":
			continue
		}
		if command.Hidden {
			continue
		}
		publicCommands[command.Name()] = true
	}

	for command := range publicCommands {
		skills := coverage.Commands[command]
		if len(skills) == 0 {
			t.Errorf("public command %q has no Agent Skill coverage", command)
		}
		for _, skill := range skills {
			assertSkillFiles(t, repoRoot, skill)
		}
	}
	for command := range coverage.Commands {
		if !publicCommands[command] {
			t.Errorf("commands.json covers unknown or non-public command %q", command)
		}
	}

	assertClaudeManifestListsAllSkills(t, repoRoot)
	assertCodexManifestExposesSkills(t, repoRoot)
}

func TestWBChangeCompletionContractIsSharedAcrossHarnesses(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	contractPath := filepath.Join(repoRoot, "ai", "skills", "wb-change", "references", "completion.md")
	contract, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"`implemented`", "`published`", "`landed`", "`blocked`", "Do not infer permission"} {
		if !strings.Contains(string(contract), required) {
			t.Errorf("completion contract missing %q", required)
		}
	}

	skill, err := os.ReadFile(filepath.Join(repoRoot, "ai", "skills", "wb-change", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(skill), "[completion.md](references/completion.md)") {
		t.Fatal("wb-change skill does not route agents to the completion contract")
	}
	ownership, err := os.ReadFile(filepath.Join(repoRoot, "ai", "skills", "wb-worktrees", "references", "ownership.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"append-only", "`active`", "`orphaned`", "`unknown`", "wb worktree list --only active", "wb worktree list --only orphaned"} {
		if !strings.Contains(string(ownership), required) {
			t.Errorf("ownership reference missing %q", required)
		}
	}
	worktreeSkill, err := os.ReadFile(filepath.Join(repoRoot, "ai", "skills", "wb-worktrees", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(worktreeSkill), "[ownership.md](references/ownership.md)") {
		t.Fatal("wb-worktrees skill does not route agents to the ownership reference")
	}

	codexContents, err := os.ReadFile(filepath.Join(repoRoot, ".codex-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var codex codexPluginManifest
	if err := json.Unmarshal(codexContents, &codex); err != nil {
		t.Fatalf("parse Codex plugin manifest: %v", err)
	}
	if codex.Skills != "./ai/skills/" || !strings.Contains(strings.Join(codex.Interface.DefaultPrompt, "\n"), "$wb-change") {
		t.Fatal("Codex must expose ai/skills and route implementation work to the shared wb-change skill")
	}

	aiReadme, err := os.ReadFile(filepath.Join(repoRoot, "ai", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(aiReadme), "Claude Code recursively auto-discovers the same skills") || !strings.Contains(string(aiReadme), "single definition of done") {
		t.Fatal("AI README must document Claude's shared skill discovery and the canonical completion contract")
	}
}

func TestWorktreeSkillExamplesCaptureMandatoryPrivatePrompt(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	skillsRoot := filepath.Join(repoRoot, "ai", "skills")
	err := filepath.WalkDir(skillsRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(contents), "\n")
		for index := 0; index < len(lines); index++ {
			example := strings.TrimSpace(lines[index])
			if !strings.HasPrefix(example, "wb ") {
				continue
			}
			for strings.HasSuffix(example, "\\") && index+1 < len(lines) {
				example = strings.TrimSpace(strings.TrimSuffix(example, "\\")) + " " + strings.TrimSpace(lines[index+1])
				index++
			}
			isLifecycleMutation := strings.Contains(example, "worktree create") ||
				(strings.Contains(example, "worktree rename") && strings.Contains(example, "--apply"))
			if !isLifecycleMutation {
				continue
			}
			if err := parseSkillCommandExample(example); err != nil {
				t.Errorf("%s has invalid lifecycle example %q: %v", path, example, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestWBMergeSkillIsOnePortableContract prevents a local harness prompt from
// regressing into the branch-prefix, background-watch, or cleanup debt that
// this merger role exists to remove. The assertion is deliberately semantic:
// it checks required safety statements and rejects executable anti-patterns,
// rather than snapshotting a particular prose layout.
func TestWBMergeSkillIsOnePortableContract(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	skillPath := filepath.Join(repoRoot, "ai", "skills", "wb-merge", "SKILL.md")
	contents, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	contract := string(contents)
	for _, required := range []string{
		"without a branch prefix",
		"WB-managed worktrees only",
		"Fetch and fast-forward the target from `origin`",
		"main`, a feature branch, or a task branch",
		"dedicated merger checkout must be clean",
		"Preserve both stated intents",
		"validate after each merge, then run the full target verification",
		"captures current `main`",
		"never waits for current target CI to turn green",
		"candidate may fix a red target",
		"exact model identifier when the runtime exposes",
		"literal `unknown` when it does not",
		"never infer or guess a model ID",
		"never omit `--model`",
		"candidate head contains the freshly fetched",
		"nonempty server-enforced strict",
		"If the target advances, rebase or reintegrate",
		"Merge-group observation is planned",
		"keep the PR unmerged",
		"post-merge target CI",
		"behavioral, design,",
		"or authority blockers",
		"one exclusive logical merger lane per",
		"`(repository, target branch)`",
		"independent of the calling session",
		"submit manual handoffs",
		"another session resumes the same logical lane",
		"one agent may own",
		"different repository/target lanes may run",
		"durable merger submit, queue, claim, run,",
		"Push whichever ref was integrated immediately",
		"direct route, push the exact target",
		"For a PR route",
		"push the source branch",
		"merge through the PR with that exact-head guard",
		"fast-forward",
		"local target to `origin/<target>`",
		"target contains the exact merge SHA",
		"required release evidence on the exact remote target SHA",
		"wb ci wait --repo <owner/repo>",
		"enumerated required-check policy",
		"same-named PR summary or legacy status is insufficient",
		"does not prove that no optional",
		"workflow can register later",
		"wb worktree cleanup <task> --apply --remote --older-than 0",
		"Work Log",
		"TestCleanupAcceptsExactDirectPushIntegrationWithoutPullRequest",
		"mechanical integration-only",
		"must not author,\nchange, or repair implementation code, tests, specs, generated artifacts, or\nfixtures",
		"repairs to a distinct implementation agent",
		"mechanical\nmerge-conflict resolution",
		"introduces no new behavior",
		"Immediately after a PR\n   into `main` reports merged, and before any release/tag advancement,\n   installation evidence, cleanup, or other merge-cycle action",
		"resolve that repository's canonical checkout",
		"Require it to be checked out on local `main` with clean\n   staged and unstaged tracked state; reject ordinary untracked paths",
		"only\n   untracked exception is an exact registered nested linked-worktree root",
		"prove `origin/main` tracks no conflicting path there",
		"snapshot that\n   nested worktree's branch, `HEAD`, and status, and recheck every snapshot is\n   unchanged after canonical synchronization",
		"All other dirty or non-`main`\n   states fail closed",
		"git fetch origin",
		"git merge --ff-only origin/main",
		"PR's exact server\n   merge-result SHA",
		"canonical checkout is missing,\n   cannot fast-forward, or has a different exact SHA, fail closed",
		"Do not advance a\n   release or tag, collect installation evidence, terminalize cleanup, or\n   start the next batch before this gate passes",
		"Never reset, switch over\n   changes, stash, discard, or otherwise repair the canonical checkout",
		"Never use a distribution channel that the\n   owning product marks blocked or unverified",
		"exact\n   source-built artifact",
		"Otherwise report release evidence blocked and leave the task queued",
	} {
		if !strings.Contains(contract, required) {
			t.Errorf("WB merger contract is missing %q", required)
		}
	}
	for _, forbidden := range []string{"git worktree add", "sleep 1800", "sonnet", "opus", "haiku", "codex/", "claude/"} {
		if strings.Contains(strings.ToLower(contract), forbidden) {
			t.Errorf("WB merger contract permits a prefix/model/raw-worktree anti-pattern %q", forbidden)
		}
	}
	polling, err := os.ReadFile(filepath.Join(repoRoot, "ai", "skills", "wb-merge", "references", "ci-polling.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"foreground", "shorter than that harness's tool timeout", "bounded quiescence receipt", "same-named PR summary", "separate release evidence", "Never detach a watcher, use a background process"} {
		if !strings.Contains(string(polling), required) {
			t.Errorf("CI polling contract is missing %q", required)
		}
	}
	adapters, err := os.ReadFile(filepath.Join(repoRoot, "ai", "skills", "wb-merge", "references", "adapters.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Checked-in adapter files alone do not mean the merger is installed", "supersedes copied legacy merger prompts", "post-PR-merge canonical-checkout\nreconciliation gate", "before release/tag advancement,\ninstallation evidence, cleanup, or the next merge-cycle action", "verified registered nested-worktree\nexception", "must not replace it, including its verified registered nested-worktree\nexception, with a shortcut or repair a blocked canonical checkout", "must not\ncollect installation, upgrade, or runtime evidence through a channel the owning\nproduct marks blocked or unverified", "exact source-built artifact\nonly where that product explicitly permits one", "product-owned policy,\nnot WB production-code configuration"} {
		if !strings.Contains(string(adapters), required) {
			t.Errorf("merger adapter contract is missing %q", required)
		}
	}

	claude, err := os.ReadFile(filepath.Join(repoRoot, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var claudeFields map[string]any
	if err := json.Unmarshal(claude, &claudeFields); err != nil {
		t.Fatal(err)
	}
	for _, autoDiscovered := range []string{"skills", "agents"} {
		if _, explicit := claudeFields[autoDiscovered]; explicit {
			t.Errorf("Claude manifest explicitly lists %s and duplicates recursive auto-discovery", autoDiscovered)
		}
	}
	claudeAgent, err := os.ReadFile(filepath.Join(repoRoot, "agents", "wb-merger.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"name: wb-merger", "description:", "Load and follow `$wb-merge`", "mechanical integration-only", "must not author or repair implementation code", "generated artifacts", "gate failures", "distinct implementation agent", "keep that branch queued", "behavioral-free mechanical merge conflicts", "After a PR into `main` merges", "canonical checkout reconciliation\ngate", "before any release/tag, installation, cleanup, or next\nmerge-cycle action", "verified registered\nnested-worktree exception", "blocked canonical checkout for its owner\nrather than repairing it"} {
		if !strings.Contains(string(claudeAgent), required) {
			t.Errorf("Claude merger agent is missing %q", required)
		}
	}
	for _, forbidden := range []string{"model:", "background:", "isolation:", "skills:"} {
		if strings.Contains(string(claudeAgent), forbidden) {
			t.Errorf("Claude merger agent must leave %s to the canonical foreground WB contract", strings.TrimSuffix(forbidden, ":"))
		}
	}
	codex, err := os.ReadFile(filepath.Join(repoRoot, ".codex-plugin", "plugin.json"))
	if err != nil || !strings.Contains(string(codex), "$wb-merge") || strings.Contains(string(codex), "reconcile the canonical checkout after every PR merge into main") {
		t.Fatalf("Codex adapter must route to, not duplicate, the canonical wb-merge contract: %v", err)
	}
	copilot, err := os.ReadFile(filepath.Join(repoRoot, ".github", "agents", "wb-merger.agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"description:", "Read and follow `ai/skills/wb-merge/SKILL.md`", "does not define a second workflow", "mechanical integration-only", "must not author or repair", "generated artifacts", "gate\nfailures", "distinct implementation agent", "Keep the branch queued", "behavioral-free mechanical\nmerge conflicts", "After a PR into `main` merges", "canonical checkout reconciliation\ngate", "before any release/tag, installation, cleanup, or\nnext merge-cycle action", "verified registered\nnested-worktree exception", "blocked canonical checkout to its owner\nwithout repairing it", "distribution channel the owning product\nmarks blocked or unverified", "exact source-built artifact only where that\nproduct explicitly permits it", "report release evidence blocked"} {
		if !strings.Contains(string(copilot), required) {
			t.Errorf("Copilot adapter is missing %q", required)
		}
	}
	if strings.Contains(string(copilot), "model:") {
		t.Error("Copilot adapter must leave model selection to its harness")
	}
	openAI, err := os.ReadFile(filepath.Join(repoRoot, "ai", "skills", "wb-merge", "agents", "openai.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"mechanical integration-only", "distinct implementation agent", "keep its branch queued", "behavioral-free mechanical merge conflicts", "After each PR into main merges", "canonical-checkout reconciliation gate, including only its verified registered nested-worktree exception, before release/tag, installation, cleanup, or the next merge-cycle action", "never use a channel the owning product marks blocked or unverified", "exact source-built artifact only where that product explicitly permits it", "report release evidence blocked and leave the task queued"} {
		if !strings.Contains(string(openAI), required) {
			t.Errorf("OpenAI merger adapter is missing %q", required)
		}
	}
}

func TestGoCIReportsRequiredCheckForEveryPullRequestAndMainPush(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "go-ci.yml")
	contents, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	var workflow map[string]any
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatalf("parse %s: %v", workflowPath, err)
	}
	triggers, ok := workflow["on"].(map[string]any)
	if !ok {
		t.Fatalf("%s has no mapping-valued on trigger", workflowPath)
	}
	for _, trigger := range []string{"pull_request", "push"} {
		configuration, ok := triggers[trigger]
		if !ok {
			t.Fatalf("%s does not run for %s", workflowPath, trigger)
		}
		if configuration, ok := configuration.(map[string]any); ok {
			if _, filtered := configuration["paths"]; filtered {
				t.Fatalf("%s filters %s paths, so its required check can remain missing", workflowPath, trigger)
			}
			if _, filtered := configuration["paths-ignore"]; filtered {
				t.Fatalf("%s ignores %s paths, so its required check can remain missing", workflowPath, trigger)
			}
		}
	}
	tidyIndex := strings.Index(string(contents), "run: go mod tidy -diff")
	formatIndex := strings.Index(string(contents), "- name: gofmt")
	if tidyIndex < 0 {
		t.Fatalf("%s does not fail when go.mod or go.sum needs go mod tidy", workflowPath)
	}
	if formatIndex < 0 || tidyIndex > formatIndex {
		t.Fatalf("%s must run go mod tidy -diff before formatting, build, and test gates", workflowPath)
	}
	if strings.Contains(string(contents), "run: go mod tidy\n") {
		t.Fatalf("%s mutates release source with bare go mod tidy instead of checking its diff", workflowPath)
	}
	releasePath := filepath.Join(repoRoot, ".goreleaser.yml")
	releaseContents, err := os.ReadFile(releasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(releaseContents), "- go mod tidy -diff") {
		t.Fatalf("%s does not fail closed on dependency metadata drift", releasePath)
	}
	if strings.Contains(string(releaseContents), "- go mod tidy\n") {
		t.Fatalf("%s mutates source before stamping release artifacts", releasePath)
	}
}

func TestReleaseWorkflowFallsBackForCLIChangingNonConventionalMerge(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	workflowPath := filepath.Join(repoRoot, ".github", "workflows", "release.yml")
	contents, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "default_bump: patch") {
		t.Fatalf("%s must publish at least a patch release when a CLI-changing main push has no conventional title", workflowPath)
	}
}

// TestCapabilityManifestKeepsImplementationHelpAndSkillsInOne Checked-in
// contract. It deliberately validates command/flag parsing without executing
// examples, so a documentation regression cannot mutate a repository.
func TestCapabilityManifestKeepsImplementationHelpAndSkillsInOne(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	schemaPath := filepath.Join(repoRoot, "ai", "cli-capability-delivery.schema.json")
	schemaContents, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	// This digest pins the immutable SpecScore schema copied from commit
	// e06f6ab. Updating it is an explicit upstream-contract migration, never a
	// local weakening of the WB validator.
	const schemaSHA256 = "d573758c9bf41c197fce3f69af7082bf02b5926b3160bd05958c1516d95232a2"
	if got := fmt.Sprintf("%x", sha256.Sum256(schemaContents)); got != schemaSHA256 {
		t.Fatalf("%s digest = %s, want immutable SpecScore contract %s", schemaPath, got, schemaSHA256)
	}
	contents, err := os.ReadFile(filepath.Join(repoRoot, "ai", "capabilities.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schemaDocument any
	if err := json.Unmarshal(schemaContents, &schemaDocument); err != nil {
		t.Fatalf("decode capability schema: %v", err)
	}
	var manifestDocument any
	if err := json.Unmarshal(contents, &manifestDocument); err != nil {
		t.Fatalf("decode capability manifest: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	const schemaURL = "https://specscore.md/new/cli-capability-delivery.schema.json"
	if err := compiler.AddResource(schemaURL, schemaDocument); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		t.Fatalf("compile exact SpecScore capability schema: %v", err)
	}
	if err := compiled.Validate(manifestDocument); err != nil {
		t.Fatalf("ai/capabilities.json violates exact SpecScore schema: %v", err)
	}
	var manifest capabilityManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != schemaURL || manifest.SchemaVersion != 1 || manifest.Binary != "wb" {
		t.Fatalf("capability manifest identity = %#v", manifest)
	}
	seen := map[string]bool{}
	capabilitiesByID := map[string]capability{}
	previous := ""
	for _, capability := range manifest.Capabilities {
		if !strings.HasPrefix(capability.ID, manifest.Binary+".") || seen[capability.ID] || (previous != "" && capability.ID <= previous) {
			t.Fatalf("capability IDs must be wb-prefixed, unique, and sorted: previous=%q current=%q", previous, capability.ID)
		}
		seen[capability.ID] = true
		capabilitiesByID[capability.ID] = capability
		previous = capability.ID
		for _, featureRef := range capability.FeatureRefs {
			assertRepoEvidencePath(t, repoRoot, capability.ID, featureRef)
		}
		for _, runtime := range capability.Surfaces.Runtime.Commands {
			assertRuntimeCommand(t, capability.ID, runtime)
		}
		for _, anchor := range capability.Surfaces.Help.Anchors {
			assertHelpAnchor(t, capability.ID, anchor)
		}
		for _, skill := range capability.Surfaces.AISkill.Skills {
			assertCapabilitySkill(t, repoRoot, capability.ID, skill)
		}
		for _, reference := range capability.Surfaces.Tests.References {
			assertCapabilityTest(t, repoRoot, capability.ID, reference)
		}
		if capability.Surfaces.Runtime.Status == "Planned" || capability.Surfaces.Runtime.Status == "Absent" {
			for surface, status := range map[string]string{"help": capability.Surfaces.Help.Status, "ai_skill": capability.Surfaces.AISkill.Status, "tests": capability.Surfaces.Tests.Status} {
				if status != capability.Surfaces.Runtime.Status {
					t.Fatalf("%s runtime is %s but %s is %s", capability.ID, capability.Surfaces.Runtime.Status, surface, status)
				}
			}
		}
	}
	mergerCapability, ok := capabilitiesByID["wb.coordination.portable-merger"]
	if !ok || !strings.Contains(mergerCapability.Notes, "Immediately after every PR into main merges") || !strings.Contains(mergerCapability.Notes, "Tracked state must be clean; ordinary untracked paths are rejected, except exact registered nested-worktree roots with proved no target conflict and unchanged branch/HEAD/status") || !strings.Contains(mergerCapability.Notes, "Until then no release/tag, installation, cleanup, or next merge-cycle action") || !strings.Contains(mergerCapability.Notes, "never resets, switches over changes, stashes, discards, or repairs that checkout") {
		t.Fatal("portable merger capability must declare the fail-closed post-PR canonical-checkout reconciliation gate")
	}

	// The manifest is the one WB CLI view, not a hand-picked feature slice.
	// Every public leaf has its conventionally named capability row, and that
	// row must point back to the executable leaf itself. Cross-cutting rows may
	// additionally enumerate several commands (for example wb.root.filter).
	for _, commandPath := range publicLeafCommandPaths(newRootCmd()) {
		capabilityID := strings.ReplaceAll(commandPath, " ", ".")
		declared, ok := capabilitiesByID[capabilityID]
		if !ok {
			t.Errorf("public leaf %q has no complete capability row %q", commandPath, capabilityID)
			continue
		}
		var runtimeEvidence *runtimeCommand
		for _, runtime := range declared.Surfaces.Runtime.Commands {
			if runtime.Path == commandPath {
				runtimeCopy := runtime
				runtimeEvidence = &runtimeCopy
				break
			}
		}
		if runtimeEvidence == nil {
			t.Errorf("capability %q does not cite its public runtime leaf %q", capabilityID, commandPath)
			continue
		}
		command, _, err := newRootCmd().Find(strings.Fields(strings.TrimPrefix(commandPath, "wb ")))
		if err != nil {
			t.Fatalf("resolve public leaf %s: %v", commandPath, err)
		}
		gotFlags := append([]string(nil), runtimeEvidence.Flags...)
		sort.Strings(gotFlags)
		wantFlags := publicLocalFlagNames(command)
		if strings.Join(gotFlags, "\n") != strings.Join(wantFlags, "\n") {
			t.Errorf("%s runtime flags drifted from non-inherited public help\ngot:  %v\nwant: %v", capabilityID, gotFlags, wantFlags)
		}
	}
}

func publicLocalFlagNames(command *cobra.Command) []string {
	var names []string
	command.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Name != "help" && !flag.Hidden {
			names = append(names, "--"+flag.Name)
		}
	})
	command.PersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if flag.Name != "help" && !flag.Hidden {
			names = append(names, "--"+flag.Name)
		}
	})
	sort.Strings(names)
	return names
}

func publicLeafCommandPaths(root *cobra.Command) []string {
	var paths []string
	var visit func(*cobra.Command)
	visit = func(parent *cobra.Command) {
		for _, command := range parent.Commands() {
			if command.Hidden || command.Name() == "completion" || command.Name() == "help" {
				continue
			}
			visibleChildren := 0
			for _, child := range command.Commands() {
				if !child.Hidden && child.Name() != "completion" && child.Name() != "help" {
					visibleChildren++
				}
			}
			if command.Runnable() || visibleChildren == 0 {
				paths = append(paths, command.CommandPath())
			}
			if visibleChildren == 0 {
				continue
			}
			visit(command)
		}
	}
	visit(root)
	sort.Strings(paths)
	return paths
}

func assertRuntimeCommand(t *testing.T, capabilityID string, runtime runtimeCommand) {
	t.Helper()
	parts := strings.Fields(runtime.Path)
	if len(parts) < 2 || parts[0] != "wb" {
		t.Fatalf("%s runtime path %q must start with wb", capabilityID, runtime.Path)
	}
	root := newRootCmd()
	found, _, err := root.Find(parts[1:])
	if err != nil || found == nil || found.CommandPath() != runtime.Path {
		t.Fatalf("%s command %q is not implemented: found=%v err=%v", capabilityID, runtime.Path, found, err)
	}
	for _, name := range runtime.Flags {
		name = strings.TrimPrefix(name, "--")
		if found.Flags().Lookup(name) == nil && found.InheritedFlags().Lookup(name) == nil {
			t.Fatalf("%s advertises unavailable flag --%s on %s", capabilityID, name, runtime.Path)
		}
	}
}

func assertHelpAnchor(t *testing.T, capabilityID string, anchor helpAnchor) {
	t.Helper()
	parts := strings.Fields(anchor.Command)
	if len(parts) < 2 || parts[0] != "wb" || parts[len(parts)-1] != "--help" {
		t.Fatalf("%s invalid help command %q", capabilityID, anchor.Command)
	}
	root := newRootCmd()
	command := root
	if len(parts) > 2 {
		found, _, err := root.Find(parts[1 : len(parts)-1])
		if err != nil {
			t.Fatalf("%s resolve help command: %v", capabilityID, err)
		}
		command = found
	}
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	if err := command.Help(); err != nil {
		t.Fatalf("%s render help: %v", capabilityID, err)
	}
	for _, fragment := range anchor.Contains {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("%s help %q does not contain %q", capabilityID, anchor.Command, fragment)
		}
	}
}

func assertCapabilitySkill(t *testing.T, repoRoot, capabilityID string, evidence skillEvidence) {
	t.Helper()
	assertRepoEvidencePath(t, repoRoot, capabilityID, evidence.Path)
	contents, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(evidence.Path)))
	if err != nil || !strings.Contains(string(contents), evidence.Marker) {
		t.Fatalf("%s skill marker %q missing from %s: %v", capabilityID, evidence.Marker, evidence.Path, err)
	}
	for _, example := range evidence.Examples {
		if !strings.Contains(string(contents), example) {
			t.Fatalf("%s skill example %q missing from %s", capabilityID, example, evidence.Path)
		}
		if err := parseSkillCommandExample(example); err != nil {
			t.Fatalf("%s skill example %q no longer parses: %v", capabilityID, example, err)
		}
	}
}

func parseSkillCommandExample(example string) error {
	parts := strings.Fields(example)
	if len(parts) < 2 || parts[0] != "wb" {
		return fmt.Errorf("example must begin with wb")
	}
	root := newRootCmd()
	command, remaining, err := root.Find(parts[1:])
	if err != nil {
		return err
	}
	if err := command.ParseFlags(remaining); err != nil {
		return err
	}
	if command.Args != nil {
		if err := command.Args(command, command.Flags().Args()); err != nil {
			return err
		}
	}
	return validateSkillExampleRequiredFlags(command)
}

func validateSkillExampleRequiredFlags(command *cobra.Command) error {
	requirePrompt := command.CommandPath() == "wb worktree create"
	requireModel := requirePrompt
	if command.CommandPath() == "wb worktree rename" {
		apply := command.Flags().Lookup("apply")
		requirePrompt = apply != nil && apply.Value.String() == "true"
		requireModel = requirePrompt
	}
	if command.CommandPath() == "wb worktree abort" {
		apply := command.Flags().Lookup("apply")
		disposition := command.Flags().Lookup("disposition")
		requireModel = apply != nil && apply.Value.String() == "true" && disposition != nil &&
			(disposition.Value.String() == "handoff" || disposition.Value.String() == "not_landed")
	}
	if requireModel {
		model := command.Flags().Lookup("model")
		if model == nil || !model.Changed || strings.TrimSpace(model.Value.String()) == "" {
			return fmt.Errorf("%s requires --model", command.CommandPath())
		}
	}
	if !requirePrompt {
		return nil
	}
	prompt := command.Flags().Lookup("original-prompt-file")
	if prompt == nil || !prompt.Changed || strings.TrimSpace(prompt.Value.String()) == "" {
		return fmt.Errorf("%s requires --original-prompt-file", command.CommandPath())
	}
	return nil
}

func assertCapabilityTest(t *testing.T, repoRoot, capabilityID string, evidence testEvidence) {
	t.Helper()
	assertRepoEvidencePath(t, repoRoot, capabilityID, evidence.Path)
	contents, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(evidence.Path)))
	if err != nil {
		t.Fatalf("%s test evidence %s: %v", capabilityID, evidence.Path, err)
	}
	if !strings.Contains(string(contents), "func "+evidence.Name+"(") {
		t.Fatalf("%s test %s is missing from %s", capabilityID, evidence.Name, evidence.Path)
	}
}

func assertRepoEvidencePath(t *testing.T, repoRoot, capabilityID, evidencePath string) {
	t.Helper()
	clean := filepath.Clean(filepath.FromSlash(evidencePath))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		t.Fatalf("%s evidence path %q is not repository-relative", capabilityID, evidencePath)
	}
	info, err := os.Stat(filepath.Join(repoRoot, clean))
	if err != nil {
		t.Fatalf("%s evidence path %s: %v", capabilityID, evidencePath, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s evidence path %s is not a regular file", capabilityID, evidencePath)
	}
}

func assertSkillFiles(t *testing.T, repoRoot, skill string) {
	t.Helper()
	skillDir := filepath.Join(repoRoot, "ai", "skills", skill)
	for _, relativePath := range []string{"SKILL.md", filepath.Join("agents", "openai.yaml")} {
		if _, err := os.Stat(filepath.Join(skillDir, relativePath)); err != nil {
			t.Errorf("%s: %v", filepath.Join(skillDir, relativePath), err)
		}
	}
}

func assertClaudeManifestListsAllSkills(t *testing.T, repoRoot string) {
	t.Helper()
	manifestPath := filepath.Join(repoRoot, ".claude-plugin", "plugin.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest claudePluginManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse %s: %v", manifestPath, err)
	}
	var fields map[string]any
	if err := json.Unmarshal(manifestBytes, &fields); err != nil {
		t.Fatal(err)
	}
	for _, autoDiscovered := range []string{"skills", "agents"} {
		if _, explicit := fields[autoDiscovered]; explicit {
			t.Fatalf("Claude manifest explicitly lists %s and duplicates recursive auto-discovery", autoDiscovered)
		}
	}
	if manifest.Version != "1.0.0" {
		t.Fatalf("Claude plugin version = %q, want initial unified 1.0.0", manifest.Version)
	}
	for _, discovered := range []string{filepath.Join("ai", "skills", "wb-merge", "SKILL.md"), filepath.Join("agents", "wb-merger.md")} {
		if info, err := os.Stat(filepath.Join(repoRoot, discovered)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("Claude auto-discovery source %s is missing or non-regular: %v", discovered, err)
		}
	}
}

func assertCodexManifestExposesSkills(t *testing.T, repoRoot string) {
	t.Helper()
	manifestPath := filepath.Join(repoRoot, ".codex-plugin", "plugin.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest codexPluginManifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("parse %s: %v", manifestPath, err)
	}
	if manifest.Skills != "./ai/skills/" {
		t.Fatalf("Codex skills root = %q, want ./ai/skills/", manifest.Skills)
	}
	claudeBytes, err := os.ReadFile(filepath.Join(repoRoot, ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var claude claudePluginManifest
	if err := json.Unmarshal(claudeBytes, &claude); err != nil {
		t.Fatal(err)
	}
	if manifest.Version == "" || manifest.Version != claude.Version {
		t.Fatalf("plugin versions differ: Codex=%q Claude=%q", manifest.Version, claude.Version)
	}
	if len(manifest.Interface.DefaultPrompt) == 0 || len(manifest.Interface.DefaultPrompt) > 3 {
		t.Fatalf("Codex plugin defaultPrompt count = %d, want 1..3", len(manifest.Interface.DefaultPrompt))
	}
	for index, prompt := range manifest.Interface.DefaultPrompt {
		if strings.TrimSpace(prompt) == "" {
			t.Fatalf("Codex plugin defaultPrompt[%d] is empty", index)
		}
	}
	skillRoot := filepath.Join(repoRoot, filepath.FromSlash(manifest.Skills))
	info, err := os.Stat(skillRoot)
	if err != nil || !info.IsDir() {
		t.Fatalf("Codex skills root %s is not an existing directory: %v", skillRoot, err)
	}
}
