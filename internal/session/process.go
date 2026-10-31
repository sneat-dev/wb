package session

import (
	"path/filepath"
	"strings"
)

// ProcessEvidence is the kernel-reported identity of a live process. The
// executable name and positional arguments are kept separate so a nested
// configuration path cannot make a shell look like a harness.
type ProcessEvidence struct {
	Executable string
	Args       []string
}

// IsRuntimeProcess corroborates an observed harness executable. Interactive
// Codex embeds its app-server in process; it can host several native threads
// without an app-server argv role. All observed Codex processes therefore need
// native-qualified custody, not only the Desktop app-server subprocess.
func IsRuntimeProcess(pid int, runtime string) bool {
	evidence, ok := processEvidence(pid)
	if !ok {
		return false
	}
	observed, known := runtimeForProcessEvidence(evidence)
	return known && observed == strings.TrimSpace(runtime)
}

// processEvidenceMatchesRuntime retains the exact Desktop role predicate for
// callers that need that role specifically. It is not generic harness discovery.
func processEvidenceMatchesRuntime(evidence ProcessEvidence, runtime string) bool {
	runtime = strings.TrimSpace(runtime)
	switch runtime {
	case "codex":
		return strings.EqualFold(filepath.Base(filepath.Clean(evidence.Executable)), "codex") && hasExactArgument(evidence.Args, "app-server")
	default:
		return false
	}
}

func hasExactArgument(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}
