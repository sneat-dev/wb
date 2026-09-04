package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// skillsTarget is one directory `wb skills sync` will install into. Harness
// is empty when the caller passed --dir rather than a named harness.
type skillsTarget struct {
	Harness string
	Dir     string
}

const (
	harnessClaude = "claude"
	harnessCursor = "cursor"
	harnessCodex  = "codex"
	harnessAll    = "all"
)

// skillsHarness describes one Agent Skills location a named harness owns.
// Each harness keeps its own directory rather than sharing Claude's: a
// Cursor Cloud Agent only syncs ~/.cursor/skills, and Codex still loads
// $CODEX_HOME/skills (~/.codex/skills).
type skillsHarness struct {
	ID        string
	Aliases   []string
	ConfigRel string
	ConfigEnv string
}

// knownSkillsHarnesses is the closed set `wb skills sync --harness` accepts.
// Order is the default sync order when several are present.
var knownSkillsHarnesses = []skillsHarness{
	{ID: harnessClaude, Aliases: []string{"claude-code"}, ConfigRel: ".claude", ConfigEnv: "CLAUDE_CONFIG_DIR"},
	{ID: harnessCursor, ConfigRel: ".cursor"},
	{ID: harnessCodex, ConfigRel: ".codex", ConfigEnv: "CODEX_HOME"},
}

func (h skillsHarness) configDir(home string) string {
	if h.ConfigEnv != "" {
		if override := strings.TrimSpace(os.Getenv(h.ConfigEnv)); override != "" {
			return override
		}
	}
	return filepath.Join(home, h.ConfigRel)
}

func (h skillsHarness) skillsDir(home string) string {
	return filepath.Join(h.configDir(home), "skills")
}

func (h skillsHarness) present(home string) bool {
	info, err := os.Stat(h.configDir(home))
	return err == nil && info.IsDir()
}

func lookupSkillsHarness(name string) (skillsHarness, bool) {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return skillsHarness{}, false
	}
	for _, harness := range knownSkillsHarnesses {
		if harness.ID == normalized {
			return harness, true
		}
		for _, alias := range harness.Aliases {
			if alias == normalized {
				return harness, true
			}
		}
	}
	return skillsHarness{}, false
}

func knownSkillsHarnessIDs() []string {
	ids := make([]string, 0, len(knownSkillsHarnesses))
	for _, harness := range knownSkillsHarnesses {
		ids = append(ids, harness.ID)
	}
	return ids
}

func skillsHarnessUsageList() string {
	return strings.Join(knownSkillsHarnessIDs(), ", ")
}

// defaultHarnessSkillsDir is Claude Code's skills directory. The Claude
// SessionStart hook is Claude-specific, so it still targets this path even
// when other harnesses are present; `wb skills sync` without --dir uses
// resolveSkillsSyncTargets instead.
func defaultHarnessSkillsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	harness, ok := lookupSkillsHarness(harnessClaude)
	if !ok {
		return "", fmt.Errorf("unknown skills harness %q", harnessClaude)
	}
	return harness.skillsDir(home), nil
}

// resolveSkillsSyncTargets picks the directories one `wb skills sync`
// invocation will write. --dir is an explicit single path and cannot be
// combined with --harness. Named harnesses are installed even when that
// harness is not yet present on the machine (so `wb skills sync --harness
// cursor` can set a new Cursor install up). With neither flag, every
// present harness is synced; if none are present, Claude remains the
// backward-compatible fallback so a first sync on a fresh HOME still has a
// well-defined target.
func resolveSkillsSyncTargets(dir string, harnessNames []string) ([]skillsTarget, error) {
	if strings.TrimSpace(dir) != "" && len(harnessNames) > 0 {
		return nil, &exitError{code: exitUsage, message: "--dir and --harness cannot be used together; pass an explicit directory or named harnesses, not both"}
	}
	if strings.TrimSpace(dir) != "" {
		return []skillsTarget{{Dir: dir}}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	if len(harnessNames) > 0 {
		return namedSkillsTargets(home, harnessNames)
	}
	var targets []skillsTarget
	for _, harness := range knownSkillsHarnesses {
		if !harness.present(home) {
			continue
		}
		targets = append(targets, skillsTarget{Harness: harness.ID, Dir: harness.skillsDir(home)})
	}
	if len(targets) == 0 {
		harness, ok := lookupSkillsHarness(harnessClaude)
		if !ok {
			return nil, fmt.Errorf("unknown skills harness %q", harnessClaude)
		}
		return []skillsTarget{{Harness: harness.ID, Dir: harness.skillsDir(home)}}, nil
	}
	return targets, nil
}

func namedSkillsTargets(home string, names []string) ([]skillsTarget, error) {
	seen := map[string]bool{}
	var targets []skillsTarget
	add := func(harness skillsHarness) {
		if seen[harness.ID] {
			return
		}
		seen[harness.ID] = true
		targets = append(targets, skillsTarget{Harness: harness.ID, Dir: harness.skillsDir(home)})
	}
	for _, raw := range names {
		for _, name := range splitHarnessName(raw) {
			if name == harnessAll {
				for _, harness := range knownSkillsHarnesses {
					add(harness)
				}
				continue
			}
			harness, ok := lookupSkillsHarness(name)
			if !ok {
				return nil, &exitError{code: exitUsage, message: fmt.Sprintf(
					"unknown skills harness %q; known harnesses are %s, or %s",
					name, skillsHarnessUsageList(), harnessAll)}
			}
			add(harness)
		}
	}
	if len(targets) == 0 {
		return nil, &exitError{code: exitUsage, message: "at least one --harness value is required"}
	}
	return targets, nil
}

func splitHarnessName(raw string) []string {
	parts := strings.Split(raw, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.ToLower(strings.TrimSpace(part))
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	return names
}

func presentSkillsTargets() ([]skillsTarget, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	var targets []skillsTarget
	for _, harness := range knownSkillsHarnesses {
		if !harness.present(home) {
			continue
		}
		targets = append(targets, skillsTarget{Harness: harness.ID, Dir: harness.skillsDir(home)})
	}
	return targets, nil
}
