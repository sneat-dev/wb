package agents

import (
	"encoding/json"
	"strings"
	"testing"
)

func codexOptions() HarnessOptions {
	return HarnessOptions{
		WorktreeDir:  "/tmp/worktrees/task-one",
		Model:        "deepseek-flash",
		Reasoning:    "high",
		ProviderName: "deepseek",
		Provider: Provider{
			BaseURL: "https://api.deepseek.com", CredentialEnv: "DEEPSEEK_API_KEY", WireAPI: WireAPIResponses,
		},
		LastMessagePath: "/tmp/runs/agt-00/last-message.txt",
	}
}

// configValues extracts the -c overrides from an argv as a map, which is how
// the harness itself reads them.
func configValues(t *testing.T, argv []string) map[string]string {
	t.Helper()
	values := map[string]string{}
	for index := 0; index < len(argv); index++ {
		if argv[index] != "-c" || index+1 >= len(argv) {
			continue
		}
		key, value, found := strings.Cut(argv[index+1], "=")
		if !found {
			t.Fatalf("override %q is not key=value", argv[index+1])
		}
		values[key] = value
	}
	return values
}

func TestCodexArgvIsolatesTheChildAndKeepsTheTaskOffArgv(t *testing.T) {
	argv, err := CodexArgv(codexOptions())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, required := range []string{
		"exec", "--ephemeral", "--json", "--ignore-user-config",
		"-C /tmp/worktrees/task-one", "-s workspace-write", "-m deepseek-flash",
		"-o /tmp/runs/agt-00/last-message.txt",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("argv is missing %q: %v", required, argv)
		}
	}
	// The trailing "-" is the harness's documented "read the prompt from
	// stdin" spelling. Without it the worker would run with an empty prompt.
	if argv[len(argv)-1] != "-" {
		t.Fatalf("argv must end with the stdin marker, got %q", argv[len(argv)-1])
	}
	if strings.Contains(joined, "prompt") || strings.Contains(joined, "SK-") {
		t.Fatalf("the task must never be an argument: %v", argv)
	}

	config := configValues(t, argv)
	want := map[string]string{
		"model_provider":                    `"deepseek"`,
		"model_providers.deepseek.name":     `"deepseek"`,
		"model_providers.deepseek.base_url": `"https://api.deepseek.com"`,
		"model_providers.deepseek.wire_api": `"responses"`,
		"model_providers.deepseek.env_key":  `"DEEPSEEK_API_KEY"`,
		"model_reasoning_effort":            `"high"`,
		"approval_policy":                   `"never"`,
		"shell_environment_policy.inherit":  `"core"`,
		"shell_environment_policy.exclude":  `["*KEY*","*TOKEN*","*SECRET*"]`,
	}
	for key, value := range want {
		if config[key] != value {
			t.Errorf("override %s = %s, want %s", key, config[key], value)
		}
	}
	// Only the variable *name* may travel: never a credential value.
	for key, value := range config {
		if strings.Contains(key, "env_key") && strings.Contains(value, "sk-") {
			t.Fatalf("a credential value reached the argument list: %s=%s", key, value)
		}
	}
}

func TestCodexArgvQuotesEveryValueAsTOML(t *testing.T) {
	options := codexOptions()
	options.ProviderName = "openrouter"
	options.Provider = Provider{BaseURL: "https://openrouter.ai/api/v1", CredentialEnv: "OPENROUTER_API_KEY", WireAPI: WireAPIResponses}
	options.Model = "deepseek/deepseek-v4.1-flash"
	argv, err := CodexArgv(options)
	if err != nil {
		t.Fatal(err)
	}
	config := configValues(t, argv)
	// The dotted, versioned model identifier must pass through untouched.
	if !containsArg(argv, options.Model) {
		t.Fatalf("model identifier was rewritten: %v", argv)
	}
	if config["model_providers.openrouter.base_url"] != `"https://openrouter.ai/api/v1"` {
		t.Fatalf("base_url not encoded as a TOML string: %s", config["model_providers.openrouter.base_url"])
	}
}

func containsArg(argv []string, want string) bool {
	for _, arg := range argv {
		if arg == want {
			return true
		}
	}
	return false
}

func TestCodexArgvOmitsReasoningWhenUnset(t *testing.T) {
	options := codexOptions()
	options.Reasoning = ""
	argv, err := CodexArgv(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := configValues(t, argv)["model_reasoning_effort"]; present {
		t.Fatalf("an absent reasoning level must pass no override at all: %v", argv)
	}
	// A reasoning level WB has never heard of must still be passed through:
	// the model, not WB, is the authority on which levels exist.
	options.Reasoning = "max"
	argv, err = CodexArgv(options)
	if err != nil {
		t.Fatal(err)
	}
	if got := configValues(t, argv)["model_reasoning_effort"]; got != `"max"` {
		t.Fatalf("reasoning passthrough = %s", got)
	}
}

func TestCodexArgvOmitsLastMessageWhenUnset(t *testing.T) {
	options := codexOptions()
	options.LastMessagePath = ""
	argv, err := CodexArgv(options)
	if err != nil {
		t.Fatal(err)
	}
	if containsArg(argv, "-o") {
		t.Fatalf("no -o is wanted when no path is configured: %v", argv)
	}
}

func TestCodexArgvRejectsIncompleteOptions(t *testing.T) {
	cases := map[string]func(*HarnessOptions){
		"no working directory": func(o *HarnessOptions) { o.WorktreeDir = "  " },
		"no model":             func(o *HarnessOptions) { o.Model = "" },
		"no wire api":          func(o *HarnessOptions) { o.Provider.WireAPI = "" },
		"no credential env":    func(o *HarnessOptions) { o.Provider.CredentialEnv = "" },
		"no base url":          func(o *HarnessOptions) { o.Provider.BaseURL = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			options := codexOptions()
			mutate(&options)
			if _, err := CodexArgv(options); err == nil {
				t.Fatalf("CodexArgv accepted %s", name)
			}
		})
	}
}

func TestSummarizeEventsExtractsFactsOnly(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t"}`,
		`not json at all: the harness merges stderr into the same file`,
		`{"type":"item.completed","item":{"id":"i0","type":"error","message":"Model metadata not found."}}`,
		`{"type":"item.completed","item":{"id":"i1","type":"command_execution","command":"ls","exit_code":0,"status":"completed"}}`,
		`{"type":"item.completed","item":{"id":"i2","type":"file_change"}}`,
		`{"type":"item.completed","item":{"id":"i3","type":"mcp_tool_call"}}`,
		`{"type":"item.completed","item":{"id":"i4","type":"agent_message","text":"I think this is correct"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":80,"cache_write_input_tokens":5,"output_tokens":10,"reasoning_output_tokens":3}}`,
		``,
	}, "\n")

	summary := SummarizeEvents(strings.NewReader(stream))
	if !summary.TurnCompleted || summary.TurnFailed {
		t.Fatalf("turn flags = %#v", summary)
	}
	if summary.ToolCalls != 3 {
		t.Fatalf("tool calls = %d, want 3", summary.ToolCalls)
	}
	if len(summary.Diagnostics) != 1 || !strings.Contains(summary.Diagnostics[0], "Model metadata") {
		t.Fatalf("diagnostics = %#v", summary.Diagnostics)
	}
	if summary.Usage == nil {
		t.Fatal("usage was not extracted")
	}
	if *summary.Usage != (Usage{InputTokens: 100, CachedInputTokens: 80, CacheWriteInputTokens: 5, OutputTokens: 10, ReasoningOutputTokens: 3}) {
		t.Fatalf("usage = %#v", *summary.Usage)
	}
}

func TestSummarizeEventsReportsFailedTurnsAndMalformedLines(t *testing.T) {
	summary := SummarizeEvents(strings.NewReader("{\"type\":\"turn.failed\"}\n"))
	if !summary.TurnFailed || summary.TurnCompleted || summary.Usage != nil {
		t.Fatalf("failed turn = %#v", summary)
	}
	summary = SummarizeEvents(strings.NewReader("{\"type\":\"error\"}\n"))
	if !summary.TurnFailed {
		t.Fatalf("a top-level error event must fail the turn: %#v", summary)
	}
	// Truncated JSON, an array, and unparsable item shapes must all be skipped
	// rather than treated as a malformed run or as a failure.
	stream := strings.Join([]string{
		`{"type":"item.completed"}`,
		`[1,2]`,
		`{"type":"turn.completed"`,
		`{"type":"item.completed","item":{"type":"error"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":5,"output_tokens":1}}`,
	}, "\n")
	summary = SummarizeEvents(strings.NewReader(stream))
	if summary.ToolCalls != 0 || len(summary.Diagnostics) != 0 || !summary.TurnCompleted {
		t.Fatalf("malformed input handling = %#v", summary)
	}
	if summary.Usage == nil || summary.Usage.InputTokens != 5 {
		t.Fatalf("a valid line after malformed ones must still be parsed: %#v", summary.Usage)
	}
	if result := SummarizeEvents(strings.NewReader("")); result.TurnCompleted || result.Usage != nil {
		t.Fatalf("an empty stream must yield an empty summary: %#v", result)
	}
}

func TestSummarizeEventsReadsTheHarnessUsageFieldNames(t *testing.T) {
	// The field names are the harness's contract; this pins them so a harness
	// rename is caught here rather than silently dropping usage.
	raw := `{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":2,"cache_write_input_tokens":3,"output_tokens":4,"reasoning_output_tokens":5,"extra_future_field":6}}`
	var event map[string]any
	if err := json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatal(err)
	}
	summary := SummarizeEvents(strings.NewReader(raw))
	if summary.Usage == nil || summary.Usage.InputTokens != 1 || summary.Usage.ReasoningOutputTokens != 5 {
		t.Fatalf("usage = %#v", summary.Usage)
	}
}

func TestRecentActionsCondensesWhatTheWorkerDid(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"item.completed","item":{"type":"command_execution","command":"  cat   a.txt "}}`,
		`{"type":"item.completed","item":{"type":"file_change"}}`,
		`{"type":"item.completed","item":{"type":"mcp_tool_call"}}`,
		`{"type":"item.completed","item":{"type":"error","message":"shortened"}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"ignored"}}`,
		`{"type":"turn.completed"}`,
		`{"type":"item.completed"}`,
		`garbage`,
	}, "\n")
	actions := RecentActions(strings.NewReader(stream), 10)
	if len(actions) != 4 {
		t.Fatalf("actions = %#v", actions)
	}
	if actions[0] != "ran: cat a.txt" {
		t.Fatalf("whitespace was not condensed: %q", actions[0])
	}
	if actions[1] != "changed files" || actions[2] != "called a tool" {
		t.Fatalf("actions = %#v", actions)
	}
	if !strings.HasPrefix(actions[3], "diagnostic: ") {
		t.Fatalf("diagnostics must be visible: %q", actions[3])
	}
}

func TestRecentActionsKeepsOnlyTheRequestedWindow(t *testing.T) {
	var builder strings.Builder
	for index := 0; index < 10; index++ {
		builder.WriteString(`{"type":"item.completed","item":{"type":"command_execution","command":"cmd-`)
		builder.WriteString(string(rune('0' + index)))
		builder.WriteString(`"}}` + "\n")
	}
	actions := RecentActions(strings.NewReader(builder.String()), 3)
	if len(actions) != 3 {
		t.Fatalf("window size = %d, want 3", len(actions))
	}
	if actions[2] != "ran: cmd-9" || actions[0] != "ran: cmd-7" {
		t.Fatalf("window kept the wrong actions: %#v", actions)
	}
	if got := RecentActions(strings.NewReader(builder.String()), 0); got != nil {
		t.Fatalf("a zero window must return nothing: %#v", got)
	}
	long := strings.Repeat("x", 1000)
	condensed := condense(long)
	if len(condensed) > maxActionDetail+3 || !strings.HasSuffix(condensed, "…") {
		t.Fatalf("condense did not bound the line: %d", len(condensed))
	}
}
