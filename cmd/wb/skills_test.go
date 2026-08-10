package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type skillCommandCoverage struct {
	Version  int                 `json:"version"`
	Commands map[string][]string `json:"commands"`
}

type claudePluginManifest struct {
	Skills []string `json:"skills"`
}

type capabilityManifest struct {
	Version        int          `json:"version"`
	ExtractionSeam string       `json:"extraction_seam"`
	Capabilities   []capability `json:"capabilities"`
}

type capability struct {
	ID          string   `json:"id"`
	Command     string   `json:"command"`
	Flags       []string `json:"flags"`
	Support     string   `json:"support"`
	HelpAnchor  string   `json:"help_anchor"`
	SkillFile   string   `json:"skill_file"`
	SkillAnchor string   `json:"skill_anchor"`
	Test        string   `json:"test"`
	Since       string   `json:"since"`
	Notes       string   `json:"notes"`
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
}

// TestCapabilityManifestKeepsImplementationHelpAndSkillsInOne Checked-in
// contract. It deliberately validates command/flag parsing without executing
// examples, so a documentation regression cannot mutate a repository.
func TestCapabilityManifestKeepsImplementationHelpAndSkillsInOne(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	contents, err := os.ReadFile(filepath.Join(repoRoot, "ai", "capabilities.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest capabilityManifest
	if err := json.Unmarshal(contents, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || manifest.ExtractionSeam == "" {
		t.Fatalf("capability manifest lacks version/extraction seam: %#v", manifest)
	}
	seen := map[string]bool{}
	for _, capability := range manifest.Capabilities {
		if capability.ID == "" || seen[capability.ID] || capability.Command == "" || capability.Support == "" || capability.HelpAnchor == "" || capability.SkillFile == "" || capability.SkillAnchor == "" || capability.Test == "" || capability.Since == "" || capability.Notes == "" {
			t.Fatalf("incomplete or duplicate capability: %#v", capability)
		}
		seen[capability.ID] = true
		command := newRootCmd()
		parts := strings.Fields(capability.Command)
		found, _, findErr := command.Find(parts)
		if findErr != nil || found == nil || found.CommandPath() != "wb "+capability.Command {
			t.Fatalf("%s command %q is not implemented: found=%v err=%v", capability.ID, capability.Command, found, findErr)
		}
		for _, flag := range capability.Flags {
			if found.Flags().Lookup(flag) == nil && found.InheritedFlags().Lookup(flag) == nil {
				t.Fatalf("%s advertises unavailable flag --%s on %s", capability.ID, flag, capability.Command)
			}
		}
		skill, readErr := os.ReadFile(filepath.Join(repoRoot, capability.SkillFile))
		if readErr != nil || !strings.Contains(string(skill), capability.SkillAnchor) {
			t.Fatalf("%s skill anchor %q missing from %s: %v", capability.ID, capability.SkillAnchor, capability.SkillFile, readErr)
		}
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
}
