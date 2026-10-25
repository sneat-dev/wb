package agents

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDshAdapterDeliversTheTaskAsOneArgument(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	launch, err := (dshHarness{}).Prepare(Launch{
		RunDir: runDir, WorktreeDir: "/work", Model: "deepseek-v4-flash", Task: "run the tests",
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if len(launch.Argv) != 5 || launch.Argv[0] != "--profile" || launch.Argv[1] != dshProfile || launch.Argv[2] != "--patch" {
		t.Fatalf("argv = %#v", launch.Argv)
	}
	if launch.Argv[4] != "run the tests" {
		t.Fatalf("task argument = %q", launch.Argv[4])
	}
	if launch.Stdin != "" {
		t.Fatalf("dsh cannot read a task from stdin, got %q", launch.Stdin)
	}
	patchPath := launch.Argv[3]
	raw, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatalf("read model patch: %v", err)
	}
	for _, required := range []string{"id: agent-default-model", "provider: " + ProviderDshDefault, "model: deepseek-v4-flash"} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("patch %q is missing %q", raw, required)
		}
	}
	info, err := os.Stat(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("patch mode = %v, want 0600", info.Mode().Perm())
	}
	if len(launch.PrivateFiles) != 1 || launch.PrivateFiles[0] != patchPath {
		t.Fatalf("private files = %#v", launch.PrivateFiles)
	}
	if capabilities := (dshHarness{}).Capabilities(); capabilities.TokenUsage != UsageUnavailable || capabilities.Effort != EffortUnsupported || capabilities.TaskDelivery != TaskDeliveryArgv {
		t.Fatalf("capabilities = %#v", capabilities)
	}
}

func TestDshAdapterRefusesTasksItCannotDeliverFaithfully(t *testing.T) {
	t.Parallel()
	harness := dshHarness{}
	runDir := t.TempDir()
	cases := map[string]Launch{
		"no workdir":     {RunDir: runDir, Model: "m", Task: "x"},
		"no model":       {RunDir: runDir, WorktreeDir: "/work", Task: "x"},
		"no run dir":     {WorktreeDir: "/work", Model: "m", Task: "x"},
		"blank task":     {RunDir: runDir, WorktreeDir: "/work", Model: "m", Task: "  "},
		"task too large": {RunDir: runDir, WorktreeDir: "/work", Model: "m", Task: strings.Repeat("a", maxHarnessArgvBytes+1)},
	}
	for name, launch := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := harness.Prepare(launch); err == nil {
				t.Fatal("Prepare accepted a task it cannot deliver")
			}
		})
	}
}

func TestDshAdapterNormalizesATaskStartingWithADash(t *testing.T) {
	t.Parallel()
	// The launcher consumes "--", so an option-shaped task would be parsed as a
	// flag. A leading newline keeps the bytes and the meaning.
	if !dshTaskNormalized("-h fix it") {
		t.Fatal("an option-shaped task must be reported as normalized")
	}
	if dshTaskNormalized("fix it") {
		t.Fatal("an ordinary task is not normalized")
	}
	launch, err := (dshHarness{}).Prepare(Launch{RunDir: t.TempDir(), WorktreeDir: "/work", Model: "m", Task: "-h fix it"})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if launch.Argv[4] != "\n-h fix it" {
		t.Fatalf("normalized task = %q", launch.Argv[4])
	}
}

func TestDshSummaryIsTheFinalMessageAndNothingInvented(t *testing.T) {
	t.Parallel()
	harness := dshHarness{}
	summary := harness.Summarize(strings.NewReader("\n  the answer  \n"))
	if !summary.TurnCompleted || summary.FinalMessage != "the answer" {
		t.Fatalf("summary = %#v", summary)
	}
	if summary.Tokens != nil || summary.CostUSD != nil || summary.ToolCalls != 0 {
		t.Fatalf("dsh reports no usage or tool calls: %#v", summary)
	}
	empty := harness.Summarize(strings.NewReader(""))
	if empty.TurnCompleted || empty.FinalMessage != "" {
		t.Fatalf("an empty stream is not completion evidence: %#v", empty)
	}
	if actions := harness.Actions(strings.NewReader("anything"), 5); actions != nil {
		t.Fatalf("dsh exposes no action stream: %#v", actions)
	}
}

func TestDshModelPatchIsWrittenIntoThePrivateRunDirectory(t *testing.T) {
	t.Parallel()
	runDir := t.TempDir()
	launch, err := (dshHarness{}).Prepare(Launch{RunDir: runDir, WorktreeDir: "/work", Model: "m", Task: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(launch.PrivateFiles[0]) != runDir {
		t.Fatalf("patch %s is not inside the run directory %s", launch.PrivateFiles[0], runDir)
	}
}
