package integration

import (
	"context"
	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAgentDispatchDoesNotCreateAWorktreeWhenTheProfileIsUnknown(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, _, stderr := f.run(t, "agent", "dispatch", "--new-worktree", "never-created", "--repo", "acme/app", "--profile", "nope", "--task", "x")
	if code != exitUsage {
		t.Fatalf("exit code = %d, want usage; stderr: %s", code, stderr)
	}
	if strings.Contains(stderr, "worktree") && !strings.Contains(stderr, "unknown agent profile") {
		t.Fatalf("an unknown profile must be refused before any worktree work: %s", stderr)
	}
}

func TestAgentStatusUnknownRunIsAFindingNotAUsageError(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, _, stderr := f.run(t, "agent", "status", "agt-00000000000000000000000000000000")
	if code != exitFindings {
		t.Fatalf("exit code = %d, want %d (findings)", code, exitFindings)
	}
	if !strings.Contains(stderr, "agt-00000000000000000000000000000000") {
		t.Fatalf("the message must name the run: %s", stderr)
	}
}

func TestAgentAwaitReturnsImmediatelyForATerminalRun(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home
	exit := 0
	record := seedAgentRun(t, home, func(record *agents.Record) {
		record.State = agents.StateFailed
		record.ExitCode = &exit
		record.Failure = "harness exited with exit status 3"
		record.FinishedAt = record.StartedAt.Add(time.Second)
	})
	code, stdout, stderr := f.run(t, "agent", "await", record.AgentID, "--json", "--wait-timeout", "30s")
	if code != exitOK {
		t.Fatalf("a terminal run must be reported without error: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, `"state": "failed"`) {
		t.Fatalf("await output = %s", stdout)
	}
}

func TestAgentAwaitResolvesAnAbandonedRunInsteadOfBlocking(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home
	record := seedAgentRun(t, home, func(record *agents.Record) {
		record.OwnerPID = 0
		record.WorkerPID = 0
		// Past the admission window: no owner was ever recorded, so no outcome
		// will ever be written and the run is conclusively abandoned.
		record.StartedAt = time.Now().UTC().Add(-10 * time.Minute)
	})
	started := time.Now()
	code, stdout, stderr := f.run(t, "agent", "await", record.AgentID, "--json", "--wait-timeout", "30s")
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
	t.Parallel()
	f := newFixture(t)
	now := time.Now()
	f.deps.Now = func() time.Time { return now }
	waits := 0
	f.deps.Wait = func(context.Context, time.Duration) error { waits++; now = now.Add(250 * time.Millisecond); return nil }
	f.service = agentrun.New(f.deps)
	home := f.home
	// A live owner keeps the run non-terminal for the whole bound.
	record := seedAgentRun(t, home, func(record *agents.Record) {
		record.OwnerPID = os.Getpid()
	})
	code, stdout, stderr := f.run(t, "agent", "await", record.AgentID, "--wait-timeout", "200ms")
	if code != exitFindings {
		t.Fatalf("exit code = %d, want findings: waiting must never report success: %s", code, stderr)
	}
	if !strings.Contains(stderr, "still running") {
		t.Fatalf("stderr = %s", stderr)
	}
	if waits != 1 {
		t.Fatal("expected one controlled durable-record wait", waits)
	}
	if !strings.Contains(stdout, "not a success") {
		t.Fatalf("text output must say the wait bound elapsed: %s", stdout)
	}
}

func TestAgentListIsEmptyThenPopulated(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home
	code, stdout, stderr := f.run(t, "agent", "list")
	if code != exitOK || !strings.Contains(stdout, "no dispatched agent runs") {
		t.Fatalf("empty list: %d %q %s", code, stdout, stderr)
	}
	code, stdout, stderr = f.run(t, "agent", "list", "--json")
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
	code, stdout, stderr = f.run(t, "agent", "list")
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
	t.Parallel()
	f := newFixture(t)
	home := f.home
	record := seedAgentRun(t, home, nil)
	store := agents.NewStore(home)
	events := `{"type":"item.completed","item":{"type":"command_execution","command":"cat a.txt"}}` + "\n" +
		`{"type":"turn.completed"}` + "\n"
	if err := os.WriteFile(store.LogPath(record.AgentID), []byte(events), 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := f.run(t, "agent", "logs", record.AgentID)
	if code != exitOK {
		t.Fatalf("logs: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, store.LogPath(record.AgentID)) {
		t.Fatalf("logs must locate the transcript: %s", stdout)
	}
	if !strings.Contains(stdout, "cat a.txt") {
		t.Fatalf("logs must summarise the worker's actions: %s", stdout)
	}

	code, stdout, _ = f.run(t, "agent", "logs", record.AgentID, "--raw")
	if code != exitOK || !strings.Contains(stdout, "turn.completed") {
		t.Fatalf("--raw must print the transcript: %d %s", code, stdout)
	}
}

func TestAgentLogsReportsAMissingTranscriptRatherThanFailing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home
	record := seedAgentRun(t, home, func(record *agents.Record) { record.LogPath = "" })
	code, stdout, stderr := f.run(t, "agent", "logs", record.AgentID)
	if code != exitOK {
		t.Fatalf("logs: %d %s", code, stderr)
	}
	if !strings.Contains(stdout, "no worker transcript was captured") {
		t.Fatalf("logs output = %s", stdout)
	}
}

func TestAgentStopRefusesTerminalAndUnknownRuns(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home
	terminal := seedAgentRun(t, home, func(record *agents.Record) {
		record.State = agents.StateCompleted
	})
	code, _, stderr := f.run(t, "agent", "stop", terminal.AgentID)
	if code != exitFindings || !strings.Contains(stderr, "already completed") {
		t.Fatalf("stopping a terminal run: %d %s", code, stderr)
	}

	code, _, stderr = f.run(t, "agent", "stop", "agt-00000000000000000000000000000000")
	if code != exitFindings || !strings.Contains(stderr, "no dispatched agent run") {
		t.Fatalf("stopping an unknown run: %d %s", code, stderr)
	}

	running := seedAgentRun(t, home, nil)
	code, _, stderr = f.run(t, "agent", "stop", running.AgentID)
	if code != exitFindings || !strings.Contains(stderr, "no recorded worker process") {
		t.Fatalf("stopping a run with no worker: %d %s", code, stderr)
	}
}

func TestAgentCommandsReportAMissingConfigurationWhenOneIsNeeded(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := os.Remove(f.configPath); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := f.run(t, "agent", "dispatch", "--new-worktree", "a", "--repo", "acme/app", "--profile", "cheap", "--task", "x")
	if code != exitUsage {
		t.Fatalf("exit = %d, want usage: %s", code, stderr)
	}
	if !strings.Contains(stderr, "(none configured)") {
		t.Fatalf("the refusal must name the configuration state: %s", stderr)
	}
}

func TestAgentStatusRejectsAMalformedRunID(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	code, _, stderr := f.run(t, "agent", "status", "not-an-agent-id")
	if code == exitOK {
		t.Fatal("a malformed run ID must not succeed")
	}
	if !strings.Contains(stderr, "agent run ID") {
		t.Fatalf("stderr = %s", stderr)
	}
}

func TestAgentDispatchPropagatesANonRequestFailureAsAFinding(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	home := f.home
	// A worktree that cannot resolve is a finding, not a usage error: the
	// invocation itself was valid.
	code, _, stderr := f.run(t, "agent", "dispatch", "--use-worktree", "does-not-exist", "--repo", "acme/app", "--profile", "cheap", "--task", "x")
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
	f := newFixture(t)
	f.writeConfig(t, baseConfig)
	t.Setenv("DEEPSEEK_API_KEY", "")
	code, _, stderr := f.run(t, "agent", "dispatch", "--new-worktree", "a", "--repo", "acme/app", "--profile", "cheap", "--task", "x")
	if code != exitFindings {
		t.Fatalf("exit = %d, want findings: %s", code, stderr)
	}
	if !strings.Contains(stderr, "DEEPSEEK_API_KEY") {
		t.Fatalf("stderr must name the variable: %s", stderr)
	}
}
