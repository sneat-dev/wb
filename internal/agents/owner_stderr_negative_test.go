//go:build e2e

package agents

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// fakeDshStderrScript prints a bounded diagnostic to stderr and a final message
// to stdout. It is the host-side fixture only: it exercises the owner's separate
// stderr redaction/truncation path, not any contained execution.
const fakeDshStderrScript = `#!/bin/sh
printf 'a diagnostic line that is deliberately longer than the log bound\n' >&2
printf 'host final message\n'
exit 0
`

func TestE2ERunOwnerBoundsTheSeparateStderrStream(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("the fake dsh fixture is a POSIX shell script")
	}
	// The host DSH adapter declares a harness-store credential, so this fixture
	// resolves no credential and can run in parallel. Redaction for an injected
	// credential is proved by the fake adapter test below.
	t.Parallel()
	home := t.TempDir()
	store := NewStore(home)
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	runGit(t, worktree, "config", "user.email", "test@example.com")
	runGit(t, worktree, "config", "user.name", "Test")
	script := filepath.Join(t.TempDir(), "dsh")
	if err := testenv.WriteExecutableFile(script, []byte(fakeDshStderrScript), 0o700); err != nil {
		t.Fatalf("write fake dsh: %v", err)
	}
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		AgentID: id, State: StateRunning, RequestedProfile: "ds",
		Resolved: Resolved{
			Profile: "ds", Harness: HarnessDSH, Provider: ProviderDshDefault, Model: "deepseek-flash",
			Routing: Provider{BaseURL: "https://api.deepseek.com", CredentialEnv: "WB_TEST_OWNER_STDERR_KEY", WireAPI: WireAPIChat},
		},
		Task: "run the bounded thing", Worktree: "task-one", WorktreeDir: worktree,
		Repository: "acme/app", WorktreeMode: ModeNew, TimeoutMS: time.Minute.Milliseconds(),
		StartedAt: time.Now().UTC(),
	}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	deps := DefaultOwnerDeps()
	deps.LoadExecutionPolicy = noMandatoryExecutionPolicy
	deps.LookPath = func(name string) (string, error) {
		if name != HarnessDSH {
			t.Errorf("owner looked up %q, want %q", name, HarnessDSH)
		}
		return script, nil
	}
	const logLimit = int64(32)
	if err := runOwnerWithSave(context.Background(), store, id, deps, store.Save, logLimit); err != nil {
		t.Fatalf("runOwnerWithSave: %v", err)
	}
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateCompleted || loaded.ExecutionStatus != StatusCompleted {
		t.Fatalf("record = %s/%s (%q)", loaded.State, loaded.ExecutionStatus, loaded.Failure)
	}
	raw, err := os.ReadFile(filepath.Join(store.Dir(id), stderrFileName))
	if err != nil {
		t.Fatalf("read bounded stderr log: %v", err)
	}
	if int64(len(raw)) > logLimit {
		t.Fatalf("stderr log grew to %d bytes, above the %d-byte bound", len(raw), logLimit)
	}
	truncated := false
	for _, event := range loaded.HarnessEvents {
		if strings.Contains(event, "run stderr log truncated") {
			truncated = true
		}
	}
	if !truncated {
		t.Fatalf("events = %#v, want the stderr truncation recorded", loaded.HarnessEvents)
	}
}

func TestE2EStopRunReportsAFailedCancellationSentinelForAContainedRun(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a root test user can write into a read-only run directory")
	}
	t.Parallel()
	store := NewStore(t.TempDir())
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	policy := executionTestPolicy()
	record := Record{
		AgentID: id, State: StateRunning, ExecutionStatus: StatusQueued,
		Resolved: Resolved{Harness: HarnessDSH, Provider: ProviderDshDefault, Model: "deepseek-flash"},
		Task:     "do the contained thing", WorktreeDir: t.TempDir(),
		TimeoutMS: time.Minute.Milliseconds(), StartedAt: time.Now().UTC(),
		Execution: &policy,
	}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	runDir := store.Dir(id)
	if err := os.Chmod(runDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(runDir, 0o700) })
	_, err = StopRun(store, id, DefaultOwnerDeps())
	if err == nil || !strings.Contains(err.Error(), "record cancellation request") {
		t.Fatalf("error = %v, want the failed cancellation-sentinel write reported", err)
	}
}

func TestE2EOwnerExecutionPolicyDefaultsToTheAuthoritativeLoader(t *testing.T) {
	// Not parallel: the authoritative loader reads XDG_CONFIG_HOME.
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	record := Record{Resolved: Resolved{Harness: HarnessDSH}}
	deps := DefaultOwnerDeps()
	if deps.LoadExecutionPolicy != nil {
		t.Fatal("the fixture must start from the nil-loader default")
	}
	configured, err := ownerExecutionPolicy(record, deps)
	if err != nil {
		t.Fatalf("ownerExecutionPolicy: %v", err)
	}
	if configured != nil {
		t.Fatalf("configured = %#v, want the absent authoritative policy", configured)
	}
}

// splitStderrCredentialHarness is a fake adapter that declares the one
// capability combination the default registry does not expose: a
// WB-injected credential together with a separate stderr stream. The owner
// therefore resolves and redacts the credential on both captured streams.
type splitStderrCredentialHarness struct{}

func (splitStderrCredentialHarness) Name() string { return "split-stderr-injected-credential-fixture" }

func (splitStderrCredentialHarness) Capabilities() HarnessCapabilities {
	return HarnessCapabilities{Credential: CredentialWBInjected, Isolation: IsolationPrivateHome}
}

func (splitStderrCredentialHarness) Prepare(Launch) (HarnessLaunch, error) {
	return HarnessLaunch{Argv: []string{"--fixture-split-stderr"}, SplitStderr: true}, nil
}

func (splitStderrCredentialHarness) Summarize(io.Reader) HarnessSummary {
	// This fixture reports the terminal turn without a stdout final message, so
	// the owner must take the final message from the existing LastMessagePath
	// fallback. That is the only way to prove the final-result redaction: the
	// captured stdout stream is already redacted before it is summarized.
	return HarnessSummary{TurnCompleted: true}
}

func (splitStderrCredentialHarness) Actions(io.Reader, int) []string { return nil }

func TestE2ERunOwnerRedactsAndBoundsBothStreamsForAnInjectedSplitStderrHarness(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("the fake harness fixture is a POSIX shell script")
	}
	t.Parallel()
	const secret = "owner-injected-fixture-secret"
	credentialPath := filepath.Join(t.TempDir(), "provider-credential")
	if err := os.WriteFile(credentialPath, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatalf("write fake provider credential: %v", err)
	}
	script := filepath.Join(t.TempDir(), "split-stderr-harness")
	body := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '%s'\nprintf '%%s' '%s' >&2\nexit 0\n",
		secret+" "+strings.Repeat("o", 64), secret+" "+strings.Repeat("e", 64))
	if err := testenv.WriteExecutableFile(script, []byte(body), 0o700); err != nil {
		t.Fatalf("write fake harness: %v", err)
	}
	home := t.TempDir()
	store := NewStore(home)
	worktree := t.TempDir()
	runGit(t, worktree, "init", "-b", "main")
	runGit(t, worktree, "config", "user.email", "test@example.com")
	runGit(t, worktree, "config", "user.name", "Test")
	id, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	const harnessName = "split-stderr-injected-credential-fixture"
	record := Record{
		AgentID: id, State: StateRunning, RequestedProfile: "ds",
		Resolved: Resolved{
			Profile: "ds", Harness: harnessName, Provider: ProviderDshDefault, Model: "deepseek-flash",
			Routing: Provider{BaseURL: "https://api.deepseek.com", CredentialFile: credentialPath, WireAPI: WireAPIChat},
		},
		Task: "run the bounded thing", Worktree: "task-one", WorktreeDir: worktree,
		Repository: "acme/app", WorktreeMode: ModeNew, TimeoutMS: time.Minute.Milliseconds(),
		StartedAt: time.Now().UTC(),
	}
	if err := store.Create(record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.LastMessagePath(id), []byte("the final message mentions "+secret+"\n"), 0o600); err != nil {
		t.Fatalf("write last message: %v", err)
	}
	deps := DefaultOwnerDeps()
	deps.LoadExecutionPolicy = noMandatoryExecutionPolicy
	deps.LookPath = func(name string) (string, error) {
		if name != harnessName {
			t.Errorf("owner looked up %q, want %q", name, harnessName)
		}
		return script, nil
	}
	deps.harnessFor = func(name string) (Harness, error) {
		if name != harnessName {
			t.Errorf("owner resolved %q, want %q", name, harnessName)
		}
		return splitStderrCredentialHarness{}, nil
	}
	const logLimit = int64(32)
	if err := runOwnerWithSave(context.Background(), store, id, deps, store.Save, logLimit); err != nil {
		t.Fatalf("runOwnerWithSave: %v", err)
	}
	loaded, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State != StateCompleted || loaded.ExecutionStatus != StatusCompleted {
		t.Fatalf("record = %s/%s (%q)", loaded.State, loaded.ExecutionStatus, loaded.Failure)
	}
	if strings.Contains(loaded.Result, secret) {
		t.Fatalf("the recorded result kept the credential: %q", loaded.Result)
	}
	if !strings.Contains(loaded.Result, redactionMarker) {
		t.Fatalf("result = %q, want the redaction marker", loaded.Result)
	}
	events := strings.Join(loaded.HarnessEvents, "\n")
	for _, want := range []string{
		"the final message contained the provider credential and was redacted",
		fmt.Sprintf("run log truncated at %d bytes", logLimit),
		fmt.Sprintf("run stderr log truncated at %d bytes", logLimit),
	} {
		if !strings.Contains(events, want) {
			t.Fatalf("events = %q, want %q", events, want)
		}
	}
	for name, path := range map[string]string{
		"stdout": store.LogPath(id),
		"stderr": filepath.Join(store.Dir(id), stderrFileName),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s log: %v", name, err)
		}
		if int64(len(raw)) > logLimit {
			t.Fatalf("%s log grew to %d bytes, above the %d-byte bound", name, len(raw), logLimit)
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%s log kept the credential: %q", name, raw)
		}
	}
	stderrRaw, err := os.ReadFile(filepath.Join(store.Dir(id), stderrFileName))
	if err != nil {
		t.Fatalf("read stderr log: %v", err)
	}
	if !strings.Contains(string(stderrRaw), redactionMarker) {
		t.Fatalf("stderr log = %q, want the redaction marker", stderrRaw)
	}
}
