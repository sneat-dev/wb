package herdr

import (
	"context"
	"errors"
	"testing"
)

func TestPaneCurrent(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "current", "--current"}, fakeCall{
		stdout: mustReadTestdata(t, "pane_current.json"),
	})
	client := newTestClient(t, runner)

	pane, err := client.PaneCurrent(context.Background())
	if err != nil {
		t.Fatalf("PaneCurrent() error = %v", err)
	}
	want := Pane{
		PaneID:        "w1:p2",
		WorkspaceID:   "w1",
		TabID:         "w1:t2",
		AgentKind:     "claude",
		AgentStatus:   StatusWorking,
		CWD:           "/Users/alex/projects",
		ForegroundCWD: "/Users/alex/projects",
		Focused:       true,
		TerminalID:    "term_fake000002",
		TerminalTitle: "WB peer connectivity",
		AgentSession: &AgentSession{
			Agent:  "claude",
			Kind:   "id",
			Source: "herdr:claude",
			Value:  "00000000-0000-4000-8000-000000000002",
		},
	}
	if pane.PaneID != want.PaneID || pane.AgentStatus != want.AgentStatus || pane.Focused != want.Focused {
		t.Fatalf("PaneCurrent() = %#v, want %#v", pane, want)
	}
	if pane.AgentSession == nil || pane.AgentSession.HarnessSessionID() != "00000000-0000-4000-8000-000000000002" {
		t.Fatalf("PaneCurrent().AgentSession = %#v", pane.AgentSession)
	}
}

func TestPaneCurrentInvalidShape(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "current", "--current"}, fakeCall{
		stdout: []byte(`{"id":"cli:pane:current","result":{"unexpected":true}}`),
	})
	client := newTestClient(t, runner)

	_, err := client.PaneCurrent(context.Background())
	if !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("PaneCurrent() error = %v, want ErrUnparseableOutput", err)
	}
}

func TestPaneListWithWorkspaceFilter(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "list", "--workspace", "w1"}, fakeCall{
		stdout: mustReadTestdata(t, "pane_list.json"),
	})
	client := newTestClient(t, runner)

	panes, err := client.PaneList(context.Background(), "w1")
	if err != nil {
		t.Fatalf("PaneList() error = %v", err)
	}
	if len(panes) != 3 {
		t.Fatalf("PaneList() returned %d panes, want 3", len(panes))
	}
	if panes[2].AgentStatus != StatusUnknown || panes[2].AgentSession != nil {
		t.Fatalf("PaneList()[2] = %#v, want unknown status and no agent session", panes[2])
	}
}

func TestPaneListPropagatesCommandError(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "list"}, fakeCall{
		stdout: []byte(`{"id":"cli:pane:list","error":{"code":"server_not_running","message":"no server"}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.PaneList(context.Background(), ""); !errors.Is(err, ErrServerUnreachable) {
		t.Fatalf("PaneList() error = %v, want ErrServerUnreachable", err)
	}
}

func TestPaneListWithoutWorkspaceFilter(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "list"}, fakeCall{
		stdout: mustReadTestdata(t, "pane_list.json"),
	})
	client := newTestClient(t, runner)

	if _, err := client.PaneList(context.Background(), ""); err != nil {
		t.Fatalf("PaneList() error = %v", err)
	}
}

func TestPaneListUnparseableEnvelope(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "list"}, fakeCall{stdout: []byte("nope")})
	client := newTestClient(t, runner)

	if _, err := client.PaneList(context.Background(), ""); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("PaneList() error = %v, want ErrUnparseableOutput", err)
	}
}

func TestPaneListUnparseableResultShape(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "list"}, fakeCall{
		stdout: []byte(`{"id":"cli:pane:list","result":{"panes":"not-an-array"}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.PaneList(context.Background(), ""); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("PaneList() error = %v, want ErrUnparseableOutput", err)
	}
}

func TestPaneGet(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "get", "w1:p1"}, fakeCall{
		stdout: mustReadTestdata(t, "pane_get.json"),
	})
	client := newTestClient(t, runner)

	pane, err := client.PaneGet(context.Background(), "w1:p1")
	if err != nil {
		t.Fatalf("PaneGet() error = %v", err)
	}
	if pane.PaneID != "w1:p1" || pane.AgentStatus != StatusIdle {
		t.Fatalf("PaneGet() = %#v", pane)
	}
}

func TestPaneSendText(t *testing.T) {
	text := "sneat-dev/wb#598: checks failed"
	runner := newFakeRunner(t).on([]string{"pane", "send-text", "w1:p2", text}, fakeCall{
		stdout: []byte(`{"id":"cli:pane:send-text","result":{"type":"ok"}}`),
	})
	client := newTestClient(t, runner)

	if err := client.PaneSendText(context.Background(), "w1:p2", text); err != nil {
		t.Fatalf("PaneSendText() error = %v", err)
	}
}

func TestPaneSendTextEmptyPaneID(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t))
	if err := client.PaneSendText(context.Background(), "", "hi"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("PaneSendText(paneID=\"\") error = %v, want ErrUnknownTarget", err)
	}
}

func TestPaneSendTextRejectsInvalidText(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t))
	if err := client.PaneSendText(context.Background(), "w1:p2", "two\nlines"); !errors.Is(err, ErrInvalidPromptText) {
		t.Fatalf("PaneSendText(bad text) error = %v, want ErrInvalidPromptText", err)
	}
}

func TestPaneSendTextPropagatesCommandError(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "send-text", "w9:p9", "hi"}, fakeCall{
		stdout: mustReadTestdata(t, "error_pane_not_found.json"),
	})
	client := newTestClient(t, runner)

	if err := client.PaneSendText(context.Background(), "w9:p9", "hi"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("PaneSendText() error = %v, want ErrUnknownTarget", err)
	}
}

func TestPaneGetEmptyID(t *testing.T) {
	client := newTestClient(t, newFakeRunner(t))
	if _, err := client.PaneGet(context.Background(), ""); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("PaneGet(\"\") error = %v, want ErrUnknownTarget", err)
	}
}

func TestPaneGetNotFound(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "get", "w9:p9"}, fakeCall{
		stdout: mustReadTestdata(t, "error_pane_not_found.json"),
	})
	client := newTestClient(t, runner)

	if _, err := client.PaneGet(context.Background(), "w9:p9"); !errors.Is(err, ErrUnknownTarget) {
		t.Fatalf("PaneGet() error = %v, want ErrUnknownTarget", err)
	}
}

func TestPaneGetInvalidShape(t *testing.T) {
	runner := newFakeRunner(t).on([]string{"pane", "get", "w1:p1"}, fakeCall{
		stdout: []byte(`{"id":"cli:pane:get","result":{"pane":{}}}`),
	})
	client := newTestClient(t, runner)

	if _, err := client.PaneGet(context.Background(), "w1:p1"); !errors.Is(err, ErrUnparseableOutput) {
		t.Fatalf("PaneGet() error = %v, want ErrUnparseableOutput for an empty pane object", err)
	}
}
