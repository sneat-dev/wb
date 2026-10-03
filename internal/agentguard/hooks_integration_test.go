package agentguard_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agentguard"
	"github.com/sneat-dev/wb/internal/cli/cmdhooks"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/wbexec"
	"github.com/spf13/cobra"
)

func newHooksCmd(inv *testInvocation) *cobra.Command {
	return cmdhooks.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: inv.projectsRoot, Filter: inv.filterFlag} }}, cmdhooks.GitOperations{}, cmdhooks.LifecycleOperations{}, cmdhooks.AgentOperations{Inspect: agentguard.Inspect, OpenInput: func(path string) (io.ReadCloser, error) { return os.Open(path) }, Executable: wbexec.HookExecutable, ResolveGovernor: wbexec.ResolveGovernorExecutable})
}

type testInvocation struct{ projectsRoot, filterFlag string }

func selected(inv *testInvocation, path ...string) *cobra.Command {
	cmd := newHooksCmd(inv)
	for _, name := range path {
		for _, child := range cmd.Commands() {
			if child.Name() == name {
				cmd = child
				break
			}
		}
	}
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	if parent := cmd.Parent(); parent != nil {
		parent.RemoveCommand(cmd)
	}
	return cmd
}

const exitOK = 0

func runAgentHook(t *testing.T, projectsRoot, payload string) (int, string, string) {
	t.Helper()
	cmd := selected(&testInvocation{projectsRoot: projectsRoot}, "agent", "pre-tool-use")
	var out, errOut bytes.Buffer
	cmd.SetIn(strings.NewReader(payload))
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	err := cmd.Execute()
	code := 0
	if err != nil {
		code = 1
	}
	return code, out.String(), errOut.String()
}

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
func TestAgentHookNeverExitsNonZero(t *testing.T) {
	projectsRoot, canonical, _ := agentGuardFixture(t)
	payloads := []struct {
		name    string
		payload string
	}{
		{"empty stdin", ""},
		{"not JSON", "this is not json"},
		{"a JSON array", "[1,2,3]"},
		{"JSON null", "null"},
		{"truncated JSON", `{"tool_name":"Bash","tool_input":{"command":`},
		{"an unknown tool", agentGuardPayload(t, "SomeFutureTool", canonical, map[string]any{"command": "git reset --hard"})},
		{"a refused call", agentGuardPayload(t, "Bash", canonical, map[string]any{"command": "git reset --hard"})},
		{"an allowed call", agentGuardPayload(t, "Bash", canonical, map[string]any{"command": "git fetch"})},
	}
	for _, testCase := range payloads {
		t.Run(testCase.name, func(t *testing.T) {
			code, _, _ := runAgentHook(t, projectsRoot, testCase.payload)
			if code != exitOK {
				t.Fatalf("exit code %d for %s; a PreToolUse guard must always exit 0", code, testCase.name)
			}
		})
	}
}
func TestAgentHookIsSilentForEveryAllow(t *testing.T) {
	projectsRoot, canonical, worktree := agentGuardFixture(t)
	allowed := []struct {
		name    string
		payload string
	}{
		{"git fetch in the clone", agentGuardPayload(t, "Bash", canonical, map[string]any{"command": "git fetch --all --prune"})},
		{"fast-forward in the clone", agentGuardPayload(t, "Bash", canonical, map[string]any{"command": "git merge --ff-only origin/main"})},
		{"git status in the clone", agentGuardPayload(t, "Bash", canonical, map[string]any{"command": "git status --porcelain"})},
		{"git log in the clone", agentGuardPayload(t, "Bash", canonical, map[string]any{"command": "git log --oneline"})},
		{"a read of the clone", agentGuardPayload(t, "Read", canonical, map[string]any{"file_path": filepath.Join(canonical, "README.md")})},
		{"a write in a worktree", agentGuardPayload(t, "Write", worktree, map[string]any{"file_path": filepath.Join(worktree, "x.go"), "content": "package x"})},
		{"a reset in a worktree", agentGuardPayload(t, "Bash", worktree, map[string]any{"command": "git reset --hard origin/main"})},
		{"garbage", "not json"},
	}
	for _, testCase := range allowed {
		t.Run(testCase.name, func(t *testing.T) {
			_, stdout, _ := runAgentHook(t, projectsRoot, testCase.payload)
			if stdout != "" {
				t.Fatalf("an allow wrote to stdout: %q", stdout)
			}
		})
	}
}
func TestAgentHookWritesTheDenyDocument(t *testing.T) {
	projectsRoot, canonical, _ := agentGuardFixture(t)
	payload := agentGuardPayload(t, "Bash", canonical, map[string]any{"command": "git checkout origin/main -- ."})
	code, stdout, _ := runAgentHook(t, projectsRoot, payload)
	if code != exitOK {
		t.Fatalf("exit code %d, want 0", code)
	}
	if !strings.HasPrefix(stdout, "{") {
		t.Fatalf("Claude Code only parses stdout beginning with {; got %q", stdout)
	}
	var document struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("the deny document is not valid JSON: %v\n%s", err, stdout)
	}
	if document.HookSpecificOutput.PermissionDecision != "deny" {
		t.Fatalf("permissionDecision = %q, want deny", document.HookSpecificOutput.PermissionDecision)
	}
	if document.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Fatalf("hookEventName = %q", document.HookSpecificOutput.HookEventName)
	}
	reason := document.HookSpecificOutput.PermissionDecisionReason
	for _, expected := range []string{canonical, "wb worktree create <task> sneat-co/backstage"} {
		if !strings.Contains(reason, expected) {
			t.Fatalf("the refusal does not name %q:\n%s", expected, reason)
		}
	}
}
func TestAgentHookGhPrMergeOverrideMustBeInlineOnTheCall(t *testing.T) {
	projectsRoot, canonical, _ := agentGuardFixture(t)

	t.Run("no override at all still refuses through the real entry point", func(t *testing.T) {
		payload := agentGuardPayload(t, "Bash", canonical, map[string]any{"command": "gh pr merge 1041"})
		code, stdout, _ := runAgentHook(t, projectsRoot, payload)
		if code != exitOK {
			t.Fatalf("exit code %d, want 0 (fail open)", code)
		}
		if !strings.Contains(stdout, `"deny"`) {
			t.Fatalf("gh pr merge with no override was not refused: %q", stdout)
		}
	})

	t.Run("an ambient WB_AGENTGUARD_ALLOW_GH_PR_MERGE is never honoured", func(t *testing.T) {
		t.Setenv("WB_AGENTGUARD_ALLOW_GH_PR_MERGE", "set ahead of time in the process env, not on the call")
		payload := agentGuardPayload(t, "Bash", canonical, map[string]any{"command": "gh pr merge 1041"})
		code, stdout, _ := runAgentHook(t, projectsRoot, payload)
		if code != exitOK {
			t.Fatalf("exit code %d, want 0 (fail open)", code)
		}
		if !strings.Contains(stdout, `"deny"`) {
			t.Fatalf("an ambient override with no inline prefix allowed gh pr merge through: %q", stdout)
		}
	})

	t.Run("an override inline on the exact call is honoured and recorded", func(t *testing.T) {
		home := filepath.Join(projectsRoot, ".wb")
		command := `WB_AGENTGUARD_ALLOW_GH_PR_MERGE="wb worktree land refuses this exact receipt, sneat-dev/wb#999" gh pr merge 1041 --admin`
		payload := agentGuardPayload(t, "Bash", canonical, map[string]any{"command": command})
		code, stdout, stderr := runAgentHook(t, projectsRoot, payload)
		if code != exitOK {
			t.Fatalf("exit code %d, want 0", code)
		}
		if stdout != "" {
			t.Fatalf("an inline override was refused instead of allowed: stdout=%q stderr=%q", stdout, stderr)
		}
		recorded, err := os.ReadFile(filepath.Join(home, "agentguard", "gh-pr-merge-overrides.jsonl"))
		if err != nil {
			t.Fatalf("read the override record: %v", err)
		}
		for _, expected := range []string{"land-with-wb-verb", "gh pr merge 1041 --admin", "sneat-dev/wb#999", "recorded_at"} {
			if !strings.Contains(string(recorded), expected) {
				t.Fatalf("override record does not contain %q:\n%s", expected, recorded)
			}
		}
	})

	t.Run("an override via env is honoured the same way", func(t *testing.T) {
		home := filepath.Join(projectsRoot, ".wb")
		command := `env WB_AGENTGUARD_ALLOW_GH_PR_MERGE="reason via env, sneat-dev/wb#999" gh pr merge 1041`
		payload := agentGuardPayload(t, "Bash", canonical, map[string]any{"command": command})
		code, stdout, stderr := runAgentHook(t, projectsRoot, payload)
		if code != exitOK {
			t.Fatalf("exit code %d, want 0", code)
		}
		if stdout != "" {
			t.Fatalf("an env-prefixed override was refused instead of allowed: stdout=%q stderr=%q", stdout, stderr)
		}
		recorded, err := os.ReadFile(filepath.Join(home, "agentguard", "gh-pr-merge-overrides.jsonl"))
		if err != nil {
			t.Fatalf("read the override record: %v", err)
		}
		if !strings.Contains(string(recorded), "reason via env, sneat-dev/wb#999") {
			t.Fatalf("override record does not contain the env-prefixed reason:\n%s", recorded)
		}
	})
}
