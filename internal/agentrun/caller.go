package agentrun

import (
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/session"
)

// CurrentCaller takes identity only from a declared live WB registration or a
// harness-provided per-chat ID. A Codex process may serve several chats,
// so its registration is linked only when it declares the same native ID.
func CurrentCaller(projectsRoot string) agents.CallerSession {
	// Invalid or root-session-only Codex context cannot be attributed to a
	// descendant chat, even when another inherited harness ID is also present.
	if os.Getenv("CODEX_THREAD_ID") != "" || os.Getenv("CODEX_SESSION_ID") != "" {
		if _, err := session.CallerNativeID("codex", os.LookupEnv); err != nil {
			return agents.CallerSession{}
		}
	}
	registered, found := session.ResolveForProcess(filepath.Join(projectsRoot, ".wb", session.DirName), os.Getpid())
	registeredName := registered.Terminal.PaneID
	if registeredName == "" {
		registeredName = registered.TmuxName
	} // historical display metadata only
	for _, candidate := range []struct{ runtime, variable string }{
		{"codex", "CODEX_THREAD_ID"},
		{"claude-code", "CLAUDE_CODE_SESSION_ID"},
	} {
		id := agents.SafeCallerID(os.Getenv(candidate.variable))
		if id == "" {
			continue
		}
		caller := agents.CallerSession{ID: id, NativeHarnessID: id, Runtime: candidate.runtime, Source: candidate.variable}
		if found && registered.NativeHarnessID == id && registered.Runtime == candidate.runtime {
			caller.WBSessionID = registered.WBSessionID
			caller.Name = registeredName
		}
		return caller
	}
	if found {
		// Codex processes can be shared by independent native threads. Without a
		// native ID from this invocation, a PID registration cannot identify
		// which chat made the call.
		if registered.Runtime == "codex" {
			return agents.CallerSession{}
		}
		id := registered.NativeHarnessID
		if id == "" {
			id = registered.WBSessionID
		}
		return agents.CallerSession{
			ID: id, WBSessionID: registered.WBSessionID,
			NativeHarnessID: registered.NativeHarnessID, Name: registeredName,
			Runtime: registered.Runtime, Source: "wb_session_registry",
		}
	}
	return agents.CallerSession{}
}
