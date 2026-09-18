package daemon

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// RuntimeDirName is the directory inside WB's home that holds daemon runtime
// state: the local socket, the lifecycle record, and the daemon's log.
const RuntimeDirName = "runtime"

// StateFileName is the durable lifecycle record inside the runtime directory.
const StateFileName = "daemon-state.json"

// LegacyRuntimeDirName reproduces the runtime directory WB used before the
// daemon consulted the home resolver: a literal ".wb" beneath the projects
// root. It exists so a daemon left there can be *detected* and reported rather
// than silently doubled. Nothing in this package writes to it.
//
// It is spelled out here, rather than reusing wbhome, precisely because it is
// the shape that must not be produced again: wbhome resolves a home, and this
// is a fixed historical path.
func LegacyRuntimeDir(projectsRoot string) string {
	root := strings.TrimSpace(projectsRoot)
	if root == "" {
		return ""
	}
	return filepath.Join(root, ".wb", RuntimeDirName)
}

// RuntimeDir resolves the daemon's runtime directory through WB's one home
// resolver, so a WB_HOME move moves the daemon with every other subsystem.
//
// This is the whole point of the function: the daemon previously built its
// runtime path by joining the projects root with a literal ".wb", which meant
// that when WB_HOME moved — including the symlink-then-revert migration of
// 2026-09-15 — every subsystem followed and the daemon stayed behind, still
// serving a socket inside a directory the rest of WB had abandoned.
func RuntimeDir(projectsRoot string) (string, error) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return "", fmt.Errorf("resolve WB home for daemon runtime: %w", err)
	}
	return filepath.Join(home, RuntimeDirName), nil
}

// StatePath is the runtime directory's lifecycle record.
func StatePath(projectsRoot string) (string, error) {
	runtime, err := RuntimeDir(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(runtime, StateFileName), nil
}

// OperationsDir is the daemon's durable operation store.
//
// It is derived from the same home as every other runtime artefact, and it is
// passed to NewService explicitly rather than resolved inside it: a
// constructor that reads the environment makes every caller share one store,
// which is both untestable in isolation and wrong for a library.
func OperationsDir(projectsRoot string) (string, error) {
	runtime, err := RuntimeDir(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(runtime, "daemon", "operations"), nil
}

// SocketFileName is the local endpoint's name inside the runtime directory.
const SocketFileName = "daemon.sock"

// SocketPath is the home-derived local endpoint of the daemon.
//
// The path is also an identity: unlike the shared loopback port it names the
// one home whose daemon may own it, which is why status reports it rather than
// inferring ownership from whoever answers on the port.
func SocketPath(projectsRoot string) (string, error) {
	runtime, err := RuntimeDir(projectsRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(runtime, SocketFileName), nil
}

// LegacyStatePath is the lifecycle record a daemon wrote before the runtime
// directory followed WB's home. It exists for detection only; nothing here
// reads it as this build's own state.
func LegacyStatePath(projectsRoot string) string {
	legacy := LegacyRuntimeDir(projectsRoot)
	if legacy == "" {
		return ""
	}
	return filepath.Join(legacy, StateFileName)
}

// IsForeignHome reports whether a record was written for a different WB home
// than the one this invocation resolves.
//
// An empty recorded home means the record predates home identity. That is
// reported as unknown rather than as a match: claiming a record is ours
// because it failed to say otherwise is exactly how a daemon from an abandoned
// home gets presented as this machine's daemon.
func (s State) IsForeignHome(currentHome string) (foreign bool, known bool) {
	recorded := strings.TrimSpace(s.WBHome)
	if recorded == "" {
		return false, false
	}
	return filepath.Clean(recorded) != filepath.Clean(strings.TrimSpace(currentHome)), true
}
