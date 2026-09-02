package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MarkerFileName is the sync record Sync writes next to the skills it
// installs -- e.g. ~/.claude/skills/.wb-skills-sync.json. Leading-dot so it
// never reads as a skill directory itself. It is the single source both the
// drift banner (main.go) and `wb skills sync`'s own idempotency check read,
// so the two never disagree about what was last installed.
const MarkerFileName = ".wb-skills-sync.json"

// markerSchemaVersion guards a future incompatible marker shape. Sync always
// writes the current version; ReadMarker refuses to interpret an unknown
// newer one rather than guess its meaning.
const markerSchemaVersion = 1

// Marker records what `wb skills sync` last installed: which wb build ran
// it, when, and the exact content hash of every skill it wrote -- so a later
// run can tell added/updated/unchanged/removed apart without re-hashing
// installed files (which a user could have hand-edited) and so wb can print
// a drift warning by comparing WBVersion against the binary currently
// running, with no filesystem walk at all.
type Marker struct {
	SchemaVersion int               `json:"schema_version"`
	WBVersion     string            `json:"wb_version"`
	SyncedAt      time.Time         `json:"synced_at"`
	Skills        map[string]string `json:"skills"`
}

// markerPath is where Marker lives for a harness skills directory.
func markerPath(skillsDir string) string {
	return filepath.Join(skillsDir, MarkerFileName)
}

// ReadMarker reads the sync marker from skillsDir. A missing marker --
// skills never synced on this machine -- is reported as os.ErrNotExist
// wrapped so callers can use errors.Is; every other read or parse failure is
// returned as-is.
func ReadMarker(skillsDir string) (Marker, error) {
	raw, err := os.ReadFile(markerPath(skillsDir))
	if err != nil {
		return Marker{}, err
	}
	var marker Marker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return Marker{}, fmt.Errorf("parse %s: %w", markerPath(skillsDir), err)
	}
	if marker.SchemaVersion > markerSchemaVersion {
		return Marker{}, fmt.Errorf("%s schema_version %d is newer than this wb build understands (%d); run a newer wb", markerPath(skillsDir), marker.SchemaVersion, markerSchemaVersion)
	}
	return marker, nil
}

// writeMarker atomically replaces the sync marker: written to a temp file in
// the same directory, then renamed over the target, so a process killed
// mid-write never leaves a truncated or corrupt marker behind.
func writeMarker(skillsDir string, marker Marker) error {
	marker.SchemaVersion = markerSchemaVersion
	encoded, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')

	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", skillsDir, err)
	}
	temp, err := os.CreateTemp(skillsDir, ".wb-skills-sync-*.tmp")
	if err != nil {
		return fmt.Errorf("stage sync marker: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write sync marker: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close sync marker: %w", err)
	}
	if err := os.Rename(tempName, markerPath(skillsDir)); err != nil {
		return fmt.Errorf("replace %s: %w", markerPath(skillsDir), err)
	}
	return nil
}
