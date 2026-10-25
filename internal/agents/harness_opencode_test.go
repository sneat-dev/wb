package agents

import (
	"strings"
	"testing"
)

func TestOpenCodeAdapterBuildsAStandaloneNonInteractiveRun(t *testing.T) {
	t.Parallel()
	harness := openCodeHarness{}
	launch, err := harness.Prepare(Launch{WorktreeDir: "/work", Model: "opencode-go/deepseek-v4.1-flash", Variant: "high", Task: "do it"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	joined := strings.Join(launch.Argv, " ")
	for _, required := range []string{"run", "--standalone", "--auto", "--format json", "--model opencode-go/deepseek-v4.1-flash#high"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("argv %q is missing %q", joined, required)
		}
	}
	if strings.Contains(joined, "--server") {
		t.Fatalf("argv %q must never attach to a shared server", joined)
	}
	if launch.Stdin != "do it" {
		t.Fatalf("task must travel on stdin, got %q", launch.Stdin)
	}
	if capabilities := harness.Capabilities(); capabilities.TaskDelivery != TaskDeliveryStdin || capabilities.Cost != CostHarnessEstimated {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

func TestOpenCodeAdapterRefusesUnsafeLaunchInputs(t *testing.T) {
	t.Parallel()
	harness := openCodeHarness{}
	cases := map[string]Launch{
		"no workdir":       {Model: "p/m"},
		"no model":         {WorktreeDir: "/work"},
		"variant in model": {WorktreeDir: "/work", Model: "p/m#high"},
	}
	for name, launch := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := harness.Prepare(launch); err == nil {
				t.Fatal("Prepare accepted an unsafe launch")
			}
		})
	}
}

func TestOpenCodeSummaryRequiresEvidenceAndReportsUsageAndCost(t *testing.T) {
	t.Parallel()
	harness := openCodeHarness{}
	stream := strings.Join([]string{
		`{"type":"step_start"}`,
		`{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed","input":{"command":"echo hi"}}}}`,
		`{"type":"step_finish","part":{"cost":0.0025,"tokens":{"input":100,"output":10,"reasoning":2,"cache":{"read":40,"write":1}}}}`,
		`{"type":"text","part":{"type":"text","text":"hi"}}`,
		`not json at all`,
	}, "\n")
	summary := harness.Summarize(strings.NewReader(stream))
	if !summary.TurnCompleted || summary.TurnFailed || summary.MalformedLines != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.ToolCalls != 1 || summary.FinalMessage != "hi" {
		t.Fatalf("tools/message = %d/%q", summary.ToolCalls, summary.FinalMessage)
	}
	if summary.Tokens == nil || *summary.Tokens.Input != 100 || *summary.Tokens.CacheRead != 40 || *summary.Tokens.CacheWrite != 1 || *summary.Tokens.Reasoning != 2 {
		t.Fatalf("tokens = %#v", summary.Tokens)
	}
	if summary.CostUSD == nil || *summary.CostUSD != 0.0025 {
		t.Fatalf("cost = %v", summary.CostUSD)
	}
}

func TestOpenCodeSummaryTreatsAnErrorEventAsAFailedTurn(t *testing.T) {
	t.Parallel()
	harness := openCodeHarness{}
	summary := harness.Summarize(strings.NewReader(`{"type":"error","error":{"type":"provider.no-route","message":"Model unavailable"}}` + "\n"))
	if !summary.TurnFailed || summary.TurnCompleted {
		t.Fatalf("summary = %#v", summary)
	}
	if len(summary.Diagnostics) != 1 || !strings.Contains(summary.Diagnostics[0], "provider.no-route") {
		t.Fatalf("diagnostics = %#v", summary.Diagnostics)
	}
}

func TestOpenCodeSummaryRejectsAMalformedEventAndAZeroCostWithTokens(t *testing.T) {
	t.Parallel()
	harness := openCodeHarness{}
	summary := harness.Summarize(strings.NewReader("{\"type\":\"text\",\"part\":{\"text\":\"x\"}}\n{\"type\":\n"))
	if summary.MalformedLines != 1 || !summary.TurnCompleted {
		t.Fatalf("malformed summary = %#v", summary)
	}
	zero := harness.Summarize(strings.NewReader(`{"type":"step_finish","part":{"cost":0,"tokens":{"input":10,"output":5}}}` + "\n"))
	if zero.CostUSD != nil || zero.CostNote == "" {
		t.Fatalf("a zero cost with tokens must be recorded as unusable: %#v", zero)
	}
	noEvidence := harness.Summarize(strings.NewReader(`{"type":"step_start"}` + "\n"))
	if noEvidence.TurnCompleted {
		t.Fatal("a stream with no text or step_finish is not a completed turn")
	}
}

func TestOpenCodeActionsCondenseToolUseAndErrors(t *testing.T) {
	t.Parallel()
	stream := strings.Join([]string{
		`{"type":"tool_use","part":{"tool":"shell","state":{"status":"completed","input":{"command":"  cat   a.txt "}}}}`,
		`{"type":"tool_use","part":{"tool":"read","state":{"status":"completed"}}}`,
		`{"type":"tool_use","part":{"tool":"shell"}}`,
		`{"type":"error","error":{"type":"provider.no-route","message":"nope"}}`,
		`garbage`,
	}, "\n")
	actions := openCodeHarness{}.Actions(strings.NewReader(stream), 10)
	if len(actions) != 3 {
		t.Fatalf("actions = %#v", actions)
	}
	if actions[0] != "ran: cat a.txt" {
		t.Fatalf("whitespace was not condensed: %q", actions[0])
	}
	if actions[1] != "called tool read" || !strings.Contains(actions[2], "provider.no-route") {
		t.Fatalf("actions = %#v", actions)
	}
	if got := (openCodeHarness{}).Actions(strings.NewReader(stream), 0); got != nil {
		t.Fatalf("a zero limit must produce no actions: %#v", got)
	}
}
