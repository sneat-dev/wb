package worktrees

import (
	"strings"
	"testing"
)

func TestIdentityFromEnvReadsADeclaration(t *testing.T) {
	t.Setenv(EnvAgentPID, "4321")
	t.Setenv(EnvAgentRuntime, "claude-code")
	t.Setenv(EnvAgentModel, "claude-sonnet-5")
	t.Setenv(EnvAgentID, "sess-abc")

	identity := IdentityFromEnv()

	if identity.PID != 4321 || identity.Runtime != "claude-code" ||
		identity.Model != "claude-sonnet-5" || identity.AgentID != "sess-abc" {
		t.Fatalf("IdentityFromEnv() = %+v, want all four fields read", identity)
	}
	if !identity.Declared() {
		t.Fatal("Declared() = false for a full declaration")
	}
}

func TestIdentityFromEnvIsUndeclaredWhenUnset(t *testing.T) {
	t.Setenv(EnvAgentPID, "")
	t.Setenv(EnvAgentRuntime, "")
	t.Setenv(EnvAgentModel, "")
	t.Setenv(EnvAgentID, "")

	if identity := IdentityFromEnv(); identity.Declared() {
		t.Fatalf("Declared() = true for %+v, want false", identity)
	}
}

// A malformed PID must leave liveness unknown rather than fail the command it
// was attached to, and must never be recorded as a real process.
func TestIdentityFromEnvRejectsAMalformedPID(t *testing.T) {
	for _, value := range []string{"not-a-number", "0", "-5", " "} {
		t.Run(value, func(t *testing.T) {
			t.Setenv(EnvAgentPID, value)
			t.Setenv(EnvAgentRuntime, "claude-code")

			identity := IdentityFromEnv()

			if identity.PID != 0 {
				t.Fatalf("PID = %d for %q, want 0", identity.PID, value)
			}
			if !identity.Declared() {
				t.Fatal("Declared() = false; the runtime was still declared")
			}
		})
	}
}

func TestAgentComposition(t *testing.T) {
	cases := []struct {
		name        string
		runtime, id string
		want        string
	}{
		{name: "both", runtime: "claude-code", id: "sess-1", want: "claude-code/sess-1"},
		{name: "runtime only", runtime: "claude-code", want: "claude-code"},
		{name: "id only", id: "sess-1", want: "sess-1"},
		{name: "neither", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AgentIdentity{Runtime: tc.runtime, AgentID: tc.id}.Agent()
			if got != tc.want {
				t.Fatalf("Agent() = %q, want %q", got, tc.want)
			}
		})
	}
}

// The warning has to be actionable on its own: an agent reading it should not
// need to consult docs to comply.
func TestUndeclaredOwnerWarningNamesBothRoutes(t *testing.T) {
	warning := UndeclaredOwnerWarning("/tmp/wt")

	for _, want := range []string{
		"/tmp/wt", "wb worktree own",
		EnvAgentPID, EnvAgentRuntime, EnvAgentModel,
	} {
		if !strings.Contains(warning, want) {
			t.Fatalf("warning does not mention %q:\n%s", want, warning)
		}
	}
}

// The environment is the most specific thing a caller said, so a per-command
// override must beat a session-wide registration.
func TestCurrentIdentityPrefersTheEnvironment(t *testing.T) {
	t.Cleanup(func() { SetSessionResolver(nil) })
	t.Setenv(EnvAgentPID, "1111")
	t.Setenv(EnvAgentRuntime, "claude-code")
	SetSessionResolver(func() (AgentIdentity, bool) {
		return AgentIdentity{Runtime: "codex", PID: 2222}, true
	})

	if got := CurrentIdentity(); got.PID != 1111 || got.Runtime != "claude-code" {
		t.Fatalf("CurrentIdentity() = %+v, want the environment declaration", got)
	}
}

// With nothing in the environment, a session registered once at start-up
// attributes the write — which is the point of registering.
func TestCurrentIdentityFallsBackToTheRegisteredSession(t *testing.T) {
	t.Cleanup(func() { SetSessionResolver(nil) })
	t.Setenv(EnvAgentPID, "")
	t.Setenv(EnvAgentRuntime, "")
	t.Setenv(EnvAgentModel, "")
	t.Setenv(EnvAgentID, "")
	SetSessionResolver(func() (AgentIdentity, bool) {
		return AgentIdentity{Runtime: "codex", AgentID: "s1", Model: "m", PID: 2222}, true
	})

	got := CurrentIdentity()
	if got.PID != 2222 || got.Agent() != "codex/s1" {
		t.Fatalf("CurrentIdentity() = %+v, want the registered session", got)
	}
}

func TestCurrentIdentityIsUndeclaredWithNoEnvAndNoSession(t *testing.T) {
	t.Cleanup(func() { SetSessionResolver(nil) })
	t.Setenv(EnvAgentPID, "")
	t.Setenv(EnvAgentRuntime, "")
	t.Setenv(EnvAgentModel, "")
	t.Setenv(EnvAgentID, "")
	SetSessionResolver(func() (AgentIdentity, bool) { return AgentIdentity{}, false })

	if got := CurrentIdentity(); got.Declared() {
		t.Fatalf("CurrentIdentity() = %+v, want undeclared", got)
	}
}

func TestRegisteredIdentityIgnoresAmbientOverrides(t *testing.T) {
	t.Cleanup(func() { SetSessionResolver(nil) })
	t.Setenv(EnvAgentPID, "1111")
	t.Setenv(EnvAgentRuntime, "shell")
	t.Setenv(EnvSessionID, "spoofed")
	SetSessionResolver(func() (AgentIdentity, bool) {
		return AgentIdentity{Runtime: "codex", Model: "gpt-5", PID: 2222, WBSessionID: "wbs-real", Registered: true}, true
	})

	got, ok := RegisteredIdentity()
	if !ok || got.WBSessionID != "wbs-real" || got.PID != 2222 {
		t.Fatalf("RegisteredIdentity() = %+v, %v; want resolver-backed session", got, ok)
	}
}

func TestRegisteredIdentityRejectsUnregisteredResolverResult(t *testing.T) {
	t.Cleanup(func() { SetSessionResolver(nil) })
	SetSessionResolver(func() (AgentIdentity, bool) {
		return AgentIdentity{PID: 2222, WBSessionID: "wbs-untrusted"}, true
	})

	if got, ok := RegisteredIdentity(); ok {
		t.Fatalf("RegisteredIdentity() = %+v, true; want no admission", got)
	}
}
