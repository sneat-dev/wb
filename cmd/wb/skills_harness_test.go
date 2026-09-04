package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSkillsSyncTargetsUsesExplicitDir(t *testing.T) {
	targets, err := resolveSkillsSyncTargets("/tmp/custom-skills", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Dir != "/tmp/custom-skills" || targets[0].Harness != "" {
		t.Fatalf("targets = %+v, want one unnamed /tmp/custom-skills", targets)
	}
}

func TestResolveSkillsSyncTargetsRejectsDirAndHarnessTogether(t *testing.T) {
	_, err := resolveSkillsSyncTargets("/tmp/custom-skills", []string{"cursor"})
	if err == nil {
		t.Fatal("expected --dir and --harness together to be rejected")
	}
	var coded *exitError
	if !asExitError(err, &coded) || coded.code != exitUsage {
		t.Fatalf("err = %v, want exitUsage", err)
	}
}

func TestResolveSkillsSyncTargetsUnknownHarnessIsUsage(t *testing.T) {
	_, err := resolveSkillsSyncTargets("", []string{"not-a-harness"})
	if err == nil {
		t.Fatal("expected unknown harness to be rejected")
	}
	var coded *exitError
	if !asExitError(err, &coded) || coded.code != exitUsage {
		t.Fatalf("err = %v, want exitUsage", err)
	}
	if !strings.Contains(err.Error(), "cursor") || !strings.Contains(err.Error(), "codex") {
		t.Errorf("error %q should name the known harnesses", err)
	}
}

func TestResolveSkillsSyncTargetsNamedHarnessesEvenWhenAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	targets, err := resolveSkillsSyncTargets("", []string{"cursor", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %+v, want cursor then codex", targets)
	}
	if targets[0].Harness != harnessCursor || targets[0].Dir != filepath.Join(home, ".cursor", "skills") {
		t.Errorf("cursor target = %+v", targets[0])
	}
	if targets[1].Harness != harnessCodex || targets[1].Dir != filepath.Join(home, ".codex", "skills") {
		t.Errorf("codex target = %+v", targets[1])
	}
}

func TestResolveSkillsSyncTargetsAllAndAliasesAndCommaLists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	targets, err := resolveSkillsSyncTargets("", []string{"claude-code,cursor", "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 3 {
		t.Fatalf("targets = %+v, want claude, cursor, codex once each", targets)
	}
	if targets[0].Harness != harnessClaude || targets[1].Harness != harnessCursor || targets[2].Harness != harnessCodex {
		t.Errorf("order/ids = %+v", targets)
	}
}

func TestResolveSkillsSyncTargetsDefaultsToPresentHarnesses(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	for _, name := range []string{".cursor", ".codex"} {
		if err := os.Mkdir(filepath.Join(home, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	targets, err := resolveSkillsSyncTargets("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Harness != harnessCursor || targets[1].Harness != harnessCodex {
		t.Fatalf("present default = %+v, want cursor and codex only", targets)
	}
}

func TestResolveSkillsSyncTargetsFallsBackToClaudeWhenNonePresent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	targets, err := resolveSkillsSyncTargets("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[0].Harness != harnessClaude {
		t.Fatalf("empty-home fallback = %+v, want claude", targets)
	}
}

func TestResolveSkillsSyncTargetsHonorsConfigEnvOverrides(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	claude := filepath.Join(t.TempDir(), "claude-config")
	codex := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	t.Setenv("CODEX_HOME", codex)

	targets, err := resolveSkillsSyncTargets("", []string{"claude", "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if targets[0].Dir != filepath.Join(claude, "skills") {
		t.Errorf("claude dir = %q, want under CLAUDE_CONFIG_DIR", targets[0].Dir)
	}
	if targets[1].Dir != filepath.Join(codex, "skills") {
		t.Errorf("codex dir = %q, want under CODEX_HOME", targets[1].Dir)
	}
}

func asExitError(err error, coded **exitError) bool {
	return errors.As(err, coded)
}
