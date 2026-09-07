package testenv

import (
	"os"
	"testing"

	"github.com/sneat-dev/wb/internal/envguard"
)

func TestIsolateClearsAgentVarsAndPinsGoworkOff(t *testing.T) {
	t.Setenv("WB_AGENT_ID", "outer-session")
	t.Setenv("WB_AGENT_PID", "999")
	t.Setenv("WB_AGENT_RUNTIME", "claude-code")
	t.Setenv("WB_AGENT_MODEL", "claude-sonnet-5")
	t.Setenv("GOWORK", "/ambient/go.work")

	Isolate(t)

	for _, name := range []string{"WB_AGENT_ID", "WB_AGENT_PID", "WB_AGENT_RUNTIME", "WB_AGENT_MODEL"} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("%s = %q after Isolate, want empty", name, value)
		}
	}
	if value := os.Getenv("GOWORK"); value != "off" {
		t.Fatalf("GOWORK = %q after Isolate, want %q", value, "off")
	}
}

func TestIsolateTrulyUnsetsAgentVarsAndRestoresAfterTest(t *testing.T) {
	// envguard.Inspect detects an agent var by key presence in the
	// environment slice, not by value -- a t.Setenv(name, "") that leaves
	// the key present with an empty value would still be detected. This
	// test proves the key is gone while the outer test runs, and that the
	// original value comes back once it completes.
	t.Setenv("WB_AGENT_ID", "outer-session")
	t.Setenv("WB_AGENT_PID", "999")

	t.Run("during", func(t *testing.T) {
		Isolate(t)
		inputs := envguard.Inspect(os.Environ())
		if len(inputs.AgentVars) != 0 {
			t.Fatalf("AgentVars = %v after Isolate, want none", inputs.AgentVars)
		}
		if _, present := os.LookupEnv("WB_AGENT_ID"); present {
			t.Fatal("WB_AGENT_ID key still present in os.Environ() after Isolate")
		}
	})

	if value := os.Getenv("WB_AGENT_ID"); value != "outer-session" {
		t.Fatalf("WB_AGENT_ID = %q after subtest completed, want restored %q", value, "outer-session")
	}
	if value := os.Getenv("WB_AGENT_PID"); value != "999" {
		t.Fatalf("WB_AGENT_PID = %q after subtest completed, want restored %q", value, "999")
	}
}

func TestIsolateLetsATestSetItsOwnAgentVarAfterward(t *testing.T) {
	t.Setenv("WB_AGENT_ID", "outer-session")
	Isolate(t)
	// A test that intentionally exercises agent-mode behavior sets its own
	// value after Isolate, and that is the one the code under test observes.
	t.Setenv("WB_AGENT_ID", "intentional-session")
	if value := os.Getenv("WB_AGENT_ID"); value != "intentional-session" {
		t.Fatalf("WB_AGENT_ID = %q, want the value set after Isolate", value)
	}
}

func TestIsolateProcessClearsAgentVarsAndPinsGoworkOff(t *testing.T) {
	// IsolateProcess uses os.Setenv/Unsetenv directly (it is meant for
	// TestMain, which has no *testing.T to restore through), so this test
	// restores the environment itself via t.Setenv/t.Cleanup rather than
	// relying on automatic restoration.
	t.Setenv("WB_AGENT_ID", "outer-session")
	t.Setenv("GOWORK", "/ambient/go.work")

	IsolateProcess()

	if value := os.Getenv("WB_AGENT_ID"); value != "" {
		t.Fatalf("WB_AGENT_ID = %q after IsolateProcess, want empty", value)
	}
	if value := os.Getenv("GOWORK"); value != "off" {
		t.Fatalf("GOWORK = %q after IsolateProcess, want %q", value, "off")
	}
}
