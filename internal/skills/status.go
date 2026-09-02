package skills

import (
	"errors"
	"io/fs"
)

// Status is the cheap, marker-only read Drift and the SessionStart hook use:
// no embedded-skill discovery, no filesystem walk of the installed skills --
// just the one small marker file, so it costs nothing to check on every wb
// invocation.
type Status struct {
	// Installed reports whether a marker exists at all, i.e. `wb skills
	// sync` has run on this machine before.
	Installed bool
	// SyncedWBVersion is the wb_version recorded in the marker. Empty when
	// !Installed.
	SyncedWBVersion string
}

// ReadStatus reads skillsDir's marker and reports it as a Status. A missing
// marker is not an error here -- it is the ordinary "never synced" case --
// but any other read or parse failure is returned.
func ReadStatus(skillsDir string) (Status, error) {
	marker, err := ReadMarker(skillsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}
	return Status{Installed: true, SyncedWBVersion: marker.WBVersion}, nil
}

// Drifted reports whether currentWBVersion disagrees with the marker enough
// to warn about it: either skills were never synced, or they were synced by
// a different wb version than the one running now. An "unknown"/"(devel)"
// currentWBVersion -- an undetermined dev build -- never counts as drifted,
// so a plain `go build` of wb does not nag a developer about a comparison
// that means nothing.
func (s Status) Drifted(currentWBVersion string) bool {
	if currentWBVersion == "" || currentWBVersion == "unknown" || currentWBVersion == "(devel)" {
		return false
	}
	if !s.Installed {
		return true
	}
	return s.SyncedWBVersion != currentWBVersion
}
