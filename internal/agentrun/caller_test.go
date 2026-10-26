package agentrun

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/session"
)

//nolint:paralleltest // Mutates the process-global CODEX_THREAD_ID, CODEX_SESSION_ID and CLAUDE_CODE_SESSION_ID harness env to prove a native thread ID outranks a shared process registration.
func TestCallerSessionUsesNativeThreadOverSharedProcessRegistration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_THREAD_ID", "thread-current")
	t.Setenv("CODEX_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	_, err := session.Register(filepath.Join(root, ".wb", session.DirName), session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-shared", Runtime: "codex",
		NativeHarnessID: "thread-other", TmuxName: "other-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	caller := CurrentCaller(root)
	if caller.ID != "thread-current" || caller.WBSessionID != "" || caller.Name != "" || caller.Source != "CODEX_THREAD_ID" {
		t.Fatalf("shared process record was misattributed: %+v", caller)
	}
	_, err = session.Register(filepath.Join(root, ".wb", session.DirName), session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-current", Runtime: "codex",
		NativeHarnessID: "thread-current", TmuxName: "current-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	caller = CurrentCaller(root)
	if caller.WBSessionID != "wbs-current" || caller.Name != "current-chat" {
		t.Fatalf("matching registered identity was not linked: %+v", caller)
	}
}

//nolint:paralleltest // Clears the process-global CODEX_THREAD_ID, CODEX_SESSION_ID and CLAUDE_CODE_SESSION_ID harness env to prove caller selection falls back to the WB session registry.
func TestCallerSessionFallsBackToRegisteredSession(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	_, err := session.Register(filepath.Join(root, ".wb", session.DirName), session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-declared", Runtime: "opencode",
		NativeHarnessID: "opencode-1", TmuxName: "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	caller := CurrentCaller(root)
	if caller.ID != "opencode-1" || caller.WBSessionID != "wbs-declared" ||
		caller.Name != "builder" || caller.Source != "wb_session_registry" {
		t.Fatalf("registered caller missing: %+v", caller)
	}
	if agents.SafeCallerID("bad\nvalue") != "" || agents.SafeCallerID("valid-id_1") != "valid-id_1" {
		t.Fatal("unsafe or valid native session ID classified incorrectly")
	}
}

//nolint:paralleltest // Clears the process-global CODEX_THREAD_ID, CODEX_SESSION_ID and CLAUDE_CODE_SESSION_ID harness env to prove a WB-only registration supplies the caller ID by WB session ID.
func TestCallerSessionUsesWBIDWhenRegisteredHarnessHasNoNativeID(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	_, err := session.Register(filepath.Join(root, ".wb", session.DirName), session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-declared", Runtime: "opencode", TmuxName: "builder",
	})
	if err != nil {
		t.Fatal(err)
	}
	caller := CurrentCaller(root)
	if caller.ID != "wbs-declared" || caller.WBSessionID != "wbs-declared" || caller.NativeHarnessID != "" {
		t.Fatalf("WB-only registration was lost: %+v", caller)
	}
}

//nolint:paralleltest // Clears the process-global CODEX_THREAD_ID, CODEX_SESSION_ID and CLAUDE_CODE_SESSION_ID harness env to prove a shared Codex app-server PID stays unattributed without a native thread ID.
func TestCallerSessionLeavesCodexWithoutNativeIDUnattributed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CODEX_SESSION_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	_, err := session.Register(filepath.Join(root, ".wb", session.DirName), session.Record{
		PID: os.Getpid(), WBSessionID: "wbs-shared", Runtime: "codex", TmuxName: "some-other-chat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if caller := CurrentCaller(root); caller.ID != "" || caller.Name != "" {
		t.Fatalf("shared Codex app-server PID attributed without a native ID: %+v", caller)
	}
}
