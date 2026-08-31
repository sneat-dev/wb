// Package buildinfo resolves the running WB binary's version so that any
// package can record it, not just the command layer.
//
// Resolution goes through github.com/strongo/buildinfo.Get("wb"), the
// fleet-wide shared module every CLI in this fleet now reports its version
// through: a release build stamps its link-time -X variables (see
// .goreleaser.yml's ldflags, which target github.com/strongo/buildinfo
// directly), a `go install` of the module leaves the version in the
// embedded build info, and an ad-hoc `go build` has neither, falling back to
// this package's own Unknown. Resolving that order in one shared place keeps
// `wb version`, `wb --version`, and the provenance WB writes into worktrees
// from ever disagreeing about which binary is running.
package buildinfo

import (
	"strings"
	"sync"

	strongobuildinfo "github.com/strongo/buildinfo"
)

// Unknown is reported when no version can be resolved at all. It is wb's own
// placeholder for strongobuildinfo's terminal fallback ("dev"), which this
// package translates on the way in, so wb's public contract here (and the
// self-update undetermined-version list in cmd/wb/selfupdate.go) never
// changes shape.
const Unknown = "unknown"

// dirtySuffix is the marker strongobuildinfo.Info.Commit carries when the
// build tree had uncommitted changes.
const dirtySuffix = "+dirty"

var (
	mu       sync.RWMutex
	override string
	resolved = resolve()
)

// resolve runs once, at package initialization: the -X stamps a release
// build sets on github.com/strongo/buildinfo's own package vars are already
// populated by the linker before any init() runs, so it is safe to read them
// here rather than deferring to an explicit Set call from main().
func resolve() strongobuildinfo.Info {
	info := strongobuildinfo.Get("wb")
	if info.Version == "dev" {
		info.Version = Unknown
	}
	return info
}

// Set overrides the resolved version. The command layer no longer needs to
// call this at start-up — .goreleaser.yml's ldflags target
// github.com/strongo/buildinfo directly, so a release build's version is
// already resolved by the time this package initializes. Set remains so
// tests can force a deterministic value (e.g. asserting that WB provenance
// appends a new entry when the reported version changes) without relinking
// the binary.
func Set(v string) {
	mu.Lock()
	defer mu.Unlock()
	override = v
}

// Version returns the override set via Set, if any, else the version
// resolved from github.com/strongo/buildinfo.Get("wb"). It never returns an
// empty string, so callers can record it unconditionally.
func Version() string {
	mu.RLock()
	v := override
	mu.RUnlock()
	if v != "" {
		return v
	}
	return resolved.Version
}

// Revision returns the resolved build's commit SHA with any "+dirty" suffix
// stripped, or "" when genuinely unknown. Pair with Modified for the
// decoration the bare SHA no longer carries.
func Revision() string {
	return strings.TrimSuffix(resolved.Commit, dirtySuffix)
}

// Modified reports whether the resolved build tree had uncommitted changes,
// per strongobuildinfo's "+dirty" commit-suffix convention.
func Modified() bool {
	return strings.HasSuffix(resolved.Commit, dirtySuffix)
}

// Date returns the resolved build's date (RFC 3339), or "" when unknown.
func Date() string {
	return resolved.Date
}
