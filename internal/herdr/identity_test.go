package herdr

import (
	"os"
	"testing"
)

func TestReadIdentityFull(t *testing.T) {
	lookup := lookupFromMap(map[string]string{
		envPaneID:              "w1:p2",
		envWorkspaceID:         "w1",
		envTabID:               "w1:t2",
		envSocketPath:          "/Users/fake/.config/herdr/herdr.sock",
		envBinPath:             "/Users/fake/.local/bin/herdr",
		envClaudeCodeSessionID: "00000000-0000-4000-8000-000000000099",
		envClaudePID:           "12345",
	})

	id := ReadIdentity(lookup)

	want := Identity{
		PaneID:           "w1:p2",
		WorkspaceID:      "w1",
		TabID:            "w1:t2",
		SocketPath:       "/Users/fake/.config/herdr/herdr.sock",
		BinPath:          "/Users/fake/.local/bin/herdr",
		Harness:          HarnessClaudeCode,
		HarnessSessionID: "00000000-0000-4000-8000-000000000099",
		HarnessPID:       "12345",
	}
	if id != want {
		t.Fatalf("ReadIdentity() = %#v, want %#v", id, want)
	}
	if !id.InHerdr() {
		t.Fatal("InHerdr() = false, want true when HERDR_PANE_ID is set")
	}
}

func TestReadIdentityEmptyEnvironment(t *testing.T) {
	id := ReadIdentity(lookupFromMap(nil))

	if id != (Identity{}) {
		t.Fatalf("ReadIdentity(empty) = %#v, want zero value", id)
	}
	if id.InHerdr() {
		t.Fatal("InHerdr() = true, want false outside herdr")
	}
}

func TestReadIdentityOutsideHerdrWithHarnessOnly(t *testing.T) {
	// A Claude Code session running in a plain terminal, not inside herdr:
	// harness fields populate, herdr fields do not, InHerdr is false.
	lookup := lookupFromMap(map[string]string{
		envClaudeCodeSessionID: "00000000-0000-4000-8000-0000000000aa",
		envClaudePID:           "999",
	})

	id := ReadIdentity(lookup)

	if id.InHerdr() {
		t.Fatal("InHerdr() = true, want false without HERDR_PANE_ID")
	}
	if id.Harness != HarnessClaudeCode || id.HarnessSessionID == "" {
		t.Fatalf("harness fields not populated: %#v", id)
	}
}

func TestReadIdentityClaudeCodeSessionIDEmptyStringDoesNotSetHarness(t *testing.T) {
	// CLAUDE_CODE_SESSION_ID set but empty must not be treated as "present".
	lookup := lookupFromMap(map[string]string{
		envClaudeCodeSessionID: "",
	})

	id := ReadIdentity(lookup)

	if id.Harness != "" {
		t.Fatalf("Harness = %q, want empty when CLAUDE_CODE_SESSION_ID is empty", id.Harness)
	}
}

func TestReadIdentityNoCodexEquivalent(t *testing.T) {
	// Documents the brief's constraint: no Codex environment variable is
	// read because none is verified/documented. A CODEX_* variable must be
	// ignored until a later change adds it deliberately.
	lookup := lookupFromMap(map[string]string{
		"CODEX_SESSION_ID": "should-be-ignored",
	})

	id := ReadIdentity(lookup)

	if id.Harness != "" || id.HarnessSessionID != "" {
		t.Fatalf("unexpected Codex harness detection: %#v", id)
	}
}

func TestOSLookupEnvReadsRealEnvironment(t *testing.T) {
	t.Setenv("WB_HERDR_TEST_PROBE", "present")

	value, ok := OSLookupEnv("WB_HERDR_TEST_PROBE")
	if !ok || value != "present" {
		t.Fatalf("OSLookupEnv(set) = (%q, %v), want (\"present\", true)", value, ok)
	}

	if _, ok := os.LookupEnv("WB_HERDR_TEST_PROBE_DEFINITELY_UNSET"); ok {
		t.Skip("ambient environment unexpectedly sets the unset-probe variable")
	}
	if _, ok := OSLookupEnv("WB_HERDR_TEST_PROBE_DEFINITELY_UNSET"); ok {
		t.Fatal("OSLookupEnv(unset) reported present")
	}
}
