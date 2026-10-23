package cmdagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"strings"
	"testing"
	"time"
)

func TestAgentDispatchRefusesAmbiguousOrIncompleteInvocations(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Dispatch = func(_ context.Context, r agentrun.DispatchRequest) (agents.Result, error) {
		_, err := (agents.Config{}).Resolve(r.Request.Profile)
		return agents.Result{}, err
	}
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
			t.Parallel()
			code, _, stderr := runFake(t, deps, testCase.arguments...)
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
	t.Parallel()
	deps := fakeDependencies()
	deps.Dispatch = func(_ context.Context, r agentrun.DispatchRequest) (agents.Result, error) {
		if r.Request.Task != "task from stdin\n" {
			t.Fatal(r.Request.Task)
		}
		return agents.Result{}, errors.New("no WB-managed worktree named nope")
	}

	var stdout, stderr bytes.Buffer
	// --use-worktree keeps this off the worktree-creation path; the task still
	// has to be read, and the run must be refused later for the missing
	// worktree rather than for a missing task.
	code := runFakeWithStdin(t, deps, []string{"agent", "dispatch", "--use-worktree", "nope", "--profile", "cheap", "--task-file", "-"},
		strings.NewReader("task from stdin\n"), &stdout, &stderr)
	if code == exitUsage {
		t.Fatalf("a task on stdin must be accepted: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "no WB-managed worktree named") {
		t.Fatalf("the run must proceed past task reading to worktree resolution: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = runFakeWithStdin(t, deps, []string{"agent", "dispatch", "--use-worktree", "nope", "--profile", "cheap", "--task-file", "-"},
		strings.NewReader("   \n"), &stdout, &stderr)
	if code != exitUsage || !strings.Contains(stderr.String(), "non-empty task") {
		t.Fatalf("an empty stdin task must be a usage error: %d %s", code, stderr.String())
	}

}

func TestAgentDispatchReportsAnUnresolvableTaskFile(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	code, _, stderr := runFake(t, deps, "agent", "dispatch", "--use-worktree", "nope", "--profile", "cheap", "--task-file", "/private/task/absent.md")
	if code != exitFindings || !strings.Contains(stderr, "read --task-file") {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
}

func TestAgentDispatchRefusesAnEmptyTaskFile(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	path := "empty.md"
	deps.ReadTaskFile = func(string) ([]byte, error) { return []byte("  \n"), nil }
	code, _, stderr := runFake(t, deps, "agent", "dispatch", "--use-worktree", "nope", "--profile", "cheap", "--task-file", path)
	if code != exitFindings || !strings.Contains(stderr, "is empty") {
		t.Fatalf("exit %d stderr %s", code, stderr)
	}
}

func TestAgentFlagsAreDocumentedInHelp(t *testing.T) {
	t.Parallel()

	cases := map[string][]string{
		"wb agent dispatch": {"--new-worktree", "--use-worktree", "--profile", "--task", "--task-file", "--repo", "--base", "--branch", "--timeout", "--format", "--json"},
		"wb agent status":   {"--format", "--json"},
		"wb agent await":    {"--wait-timeout", "--format", "--json"},
		"wb agent list":     {"--format", "--json"},
		"wb agent logs":     {"--tail", "--raw"},
	}
	for path, flags := range cases {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			root := fakeCommandRoot(fakeDependencies())
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

func TestAgentStatusRendersExecutionFactsWithoutThePrivateTask(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	exit := 0
	record := presentationRecord(func(record *agents.Record) {
		record.State = agents.StateCompleted
		record.FinishedAt = record.StartedAt.Add(3 * time.Second)
		record.DurationMS = 3000
		record.ExitCode = &exit
		record.ToolCalls = 4
		record.Usage = &agents.Usage{InputTokens: 100, CachedInputTokens: 80, OutputTokens: 9, ReasoningOutputTokens: 4}
		record.Changes = &agents.ChangeSummary{FilesChanged: 2, Insertions: 5, Deletions: 1, Files: []string{"a.txt", "b.txt"}}
		record.Result = "done"
	})

	deps.Status = func(context.Context, agentrun.LookupRequest) (agents.Result, error) {
		return (agents.Store{}).Render(record), nil
	}
	code, stdout, stderr := runFake(t, deps, "agent", "status", record.AgentID)
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

	code, stdout, stderr = runFake(t, deps, "agent", "status", record.AgentID, "--json")
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
	_, shortJSON, _ := runFake(t, deps, "agent", "status", record.AgentID, "--json")
	_, longJSON, _ := runFake(t, deps, "agent", "status", record.AgentID, "--format", "json")
	if shortJSON != longJSON {
		t.Fatalf("--json and --format json disagree:\n%s\n%s", shortJSON, longJSON)
	}
}

func TestAgentHelpDocumentsTheRemoteFlags(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"dispatch", "status", "await", "list", "logs", "stop"} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			root := fakeCommandRoot(fakeDependencies())
			found, _, err := root.Find([]string{"agent", path})
			if err != nil {
				t.Fatal(err)
			}
			var help bytes.Buffer
			found.SetOut(&help)
			if err := found.Help(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(help.String(), "--to") {
				t.Errorf("wb agent %s help does not offer --to", path)
			}
		})
	}
}
