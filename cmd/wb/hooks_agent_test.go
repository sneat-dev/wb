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

// agentGuardFixture builds a projects root with one canonical clone and one
// linked worktree, using real Git.
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

// runAgentHook drives the command exactly as a settings file would, and
// returns the exit code alongside both streams.
func runAgentHook(t *testing.T, projectsRoot, payload string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runWithStdin(
		[]string{"--projects-root", projectsRoot, "hooks", "agent", "pre-tool-use"},
		strings.NewReader(payload), &stdout, &stderr,
	)
	return code, stdout.String(), stderr.String()
}

// TestAgentHookNeverExitsNonZero is the fail-open contract at the process
// boundary. Claude Code blocks a tool call whose PreToolUse hook exits 2, and
// WB spends exit 2 on usage errors — so this command must reach exit 0 for
// every input, including every input it cannot understand.
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

// TestAgentHookIsSilentForEveryAllow keeps silence as the allow signal. Any
// stdout that begins with { is parsed as a decision, so an allow must write
// nothing at all.
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

// TestAgentHookWritesTheDenyDocument checks the whole path end to end: the
// payload shape Claude Code sends in, the response shape it reads back, and an
// actionable remedy inside it.
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

// TestAgentHookGhPrMergeOverrideMustBeInlineOnTheCall drives the escape
// hatch end to end through the real `wb hooks agent pre-tool-use` entry
// point, with a JSON payload on stdin exactly the way the harness sends one
// — not by calling agentguard.Inspect directly. wb#500 review, Should-fix 3:
// the pre-fix implementation read WB_AGENTGUARD_ALLOW_GH_PR_MERGE from the
// hook process's own ambient environment via os.Getenv. That process is
// spawned fresh by the harness's PreToolUse mechanism for every tool call —
// a tree the Bash tool's shell never reaches — so an ambient value can never
// be scoped to "the one call it is set on" the way the refusal text and this
// command's own doc comment promise; at best it is a human export that
// silently allows every gh pr merge for the rest of a session. This proves
// the fix end to end: only a value inline on the refused Bash command's own
// words is ever honoured, and the ambient case is proven to do nothing here,
// not only in the internal/agentguard package's own unit tests.
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
		home := t.TempDir()
		t.Setenv("WB_HOME", home)
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
		home := t.TempDir()
		t.Setenv("WB_HOME", home)
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

// TestAgentHookReadsAPayloadFile covers the --input path used for testing an
// installation without a pipe.
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

// TestAgentHookShellCommandForcesExitZero pins the wrapper. Without the
// trailing `exit 0`, a WB too old to know this subcommand would exit non-zero
// and Claude Code would treat exit 2 as a block, refusing every tool call on
// the machine.
func TestAgentHookShellCommandForcesExitZero(t *testing.T) {
	command := agentHookShellCommand("/opt/homebrew/bin/wb")
	for _, expected := range []string{"/opt/homebrew/bin/wb", agentHookInvocation, "2>/dev/null", "exit 0"} {
		if !strings.Contains(command, expected) {
			t.Fatalf("the hook command is missing %q: %s", expected, command)
		}
	}
	if quoted := agentHookShellCommand("/path with spaces/wb"); !strings.Contains(quoted, `'/path with spaces/wb'`) {
		t.Fatalf("an executable path with spaces was not quoted: %s", quoted)
	}
}

// TestMergeAgentHookSettingsIsIdempotent keeps re-running the installer from
// stacking duplicate entries, and keeps every key WB does not own.
func TestMergeAgentHookSettingsIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	existing := `{
	  "model": "opus",
	  "hooks": {
	    "PreToolUse": [
	      {"matcher": "Bash", "hooks": [{"type": "command", "command": "other-guard"}]}
	    ],
	    "Stop": [{"hooks": [{"type": "command", "command": "notify"}]}]
	  }
	}`
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("seed the settings file: %v", err)
	}
	shellCommand := agentHookShellCommand("/usr/local/bin/wb")

	document, changed, err := mergeAgentHookSettings(path, shellCommand)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !changed {
		t.Fatal("the first merge reported no change")
	}
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatalf("persist the merged document: %v", err)
	}

	second, changed, err := mergeAgentHookSettings(path, shellCommand)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if changed {
		t.Fatal("re-running the installer changed the settings file again")
	}
	if !bytes.Equal(document, second) {
		t.Fatalf("re-running the installer rewrote the document:\n%s\n---\n%s", document, second)
	}

	var settings map[string]any
	if err := json.Unmarshal(second, &settings); err != nil {
		t.Fatalf("the merged document is not valid JSON: %v", err)
	}
	if settings["model"] != "opus" {
		t.Fatalf("an unrelated key was lost: %v", settings["model"])
	}
	hooks, _ := settings["hooks"].(map[string]any)
	if _, ok := hooks["Stop"]; !ok {
		t.Fatal("an unrelated hook event was lost")
	}
	entries, _ := hooks["PreToolUse"].([]any)
	if len(entries) != 2 {
		t.Fatalf("PreToolUse has %d entries, want the pre-existing one plus WB's", len(entries))
	}
	if !strings.Contains(string(second), "other-guard") {
		t.Fatal("a pre-existing PreToolUse hook was lost")
	}
	if !strings.Contains(string(second), agentHookMatcher) {
		t.Fatalf("the WB matcher is missing from %s", second)
	}
}

// TestMergeAgentHookSettingsWidensAStaleMatcher covers a fleet install from
// before the Agent/Task policies (missing model, literal report path, live
// claim): re-running install must widen the existing entry's matcher in
// place rather than leaving it stale or appending a duplicate. Without this,
// `wb hooks agent install`'s "re-running it changes nothing once the entry is
// present" guarantee would keep an already-installed fleet on the narrower,
// pre-policy matcher forever, because the original idempotency check
// compared the command only, never the matcher.
func TestMergeAgentHookSettingsWidensAStaleMatcher(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	shellCommand := agentHookShellCommand("/usr/local/bin/wb")
	staleMatcher := `{
	  "hooks": {
	    "PreToolUse": [
	      {"matcher": "Bash|Write|Edit|MultiEdit|NotebookEdit", "hooks": [{"type": "command", "command": ` + jsonString(shellCommand) + `, "timeout": 10}]}
	    ]
	  }
	}`
	if err := os.WriteFile(path, []byte(staleMatcher), 0o644); err != nil {
		t.Fatalf("seed the settings file: %v", err)
	}

	document, changed, err := mergeAgentHookSettings(path, shellCommand)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if !changed {
		t.Fatal("re-running install over a stale matcher reported no change")
	}
	if !strings.Contains(string(document), agentHookMatcher) {
		t.Fatalf("the widened matcher is missing from %s", document)
	}

	var settings map[string]any
	if err := json.Unmarshal(document, &settings); err != nil {
		t.Fatalf("the merged document is not valid JSON: %v", err)
	}
	hooks, _ := settings["hooks"].(map[string]any)
	entries, _ := hooks["PreToolUse"].([]any)
	if len(entries) != 1 {
		t.Fatalf("widening the matcher produced %d entries, want the same one entry updated in place", len(entries))
	}

	// A third run, now that the matcher matches, must be a true no-op.
	if err := os.WriteFile(path, document, 0o644); err != nil {
		t.Fatal(err)
	}
	again, changedAgain, err := mergeAgentHookSettings(path, shellCommand)
	if err != nil {
		t.Fatalf("third merge: %v", err)
	}
	if changedAgain {
		t.Fatal("re-running install a second time, after the matcher was widened, reported a change")
	}
	if !bytes.Equal(document, again) {
		t.Fatalf("re-running install rewrote the already-current document:\n%s\n---\n%s", document, again)
	}
}

// TestMergeAgentHookSettingsCreatesAMissingFile covers a first install on a
// machine with no settings file yet.
func TestMergeAgentHookSettingsCreatesAMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	document, changed, err := mergeAgentHookSettings(path, agentHookShellCommand("wb"))
	if err != nil || !changed {
		t.Fatalf("merge on a missing file: changed=%v err=%v", changed, err)
	}
	if err := writeSettingsAtomically(path, document); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), "PreToolUse") {
		t.Fatalf("the installed document is missing the hook: %s", raw)
	}
}

// TestMergeAgentHookSettingsRefusesAnUnparseableFile keeps the installer from
// silently replacing a settings file it could not read.
func TestMergeAgentHookSettingsRefusesAnUnparseableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := mergeAgentHookSettings(path, "wb"); err == nil {
		t.Fatal("an unparseable settings file was accepted")
	}
}
