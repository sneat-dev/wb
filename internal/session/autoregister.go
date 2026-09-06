package session

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Unknown is what a park-time registration records for an identity field WB was
// neither told nor able to observe. `wb session list` already shows it for
// sessions that registered without a model, so it reads as a known gap rather
// than as a confident lie: a wrong runtime or model is worse than an admitted
// missing one, and neither is a reason to refuse to park work.
const Unknown = "unknown"

// Environment variables through which a session may already have declared
// itself. The names mirror internal/worktrees' WB_AGENT_* constants; they are
// repeated here rather than imported because internal/worktrees depends on this
// package, never the other way round.
const (
	envAgentPID     = "WB_AGENT_PID"
	envAgentRuntime = "WB_AGENT_RUNTIME"
	envAgentModel   = "WB_AGENT_MODEL"
	envAgentID      = "WB_AGENT_ID"
)

// AutoRegisterHints is what a caller explicitly declared about the session it
// is about to park. A populated field is never overridden by inference: a
// declaration always outranks an observation.
type AutoRegisterHints struct {
	PID             int
	Runtime         string
	Model           string
	NativeHarnessID string
	// WBSessionID targets one already-registered session instead of resolving
	// or registering from this process. It never creates a registration.
	WBSessionID string
}

// ResolveOrRegisterForProcess returns the session that owns startPID,
// registering one from observable evidence when nothing is registered yet.
// The second result reports whether this call created that registration.
//
// Requiring a prior `wb session register` before `wb session park` cost more
// than it protected: an agent that hit the precondition mid-task hand-rolled
// its own parking instead of using the verb, which is exactly the outcome the
// verb exists to prevent. Park is therefore free to register the caller first.
// That is not guessing an owner — nothing here is attributed to some other
// session — it is WB writing down the identity of the process that asked to be
// parked, marking it as inferred, and continuing.
func ResolveOrRegisterForProcess(dir string, startPID int, hints AutoRegisterHints) (Record, bool, error) {
	if wanted := strings.TrimSpace(hints.WBSessionID); wanted != "" {
		record, ok := LookupByWBSessionID(dir, wanted)
		if !ok {
			return Record{}, false, fmt.Errorf("no live registered session has WB session ID %s", wanted)
		}
		return record, false, nil
	}
	if record, ok := ResolveForProcess(dir, startPID); ok {
		return record, false, nil
	}
	record, err := InferRecordForProcess(startPID, hints)
	if err != nil {
		return Record{}, false, err
	}
	// A finished session's record must not be overwritten by a new one at the
	// same PID: its parked aggregate is still resumable, and the registry row
	// is how a coordinator finds it. This refuses a lifecycle collision, never
	// a missing-metadata one.
	if previous, ok := readRecord(recordPath(dir, record.PID)); ok {
		lifecycle := ""
		switch {
		case previous.Lifecycle == "resumed" || resumed(dir, previous.WBSessionID):
			lifecycle = "resumed"
		case previous.Lifecycle == "parked" || parked(dir, previous.WBSessionID):
			lifecycle = "parked"
		}
		if lifecycle != "" {
			return Record{}, false, fmt.Errorf(
				"process %d is already registered as session %s, which is %s; resume it with wb session resume %s, or register the live session explicitly with wb session register --pid <agent-pid>",
				record.PID, previous.WBSessionID, lifecycle, previous.ParkedSessionID)
		}
	}
	written, err := Register(dir, record)
	if err != nil {
		return Record{}, false, err
	}
	return written, true, nil
}

// InferRecordForProcess builds the registration WB would write for a caller
// that never registered. Every field is either declared by the caller, read
// from an environment declaration the session already exported, observed from
// the process tree, or recorded as Unknown. Nothing is invented.
func InferRecordForProcess(startPID int, hints AutoRegisterHints) (Record, error) {
	ancestorPID, ancestorRuntime := harnessAncestor(startPID)
	record := Record{
		PID:             hints.PID,
		Runtime:         strings.TrimSpace(hints.Runtime),
		Model:           strings.TrimSpace(hints.Model),
		NativeHarnessID: strings.TrimSpace(hints.NativeHarnessID),
	}
	if record.PID <= 0 {
		record.PID = envPID()
	}
	if record.PID <= 0 {
		record.PID = ancestorPID
	}
	if record.PID <= 0 {
		// No ancestor declared itself and none carries a harness name WB
		// recognises, so the only process WB can honestly name is its own. A
		// short-lived PID costs nothing here: this registration is parked by
		// the same command, and a parked session is never live or claimable.
		record.PID = startPID
	}
	if record.Runtime == "" {
		record.Runtime = strings.TrimSpace(os.Getenv(envAgentRuntime))
	}
	if record.Runtime == "" {
		record.Runtime = ancestorRuntime
	}
	if record.Runtime == "" {
		record.Runtime = Unknown
	}
	if record.Model == "" {
		record.Model = strings.TrimSpace(os.Getenv(envAgentModel))
	}
	if record.Model == "" {
		record.Model = Unknown
	}
	if record.NativeHarnessID == "" {
		record.NativeHarnessID = strings.TrimSpace(os.Getenv(envAgentID))
	}
	id, err := NewID()
	if err != nil {
		return Record{}, err
	}
	// Always a fresh identity, never the one a stale record at this PID
	// carries: a recycled PID must not inherit a finished session's lifecycle.
	record.WBSessionID = id
	record.RegisteredAtPark = true
	return record, nil
}

// LookupByWBSessionID finds one live registered session by its stable WB
// session ID. It is how an explicit --wb-session-id targets a session that
// registered from a process this one is not descended from.
func LookupByWBSessionID(dir, wbSessionID string) (Record, bool) {
	wbSessionID = strings.TrimSpace(wbSessionID)
	if wbSessionID == "" {
		return Record{}, false
	}
	views, err := List(dir)
	if err != nil {
		return Record{}, false
	}
	for _, view := range views {
		if view.WBSessionID != wbSessionID {
			continue
		}
		if record, ok := Lookup(dir, view.PID); ok {
			return record, true
		}
	}
	return Record{}, false
}

// harnessAncestor is indirected so tests can observe both branches without
// depending on what happens to be running above the test binary.
var harnessAncestor = findHarnessAncestor

// findHarnessAncestor reports the closest ancestor of startPID whose executable
// name is one WB can name as a harness, and the runtime name it records for it.
//
// This is weaker evidence than a registration and is used only as such: it
// never attributes work to an existing session, it only helps a park-time
// registration point at the process an agent would have named itself. Depth is
// bounded because a corrupted process chain must not spin.
func findHarnessAncestor(startPID int) (int, string) {
	const maxDepth = 12
	pid := startPID
	for depth := 0; depth < maxDepth && pid > 1; depth++ {
		parent, ok := parentPID(pid)
		if !ok || parent <= 1 {
			return 0, ""
		}
		if runtime, ok := runtimeForProcessName(processName(parent)); ok {
			return parent, runtime
		}
		pid = parent
	}
	return 0, ""
}

// knownHarnessProcessNames maps an observed executable name to the runtime name
// WB records for it. Only names that identify one harness unambiguously belong
// here: a generic interpreter name (node, python) says nothing about which
// agent is driving, and Unknown is the better answer than a plausible guess.
var knownHarnessProcessNames = map[string]string{
	"aider":        "aider",
	"claude":       "claude-code",
	"claude-code":  "claude-code",
	"codex":        "codex",
	"copilot":      "copilot-cli",
	"copilot-cli":  "copilot-cli",
	"crush":        "crush",
	"cursor-agent": "cursor-agent",
	"gemini":       "gemini-cli",
	"goose":        "goose",
	"opencode":     "opencode",
}

func runtimeForProcessName(name string) (string, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.TrimSuffix(name, ".exe")
	runtime, ok := knownHarnessProcessNames[name]
	return runtime, ok
}

// envPID reads WB_AGENT_PID. A missing, malformed, or non-positive value is
// treated as undeclared rather than as an error, exactly as the worktree
// identity reader treats it.
func envPID() int {
	pid, err := strconv.Atoi(strings.TrimSpace(os.Getenv(envAgentPID)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
