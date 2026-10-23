package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func agentGuardFixture(t *testing.T) (projectsRoot, canonical, worktree string) {
	t.Helper()
	root := t.TempDir()
	projectsRoot = filepath.Join(root, "projects")
	canonical = filepath.Join(projectsRoot, "sneat-co", "backstage")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatalf("create canonical clone: %v", err)
	}
	agentGuardGit(t, canonical, "init", "-q", "-b", "main")
	agentGuardGit(t, canonical, "config", "user.email", "guard@example.test")
	agentGuardGit(t, canonical, "config", "user.name", "guard")
	if err := os.WriteFile(filepath.Join(canonical, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seed the clone: %v", err)
	}
	agentGuardGit(t, canonical, "add", "-A")
	agentGuardGit(t, canonical, "commit", "-qm", "init")
	worktree = filepath.Join(root, "worktrees", "task", "sneat-co", "backstage")
	agentGuardGit(t, canonical, "worktree", "add", "-q", "-b", "task", worktree)
	return projectsRoot, canonical, worktree
}
func agentGuardGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}
func agentGuardPayload(t *testing.T, tool, cwd string, input map[string]any) string {
	t.Helper()
	payload := map[string]any{
		"hook_event_name": "PreToolUse",
		"tool_name":       tool,
		"cwd":             cwd,
		"tool_input":      input,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode the payload: %v", err)
	}
	return string(encoded)
}
func TestAgentHookReadsAPayloadFile(t *testing.T) {
	projectsRoot, canonical, _ := agentGuardFixture(t)
	path := filepath.Join(t.TempDir(), "payload.json")
	payload := agentGuardPayload(t, "Bash", canonical, map[string]any{"command": "git reset --hard"})
	if err := os.WriteFile(path, []byte(payload), 0o644); err != nil {
		t.Fatalf("write the payload: %v", err)
	}
	var stdout, stderr bytes.Buffer
	code := runWithStdin(
		[]string{"--projects-root", projectsRoot, "hooks", "agent", "pre-tool-use", "--input", path},
		strings.NewReader(""), &stdout, &stderr,
	)
	if code != exitOK || !strings.Contains(stdout.String(), `"deny"`) {
		t.Fatalf("--input did not reach a refusal: code=%d stdout=%q", code, stdout.String())
	}
	// A payload file that is not there is still an allow.
	stdout.Reset()
	code = runWithStdin(
		[]string{"--projects-root", projectsRoot, "hooks", "agent", "pre-tool-use", "--input", filepath.Join(t.TempDir(), "absent.json")},
		strings.NewReader(""), &stdout, &stderr,
	)
	if code != exitOK || stdout.Len() != 0 {
		t.Fatalf("a missing payload file failed closed: code=%d stdout=%q", code, stdout.String())
	}
}
