package herdr

import (
	"context"
	"errors"
	"testing"
)

func TestAgentList(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "list"}, fakeCall{
		stdout: mustReadTestdata(t, "agent_list.json"),
	})
	client := newTestClient(t, runner)

	agents, err := client.AgentList(context.Background())
	if err != nil {
		t.Fatalf("AgentList() error = %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("AgentList() returned %d agents, want 2", len(agents))
	}
	if agents[1].Status != StatusWorking || agents[1].PaneID != "w1:p2" {
		t.Fatalf("AgentList()[1] = %#v", agents[1])
	}
	if got := agents[1].HarnessSessionID(); got != "00000000-0000-4000-8000-000000000002" {
		t.Fatalf("AgentList()[1].HarnessSessionID() = %q", got)
	}
}

func TestAgentListPropagatesCommandError(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "list"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:list","error":{"code":"server_not_running","message":"no server"}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentList(context.Background()); !errors.Is(err, ErrServerUnreachable) {
		t.Fatalf("AgentList() error = %v, want ErrServerUnreachable", err)
	}
}

func TestAgentListUnparseableEnvelope(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "list"}, fakeCall{stdout: []byte("garbage")})
	client := newTestClient(t, runner)

	if _, err := client.AgentList(context.Background()); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("AgentList() error = %v, want ErrUnparseableOutput", err)
	}
}

func TestAgentListMissingTypeKey(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "list"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:list","result":{"agents":[]}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentList(context.Background()); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("AgentList() error = %v, want ErrUnparseableOutput for a missing type key", err)
	}
}

func TestAgentListWrongTypeKey(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "list"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:list","result":{"type":"pane_list","agents":[]}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentList(context.Background()); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("AgentList() error = %v, want ErrUnparseableOutput for a mismatched type key", err)
	}
}

func TestAgentListEntryMissingID(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "list"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:list","result":{"type":"agent_list","agents":[{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","agent_status":"idle","focused":false,"revision":1},{"agent_status":"idle","focused":false,"revision":0}]}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentList(context.Background()); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("AgentList() error = %v, want ErrUnparseableOutput for an entry missing pane_id", err)
	}
}

func TestAgentListUnparseableResultShape(t *testing.T) {
	// The envelope itself is valid JSON; "agents" is the wrong JSON type,
	// so AgentList's own decode (not the envelope decode) fails.
	runner := newFakeRunner(t).on([]string{"agent", "list"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:list","result":{"agents":"not-an-array"}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentList(context.Background()); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("AgentList() error = %v, want ErrUnparseableOutput", err)
	}
}

func TestAgentGet(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "get", "w1:p2"}, fakeCall{
		stdout: mustReadTestdata(t, "agent_get.json"),
	})
	client := newTestClient(t, runner)

	agent, err := client.AgentGet(context.Background(), "w1:p2")
	if err != nil {
		t.Fatalf("AgentGet() error = %v", err)
	}
	if agent.Status != StatusWorking || agent.Kind != "claude" {
		t.Fatalf("AgentGet() = %#v", agent)
	}
}

func TestAgentGetEmptyTarget(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t))
	if _, err := client.AgentGet(context.Background(), ""); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("AgentGet(\"\") error = %v, want ErrUnknownTarget", err)
	}
}

func TestAgentGetNotFound(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "get", "nonexistent-target-xyz"}, fakeCall{
		stdout: mustReadTestdata(t, "error_agent_not_found.json"),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentGet(context.Background(), "nonexistent-target-xyz"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("AgentGet() error = %v, want ErrUnknownTarget", err)
	}
}

func TestAgentGetInvalidShape(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "get", "w1:p2"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:get","result":{"agent":{}}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentGet(context.Background(), "w1:p2"); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("AgentGet() error = %v, want ErrUnparseableOutput", err)
	}
}

func TestAgentRead(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "read", "w1:p2", "--source", "recent-unwrapped"}, fakeCall{
		stdout: mustReadTestdata(t, "agent_read.json"),
	})
	client := newTestClient(t, runner)

	screen, err := client.AgentRead(context.Background(), "w1:p2", ReadSourceRecentUnwrapped)
	if err != nil {
		t.Fatalf("AgentRead() error = %v", err)
	}
	if screen.PaneID != "w1:p2" || screen.Source != ReadSourceRecentUnwrapped || screen.Format != ReadFormatText {
		t.Fatalf("AgentRead() = %#v", screen)
	}
	if screen.Text == "" {
		t.Fatal("AgentRead().Text is empty")
	}
}

func TestAgentReadEmptyTarget(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t))
	if _, err := client.AgentRead(context.Background(), "", ReadSourceVisible); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("AgentRead(\"\") error = %v, want ErrUnknownTarget", err)
	}
}

func TestAgentReadUsesHyphenatedCLISourceButUnderscoredJSONSource(t *testing.T) {
	// Locks in the CLI/wire spelling difference discovered via `herdr
	// agent`'s group help (--source recent-unwrapped) vs `herdr api schema
	// --json` (ReadSource enum value "recent_unwrapped").
	runner := newFakeRunner(t).on([]string{"agent", "read", "w1:p2", "--source", "recent-unwrapped"}, fakeCall{
		stdout: mustReadTestdata(t, "agent_read.json"),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentRead(context.Background(), "w1:p2", ReadSourceRecentUnwrapped); err != nil {
		t.Fatalf("AgentRead() error = %v", err)
	}
}

func TestAgentReadInvalidShape(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "read", "w1:p2", "--source", "visible"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:read","result":{"type":"pane_read","read":{}}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentRead(context.Background(), "w1:p2", ReadSourceVisible); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("AgentRead() error = %v, want ErrUnparseableOutput", err)
	}
}

func TestAgentReadPropagatesCommandError(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "read", "ghost", "--source", "visible"}, fakeCall{
		stdout: mustReadTestdata(t, "error_agent_not_found.json"),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentRead(context.Background(), "ghost", ReadSourceVisible); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("AgentRead() error = %v, want ErrUnknownTarget", err)
	}
}

func TestAgentPromptRejectsInvalidText(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t))

	if err := client.AgentPrompt(context.Background(), "reviewer", "two\nlines"); !errors.Is(err, ErrInvalidPromptText) {
		t.Fatalf("AgentPrompt() error = %v, want ErrInvalidPromptText", err)
	}
	if err := client.AgentPrompt(context.Background(), "", "hello"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("AgentPrompt(target=\"\") error = %v, want ErrUnknownTarget", err)
	}
}

func TestAgentPromptRejectsLeadingHyphen(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t))
	if err := client.AgentPrompt(context.Background(), "reviewer", "-1"); !errors.Is(err, ErrInvalidPromptText) {
		t.Fatalf("AgentPrompt(text starting with -) error = %v, want ErrInvalidPromptText", err)
	}
}

func TestAgentPromptSendsExactArgv(t *testing.T) {
	text := "sneat-dev/wb#598: checks failed; do not $(rm -rf /) me"
	runner := newFakeRunner(t).on([]string{"agent", "prompt", "reviewer", text}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:prompt","result":{}}`),
	})
	client := newTestClient(t, runner)

	if err := client.AgentPrompt(context.Background(), "reviewer", text); err != nil {
		t.Fatalf("AgentPrompt() error = %v", err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("AgentPrompt() issued %d calls, want 1", len(runner.calls))
	}
}

func TestAgentPromptPropagatesCommandError(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "prompt", "reviewer", "hi"}, fakeCall{
		stdout: mustReadTestdata(t, "error_agent_not_found.json"),
	})
	client := newTestClient(t, runner)

	if err := client.AgentPrompt(context.Background(), "reviewer", "hi"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("AgentPrompt() error = %v, want ErrUnknownTarget", err)
	}
}

func TestAgentSendKeysValidatesEveryKey(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t))

	if err := client.AgentSendKeys(context.Background(), "", "esc"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("AgentSendKeys(target=\"\") error = %v, want ErrUnknownTarget", err)
	}
	if err := client.AgentSendKeys(context.Background(), "reviewer"); !errors.Is(err, ErrInvalidKeyName) {
		t.Fatalf("AgentSendKeys(no keys) error = %v, want ErrInvalidKeyName", err)
	}
	if err := client.AgentSendKeys(context.Background(), "reviewer", "esc", "; rm -rf /"); !errors.Is(err, ErrInvalidKeyName) {
		t.Fatalf("AgentSendKeys(bad key) error = %v, want ErrInvalidKeyName", err)
	}
}

func TestAgentSendKeysSendsExactArgv(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "send-keys", "reviewer", "ctrl+c", "esc"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:send-keys","result":{}}`),
	})
	client := newTestClient(t, runner)

	if err := client.AgentSendKeys(context.Background(), "reviewer", "ctrl+c", "esc"); err != nil {
		t.Fatalf("AgentSendKeys() error = %v", err)
	}
}

func TestAgentWaitEmptyTarget(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t))
	if _, err := client.AgentWait(context.Background(), ""); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("AgentWait(\"\") error = %v, want ErrUnknownTarget", err)
	}
}

func TestAgentWaitWithUntilBuildsArgs(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "wait", "reviewer", "--until", "idle", "--until", "blocked"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:wait","result":{"agent":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t2","agent":"claude","agent_status":"idle"}}}`),
	})
	client := newTestClient(t, runner)

	agent, err := client.AgentWait(context.Background(), "reviewer", StatusIdle, StatusBlocked)
	if err != nil {
		t.Fatalf("AgentWait() error = %v", err)
	}
	if agent.Status != StatusIdle || agent.PaneID != "w1:p2" {
		t.Fatalf("AgentWait() = %#v", agent)
	}
}

func TestAgentWaitBareShape(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "wait", "reviewer"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:wait","result":{"pane_id":"w1:p2","workspace_id":"w1","tab_id":"w1:t2","agent":"claude","agent_status":"working"}}`),
	})
	client := newTestClient(t, runner)

	agent, err := client.AgentWait(context.Background(), "reviewer")
	if err != nil {
		t.Fatalf("AgentWait() error = %v", err)
	}
	if agent.Status != StatusWorking {
		t.Fatalf("AgentWait() = %#v", agent)
	}
}

func TestAgentWaitUnrecognizedShape(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "wait", "reviewer"}, fakeCall{
		stdout: []byte(`{"id":"cli:agent:wait","result":{"totally":"different"}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentWait(context.Background(), "reviewer"); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("AgentWait() error = %v, want ErrUnparseableOutput", err)
	}
}

func TestAgentWaitPropagatesCommandError(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"agent", "wait", "reviewer"}, fakeCall{
		stdout: mustReadTestdata(t, "error_agent_not_found.json"),
	})
	client := newTestClient(t, runner)

	if _, err := client.AgentWait(context.Background(), "reviewer"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("AgentWait() error = %v, want ErrUnknownTarget", err)
	}
}
