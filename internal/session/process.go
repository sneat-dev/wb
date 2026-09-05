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

// IsRuntimeProcess reports whether pid is the declared harness runtime. This
// is intentionally narrower than matching arbitrary command-line text: the
// executable basename identifies the runtime and its role argument identifies
// the Codex app-server process.
func IsRuntimeProcess(pid int, runtime string) bool {
	evidence, ok := processEvidence(pid)
	if !ok {
		return false
	}
	return processEvidenceMatchesRuntime(evidence, runtime)
}

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
