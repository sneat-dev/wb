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
)

type skillCommandCoverage struct {
	Version  int                 `json:"version"`
	Commands map[string][]string `json:"commands"`
}

type claudePluginManifest struct {
	Skills []string `json:"skills"`
	Agents []string `json:"agents"`
}

type codexPluginManifest struct {
	Skills string `json:"skills"`
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
		"Push the exact target immediately",
		"remote target SHA",
		"wb ci wait --repo <owner/repo>",
		"enumerated required-check policy",
		"does not prove that no optional",
		"workflow can register later",
		"wb worktree cleanup <task> --apply --remote --older-than 0",
		"Work Log",
		"TestCleanupAcceptsExactDirectPushIntegrationWithoutPullRequest",
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
	for _, required := range []string{"foreground", "shorter than that harness's tool timeout", "bounded quiescence receipt", "separate release evidence", "Never detach a watcher, use a background process"} {
		if !strings.Contains(string(polling), required) {
			t.Errorf("CI polling contract is missing %q", required)
		}
	}

	claude, err := os.ReadFile(filepath.Join(repoRoot, ".claude-plugin", "plugin.json"))
	if err != nil || !strings.Contains(string(claude), "./ai/skills/wb-merge") {
		t.Fatalf("Claude adapter does not expose wb-merge: %v", err)
	}
	claudeAgent, err := os.ReadFile(filepath.Join(repoRoot, "agents", "wb-merger.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"name: wb-merger", "description:", "skills: [wb-merge]", "Follow the preloaded `$wb-merge` skill"} {
		if !strings.Contains(string(claudeAgent), required) {
			t.Errorf("Claude merger agent is missing %q", required)
		}
	}
	for _, forbidden := range []string{"model:", "background:", "isolation:"} {
		if strings.Contains(string(claudeAgent), forbidden) {
			t.Errorf("Claude merger agent must leave %s to the canonical foreground WB contract", strings.TrimSuffix(forbidden, ":"))
		}
	}
	codex, err := os.ReadFile(filepath.Join(repoRoot, ".codex-plugin", "plugin.json"))
	if err != nil || !strings.Contains(string(codex), "WB merger workflow") {
		t.Fatalf("Codex adapter does not expose wb-merge: %v", err)
	}
	copilot, err := os.ReadFile(filepath.Join(repoRoot, ".github", "agents", "wb-merger.agent.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"description:", "Read and follow `ai/skills/wb-merge/SKILL.md`", "does not define a second workflow"} {
		if !strings.Contains(string(copilot), required) {
			t.Errorf("Copilot adapter is missing %q", required)
		}
	}
	if strings.Contains(string(copilot), "model:") {
		t.Error("Copilot adapter must leave model selection to its harness")
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
			if visibleChildren == 0 {
				paths = append(paths, command.CommandPath())
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
	if command.CommandPath() == "wb worktree rename" {
		apply := command.Flags().Lookup("apply")
		requirePrompt = apply != nil && apply.Value.String() == "true"
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

	got := append([]string(nil), manifest.Skills...)
	sort.Strings(got)
	entries, err := os.ReadDir(filepath.Join(repoRoot, "ai", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillPath := filepath.Join(repoRoot, "ai", "skills", entry.Name(), "SKILL.md")
		if _, err := os.Stat(skillPath); err != nil {
			continue
		}
		want = append(want, "./ai/skills/"+entry.Name())
	}
	sort.Strings(want)

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Claude skill manifest mismatch\ngot:\n%s\nwant:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if strings.Join(manifest.Agents, "\n") != "./agents/wb-merger.md" {
		t.Fatalf("Claude merger agent manifest = %q, want ./agents/wb-merger.md", manifest.Agents)
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
	skillRoot := filepath.Join(repoRoot, filepath.FromSlash(manifest.Skills))
	info, err := os.Stat(skillRoot)
	if err != nil || !info.IsDir() {
		t.Fatalf("Codex skills root %s is not an existing directory: %v", skillRoot, err)
	}
}
