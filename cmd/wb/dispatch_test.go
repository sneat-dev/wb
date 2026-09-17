package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/hooks"
	"github.com/sneat-dev/wb/internal/sessionlaunch"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// routingCodes are deliberately outside wb's documented 0/1/2 contract: a test
// that reads one back knows which handler produced it, not merely that some
// handler ran.
const (
	routedPrivateLauncher = 101
	routedOwnerCLI        = 102
	routedAgentRemote     = 103
	routedSecureGitHelper = 104
)

// stubProcessHandlers records every entry point dispatch reaches and returns a
// distinctive code, so routing is asserted without invoking the real handlers.
// That matters for the six secure Git helpers: they deliberately read inherited
// descriptors 3..9, and invoking one in-process against the test binary's own
// descriptors tears down the Go runtime's netpoll fd.
func stubProcessHandlers(calls *[]string) processHandlers {
	return processHandlers{
		privateLauncher: func(args []string) int {
			*calls = append(*calls, "privateLauncher:"+strings.Join(args, " "))
			return routedPrivateLauncher
		},
		ownerCLI: func(args []string, _ agents.OwnerDeps) int {
			*calls = append(*calls, "ownerCLI:"+strings.Join(args, " "))
			return routedOwnerCLI
		},
		agentRemote: func(_ io.Reader, _, _ io.Writer) int {
			*calls = append(*calls, "agentRemote")
			return routedAgentRemote
		},
		secureGitHelper: func(argument string, args []string) (int, bool) {
			if _, known := secureGitHelpers()[argument]; !known {
				return 0, false
			}
			*calls = append(*calls, "secureGitHelper:"+argument+":"+strings.Join(args, " "))
			return routedSecureGitHelper, true
		},
		lookupEnv:  func(string) (string, bool) { return "/opt/wb/current/wb", true },
		executable: func() (string, error) { return "/opt/wb/current/wb", nil },
		setEnv:     func(string, string) error { return nil },
	}
}

// TestSecureGitHelpersTableIsExactlyTheDocumentedProtocolArguments pins the
// table's membership. Growing the protocol surface without teaching dispatch
// about it would otherwise silently downgrade a hidden helper into an unknown
// cobra command.
func TestSecureGitHelpersTableIsExactlyTheDocumentedProtocolArguments(t *testing.T) {
	table := secureGitHelpers()
	want := []string{
		worktrees.SecureCleanupGitHelperArgument,
		hooks.SecureHooksGitHelperArgument,
		worktrees.SecureStageGitHelperArgument,
		worktrees.SecureCanonicalGitHelperArgument,
		worktrees.SecureStageCanonicalGitHelperArgument,
		worktrees.SecureRenameGitHelperArgument,
	}
	if len(table) != len(want) {
		t.Fatalf("secureGitHelpers() has %d entries, want %d: %v", len(table), len(want), table)
	}
	for _, argument := range want {
		if _, known := table[argument]; !known {
			t.Errorf("secureGitHelpers() has no handler for %q", argument)
		}
	}
}

// TestDispatchWithHandlersRoutesHiddenArgumentsBeforeCobra proves each hidden
// protocol value is resolved by dispatch and never reaches cobra, and that the
// argument tail is forwarded verbatim.
func TestDispatchWithHandlersRoutesHiddenArgumentsBeforeCobra(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantCall string
		wantCode int
	}{
		{
			name:     "private launcher",
			args:     []string{sessionlaunch.PrivateLauncherArgument, "/store", "handoff"},
			wantCall: "privateLauncher:/store handoff",
			wantCode: routedPrivateLauncher,
		},
		{
			name:     "agent owner",
			args:     []string{agents.OwnerArgument, "--run-dir", "/runs/agent-1"},
			wantCall: "ownerCLI:--run-dir /runs/agent-1",
			wantCode: routedOwnerCLI,
		},
		{
			name:     "agent remote",
			args:     []string{agents.RemoteArgument},
			wantCall: "agentRemote",
			wantCode: routedAgentRemote,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var calls []string
			var out, errOut bytes.Buffer
			code := dispatchWithHandlers(
				stubProcessHandlers(&calls), testCase.args, strings.NewReader(""), &out, &errOut)
			if code != testCase.wantCode {
				t.Fatalf("dispatchWithHandlers(%q) = %d, want %d", testCase.args, code, testCase.wantCode)
			}
			if len(calls) != 1 || calls[0] != testCase.wantCall {
				t.Fatalf("handlers called %v, want exactly [%s]", calls, testCase.wantCall)
			}
		})
	}
}

// TestDispatchWithHandlersRoutesEverySecureGitHelper covers all six helpers,
// including the three that cannot be invoked in-process at all. This is the
// only place that routing is proven for them.
func TestDispatchWithHandlersRoutesEverySecureGitHelper(t *testing.T) {
	for argument := range secureGitHelpers() {
		t.Run(argument, func(t *testing.T) {
			var calls []string
			var out, errOut bytes.Buffer
			code := dispatchWithHandlers(
				stubProcessHandlers(&calls), []string{argument, "extra"}, strings.NewReader(""), &out, &errOut)
			if code != routedSecureGitHelper {
				t.Fatalf("dispatchWithHandlers(%q) = %d, want %d", argument, code, routedSecureGitHelper)
			}
			want := "secureGitHelper:" + argument + ":extra"
			if len(calls) != 1 || calls[0] != want {
				t.Fatalf("handlers called %v, want exactly [%s]", calls, want)
			}
		})
	}
}

// TestDispatchWithHandlersFallsThroughToCobraForUnknownArguments proves an
// argv value that names no helper is not swallowed: cobra still receives it.
func TestDispatchWithHandlersFallsThroughToCobraForUnknownArguments(t *testing.T) {
	var calls []string
	var out, errOut bytes.Buffer
	code := dispatchWithHandlers(
		stubProcessHandlers(&calls), []string{"--version"}, strings.NewReader(""), &out, &errOut)
	if code != exitOK {
		t.Fatalf("dispatchWithHandlers(--version) = %d, want %d", code, exitOK)
	}
	if len(calls) != 0 {
		t.Fatalf("handlers called %v, want none for a non-protocol argument", calls)
	}
	if out.Len() == 0 {
		t.Fatal("--version produced no output on stdout")
	}
}

// TestDispatchWithHandlersAnswersBareInvocation is the regression guard for
// empty argv. A bare `wb` is a valid invocation that must reach cobra and
// answer with help; deriving the argument tail before checking the length made
// it panic with a slice-bounds error instead. The slice is deliberately empty
// and non-nil, exactly as os.Args[1:] is for a process started with no
// arguments -- a nil slice would instead make cobra re-read the test binary's
// own os.Args.
func TestDispatchWithHandlersAnswersBareInvocation(t *testing.T) {
	var calls []string
	var out, errOut bytes.Buffer
	code := dispatchWithHandlers(
		stubProcessHandlers(&calls), []string{}, strings.NewReader(""), &out, &errOut)
	if code != exitOK {
		t.Fatalf("dispatchWithHandlers() with no arguments = %d, want %d", code, exitOK)
	}
	if len(calls) != 0 {
		t.Fatalf("handlers called %v, want none for a bare invocation", calls)
	}
	if out.Len() == 0 {
		t.Fatal("a bare wb invocation produced no output on stdout")
	}
}

// TestDispatchWithHandlersReportsRuntimeExecutableHandoffFailure covers the one
// recoverable failure inside dispatch: a child Git hook cannot be given a route
// back to this binary, which is a finding rather than a usage error.
func TestDispatchWithHandlersReportsRuntimeExecutableHandoffFailure(t *testing.T) {
	handlers := stubProcessHandlers(nil)
	handlers.lookupEnv = func(string) (string, bool) { return "", false }
	handlers.executable = func() (string, error) { return "", errors.New("no executable") }
	var out, errOut bytes.Buffer
	code := dispatchWithHandlers(handlers, []string{"--version"}, strings.NewReader(""), &out, &errOut)
	if code != exitFindings {
		t.Fatalf("dispatchWithHandlers() = %d, want %d", code, exitFindings)
	}
	if !strings.Contains(errOut.String(), "establish runtime executable for child Git hooks") {
		t.Fatalf("stderr = %q, want the handoff failure narrated", errOut.String())
	}
}

// TestDispatchRoutesSecureCleanupHelperInProcess exercises the real handler
// table for the one helper that fails before it touches an inherited
// descriptor, so the default table's closure is covered rather than only
// stubbed. The second call takes an argument that names no helper, covering the
// other half of that closure.
func TestDispatchRoutesSecureCleanupHelperInProcess(t *testing.T) {
	// Keep propagateRuntimeWBExecutable from mutating this test process's
	// environment: an explicit WB_EXECUTABLE is preserved as-is.
	t.Setenv("WB_EXECUTABLE", "/opt/wb/current/wb")
	var out, errOut bytes.Buffer
	code := dispatch([]string{worktrees.SecureCleanupGitHelperArgument}, strings.NewReader(""), &out, &errOut)
	if code != 1 {
		t.Fatalf("dispatch(%q) = %d, want 1 for a missing worktree path",
			worktrees.SecureCleanupGitHelperArgument, code)
	}

	out.Reset()
	errOut.Reset()
	if code := dispatch([]string{"--version"}, strings.NewReader(""), &out, &errOut); code != exitOK {
		t.Fatalf("dispatch(--version) = %d, want %d", code, exitOK)
	}
	if out.Len() == 0 {
		t.Fatal("--version produced no output on stdout")
	}
}

// TestPropagateRuntimeWBExecutableReportsExportFailure covers the last
// unreachable-by-inspection branch of the handoff: a process that cannot export
// WB_EXECUTABLE must fail closed rather than leave a child Git hook with no
// route back to this binary.
func TestPropagateRuntimeWBExecutableReportsExportFailure(t *testing.T) {
	sentinel := errors.New("environment is full")
	err := propagateRuntimeWBExecutable(
		func(string) (string, bool) { return "", false },
		func() (string, error) { return "/opt/wb/current/wb", nil },
		func(name, value string) error {
			if name != "WB_EXECUTABLE" || value != "/opt/wb/current/wb" {
				t.Fatalf("export = %s=%q, want WB_EXECUTABLE=/opt/wb/current/wb", name, value)
			}
			return sentinel
		},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "export WB_EXECUTABLE") {
		t.Fatalf("error = %v, want it to name the failed export", err)
	}
}

// TestPushTierDecisionReportsTheTierForAPublicationPush covers the branch whose
// exit code is the answer a Git hook acts on.
func TestPushTierDecisionReportsTheTierForAPublicationPush(t *testing.T) {
	stdin := strings.NewReader(
		"refs/tags/v1.0.0 1111111111111111111111111111111111111111 refs/tags/v1.0.0 2222222222222222222222222222222222222222\n")
	code, message := pushTierDecision(stdin)
	if code != int(hooks.TierPublication) {
		t.Fatalf("code = %d, want %d", code, int(hooks.TierPublication))
	}
	want := "WB hook: tier 2 — refs/tags/v1.0.0 is a tag: publication push\n"
	if message != want {
		t.Fatalf("message = %q, want %q", message, want)
	}
}

// TestPushTierDecisionDefaultsToTheFastLaneOnMalformedInput covers the branch
// that must never block a push: an unparseable ref list degrades to tier 1
// instead of failing closed.
func TestPushTierDecisionDefaultsToTheFastLaneOnMalformedInput(t *testing.T) {
	code, message := pushTierDecision(strings.NewReader("not-a-ref-update\n"))
	if code != int(hooks.TierLint) {
		t.Fatalf("code = %d, want %d", code, int(hooks.TierLint))
	}
	want := "WB hook: tier 1 — classification failed " +
		"(malformed pushed-ref line \"not-a-ref-update\": want 4 fields, got 1); " +
		"defaulting to the fast lane, CI is the real gate\n"
	if message != want {
		t.Fatalf("message = %q, want %q", message, want)
	}
}

// failingWriter always fails, so a test can observe whether a write error is
// propagated rather than swallowed.
type failingWriter struct{ err error }

func (writer failingWriter) Write([]byte) (int, error) { return 0, writer.err }

// TestAnsiStrippingWriterPropagatesWriteError covers the writer's error path:
// stripping ANSI codes must not turn an unwritable stdout into a silent success.
func TestAnsiStrippingWriterPropagatesWriteError(t *testing.T) {
	sentinel := errors.New("stdout is closed")
	writer := ansiStrippingWriter{Writer: failingWriter{err: sentinel}}
	written, err := writer.Write([]byte("\x1b[31mred\x1b[0m"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Write() error = %v, want %v", err, sentinel)
	}
	if written != 0 {
		t.Fatalf("Write() = %d bytes, want 0 on failure", written)
	}
}
