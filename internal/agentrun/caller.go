package agentrun

import (
	"os"
	"path/filepath"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/session"
)

// CurrentCaller takes identity only from a declared live WB registration or a
// harness-provided session ID. A Codex app-server PID may serve several chats,
// so its registration is linked only when it declares the same native ID.
func CurrentCaller(projectsRoot string) agents.CallerSession {
	registered, found := session.ResolveForProcess(filepath.Join(projectsRoot, ".wb", session.DirName), os.Getpid())
	for _, candidate := range []struct{ runtime, variable string }{
		{"codex", "CODEX_THREAD_ID"},
		{"codex", "CODEX_SESSION_ID"},
		{"claude-code", "CLAUDE_CODE_SESSION_ID"},
	} {
		id := agents.SafeCallerID(os.Getenv(candidate.variable))
		if id == "" {
			continue
		}
		caller := agents.CallerSession{ID: id, NativeHarnessID: id, Runtime: candidate.runtime, Source: candidate.variable}
		if found && registered.NativeHarnessID == id && registered.Runtime == candidate.runtime {
			caller.WBSessionID = registered.WBSessionID
			caller.Name = registered.TmuxName
		}
		return caller
	}
	if found {
		// Codex app-server PIDs are shared by independent chats. Without a
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
			NativeHarnessID: registered.NativeHarnessID, Name: registered.TmuxName,
			Runtime: registered.Runtime, Source: "wb_session_registry",
		}
	}
	return agents.CallerSession{}
}
