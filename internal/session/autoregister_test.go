package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// withoutHarnessAncestor pins the process-tree observation off, so a test can
// assert the Unknown fallback regardless of which harness happens to be running
// the test binary. Its counterpart pins one on.
func withoutHarnessAncestor(t *testing.T) {
	t.Helper()
	previous := harnessAncestor
	harnessAncestor = func(int) (int, string) { return 0, "" }
	t.Cleanup(func() { harnessAncestor = previous })
}

func withHarnessAncestor(t *testing.T, pid int, runtime string) {
	t.Helper()
	previous := harnessAncestor
	harnessAncestor = func(int) (int, string) { return pid, runtime }
	t.Cleanup(func() { harnessAncestor = previous })
}

func TestResolveOrRegisterKeepsAnAlreadyRegisteredSessionUntouched(t *testing.T) {
	withoutHarnessAncestor(t)
	dir := filepath.Join(t.TempDir(), "sessions")
	registered, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-declared", Runtime: "codex",
		Model: "gpt-6", StartedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(recordPath(dir, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}

	resolved, registeredAtPark, err := ResolveOrRegisterForProcess(dir, os.Getpid(), AutoRegisterHints{})
	if err != nil {
		t.Fatal(err)
	}
	if registeredAtPark {
		t.Fatal("an already-registered session was re-registered at park")
	}
	if resolved.WBSessionID != registered.WBSessionID || resolved.Runtime != "codex" || resolved.Model != "gpt-6" {
		t.Fatalf("resolved = %#v, want the declared registration", resolved)
	}
	if resolved.RegisteredAtPark {
		t.Fatal("a declared registration was reported as registered at park")
	}
	after, err := os.ReadFile(recordPath(dir, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("resolving an existing session rewrote its registration")
	}
}

func TestResolveOrRegisterRegistersAnUnregisteredProcessWithInferredIdentity(t *testing.T) {
	withHarnessAncestor(t, os.Getpid(), "claude-code")
	t.Setenv("WB_AGENT_PID", "")
	t.Setenv("WB_AGENT_RUNTIME", "")
	t.Setenv("WB_AGENT_MODEL", "")
	dir := filepath.Join(t.TempDir(), "sessions")

	registered, registeredAtPark, err := ResolveOrRegisterForProcess(dir, os.Getpid(), AutoRegisterHints{})
	if err != nil {
		t.Fatal(err)
	}
	if !registeredAtPark {
		t.Fatal("an unregistered process was not registered at park")
	}
	if registered.WBSessionID == "" || !registered.RegisteredAtPark {
		t.Fatalf("registered = %#v, want a stable ID flagged registered_at_park", registered)
	}
	if registered.PID != os.Getpid() || registered.Runtime != "claude-code" {
		t.Fatalf("registered = %#v, want the observed harness PID and runtime", registered)
	}
	// A model nobody declared is admitted as unknown rather than guessed, and
	// the machine is the one fact WB can always see for itself.
	if registered.Model != Unknown || registered.Machine == "" {
		t.Fatalf("registered = %#v, want an unknown model and a resolved machine", registered)
	}
	stored, ok := readRecord(recordPath(dir, registered.PID))
	if !ok || stored.WBSessionID != registered.WBSessionID || !stored.RegisteredAtPark {
		t.Fatalf("stored record = %#v (ok=%t), want the durable park-time registration", stored, ok)
	}
	// The registration is immediately resolvable, so the rest of park attributes
	// its writes to exactly this session.
	resolved, again, err := ResolveOrRegisterForProcess(dir, os.Getpid(), AutoRegisterHints{})
	if err != nil || again || resolved.WBSessionID != registered.WBSessionID {
		t.Fatalf("re-resolve = (%#v, registeredAtPark=%t, err=%v)", resolved, again, err)
	}
}

func TestInferRecordPrefersDeclarationsThenEnvironmentThenUnknown(t *testing.T) {
	withoutHarnessAncestor(t)
	t.Run("explicit declaration wins", func(t *testing.T) {
		t.Setenv("WB_AGENT_PID", "4242")
		t.Setenv("WB_AGENT_RUNTIME", "codex")
		t.Setenv("WB_AGENT_MODEL", "env-model")
		record, err := InferRecordForProcess(os.Getpid(), AutoRegisterHints{PID: 31337, Runtime: "claude-code", Model: "opus"})
		if err != nil {
			t.Fatal(err)
		}
		if record.PID != 31337 || record.Runtime != "claude-code" || record.Model != "opus" {
			t.Fatalf("record = %#v, want the declared identity", record)
		}
	})
	t.Run("environment declaration is used", func(t *testing.T) {
		t.Setenv("WB_AGENT_PID", "4242")
		t.Setenv("WB_AGENT_RUNTIME", "codex")
		t.Setenv("WB_AGENT_MODEL", "env-model")
		record, err := InferRecordForProcess(os.Getpid(), AutoRegisterHints{})
		if err != nil {
			t.Fatal(err)
		}
		if record.PID != 4242 || record.Runtime != "codex" || record.Model != "env-model" {
			t.Fatalf("record = %#v, want the environment declaration", record)
		}
	})
	t.Run("nothing observable is unknown, never a guess", func(t *testing.T) {
		t.Setenv("WB_AGENT_PID", "")
		t.Setenv("WB_AGENT_RUNTIME", "")
		t.Setenv("WB_AGENT_MODEL", "")
		record, err := InferRecordForProcess(os.Getpid(), AutoRegisterHints{})
		if err != nil {
			t.Fatal(err)
		}
		if record.PID != os.Getpid() || record.Runtime != Unknown || record.Model != Unknown {
			t.Fatalf("record = %#v, want the observing process with unknown runtime and model", record)
		}
		if !record.RegisteredAtPark || record.WBSessionID == "" {
			t.Fatalf("record = %#v, want a fresh identity flagged registered_at_park", record)
		}
	})
}

func TestResolveOrRegisterTargetsAnExplicitWBSessionIDWithoutRegistering(t *testing.T) {
	withoutHarnessAncestor(t)
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-target", Runtime: "codex"}); err != nil {
		t.Fatal(err)
	}
	resolved, registeredAtPark, err := ResolveOrRegisterForProcess(dir, os.Getpid(), AutoRegisterHints{WBSessionID: "wbs-target"})
	if err != nil || registeredAtPark || resolved.WBSessionID != "wbs-target" {
		t.Fatalf("targeted resolve = (%#v, registeredAtPark=%t, err=%v)", resolved, registeredAtPark, err)
	}
	if _, _, err := ResolveOrRegisterForProcess(dir, os.Getpid(), AutoRegisterHints{WBSessionID: "wbs-absent"}); err == nil {
		t.Fatal("an unknown WB session ID was accepted, which would invent a session")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("targeting a WB session ID created %d records, want exactly the pre-existing one", len(entries))
	}
}

func TestResolveOrRegisterRefusesToOverwriteAParkedRecordAtTheSamePID(t *testing.T) {
	withHarnessAncestor(t, os.Getpid(), "codex")
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-already-parked", Runtime: "codex"}); err != nil {
		t.Fatal(err)
	}
	if _, err := MarkParked(dir, os.Getpid(), "park-existing"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(recordPath(dir, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	_, registeredAtPark, err := ResolveOrRegisterForProcess(dir, os.Getpid(), AutoRegisterHints{})
	if err == nil {
		t.Fatal("park-time registration overwrote a parked session's registry row")
	}
	if registeredAtPark {
		t.Fatal("a refused registration was reported as registered at park")
	}
	after, err := os.ReadFile(recordPath(dir, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a refused park-time registration mutated the existing record")
	}
}

func TestFindHarnessAncestorNamesOnlyKnownHarnesses(t *testing.T) {
	for name, want := range map[string]string{
		"claude": "claude-code", "Codex": "codex", "copilot.exe": "copilot-cli",
	} {
		if runtime, ok := runtimeForProcessName(name); !ok || runtime != want {
			t.Fatalf("runtimeForProcessName(%q) = (%q, %t), want %q", name, runtime, ok, want)
		}
	}
	// A generic interpreter says nothing about which agent is driving, so it
	// must not be turned into a runtime name.
	for _, name := range []string{"node", "python3", "zsh", "", "wb"} {
		if runtime, ok := runtimeForProcessName(name); ok {
			t.Fatalf("runtimeForProcessName(%q) invented runtime %q", name, runtime)
		}
	}
	if pid, runtime := findHarnessAncestor(1); pid != 0 || runtime != "" {
		t.Fatalf("findHarnessAncestor(1) = (%d, %q), want no ancestor above init", pid, runtime)
	}
}

func TestLookupByWBSessionIDIgnoresParkedAndUnknownSessions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	if _, err := Register(dir, Record{PID: os.Getpid(), WBSessionID: "wbs-live", Runtime: "codex"}); err != nil {
		t.Fatal(err)
	}
	if record, ok := LookupByWBSessionID(dir, "wbs-live"); !ok || record.PID != os.Getpid() {
		t.Fatalf("LookupByWBSessionID(live) = (%#v, %t)", record, ok)
	}
	if _, ok := LookupByWBSessionID(dir, "wbs-missing"); ok {
		t.Fatal("LookupByWBSessionID matched a session that never registered")
	}
	if _, err := MarkParked(dir, os.Getpid(), "park-lookup"); err != nil {
		t.Fatal(err)
	}
	if _, ok := LookupByWBSessionID(dir, "wbs-live"); ok {
		t.Fatal("LookupByWBSessionID returned a parked session as a park target")
	}
}
