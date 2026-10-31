//go:build e2e && linux

package sessionlaunch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/herdr"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// The owned test binary supplies WB's real private early-dispatch boundary and
// a deterministic interactive harness, avoiding a second nested project build.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == PrivateLauncherArgument {
		stage := "wrapper-enter"
		if len(os.Args) > 2 && os.Args[2] == "--supervise" {
			stage = "supervisor-enter"
		}
		writeOwnedFixtureStage(stage, nil)
		originalGroup := unix.Getpgrp()
		var originalState *term.State
		if stage == "supervisor-enter" && os.Getenv("WB_TEST_HERDR_EXIT_GATE") == "owned" {
			var err error
			originalState, err = term.GetState(int(os.Stdin.Fd()))
			if err != nil {
				writeOwnedFixtureStage("supervisor-original-termios-failed", err)
				os.Exit(105)
			}
		}
		status := RunPrivateLauncher(os.Args[2:])
		if stage == "supervisor-enter" && os.Getenv("WB_TEST_HERDR_EXIT_GATE") == "owned" {
			status = holdOwnedSupervisorAfterRestore(status, originalGroup, originalState)
		}
		os.Exit(status)
	}
	if os.Getenv("WB_TEST_HERDR_FIXTURE_ROLE") == "current-probe" {
		os.Exit(runHerdrFixtureCurrentProbe())
	}
	if os.Getenv("WB_TEST_HERDR_FIXTURE_ROLE") == "harness" {
		os.Exit(runHerdrFixtureHarness())
	}
	os.Exit(m.Run())
}

// Test-only hold after production cleanup/restoration returns. The original
// Herdr PTY child is still alive here, so its restored foreground can be proved
// before the pinned server intentionally removes the pane on process exit.
func holdOwnedSupervisorAfterRestore(status, originalGroup int, originalState *term.State) int {
	root := os.Getenv("WB_TEST_HERDR_ROOT")
	foreground, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
	group := unix.Getpgrp()
	generation, known := session.ObserveProcessGeneration(os.Getpid())
	if !filepath.IsAbs(root) || err != nil || !known || foreground != group || group != originalGroup {
		writeOwnedFixtureStage("supervisor-restore-proof-failed", err)
		return 105
	}
	restoredState, err := term.GetState(int(os.Stdin.Fd()))
	if err != nil || originalState == nil || !reflect.DeepEqual(originalState, restoredState) {
		writeOwnedFixtureStage("supervisor-termios-restore-failed", err)
		return 108
	}
	proof, _ := json.Marshal(ownedSupervisorRestore{os.Getpid(), group, foreground, generation, status, true})
	if err := filewrite.WriteBytesAtomic(root, "supervisor-restored.json", proof, 0600); err != nil {
		return 106
	}
	writeOwnedFixtureStage("supervisor-restored-held", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		if raw, err := os.ReadFile(filepath.Join(root, "supervisor-exit-release")); err == nil && string(raw) == "owned-exit-release" {
			return status
		}
		select {
		case <-ctx.Done():
			writeOwnedFixtureStage("supervisor-exit-gate-timeout", ctx.Err())
			return 107
		case <-time.After(10 * time.Millisecond):
		}
	}
}

type ownedSupervisorRestore struct {
	PID               int       `json:"pid"`
	Group             int       `json:"group"`
	Foreground        int       `json:"foreground"`
	Generation        time.Time `json:"generation"`
	Status            int       `json:"status"`
	TermModesRestored bool      `json:"term_modes_restored"`
}

func runHerdrFixtureHarness() (status int) {
	writeOwnedFixtureStage("harness-enter", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	identity := herdr.ReadIdentity(os.LookupEnv)
	oldTerminal, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		writeOwnedFixtureStage("raw-terminal-failed", err)
		return 95
	}
	defer func() {
		if err := term.Restore(int(os.Stdin.Fd()), oldTerminal); err != nil {
			writeOwnedFixtureStage("harness-termios-restore-failed", err)
			status = 109
		}
	}()
	foreground, err := unix.IoctlGetInt(int(os.Stdin.Fd()), unix.TIOCGPGRP)
	if err != nil || foreground != os.Getpid() {
		writeOwnedFixtureStage("foreground-mismatch", err)
		return 96
	}
	// The fixture consumes raw protocol bytes rather than emulating Codex's
	// bracketed-paste parser. Disable this owned terminal mode explicitly.
	if _, err := os.Stdout.WriteString("\x1b[?2004l"); err != nil {
		return 97
	}

	if _, err := os.Stat(os.Getenv("WB_TEST_HERDR_ATTACH")); err != nil {
		return 90
	}
	directory := filepath.Join(os.Getenv("WB_TEST_HERDR_ROOT"), ".wb", session.DirName)
	initial, live := session.Lookup(directory, os.Getpid())
	if !live {
		writeOwnedFixtureStage("startup-registration-missing", nil)
		return 100
	}
	initial.NativeHarnessID = "native-wb-owned-fixture"
	initial.Model = "fixture-known-model"
	before, err := os.ReadFile(filepath.Join(directory, fmt.Sprint(os.Getpid())+".json"))
	if err != nil {
		return 110
	}
	for _, identity := range []struct{ thread, root string }{{initial.NativeHarnessID, initial.NativeHarnessID}, {"native-prebinding-child", initial.NativeHarnessID}, {"native-prebinding-second-root", "native-prebinding-second-root"}} {
		_ = os.Setenv("CODEX_THREAD_ID", identity.thread)
		_ = os.Setenv("CODEX_SESSION_ID", identity.root)
		candidate := initial
		candidate.NativeHarnessID = identity.thread
		if _, err := session.RegisterCaller(directory, candidate); !errors.Is(err, session.ErrNativeBindingPending) {
			writeOwnedFixtureStage("startup-unbound-registration-not-pending", err)
			return 101
		}
		if _, created, err := session.ResolveOrRegisterForProcess(directory, os.Getpid(), session.AutoRegisterHints{PID: os.Getpid(), Runtime: "codex", Model: session.Unknown, NativeHarnessID: identity.thread}); !errors.Is(err, session.ErrNativeBindingPending) || created {
			writeOwnedFixtureStage("startup-unbound-autoregistration-not-pending", err)
			return 115
		}
		if after, err := os.ReadFile(filepath.Join(directory, fmt.Sprint(os.Getpid())+".json")); err != nil || !bytes.Equal(before, after) {
			return 111
		}
		views, err := session.List(directory)
		if err != nil || len(views) != 1 {
			return 116
		}
	}
	_ = os.Setenv("CODEX_THREAD_ID", initial.NativeHarnessID)
	_ = os.Setenv("CODEX_SESSION_ID", initial.NativeHarnessID)
	writeOwnedFixtureStage("harness-registration-pending-until-observation", nil)
	custom := exec.CommandContext(ctx, os.Getenv("WB_TEST_HERDR_BINARY"), "pane", "report-agent-session", identity.PaneID, "--source", "wb-owned-fixture", "--agent", "codex", "--agent-session-id", "native-wb-owned-fixture")
	if err := custom.Run(); err != nil {
		writeOwnedFixtureStage("custom-source-report-failed", err)
		return 102
	}
	observer, err := herdr.NewClient(os.LookupEnv, herdr.WithSocketPath(identity.SocketPath))
	if err != nil {
		writeOwnedFixtureStage("native-observer-failed", err)
		return 103
	}
	customPane, err := observer.PaneGet(ctx, identity.PaneID)
	if err != nil || customPane.AgentSession.HarnessSessionID() != "" {
		writeOwnedFixtureStage("custom-source-gained-native-authority", err)
		return 104
	}
	writeOwnedFixtureStage("custom-source-not-native-authority", nil)

	for _, args := range [][]string{
		{"pane", "report-agent-session", identity.PaneID, "--source", "herdr:codex", "--agent", "codex", "--agent-session-id", "native-wb-owned-fixture"},
		{"pane", "report-agent", identity.PaneID, "--source", "herdr:codex", "--agent", "codex", "--state", "idle", "--agent-session-id", "native-wb-owned-fixture"},
	} {
		command := exec.CommandContext(ctx, os.Getenv("WB_TEST_HERDR_BINARY"), args...)
		if err := command.Run(); err != nil {
			writeOwnedFixtureStage("report-failed", err)
			return 91
		}
	}
	// The plan-owned parent binds actual Herdr observation. A caller cannot
	// pick a root from an unknown-native shared process, even with matching
	// public lineage. Retry only once that independently observed binding exists.
	for {
		observed, live := session.Lookup(directory, os.Getpid())
		if live && observed.NativeHarnessID == initial.NativeHarnessID {
			break
		}
		select {
		case <-ctx.Done():
			writeOwnedFixtureStage("startup-native-binding-timeout", ctx.Err())
			return 112
		case <-time.After(10 * time.Millisecond):
		}
	}
	registered, err := session.RegisterCaller(directory, initial)
	if err != nil || registered.WBSessionID != initial.WBSessionID || registered.Scope != "" {
		writeOwnedFixtureStage("startup-bound-registration-failed", err)
		return 113
	}
	writeOwnedFixtureStage("harness-reregistered-after-observed-binding", nil)
	probe := exec.CommandContext(ctx, os.Getenv("WB_TEST_HERDR_WB_BINARY"))
	probe.Env = append(os.Environ(), "WB_TEST_HERDR_FIXTURE_ROLE=current-probe", "WB_TEST_EXPECTED_WBID="+initial.WBSessionID, "CODEX_THREAD_ID="+initial.NativeHarnessID, "CODEX_SESSION_ID=owned-distinct-root-context")
	if err := probe.Run(); err != nil {
		writeOwnedFixtureStage("current-cli-probe-failed", err)
		return 114
	}
	if err := os.WriteFile(os.Getenv("WB_TEST_HERDR_READY"), []byte("harness started after gate\n"), 0o600); err != nil {
		return 92
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\r')
	if err != nil {
		return 93
	}

	if reader.Buffered() != 0 {
		return 98
	}
	poll := []unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}
	if count, err := unix.Poll(poll, 250); err != nil || count != 0 {
		writeOwnedFixtureStage("extra-submission-input", err)
		return 99
	}
	if err := os.WriteFile(os.Getenv("WB_TEST_HERDR_CAPTURE"), []byte(line), 0o600); err != nil {
		return 94
	}
	return 17
}

// This is an actual WB-shaped tool child of the owned copied codex binary.
// Production kernel ancestry and per-thread namespace resolution are exercised;
// no fake observed process or native alias resolver is injected here.
func runHerdrFixtureCurrentProbe() int {
	root := os.Getenv("WB_TEST_HERDR_ROOT")
	directory := filepath.Join(root, ".wb", session.DirName)
	parent := os.Getppid()
	original, found, err := session.ResolveCurrentForProcess(directory, os.Getpid())
	if err != nil || !found || original.PID != parent || original.WBSessionID != os.Getenv("WB_TEST_EXPECTED_WBID") || original.Scope != "" {
		return 120
	}
	before, err := os.ReadFile(filepath.Join(directory, fmt.Sprint(parent)+".json"))
	if err != nil {
		return 121
	}
	identities := []session.Record{original}
	for _, identity := range []struct{ thread, root string }{{"native-owned-descendant", original.NativeHarnessID}, {"native-owned-second-root", "native-owned-second-root"}} {
		_ = os.Setenv("CODEX_THREAD_ID", identity.thread)
		_ = os.Setenv("CODEX_SESSION_ID", identity.root)
		native, err := session.CallerNativeID("codex", os.LookupEnv)
		if err != nil || native != identity.thread {
			return 122
		}
		candidate := session.Record{PID: parent, Runtime: "codex", Model: session.Unknown, NativeHarnessID: native, WBSessionID: original.WBSessionID, Machine: original.Machine, Terminal: original.Terminal}
		if _, err := session.RegisterCaller(directory, candidate); err == nil {
			return 123
		}
		candidate.WBSessionID = ""
		registered, err := session.RegisterCaller(directory, candidate)
		if err != nil || registered.WBSessionID == original.WBSessionID || registered.Scope == "" {
			return 124
		}
		resolved, found, err := session.ResolveCurrentForProcess(directory, os.Getpid())
		if err != nil || !found || resolved.WBSessionID != registered.WBSessionID || resolved.NativeHarnessID != native || resolved.ProcessStartedAt != original.ProcessStartedAt {
			return 125
		}
		identities = append(identities, resolved)
	}
	if after, err := os.ReadFile(filepath.Join(directory, fmt.Sprint(parent)+".json")); err != nil || !bytes.Equal(before, after) {
		return 126
	}
	if identities[1].WBSessionID == identities[2].WBSessionID || identities[1].Scope == identities[2].Scope {
		return 127
	}
	raw, err := json.Marshal(identities)
	if err != nil {
		return 128
	}
	if err := filewrite.WriteBytesAtomic(root, "current-cli-probe.json", raw, 0600); err != nil {
		return 129
	}
	return 0
}

// TestE2EOriginalHerdrNativeJourney opts into the pinned, original TUI server.
// Root supplies a hash-verified Linux executable only inside the capped worker.
// Total deadline is105s plus at most10s of exact owned-server cleanup.
func TestE2EOriginalHerdrNativeJourney(t *testing.T) {
	binary := os.Getenv("WB_TEST_HERDR_BINARY")
	if binary == "" {
		t.Skip("set WB_TEST_HERDR_BINARY to pinned original Herdr0.9.1 in capped Linux")
	}
	root := t.TempDir()
	fixtureWB, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	runtimeDir := filepath.Join(root, "runtime")
	for _, path := range []string{home, runtimeDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	socket := filepath.Join(runtimeDir, "owned.sock")
	for key, value := range map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_RUNTIME_DIR": runtimeDir, "HERDR_CONFIG_PATH": filepath.Join(home, "config.toml"), "HERDR_SOCKET_PATH": socket, "HERDR_BIN_PATH": binary, "HERDR_SESSION": "", "HERDR_PANE_ID": "", "HERDR_TAB_ID": "", "HERDR_WORKSPACE_ID": "", "HERDR_CLIENT_SOCKET_PATH": "", "SHELL": "/bin/sh", "TERM": "xterm-256color"} {
		t.Setenv(key, value)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 105*time.Second)
	defer cancel()
	client, err := herdr.NewClient(os.LookupEnv, herdr.WithSocketPath(socket))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.EnsureTUIProtocol(ctx); err != nil {
		t.Fatal(err)
	}
	attach, ready, capture := filepath.Join(root, "attached"), filepath.Join(root, "ready"), filepath.Join(root, "capture")
	for key, value := range map[string]string{"WB_TEST_HERDR_FIXTURE_ROLE": "harness", "WB_TEST_HERDR_EXIT_GATE": "owned", "WB_TEST_HERDR_WB_BINARY": fixtureWB, "WB_TEST_HERDR_ROOT": root, "WB_TEST_HERDR_ATTACH": attach, "WB_TEST_HERDR_READY": ready, "WB_TEST_HERDR_CAPTURE": capture, "WB_TEST_HERDR_STAGE": filepath.Join(root, "fixture-stage.jsonl")} {
		t.Setenv(key, value)
	}
	server := exec.CommandContext(ctx, binary, "server")
	server.Env = os.Environ()
	stdout, stderr := &terminalDiagnostic{}, &terminalDiagnostic{}
	server.Stdout, server.Stderr = stdout, stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	stage := "server-started"
	defer func() {
		if t.Failed() {
			retainOwnedHerdrFailure(t, binary, server.Env, root, stage, stdout.snapshot(), stderr.snapshot())
		}
	}()
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	t.Cleanup(func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		command := exec.CommandContext(stopCtx, binary, "server", "stop")
		command.Env = server.Env
		_ = command.Run()
		select {
		case <-done:
			return
		case <-time.After(3 * time.Second):
			_ = server.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("owned Herdr server did not stop within cleanup bound")
		}
	})
	readyCtx, readyCancel := context.WithTimeout(ctx, 20*time.Second)
	defer readyCancel()
	for {
		if _, err := client.PaneList(readyCtx, ""); err == nil {
			break
		}
		select {
		case <-readyCtx.Done():
			t.Fatalf("owned Herdr server not ready: %v", readyCtx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	stage = "server-ready"
	// This second owned pane is a foreign-to-launch shell sentinel. Cleanup of
	// the launched process tree must leave its exact identity/generation intact.
	foreign, err := client.WorkspaceCreate(ctx, root, "owned-unrelated-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	foreignBefore, err := client.PaneProcessInfo(ctx, foreign.Pane.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	foreignGeneration, known := session.ObserveProcessGeneration(foreignBefore.ShellPID)
	if !known {
		t.Fatal("foreign owned shell sentinel has no native generation")
	}
	store := sessionpark.NewStore(filepath.Join(root, ".wb", sessionpark.SourceDirName))
	parkID := "park-owned-herdr"
	bundle := sessionpark.Bundle{SchemaVersion: sessionpark.SchemaVersion, ParkedSessionID: parkID, Source: session.Record{PID: os.Getpid(), WBSessionID: "wbs-owned-source", Machine: "owned-linux", Runtime: RuntimeCodex, Model: session.Unknown, StartedAt: time.Now().UTC()}, Continuation: "owned Herdr journey", ParkedAt: time.Now().UTC()}
	if _, err := store.Create(bundle); err != nil {
		t.Fatal(err)
	}
	lock, err := store.Acquire(ctx, parkID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	if _, _, err := store.PrepareLocalUnderLock(lock, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	continuationPath, continuation, err := store.EnsureLocalSuccessorContextUnderLock(lock, nil)
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := store.LocalLaunchRootUnderLock(lock)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sessionpark.EncodeBundle(bundle)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := sessionpark.LocalLaunchAuthority(bundle, sessionmove.DigestBytes(raw), continuationPath, continuation)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(bin, "codex")
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.OpenFile(harness, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(copied, source)
	closeErr := copied.Close()
	_ = source.Close()
	if copyErr != nil || closeErr != nil {
		t.Fatalf("owned executable copy: %v %v", copyErr, closeErr)
	}
	if path, err := cleanAbsoluteExecutable(harness); err != nil || path != harness {
		t.Fatalf("owned fixed harness path: %q %v", path, err)
	}
	transport := osHerdr{executable: binary, socket: socket, clients: &sync.Map{}}
	deps := dependencies{terminal: transport, lookPath: func(name string) (string, error) {
		if name != "codex" {
			return "", fmt.Errorf("owned fixture refuses executable %q", name)
		}
		return harness, nil
	}, wbExecutable: func() (string, error) { return executable, nil }, sessionDir: func(string) (string, error) { return filepath.Join(root, ".wb", session.DirName), nil }, now: func() time.Time { return time.Now().UTC() }, pollInterval: 25 * time.Millisecond, startTimeout: 45 * time.Second, verifyPinned: verifyPinnedWorktree, processStatus: processStatus}
	options := Options{ProjectsRoot: root, Authority: &authority, StoreRoot: store.Root, Fence: lock, WorktreeDir: worktree, BeforeRelease: func(_ context.Context, prepared Prepared) (string, error) {
		if _, err := os.Stat(ready); !os.IsNotExist(err) {
			return "", fmt.Errorf("harness executed before release gate")
		}
		if prepared.Session.Terminal.Kind != "herdr" || prepared.Session.PID <= 0 {
			return "", fmt.Errorf("prepared child has no Herdr identity")
		}
		return "", os.WriteFile(attach, []byte("all members attached"), 0o600)
	}}
	stage = "start-with-dependencies"
	result, err := startWithDependencies(ctx, options, deps)
	if err != nil {
		t.Fatal(err)
	}
	registeredChildGeneration, known := session.ObserveProcessGeneration(result.PID)
	if !known {
		t.Fatal("registered owned harness generation unavailable")
	}
	originalPane, err := client.PaneProcessInfo(ctx, result.Terminal.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	originalGeneration, known := session.ObserveProcessGeneration(originalPane.ShellPID)
	if !known || originalPane.ShellPID == result.PID {
		t.Fatal("owned supervisor lacks a distinct original PTY generation")
	}
	if result.NativeHarnessID != "native-wb-owned-fixture" || result.Terminal.Socket != socket || result.TmuxName != "" {
		t.Fatalf("result lacks exact Herdr/native identity: %#v", result)
	}
	stage = "native-harness-bound"
	for {
		raw, err := os.ReadFile(filepath.Join(root, "current-cli-probe.json"))
		if err == nil {
			var identities []session.Record
			if err := json.Unmarshal(raw, &identities); err != nil {
				t.Fatal(err)
			}
			if len(identities) != 3 || identities[0].WBSessionID != result.WBSessionID || identities[0].PID != result.PID || identities[1].PID != result.PID || identities[2].PID != result.PID || identities[1].Scope == "" || identities[2].Scope == "" || identities[1].WBSessionID == identities[2].WBSessionID {
				t.Fatalf("CLI native-thread isolation proof invalid: %#v", identities)
			}
			break
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("actual CLI-shaped current discovery proof missing")
		case <-time.After(10 * time.Millisecond):
		}
	}
	message := sessionmove.Message{SchemaVersion: sessionmove.MessageSchemaVersion, MessageID: "message-owned-herdr", HandoffID: parkID, SenderWBSessionID: bundle.Source.WBSessionID, RecipientWBSessionID: result.WBSessionID, ReplyToWBSessionID: bundle.Source.WBSessionID, Kind: sessionmove.MessageKindText, Body: "owned large Unicode query 👩‍💻\r\n" + strings.Repeat("\x01", 24<<10), SentAt: time.Now().UTC()}
	messageRaw, err := sessionmove.EncodeMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if len(messageRaw) <= 128<<10 || len(message.Body) > sessionmove.MaxMessageBodyBytes {
		t.Fatal("fixture did not exercise the single-argv limit with a supported semantic body")
	}
	acknowledgment, err := client.AgentPromptSocket(ctx, result.Terminal.PaneID, string(messageRaw))
	if err != nil || acknowledgment.HarnessSessionID() != result.NativeHarnessID {
		t.Fatalf("owned large typed socket submission: %#v %v", acknowledgment, err)
	}
	for {
		raw, err := os.ReadFile(capture)
		if err == nil {
			expected := append(append([]byte(nil), messageRaw...), '\r')
			if !bytes.Equal(raw, expected) || sessionmove.DigestBytes(raw[:len(raw)-1]) != sessionmove.DigestBytes(messageRaw) {
				t.Fatalf("literal canonical submitted input/sole Enter changed: got %d bytes expected %d", len(raw), len(expected))
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	plan, _, err := loadPlan(store.Root, parkID)
	if err != nil {
		t.Fatal(err)
	}
	for {
		failure, found, err := transport.PaneFailure(ctx, terminalAddress(plan))
		if err != nil {
			t.Fatal(err)
		}
		if found {
			if failure.ExitStatus != 17 {
				t.Fatalf("owned fixture exit=%d", failure.ExitStatus)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	for {
		raw, err := os.ReadFile(filepath.Join(root, "supervisor-restored.json"))
		if err == nil {
			var proof ownedSupervisorRestore
			if err := json.Unmarshal(raw, &proof); err != nil {
				t.Fatal(err)
			}
			if proof.PID != originalPane.ShellPID || proof.Group <= 0 || proof.Foreground != proof.Group || !proof.Generation.Equal(originalGeneration) || proof.Status != 17 || !proof.TermModesRestored {
				t.Fatalf("wrong owned restoration proof: %#v", proof)
			}
			break
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("owned supervisor restoration proof unavailable")
		case <-time.After(10 * time.Millisecond):
		}
	}
	observed, err := client.PaneProcessInfo(ctx, result.Terminal.PaneID)
	if err != nil {
		t.Fatal(err)
	}
	restored := false
	for _, foreground := range observed.Foreground {
		if foreground.PID == originalPane.ShellPID {
			restored = true
		}
	}
	if observed.ShellPID != originalPane.ShellPID || !restored {
		t.Fatalf("original owned PTY foreground not restored while held: %#v", observed)
	}
	stage = "foreground-restored-before-supervisor-exit"
	if err := os.WriteFile(filepath.Join(root, "supervisor-exit-release"), []byte("owned-exit-release"), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		_, paneErr := client.PaneGet(ctx, result.Terminal.PaneID)
		goneErr := session.ProveGenerationGone(session.Record{PID: originalPane.ShellPID, ProcessStartedAt: originalGeneration})
		if errors.Is(paneErr, herdr.ErrUnknownTarget) && goneErr == nil {
			break
		}
		if paneErr != nil && !errors.Is(paneErr, herdr.ErrUnknownTarget) {
			t.Fatal(paneErr)
		}
		select {
		case <-ctx.Done():
			t.Fatal("owned supervisor generation/pane remained after exact exit release")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := session.ProveGenerationGone(session.Record{PID: result.PID, ProcessStartedAt: registeredChildGeneration}); err != nil {
		t.Fatalf("owned harness generation survived: %v", err)
	}
	foreignAfter, err := client.PaneProcessInfo(ctx, foreign.Pane.PaneID)
	if err != nil || foreignAfter.PaneID != foreignBefore.PaneID || foreignAfter.ShellPID != foreignBefore.ShellPID {
		t.Fatalf("unrelated owned pane shell changed: %#v %v", foreignAfter, err)
	}
	if generation, known := session.ObserveProcessGeneration(foreignAfter.ShellPID); !known || !generation.Equal(foreignGeneration) {
		t.Fatal("unrelated owned pane sentinel generation changed")
	}
	stage = "owned-pane-closed-foreign-sentinel-preserved"
	if raw, err := os.ReadFile(continuationPath); err != nil || !strings.HasPrefix(string(raw), bundle.Continuation) {
		t.Fatal("private continuation changed")
	}
}

func TestE2ESupervisedNativeChildCapturesBoundedFailureAndWaitsForOwnedProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native child lifecycle proof runs only in capped Linux")
	}
	_, attempt, plan, digest, _, _ := newHerdrLaunchFixture(t)
	path := filepath.Join(t.TempDir(), "owned-child")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nsleep 0.1\nprintf 'owned fixture failure' >&2\nexit 17\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	plan.WBExecutable = path
	exit, err := superviseOwnedChild(plan, attempt, digest)
	if err != nil || exit.ExitStatus != 17 || exit.Diagnostic != "owned fixture failure" {
		t.Fatalf("owned child: %#v %v", exit, err)
	}
	if err := session.ProveGenerationGone(session.Record{PID: exit.Child.PID, ProcessStartedAt: exit.Child.ProcessStartedAt}); err != nil {
		t.Fatal(err)
	}
	capture := terminalDiagnostic{}
	for index := 0; index < 10000; index++ {
		_, _ = capture.Write([]byte("1234567890"))
	}
	if len(capture.bytes) != 4096 {
		t.Fatal("diagnostic capture grew beyond bound")
	}
}

// Only the deterministic owned fixture sets this variable. It records no
// caller environment beyond its synthetic Herdr coordinates.
func writeOwnedFixtureStage(stage string, failure error) {
	path := os.Getenv("WB_TEST_HERDR_STAGE")
	if path == "" {
		return
	}
	entry := struct {
		Stage    string         `json:"stage"`
		PID      int            `json:"pid"`
		Parent   int            `json:"parent_pid"`
		Identity herdr.Identity `json:"identity"`
		Error    string         `json:"error,omitempty"`
	}{Stage: stage, PID: os.Getpid(), Parent: os.Getppid(), Identity: herdr.ReadIdentity(os.LookupEnv)}
	if failure != nil {
		entry.Error = failure.Error()
		if len(entry.Error) > 1024 {
			entry.Error = entry.Error[:1024]
		}
	}
	raw, _ := json.Marshal(entry)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = file.Write(append(raw, '\n'))
}

// retainOwnedHerdrFailure runs before test defers cancel the owned server and
// before t.TempDir removes artifacts. It is read-only against that server and
// writes bounded synthetic diagnostics only to an explicitly selected proofdir.
func retainOwnedHerdrFailure(t *testing.T, binary string, environment []string, root, stage, serverOut, serverErr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	type reply struct {
		Argv   []string `json:"argv"`
		Stdout string   `json:"stdout"`
		Stderr string   `json:"stderr"`
		Error  string   `json:"error,omitempty"`
	}
	proof := struct {
		Stage        string            `json:"stage"`
		ServerStdout string            `json:"server_stdout"`
		ServerStderr string            `json:"server_stderr"`
		Replies      []reply           `json:"replies"`
		Files        map[string]string `json:"files"`
	}{Stage: stage, ServerStdout: serverOut, ServerStderr: serverErr, Files: map[string]string{}}
	query := func(args ...string) string {
		command := exec.CommandContext(ctx, binary, args...)
		command.Env = environment
		command.WaitDelay = 250 * time.Millisecond
		stdout, stderr := &terminalDiagnostic{}, &terminalDiagnostic{}
		command.Stdout, command.Stderr = stdout, stderr
		err := command.Run()
		entry := reply{Argv: args, Stdout: stdout.snapshot(), Stderr: stderr.snapshot()}
		if err != nil {
			entry.Error = err.Error()
		}
		proof.Replies = append(proof.Replies, entry)
		return entry.Stdout
	}
	raw := query("pane", "list")
	var panes struct {
		Result struct {
			Panes []struct {
				ID string `json:"pane_id"`
			} `json:"panes"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(raw), &panes) == nil {
		for index, pane := range panes.Result.Panes {
			if index >= 8 {
				break
			}
			if herdr.ValidateTarget("owned diagnostic pane", pane.ID) != nil {
				continue
			}
			query("pane", "get", pane.ID)
			query("pane", "process-info", "--pane", pane.ID)
			query("pane", "read", pane.ID, "--source", "recent", "--lines", "80", "--format", "text")
		}
	}
	count, total := 0, 0
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || !entry.Type().IsRegular() || count >= 64 || total >= 1<<20 {
			return nil
		}
		ext := filepath.Ext(entry.Name())
		if ext != ".json" && ext != ".jsonl" && ext != ".log" && ext != ".toml" && entry.Name() != "attached" && entry.Name() != "ready" && entry.Name() != "capture" {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		content, err := io.ReadAll(io.LimitReader(file, 16<<10))
		_ = file.Close()
		if err != nil {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		proof.Files[relative] = string(content)
		count++
		total += len(content)
		return nil
	})
	encoded, _ := json.MarshalIndent(proof, "", "  ")
	proofDir := os.Getenv("WB_TEST_HERDR_PROOF_DIR")
	if proofDir == "" {
		t.Logf("owned Herdr diagnostics (set WB_TEST_HERDR_PROOF_DIR to retain): %s", encoded)
		return
	}
	if !filepath.IsAbs(proofDir) || filepath.Clean(proofDir) != proofDir {
		t.Log("owned Herdr proofdir must be one clean absolute path")
		return
	}
	if err := os.MkdirAll(proofDir, 0o700); err != nil {
		t.Logf("create owned Herdr proofdir: %v", err)
		return
	}
	path := filepath.Join(proofDir, "owned-herdr-failure.json")
	if err := os.WriteFile(path, append(encoded, '\n'), 0o600); err != nil {
		t.Logf("write owned Herdr proof: %v", err)
		return
	}
	t.Logf("owned Herdr failure retained at %s", path)
}

func TestE2ESupervisorBoundsInheritedPipeAndCancellationWithoutKillingUnrelatedProcess(t *testing.T) {
	for _, mode := range []string{"leader-exits", "cancel-live-tree"} {
		t.Run(mode, func(t *testing.T) {
			_, attempt, plan, digest, _, _ := newHerdrLaunchFixture(t)
			root := t.TempDir()
			pidFile := filepath.Join(root, "owned-grandchild.pid")
			script := "#!/bin/sh\nsleep 60 &\nowned=$!\nprintf '%s' \"$owned\" > '" + pidFile + "'\nprintf 'owned inherited stderr' >&2\n"
			if mode == "leader-exits" {
				script += "exit 17\n"
			} else {
				script += "trap 'wait \"$owned\"; exit 23' TERM HUP INT\nwait \"$owned\"\nexit 23\n"
			}
			path := filepath.Join(root, "owned-child")
			if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			plan.WBExecutable = path
			unrelated := exec.Command("sleep", "60")
			if err := unrelated.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unrelated.Process.Kill(); _ = unrelated.Wait() }()
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			type outcome struct {
				exit herdrExit
				err  error
			}
			done := make(chan outcome, 1)
			began := time.Now()
			go func() { exit, err := superviseChildContext(ctx, plan, attempt, digest); done <- outcome{exit, err} }()
			var grandchild int
			ready := time.NewTimer(2 * time.Second)
			defer ready.Stop()
			for grandchild == 0 {
				raw, err := os.ReadFile(pidFile)
				if err == nil {
					grandchild, _ = strconv.Atoi(string(raw))
				}
				if grandchild > 0 {
					break
				}
				select {
				case <-ready.C:
					t.Fatal("owned grandchild never started")
				case <-time.After(10 * time.Millisecond):
				}
			}
			if mode == "cancel-live-tree" {
				cancel()
			}
			select {
			case result := <-done:
				if result.err != nil {
					t.Fatal(result.err)
				}
				if mode == "leader-exits" && result.exit.ExitStatus != 17 {
					t.Fatalf("leader status changed: %#v", result.exit)
				}
				if elapsed := time.Since(began); elapsed > 2*time.Second {
					t.Fatalf("inherited stderr/process cleanup took %v", elapsed)
				}
				if session.ProcessAlive(result.exit.Child.PID) {
					t.Fatal("owned direct child survived")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("supervisor blocked on inherited stderr or live descendants")
			}
			if !session.ProcessAlive(unrelated.Process.Pid) {
				t.Fatal("cleanup killed an unrelated process")
			}
			// An orphan may briefly await the container init's reaper. Kernel zombie
			// state is dead, never counted as a running survivor or reused as identity.
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", grandchild))
			if err == nil {
				tail := string(stat)
				closing := strings.LastIndex(tail, ") ")
				if closing < 0 || closing+2 >= len(tail) || tail[closing+2] != 'Z' {
					t.Fatalf("owned grandchild still running after cleanup: %s", tail)
				}
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		})
	}
}
