package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// daemonIdentity is what a reader could establish about *whose* daemon a
// lifecycle record describes. It is deliberately a different question from
// whether something answered on an endpoint: WB's two invisible daemon
// failures were both cases of "something answered, and it was not ours".
type daemonIdentity string

const (
	// identityAbsent: this home records no daemon at all.
	identityAbsent daemonIdentity = "absent"
	// identityCurrent: the record belongs to this home and its process
	// generation is live.
	identityCurrent daemonIdentity = "current"
	// identityForeignHome: the record names a different WB home.
	identityForeignHome daemonIdentity = "foreign_home"
	// identityForeignStatePath: the record names a different state file.
	identityForeignStatePath daemonIdentity = "foreign_state_path"
	// identityUnrecorded: the record predates home identity, so it cannot be
	// shown to belong to this home. Unknown is reported as unknown rather than
	// assumed to match.
	identityUnrecorded daemonIdentity = "unrecorded"
	// identityProcessRecycled: the recorded PID now belongs to a different
	// process, so "still running" would be a claim about someone else.
	identityProcessRecycled daemonIdentity = "process_recycled"
	// identityStopped: the record is this home's and its process is gone.
	identityStopped daemonIdentity = "stopped"
)

// daemonLocation is where this invocation resolves the daemon's runtime state:
// the one home, and the four paths under it that status must be able to name
// without starting a daemon.
type daemonLocation struct {
	Home       string
	RuntimeDir string
	SocketPath string
	StatePath  string
}

func resolveDaemonLocation(root string) (daemonLocation, error) {
	home, err := wbhome.Root(root)
	if err != nil {
		return daemonLocation{}, err
	}
	runtimeDir, err := daemon.RuntimeDir(root)
	if err != nil {
		return daemonLocation{}, err
	}
	socketPath, err := daemon.SocketPath(root)
	if err != nil {
		return daemonLocation{}, err
	}
	statePath, err := daemon.StatePath(root)
	if err != nil {
		return daemonLocation{}, err
	}
	return daemonLocation{Home: home, RuntimeDir: runtimeDir, SocketPath: socketPath, StatePath: statePath}, nil
}

// assessIdentity answers "is this record this home's daemon?" from local
// evidence alone — the record's own account of where it lives, and the process
// generation behind its PID. It never consults the loopback port: reachability
// is a fact about an endpoint, not about ownership, and treating it as
// ownership is exactly how a stranded daemon looked healthy.
func (controller daemonController) assessIdentity(state daemon.State, found bool) (identity daemonIdentity, detail string, alive bool) {
	if !found {
		return identityAbsent, "this WB home records no daemon", false
	}
	location, err := resolveDaemonLocation(controller.root)
	if err != nil {
		return identityUnrecorded, err.Error(), false
	}
	// Liveness is reported alongside identity rather than folded into it: a
	// process can be running and still not be this home's daemon, and status
	// must be able to say "reachable, but not ours".
	processAlive := state.PID > 0 && controller.deps.alive(state.PID)
	if recorded := strings.TrimSpace(state.WBHome); recorded != "" && !daemonSamePath(recorded, location.Home) {
		return identityForeignHome, fmt.Sprintf("record belongs to WB home %s; this invocation resolves %s", recorded, location.Home), processAlive
	}
	if recorded := strings.TrimSpace(state.StatePath); recorded != "" && !daemonSamePath(recorded, location.StatePath) {
		return identityForeignStatePath, fmt.Sprintf("record names state file %s; this invocation reads %s", recorded, location.StatePath), processAlive
	}
	if strings.TrimSpace(state.WBHome) == "" || strings.TrimSpace(state.StatePath) == "" {
		return identityUnrecorded, "record predates home identity; restart the daemon so it records which home it belongs to", processAlive
	}
	if !processAlive {
		return identityStopped, "the recorded process is not running", false
	}
	// Routed through the injectable seam, not called directly: a hard-coded
	// test PID (like the 900-series fixtures used throughout this package's
	// tests) must never be checked against whatever real process happens to
	// hold that number on the machine running the suite
	// (sneat-dev/wb#622 review item 8 — this call site was the one place
	// that still read the real process table directly after the seam was
	// introduced for stop() and waitForSupervisorReplacement()).
	processStartTime := controller.deps.processStartTime
	if processStartTime == nil {
		processStartTime = daemon.ProcessStartTime
	}
	observed, observedKnown := processStartTime(state.PID)
	match, generationKnown := state.ProcessGenerationMatches(observed, observedKnown)
	switch {
	case generationKnown && !match:
		return identityProcessRecycled, fmt.Sprintf("PID %d now belongs to a process started %s, not the recorded one", state.PID, observed.UTC().Format(time.RFC3339)), true
	case generationKnown:
		return identityCurrent, "", true
	default:
		return identityCurrent, "this platform cannot observe a process start time; liveness is the PID coordinate alone", true
	}
}

// daemonSamePath compares two absolute paths that may differ only by a
// symlinked ancestor, which is how a home and its recorded name diverge after
// a move.
func daemonSamePath(left, right string) bool {
	resolve := func(path string) string {
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return filepath.Clean(resolved)
		}
		return filepath.Clean(path)
	}
	return resolve(left) == resolve(right)
}

// daemonLegacyEndpoint is a daemon still serving the runtime directory WB used
// before it resolved its home. WB reports it and disturbs nothing: the socket
// and state file may belong to a live daemon whose supervisor still points at
// them.
type daemonLegacyEndpoint struct {
	RuntimeDir    string `json:"runtime_dir"`
	SocketPath    string `json:"socket_path,omitempty"`
	SocketAnswers bool   `json:"socket_accepts_connections"`
	StatePath     string `json:"state_path,omitempty"`
	StateFound    bool   `json:"state_found"`
	StatePID      int    `json:"state_pid,omitempty"`
	StateLive     bool   `json:"state_live"`
}

// present reports whether the legacy endpoint holds a daemon at all. An
// accepting socket is enough: the legacy daemon does not have to be healthy for
// a second one to be a mistake.
func (endpoint daemonLegacyEndpoint) present() bool {
	return endpoint.SocketAnswers || (endpoint.StateFound && endpoint.StateLive)
}

// describe names the legacy endpoint the way an operator needs to hear it.
func (endpoint daemonLegacyEndpoint) describe() string {
	parts := make([]string, 0, 3)
	if endpoint.SocketAnswers && endpoint.SocketPath != "" {
		parts = append(parts, fmt.Sprintf("an accepting socket at %s", endpoint.SocketPath))
	}
	if endpoint.StateFound {
		state := fmt.Sprintf("a lifecycle record at %s", endpoint.StatePath)
		if endpoint.StatePID > 0 {
			state += fmt.Sprintf(" naming PID %d", endpoint.StatePID)
			if endpoint.StateLive {
				state += ", which is running"
			}
		}
		parts = append(parts, state)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("the runtime directory %s exists", endpoint.RuntimeDir)
	}
	return fmt.Sprintf("the runtime directory %s holds %s", endpoint.RuntimeDir, strings.Join(parts, " and "))
}

// detectLegacyDaemon looks, without modifying anything, for a daemon left in a
// state home this invocation no longer serves: the fixed pre-resolver
// <root>/.wb runtime directory, or a retired layout the home resolver still
// reports for root. A directory this build writes to is never a leftover.
//
// The one-root schema makes <root>/.wb the current home, so the retired
// default state directory $HOME/.wb is what a leftover daemon is normally
// serving; the fixed shape is kept because a future layout may separate them
// again.
func detectLegacyDaemon(root string, alive func(int) bool) daemonLegacyEndpoint {
	currentRuntime, currentErr := daemon.RuntimeDir(root)
	for _, legacyDir := range daemonLegacyRuntimeDirs(root) {
		// A home pinned to the legacy shape resolves its own runtime directory
		// to that same path, and the same directory is not a leftover:
		// detection is about a directory this build no longer writes to, not
		// about the name.
		if currentErr == nil && daemonSamePath(currentRuntime, legacyDir) {
			continue
		}
		if endpoint := inspectLegacyRuntimeDir(legacyDir, alive); endpoint.present() {
			return endpoint
		}
	}
	return daemonLegacyEndpoint{}
}

// daemonLegacyRuntimeDirs lists the runtime directories this build no longer
// writes for a projects root, deduplicated and in preference order: the fixed
// pre-resolver shape first, then every retired state home the resolver reports
// (the retired default state directory $HOME/.wb when it holds checkouts).
func daemonLegacyRuntimeDirs(root string) []string {
	dirs := make([]string, 0, 3)
	add := func(dir string) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		for _, existing := range dirs {
			if daemonSamePath(existing, dir) {
				return
			}
		}
		dirs = append(dirs, dir)
	}
	add(daemon.LegacyRuntimeDir(root))
	if resolution, err := wbhome.Resolve(root); err == nil {
		for _, layout := range resolution.Read {
			if layout.Legacy {
				add(filepath.Join(layout.Home, daemon.RuntimeDirName))
			}
		}
	}
	return dirs
}

// inspectLegacyRuntimeDir describes what, if anything, still serves one
// candidate runtime directory. It never modifies the directory.
func inspectLegacyRuntimeDir(legacyDir string, alive func(int) bool) daemonLegacyEndpoint {
	if _, err := os.Lstat(legacyDir); err != nil {
		return daemonLegacyEndpoint{}
	}
	endpoint := daemonLegacyEndpoint{
		RuntimeDir: legacyDir,
		StatePath:  filepath.Join(legacyDir, daemon.StateFileName),
	}
	if socketPath, ok := daemonSocketPathIn(legacyDir); ok {
		endpoint.SocketPath = socketPath
		endpoint.SocketAnswers = daemonSocketAnswers(socketPath)
	}
	state, found, err := (daemon.Store{Path: endpoint.StatePath}).Load()
	if err == nil && found {
		endpoint.StateFound = true
		endpoint.StatePID = state.PID
		endpoint.StateLive = state.PID > 0 && alive != nil && alive(state.PID)
	}
	return endpoint
}

// daemonSocketAnswers reports whether a socket path accepts a connection. A
// stale socket file fails to dial and is not a daemon.
func daemonSocketAnswers(path string) bool {
	if path == "" {
		return false
	}
	connection, err := net.DialTimeout(daemonLocalNetwork, path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}
