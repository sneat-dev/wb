package agents

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAntigravityLaunchKeepsTaskPrivateAndPreservesPermissions(t *testing.T) {
	t.Parallel()
	adapter, err := HarnessFor(HarnessAntigravity)
	if err != nil || adapter.Name() != HarnessAntigravity {
		t.Fatalf("adapter: %v %v", adapter, err)
	}
	launch := Launch{RunDir: t.TempDir(), WorktreeDir: t.TempDir(), Model: "gemini-test", Effort: "high", Task: "private task\nwith exact bytes", Timeout: 11 * time.Minute}
	prepared, err := adapter.Prepare(launch)
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(prepared.Argv, " ")
	for _, required := range []string{"--mode accept-edits", "--sandbox", "--model gemini-test", "--effort high", "--print-timeout 11m0s", "--add-dir " + filepath.Join(launch.RunDir, "antigravity-brief")} {
		if !strings.Contains(argv, required) {
			t.Errorf("missing %q in %v", required, prepared.Argv)
		}
	}
	if strings.Contains(argv, launch.Task) || strings.Contains(argv, "skip-permissions") || prepared.Stdin != "" || !prepared.SplitStderr {
		t.Fatalf("unsafe launch: %+v", prepared)
	}
	if len(prepared.PrivateFiles) != 1 {
		t.Fatal(prepared.PrivateFiles)
	}
	data, err := os.ReadFile(prepared.PrivateFiles[0])
	if err != nil || string(data) != launch.Task {
		t.Fatalf("task bytes: %q %v", data, err)
	}
	info, err := os.Stat(prepared.PrivateFiles[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("task mode: %v %v", info, err)
	}
	caps := adapter.Capabilities()
	if caps.TaskDelivery != TaskDeliveryPrivateFile || caps.Cost != CostUnavailable || caps.Credential != CredentialHarnessStore || caps.Effort != EffortConfig {
		t.Fatal(caps)
	}
	launch.Effort, launch.Timeout = "", 0
	if _, err := adapter.Prepare(launch); err != nil {
		t.Fatal(err)
	}
	launch.Effort = "xhigh"
	if _, err := adapter.Prepare(launch); err == nil {
		t.Fatal("accepted unsupported effort")
	}
	if _, err := adapter.Prepare(Launch{}); err == nil {
		t.Fatal("accepted empty launch")
	}
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	launch.Effort, launch.RunDir = "", blocked
	if _, err := adapter.Prepare(launch); err == nil {
		t.Fatal("ignored private file failure")
	}
	launch.RunDir = t.TempDir()
	if err := os.MkdirAll(filepath.Join(launch.RunDir, "antigravity-brief", "task.txt"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Prepare(launch); err == nil {
		t.Fatal("ignored unwritable task path")
	}
}

func TestAntigravitySummaryRequiresResultAndPreservesReportedUsage(t *testing.T) {
	t.Parallel()
	stream := `{"event":"init"}
{"event":"step_update","step_update":{"step_index":1,"step_type":"tool","state":"ACTIVE","tool_name":"run_command"}}
{"event":"step_update","step_update":{"step_index":1,"step_type":"tool","state":"DONE","tool_name":"run_command","tool_info":{"parameters":{"CommandLine":"go test ./..."}}}}
{"event":"step_update","step_update":{"step_index":1,"step_type":"tool","state":"DONE","tool_name":"run_command"}}
{"event":"step_update","step_update":{"step_index":2,"step_type":"tool","state":"DONE","tool_name":"write_to_file"}}
{"event":"result","result":{"status":"SUCCESS","response":"done","usage":{"input_tokens":12,"output_tokens":0,"thinking_tokens":3}}}
`
	adapter := antigravityHarness{}
	summary := adapter.Summarize(strings.NewReader(stream))
	if !summary.TurnCompleted || summary.TurnFailed || summary.FinalMessage != "done" || summary.ToolCalls != 2 || summary.Tokens == nil || *summary.Tokens.Input != 12 || *summary.Tokens.Output != 0 || *summary.Tokens.Reasoning != 3 || summary.Tokens.CacheRead != nil || summary.CostUSD != nil {
		t.Fatalf("summary: %+v", summary)
	}
	actions := adapter.Actions(strings.NewReader(stream), 1)
	if len(actions) != 1 || actions[0] != "called tool write_to_file" {
		t.Fatal(actions)
	}
	actions = adapter.Actions(strings.NewReader(stream), 9)
	if len(actions) != 2 || actions[0] != "ran: go test ./..." {
		t.Fatal(actions)
	}
	if actions := adapter.Actions(strings.NewReader(stream), 0); len(actions) != 0 {
		t.Fatal(actions)
	}
	for _, status := range []string{"ERROR", "CANCELED", "INTERRUPTED", "INVALID", "WAITING", "RUNNING", "unknown", ""} {
		summary := adapter.Summarize(strings.NewReader(`{"event":"result","result":{"status":"` + status + `","error":"failed"}}`))
		if summary.TurnCompleted || !summary.TurnFailed || summary.Tokens != nil || len(summary.Diagnostics) != 2 {
			t.Fatalf("%q: %+v", status, summary)
		}
	}
	for _, stream := range []string{"diagnostic\n\n{bad\n{}\n", `{"event":"result","result":{"status":"SUCCESS","usage":{}}}`} {
		if summary := adapter.Summarize(strings.NewReader(stream)); summary.Tokens != nil {
			t.Fatal(summary)
		}
	}
	if summary := adapter.Summarize(strings.NewReader("{bad")); summary.MalformedLines != 1 || summary.TurnCompleted {
		t.Fatal(summary)
	}
	if summary := adapter.Summarize(failingAntigravityReader{}); !summary.TurnFailed || len(summary.Diagnostics) != 2 {
		t.Fatal(summary)
	}
}

type failingAntigravityReader struct{}

func (failingAntigravityReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestAntigravityProfilesAndExecutableResolution(t *testing.T) {
	t.Parallel()
	config := Config{Profiles: map[string]Profile{"agy": {Harness: HarnessAntigravity, Model: "gemini-test", Effort: "medium"}}}
	resolved, err := config.Resolve("agy")
	if err != nil || resolved.Provider != "" || resolved.Harness != HarnessAntigravity {
		t.Fatalf("resolve: %+v %v", resolved, err)
	}
	deps := PreflightDeps{LookPath: func(name string) (string, error) {
		if name != "agy" {
			t.Errorf("executable %q", name)
		}
		return "/bin/agy", nil
	}, Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[0] == "--help" {
			return []byte("--print --output-format --mode --sandbox --model --effort --add-dir --print-timeout"), nil
		}
		return []byte("1.1.13\n"), nil
	}}
	if err := Preflight(context.Background(), resolved, deps); err != nil {
		t.Fatal(err)
	}
	if got := DetectVersion(context.Background(), HarnessAntigravity, deps); got != "1.1.13" {
		t.Fatal(got)
	}
	goodRun := deps.Run
	deps.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("probe failed") }
	if err := Preflight(context.Background(), resolved, deps); err == nil {
		t.Fatal("ignored probe failure")
	}
	deps.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte("old CLI"), nil }
	if err := Preflight(context.Background(), resolved, deps); err == nil {
		t.Fatal("accepted missing capabilities")
	}
	deps.Run = goodRun
	for _, profile := range []Profile{{Harness: HarnessAntigravity, Model: "m", Provider: "google"}, {Harness: HarnessAntigravity, Model: "m", Effort: "xhigh"}} {
		if _, err := config.resolveProfile("bad", profile); err == nil {
			t.Fatal("accepted unsupported profile")
		}
	}
	deps.LookPath = func(string) (string, error) { return "", errors.New("missing") }
	if err := Preflight(context.Background(), resolved, deps); err == nil {
		t.Fatal("ignored missing agy")
	}
}

func TestAntigravityEffortTranslationRejectsUnsupportedLevels(t *testing.T) {
	t.Parallel()
	planner := Planner{}
	for _, effort := range []string{"low", "medium", "high"} {
		var plan Plan
		if err := planner.resolveEffort(&plan, RunRequest{Effort: effort}, Resolved{Harness: HarnessAntigravity}, TaskType{}); err != nil || !plan.EffortApplied || plan.EffortEffective != effort {
			t.Fatalf("effort %q: %+v %v", effort, plan, err)
		}
	}
	if err := planner.resolveEffort(&Plan{}, RunRequest{Effort: "xhigh"}, Resolved{Harness: HarnessAntigravity}, TaskType{}); err == nil {
		t.Fatal("silently applied xhigh")
	}
}

func TestAntigravityPreflightReadsSuccessfulHelpFromStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture requires POSIX sh")
	}
	cli := filepath.Join(t.TempDir(), "agy")
	script := `#!/bin/sh
if [ "$1" = "--help" ]; then
  printf '%s\n' '--print --output-format --mode --sandbox --model --effort --add-dir --print-timeout' >&2
elif [ "$1" = "--version" ]; then
  printf '%s\n' 'diagnostic on stderr' >&2
  printf '%s\n' '1.4.7'
fi
`
	if err := os.WriteFile(cli, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	deps := DefaultPreflightDeps()
	deps.LookPath = func(name string) (string, error) {
		if name != "agy" {
			t.Fatalf("looked up %q", name)
		}
		return cli, nil
	}
	if err := Preflight(context.Background(), Resolved{Harness: HarnessAntigravity, Model: "test-model"}, deps); err != nil {
		t.Fatal(err)
	}
	if got := DetectVersion(context.Background(), HarnessAntigravity, deps); got != "1.4.7" {
		t.Fatalf("version = %q", got)
	}
}
