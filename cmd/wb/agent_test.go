package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
)

// agentTestEnv gives a test its own projects root and its own agent
// configuration, so nothing here can read or write the operator's real state.
// It returns the derived state home, <root>/.wb.
func agentTestEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, ".wb")
	configHome := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", root)
	// The global is what the in-process remote entry point reads; pin it too so
	// a test's own root never depends on which test ran first.
	previousProjectsRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousProjectsRoot })
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("DEEPSEEK_API_KEY", "test-credential")
	if err := os.MkdirAll(filepath.Join(configHome, "wb"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeAgentConfig(t, configHome, `
agents:
  profiles:
    cheap:
      harness: codex
      provider: deepseek
      model: deepseek-flash
      reasoning: high
`)
	return home
}

func writeAgentConfig(t *testing.T, configHome, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(configHome, "wb", "wb.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func seedAgentRun(t *testing.T, home string, mutate func(*agents.Record)) agents.Record {
	t.Helper()
	store := agents.NewStore(home)
	id, err := agents.NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := agents.Record{
		AgentID: id, State: agents.StateRunning, RequestedProfile: "cheap",
		Resolved: agents.Resolved{
			Profile: "cheap", Harness: agents.HarnessCodex, Provider: "deepseek",
			Model: "deepseek-flash", Reasoning: "high",
			Routing: agents.Provider{BaseURL: "https://api.deepseek.com", CredentialEnv: "DEEPSEEK_API_KEY", WireAPI: agents.WireAPIResponses},
		},
		Task: "the private bounded task", TaskSummary: "the private bounded task",
		Repository: "acme/app", WorktreeMode: agents.ModeNew, Worktree: "task-one",
		WorktreeDir: "/fleet/.worktrees/task-one", Branch: "task-one", Base: "main", BaseSHA: "abc123",
		StartedAt: time.Now().UTC(), LogPath: store.LogPath(id),
	}
	if mutate != nil {
		mutate(&record)
	}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	return record
}

func runAgentCLI(t *testing.T, arguments ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(arguments, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestAgentCommandFamilyIsPublicAndDiscoverable(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{
		{"agent"}, {"agent", "dispatch"}, {"agent", "status"}, {"agent", "await"},
		{"agent", "list"}, {"agent", "logs"}, {"agent", "stop"},
	} {
		found, _, err := root.Find(path)
		if err != nil || found == nil {
			t.Fatalf("wb %s is not registered: %v", strings.Join(path, " "), err)
		}
		if found.CommandPath() != "wb "+strings.Join(path, " ") {
			t.Fatalf("wb %s resolved to %q", strings.Join(path, " "), found.CommandPath())
		}
	}

	// The machine-readable catalog is how a cold agent finds a command, so the
	// agent family must be reachable through it.
	code, stdout, stderr := runAgentCLI(t, "commands", "--search", "offload worker", "--format", "json")
	if code != exitOK {
		t.Fatalf("commands exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "wb agent dispatch") {
		t.Fatalf("the catalog does not surface wb agent dispatch:\n%s", stdout)
	}
}

func TestAgentDispatchRefusesAmbiguousOrIncompleteInvocations(t *testing.T) {
	agentTestEnv(t)
	cases := []struct {
		name      string
		arguments []string
		fragment  string
	}{
		{"no worktree mode", []string{"agent", "dispatch", "--profile", "cheap", "--task", "x"}, "exactly one of --new-worktree or --use-worktree"},
		{"both worktree modes", []string{"agent", "dispatch", "--new-worktree", "a", "--use-worktree", "b", "--profile", "cheap", "--task", "x"}, "mutually exclusive"},
		{"no task", []string{"agent", "dispatch", "--new-worktree", "a", "--profile", "cheap"}, "one of --task or --task-file"},
		{"both task sources", []string{"agent", "dispatch", "--new-worktree", "a", "--profile", "cheap", "--task", "x", "--task-file", "/tmp/x"}, "mutually exclusive"},
		{"no profile", []string{"agent", "dispatch", "--new-worktree", "a", "--task", "x"}, "--profile is required"},
		{"unknown profile", []string{"agent", "dispatch", "--new-worktree", "a", "--profile", "nope", "--task", "x"}, "unknown agent profile"},
		{"bad format", []string{"agent", "status", "agt-x", "--format", "xml"}, "unsupported format"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			code, _, stderr := runAgentCLI(t, testCase.arguments...)
			if code != exitUsage {
				t.Fatalf("exit code = %d, want %d (usage); stderr: %s", code, exitUsage, stderr)
			}
			if !strings.Contains(stderr, testCase.fragment) {
				t.Fatalf("stderr %q does not mention %q", stderr, testCase.fragment)
			}
		})
	}
}

func TestAgentDispatchReadsTheTaskFromStdin(t *testing.T) {
	home := agentTestEnv(t)
	var stdout, stderr bytes.Buffer
	// --use-worktree keeps this off the worktree-creation path; the task still
	// has to be read, and the run must be refused later for the missing
	// worktree rather than for a missing task.
	code := runWithStdin([]string{"agent", "dispatch", "--use-worktree", "nope", "--profile", "cheap", "--task-file", "-"},
		strings.NewReader("task from stdin\n"), &stdout, &stderr)
	if code == exitUsage {
		t.Fatalf("a task on stdin must be accepted: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "no WB-managed worktree named") {
		t.Fatalf("the run must proceed past task reading to worktree resolution: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = runWithStdin([]string{"agent", "dispatch", "--use-worktree", "nope", "--profile", "cheap", "--task-file", "-"},
		strings.NewReader("   \n"), &stdout, &stderr)
	if code != exitUsage || !strings.Contains(stderr.String(), "non-empty task") {
		t.Fatalf("an empty stdin task must be a usage error: %d %s", code, stderr.String())
	}
	_ = home
}

func TestAgentDispatchReportsAnUnresolvableTaskFile(t *testing.T) {
	agentTestEnv(t)
	code, _, stderr := runAgentCLI(t, "agent", "dispatch", "--use-worktree", "nope", "--profile", "cheap", "--task-file", filepath.Join(t.TempDir(), "absent.md"))
	if code != exitFindings || !strings.Contains(stderr, "read --task-file") {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
}

func TestAgentDispatchRefusesAnEmptyTaskFile(t *testing.T) {
	agentTestEnv(t)
	path := filepath.Join(t.TempDir(), "empty.md")
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runAgentCLI(t, "agent", "dispatch", "--use-worktree", "nope", "--profile", "cheap", "--task-file", path)
	if code != exitFindings || !strings.Contains(stderr, "is empty") {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
}

func TestAgentDispatchDoesNotCreateAWorktreeWhenTheProfileIsUnknown(t *testing.T) {
	agentTestEnv(t)
	code, _, stderr := runAgentCLI(t, "agent", "dispatch", "--new-worktree", "never-created", "--repo", "acme/app", "--profile", "nope", "--task", "x")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage; stderr: %s", code, stderr)
	}
	if strings.Contains(stderr, "worktree") && !strings.Contains(stderr, "unknown agent profile") {
		t.Fatalf("an unknown profile must be refused before any worktree work: %s", stderr)
	}
}

func TestAgentStatusRendersExecutionFactsWithoutThePrivateTask(t *testing.T) {
	home := agentTestEnv(t)
	exit := 0
	record := seedAgentRun(t, home, func(record *agents.Record) {
		record.State = agents.StateCompleted
		record.FinishedAt = record.StartedAt.Add(3 * time.Second)
		record.DurationMS = 3000
		record.ExitCode = &exit
		record.ToolCalls = 4
		record.Usage = &agents.Usage{InputTokens: 100, CachedInputTokens: 80, OutputTokens: 9, ReasoningOutputTokens: 4}
		record.Changes = &agents.ChangeSummary{FilesChanged: 2, Insertions: 5, Deletions: 1, Files: []string{"a.txt", "b.txt"}}
		record.Result = "done"
	})

	code, stdout, stderr := runAgentCLI(t, "agent", "status", record.AgentID)
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for _, fragment := range []string{record.AgentID, "completed", "codex", "deepseek-flash", "task-one", "abc123"} {
		if !strings.Contains(stdout, fragment) {
			t.Errorf("text status is missing %q:\n%s", fragment, stdout)
		}
	}
	if strings.Contains(stdout, "the private bounded task") {
		t.Fatalf("text status leaked the private task:\n%s", stdout)
	}

	code, stdout, stderr = runAgentCLI(t, "agent", "status", record.AgentID, "--json")
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	for _, key := range []string{"agent_id", "state", "terminal", "resolved", "worktree", "branch", "exit_code", "usage", "changes", "log_path"} {
		if _, present := document[key]; !present {
			t.Errorf("json status is missing %q: %s", key, stdout)
		}
	}
	for _, key := range []string{"task", "task_summary"} {
		if _, present := document[key]; present {
			t.Errorf("json status leaked %q: %s", key, stdout)
		}
	}
	if document["terminal"] != true || document["state"] != "completed" {
		t.Fatalf("json status = %#v", document)
	}

	// --json must be the exact shortcut for --format json.
	_, shortJSON, _ := runAgentCLI(t, "agent", "status", record.AgentID, "--json")
	_, longJSON, _ := runAgentCLI(t, "agent", "status", record.AgentID, "--format", "json")
	if shortJSON != longJSON {
		t.Fatalf("--json and --format json disagree:\n%s\n%s", shortJSON, longJSON)
	}
}

func TestAgentStatusUnknownRunIsAFindingNotAUsageError(t *testing.T) {
	agentTestEnv(t)
	code, _, stderr := runAgentCLI(t, "agent", "status", "agt-00000000000000000000000000000000")
	if code != exitFindings {
		t.Fatalf("exit code = %d, want %d (findings)", code, exitFindings)
	}
	if !strings.Contains(stderr, "agt-00000000000000000000000000000000") {
		t.Fatalf("the message must name the run: %s", stderr)
	}
}

func TestAgentAwaitReturnsImmediatelyForATerminalRun(t *testing.T) {
	home := agentTestEnv(t)
	exit := 0
	record := seedAgentRun(t, home, func(record *agents.Record) {
		record.State = agents.StateFailed
		record.ExitCode = &exit
		record.Failure = "harness exited with exit status 3"
		record.FinishedAt = record.StartedAt.Add(time.Second)
	})
	code, stdout, stderr := runAgentCLI(t, "agent", "await", record.AgentID, "--json", "--wait-timeout", "30s")
	if code != exitOK {
		t.Fatalf("a terminal run must be reported without error: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, `"state": "failed"`) {
		t.Fatalf("await output = %s", stdout)
	}
}

func TestAgentAwaitResolvesAnAbandonedRunInsteadOfBlocking(t *testing.T) {
	home := agentTestEnv(t)
	record := seedAgentRun(t, home, func(record *agents.Record) {
		record.OwnerPID = 0
		record.WorkerPID = 0
		// Past the admission window: no owner was ever recorded, so no outcome
		// will ever be written and the run is conclusively abandoned.
		record.StartedAt = time.Now().UTC().Add(-10 * time.Minute)
	})
	started := time.Now()
	code, stdout, stderr := runAgentCLI(t, "agent", "await", record.AgentID, "--json", "--wait-timeout", "30s")
	if code != exitOK {
		t.Fatalf("an abandoned run is a terminal state, not a failure to await: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, `"state": "abandoned"`) {
		t.Fatalf("await output = %s", stdout)
	}
	if time.Since(started) > 20*time.Second {
		t.Fatal("await blocked on an abandoned run instead of resolving it")
	}
}

func TestAgentAwaitReportsAnElapsedBoundAsAFinding(t *testing.T) {
	home := agentTestEnv(t)
	// A live owner keeps the run non-terminal for the whole bound.
	record := seedAgentRun(t, home, func(record *agents.Record) {
		record.OwnerPID = os.Getpid()
	})
	code, stdout, stderr := runAgentCLI(t, "agent", "await", record.AgentID, "--wait-timeout", "200ms")
	if code != exitFindings {
		t.Fatalf("exit code = %d, want findings: waiting must never report success: %s", code, stderr)
	}
	if !strings.Contains(stderr, "still running") {
		t.Fatalf("stderr = %s", stderr)
	}
	if !strings.Contains(stdout, "not a success") {
		t.Fatalf("text output must say the wait bound elapsed: %s", stdout)
	}
}

func TestAgentListIsEmptyThenPopulated(t *testing.T) {
	home := agentTestEnv(t)
	code, stdout, stderr := runAgentCLI(t, "agent", "list")
	if code != exitOK || !strings.Contains(stdout, "no dispatched agent runs") {
		t.Fatalf("empty list: %d %q %s", code, stdout, stderr)
	}
	code, stdout, stderr = runAgentCLI(t, "agent", "list", "--json")
	if code != exitOK {
		t.Fatalf("empty json list: %d %s", code, stderr)
	}
	if strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("empty json list = %q, want an empty array", stdout)
	}

	first := seedAgentRun(t, home, func(record *agents.Record) {
		record.Worktree = "older"
		record.StartedAt = time.Now().UTC().Add(-time.Hour)
	})
	second := seedAgentRun(t, home, func(record *agents.Record) { record.Worktree = "newer" })
	code, stdout, stderr = runAgentCLI(t, "agent", "list")
	if code != exitOK {
		t.Fatalf("list: %d %s", code, stderr)
	}
	if strings.Index(stdout, "newer") > strings.Index(stdout, "older") {
		t.Fatalf("list must be newest first:\n%s", stdout)
	}
	if !strings.Contains(stdout, first.AgentID) || !strings.Contains(stdout, second.AgentID) {
		t.Fatalf("list is missing runs:\n%s", stdout)
	}
}

func TestAgentLogsLocatesTheTranscriptAndBoundsTheSummary(t *testing.T) {
	home := agentTestEnv(t)
	record := seedAgentRun(t, home, nil)
	store := agents.NewStore(home)
	events := `{"type":"item.completed","item":{"type":"command_execution","command":"cat a.txt"}}` + "\n" +
		`{"type":"turn.completed"}` + "\n"
	if err := os.WriteFile(store.LogPath(record.AgentID), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runAgentCLI(t, "agent", "logs", record.AgentID)
	if code != exitOK {
		t.Fatalf("logs: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, store.LogPath(record.AgentID)) {
		t.Fatalf("logs must locate the transcript: %s", stdout)
	}
	if !strings.Contains(stdout, "cat a.txt") {
		t.Fatalf("logs must summarise the worker's actions: %s", stdout)
	}

	code, stdout, _ = runAgentCLI(t, "agent", "logs", record.AgentID, "--raw")
	if code != exitOK || !strings.Contains(stdout, "turn.completed") {
		t.Fatalf("--raw must print the transcript: %d %s", code, stdout)
	}
}

func TestAgentLogsReportsAMissingTranscriptRatherThanFailing(t *testing.T) {
	home := agentTestEnv(t)
	record := seedAgentRun(t, home, func(record *agents.Record) { record.LogPath = "" })
	code, stdout, stderr := runAgentCLI(t, "agent", "logs", record.AgentID)
	if code != exitOK {
		t.Fatalf("logs: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, "no worker transcript was captured") {
		t.Fatalf("logs output = %s", stdout)
	}
}

func TestAgentStopRefusesTerminalAndUnknownRuns(t *testing.T) {
	home := agentTestEnv(t)
	terminal := seedAgentRun(t, home, func(record *agents.Record) {
		record.State = agents.StateCompleted
	})
	code, _, stderr := runAgentCLI(t, "agent", "stop", terminal.AgentID)
	if code != exitFindings || !strings.Contains(stderr, "already completed") {
		t.Fatalf("stopping a terminal run: %d %s", code, stderr)
	}

	code, _, stderr = runAgentCLI(t, "agent", "stop", "agt-00000000000000000000000000000000")
	if code != exitFindings || !strings.Contains(stderr, "no dispatched agent run") {
		t.Fatalf("stopping an unknown run: %d %s", code, stderr)
	}

	running := seedAgentRun(t, home, nil)
	code, _, stderr = runAgentCLI(t, "agent", "stop", running.AgentID)
	if code != exitFindings || !strings.Contains(stderr, "no recorded worker process") {
		t.Fatalf("stopping a run with no worker: %d %s", code, stderr)
	}
}

func TestAgentCommandsReportAMissingConfigurationWhenOneIsNeeded(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("DEEPSEEK_API_KEY", "test-credential")

	code, _, stderr := runAgentCLI(t, "agent", "dispatch", "--new-worktree", "a", "--repo", "acme/app", "--profile", "cheap", "--task", "x")
	if code != exitUsage {
		t.Fatalf("exit = %d, want usage: %s", code, stderr)
	}
	if !strings.Contains(stderr, "(none configured)") {
		t.Fatalf("the refusal must name the configuration state: %s", stderr)
	}
}

func TestAgentStatusRejectsAMalformedRunID(t *testing.T) {
	agentTestEnv(t)
	code, _, stderr := runAgentCLI(t, "agent", "status", "not-an-agent-id")
	if code == exitOK {
		t.Fatal("a malformed run ID must not succeed")
	}
	if !strings.Contains(stderr, "agent run ID") {
		t.Fatalf("stderr = %s", stderr)
	}
}

func TestAgentFlagsAreDocumentedInHelp(t *testing.T) {
	agentTestEnv(t)
	cases := map[string][]string{
		"wb agent dispatch": {"--new-worktree", "--use-worktree", "--profile", "--task", "--task-file", "--repo", "--base", "--branch", "--timeout", "--format", "--json"},
		"wb agent status":   {"--format", "--json"},
		"wb agent await":    {"--wait-timeout", "--format", "--json"},
		"wb agent list":     {"--format", "--json"},
		"wb agent logs":     {"--tail", "--raw"},
	}
	for path, flags := range cases {
		t.Run(path, func(t *testing.T) {
			root := newRootCmd()
			found, _, err := root.Find(strings.Fields(strings.TrimPrefix(path, "wb ")))
			if err != nil {
				t.Fatal(err)
			}
			var help bytes.Buffer
			found.SetOut(&help)
			if err := found.Help(); err != nil {
				t.Fatal(err)
			}
			for _, flag := range flags {
				if !strings.Contains(help.String(), flag) {
					t.Errorf("%s help does not document %s", path, flag)
				}
			}
		})
	}
}

func TestAgentDispatchPropagatesANonRequestFailureAsAFinding(t *testing.T) {
	home := agentTestEnv(t)
	// A worktree that cannot resolve is a finding, not a usage error: the
	// invocation itself was valid.
	code, _, stderr := runAgentCLI(t, "agent", "dispatch", "--use-worktree", "does-not-exist", "--repo", "acme/app", "--profile", "cheap", "--task", "x")
	if code != exitFindings {
		t.Fatalf("exit = %d, want findings: %s", code, stderr)
	}
	if !strings.Contains(stderr, "no WB-managed worktree named") {
		t.Fatalf("stderr = %s", stderr)
	}
	records, err := agents.NewStore(home).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("a refused dispatch must leave no run record: %#v", records)
	}
}

func TestAgentDispatchReportsAMissingProviderCredential(t *testing.T) {
	agentTestEnv(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	code, _, stderr := runAgentCLI(t, "agent", "dispatch", "--new-worktree", "a", "--repo", "acme/app", "--profile", "cheap", "--task", "x")
	if code != exitFindings {
		t.Fatalf("exit = %d, want findings: %s", code, stderr)
	}
	if !strings.Contains(stderr, "DEEPSEEK_API_KEY") {
		t.Fatalf("stderr must name the variable: %s", stderr)
	}
}
