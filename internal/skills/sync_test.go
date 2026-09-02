package skills

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func fixtureSource(t *testing.T, contents map[string]string) fs.FS {
	t.Helper()
	fsys := fstest.MapFS{}
	for path, data := range contents {
		fsys[path] = &fstest.MapFile{Data: []byte(data), Mode: 0o644}
	}
	return fsys
}

func twoSkillSource(t *testing.T) fs.FS {
	return fixtureSource(t, map[string]string{
		"wb-alpha/SKILL.md":           "# alpha v1\n",
		"wb-alpha/references/deep.md": "deep\n",
		"wb-beta/SKILL.md":            "# beta v1\n",
		"stray-file.json":             "{}",            // not a skill: a file, not a dir
		"not-a-skill/README.md":       "no SKILL.md\n", // dir without SKILL.md
	})
}

func TestDiscoverOnlyReturnsDirectoriesWithSkillMD(t *testing.T) {
	t.Parallel()
	discovered, err := Discover(twoSkillSource(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 2 {
		t.Fatalf("Discover returned %d skills, want 2: %+v", len(discovered), discovered)
	}
	if discovered[0].Name != "wb-alpha" || discovered[1].Name != "wb-beta" {
		t.Fatalf("Discover order/names = %+v, want [wb-alpha wb-beta]", discovered)
	}
	if discovered[0].Hash == "" || discovered[1].Hash == "" {
		t.Fatalf("Discover left an empty hash: %+v", discovered)
	}
	if discovered[0].Hash == discovered[1].Hash {
		t.Fatalf("different skills hashed the same: %+v", discovered)
	}
}

func TestHashSkillChangesWithContentAndWithPath(t *testing.T) {
	t.Parallel()
	base := twoSkillSource(t)
	baseHash, err := hashSkill(base, "wb-alpha")
	if err != nil {
		t.Fatal(err)
	}

	editedContent := fixtureSource(t, map[string]string{
		"wb-alpha/SKILL.md":           "# alpha v2\n",
		"wb-alpha/references/deep.md": "deep\n",
	})
	editedHash, err := hashSkill(editedContent, "wb-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if editedHash == baseHash {
		t.Fatal("changing file content did not change the hash")
	}

	renamedPath := fixtureSource(t, map[string]string{
		"wb-alpha/SKILL.md":              "# alpha v1\n",
		"wb-alpha/references/renamed.md": "deep\n",
	})
	renamedHash, err := hashSkill(renamedPath, "wb-alpha")
	if err != nil {
		t.Fatal(err)
	}
	if renamedHash == baseHash {
		t.Fatal("renaming a file with identical content did not change the hash")
	}
}

func TestSyncFirstRunInstallsEverythingAndWritesMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	report, err := Sync(Options{Source: twoSkillSource(t), Dir: dir, WBVersion: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if report.PriorWBVersion != "" {
		t.Fatalf("PriorWBVersion = %q, want empty on first sync", report.PriorWBVersion)
	}
	if !report.Changed() {
		t.Fatal("first sync reported no change")
	}
	if got := report.Names(Added); len(got) != 2 || got[0] != "wb-alpha" || got[1] != "wb-beta" {
		t.Fatalf("Added = %v, want [wb-alpha wb-beta]", got)
	}

	for _, name := range []string{"wb-alpha", "wb-beta"} {
		if _, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err != nil {
			t.Fatalf("%s/SKILL.md missing after sync: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "wb-alpha", "references", "deep.md")); err != nil {
		t.Fatalf("nested reference missing after sync: %v", err)
	}

	marker, err := ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if marker.WBVersion != "1.2.3" {
		t.Fatalf("marker.WBVersion = %q, want 1.2.3", marker.WBVersion)
	}
	if len(marker.Skills) != 2 {
		t.Fatalf("marker.Skills = %+v, want 2 entries", marker.Skills)
	}
}

func TestSyncSecondRunWithNoChangeIsUnchangedAndIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := twoSkillSource(t)
	if _, err := Sync(Options{Source: source, Dir: dir, WBVersion: "1.2.3"}); err != nil {
		t.Fatal(err)
	}
	beforeMarker, err := os.ReadFile(markerPath(dir))
	if err != nil {
		t.Fatal(err)
	}

	report, err := Sync(Options{Source: source, Dir: dir, WBVersion: "1.2.3"})
	if err != nil {
		t.Fatal(err)
	}
	if report.PriorWBVersion != "1.2.3" {
		t.Fatalf("PriorWBVersion = %q, want 1.2.3", report.PriorWBVersion)
	}
	if report.Changed() {
		t.Fatalf("second identical sync reported a change: %+v", report.Changes)
	}
	if got := report.Names(Unchanged); len(got) != 2 {
		t.Fatalf("Unchanged = %v, want both skills", got)
	}

	afterMarker, err := os.ReadFile(markerPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if string(beforeMarker) != string(afterMarker) {
		t.Fatal("an idempotent sync rewrote the marker")
	}
}

// TestSyncRewritesTheMarkerOnAVersionBumpEvenWithIdenticalSkillContent
// covers the case a self-update leaves behind: the running wb changed but
// happened to ship byte-identical skills. Without this, the drift banner
// would keep telling a user to sync forever after they already did.
func TestSyncRewritesTheMarkerOnAVersionBumpEvenWithIdenticalSkillContent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := twoSkillSource(t)
	if _, err := Sync(Options{Source: source, Dir: dir, WBVersion: "1.2.3"}); err != nil {
		t.Fatal(err)
	}

	report, err := Sync(Options{Source: source, Dir: dir, WBVersion: "1.3.0"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Changed() {
		t.Fatalf("no skill content changed, want no reported change: %+v", report.Changes)
	}

	marker, err := ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if marker.WBVersion != "1.3.0" {
		t.Fatalf("marker.WBVersion = %q, want 1.3.0 recorded despite unchanged skill content", marker.WBVersion)
	}
}

func TestSyncDetectsUpdateAndReplacesStaleFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	original := fixtureSource(t, map[string]string{
		"wb-alpha/SKILL.md":        "# v1\n",
		"wb-alpha/references/a.md": "a\n",
	})
	if _, err := Sync(Options{Source: original, Dir: dir, WBVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}

	updated := fixtureSource(t, map[string]string{
		"wb-alpha/SKILL.md":        "# v2\n",
		"wb-alpha/references/b.md": "b\n", // a.md dropped, b.md added between versions
	})
	report, err := Sync(Options{Source: updated, Dir: dir, WBVersion: "1.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Updated); len(got) != 1 || got[0] != "wb-alpha" {
		t.Fatalf("Updated = %v, want [wb-alpha]", got)
	}

	content, err := os.ReadFile(filepath.Join(dir, "wb-alpha", "SKILL.md"))
	if err != nil || string(content) != "# v2\n" {
		t.Fatalf("SKILL.md not replaced: content=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wb-alpha", "references", "a.md")); !os.IsNotExist(err) {
		t.Fatalf("stale reference a.md survived an update: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wb-alpha", "references", "b.md")); err != nil {
		t.Fatalf("new reference b.md missing after update: %v", err)
	}
}

func TestSyncRemovesSkillsNoLongerShippedThatItPreviouslyInstalled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := Sync(Options{Source: twoSkillSource(t), Dir: dir, WBVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}

	onlyAlpha := fixtureSource(t, map[string]string{"wb-alpha/SKILL.md": "# alpha v1\n"})
	report, err := Sync(Options{Source: onlyAlpha, Dir: dir, WBVersion: "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Removed); len(got) != 1 || got[0] != "wb-beta" {
		t.Fatalf("Removed = %v, want [wb-beta]", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "wb-beta")); !os.IsNotExist(err) {
		t.Fatalf("wb-beta directory survived removal: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "wb-alpha", "SKILL.md")); err != nil {
		t.Fatalf("unrelated wb-alpha removed by mistake: %v", err)
	}
}

func TestSyncNeverOverwritesAPreExistingUnmanagedDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "wb-alpha"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wb-alpha", "SKILL.md"), []byte("mine, not wb's\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Sync(Options{Source: twoSkillSource(t), Dir: dir, WBVersion: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Conflict); len(got) != 1 || got[0] != "wb-alpha" {
		t.Fatalf("Conflict = %v, want [wb-alpha]", got)
	}
	if got := report.Names(Added); len(got) != 1 || got[0] != "wb-beta" {
		t.Fatalf("Added = %v, want [wb-beta] (wb-alpha must be skipped)", got)
	}

	content, err := os.ReadFile(filepath.Join(dir, "wb-alpha", "SKILL.md"))
	if err != nil || string(content) != "mine, not wb's\n" {
		t.Fatalf("conflicting directory was overwritten: content=%q err=%v", content, err)
	}

	marker, err := ReadMarker(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, recorded := marker.Skills["wb-alpha"]; recorded {
		t.Fatal("a conflicting skill must never be recorded as wb-managed")
	}
}

func TestSyncDryRunPlansWithoutWritingAnything(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	report, err := Sync(Options{Source: twoSkillSource(t), Dir: dir, WBVersion: "1.2.3", DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun {
		t.Fatal("DryRun not reflected on Report")
	}
	if got := report.Names(Added); len(got) != 2 {
		t.Fatalf("dry run Added = %v, want both skills planned", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("dry run wrote to %s: %v", dir, entries)
	}
	if _, err := ReadMarker(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry run wrote a marker: err=%v", err)
	}
}

func TestSyncRecoversASkillDeletedFromDiskDespiteAMatchingMarker(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := twoSkillSource(t)
	if _, err := Sync(Options{Source: source, Dir: dir, WBVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "wb-alpha")); err != nil {
		t.Fatal(err)
	}

	report, err := Sync(Options{Source: source, Dir: dir, WBVersion: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := report.Names(Added); len(got) != 1 || got[0] != "wb-alpha" {
		t.Fatalf("Added = %v, want [wb-alpha] recovered", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "wb-alpha", "SKILL.md")); err != nil {
		t.Fatalf("wb-alpha not restored: %v", err)
	}
}

func TestReadMarkerReportsNotExistWhenAbsent(t *testing.T) {
	t.Parallel()
	if _, err := ReadMarker(t.TempDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadMarker on an empty dir: err=%v, want fs.ErrNotExist", err)
	}
}

func TestReadMarkerRejectsANewerSchemaVersion(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(markerPath(dir), []byte(`{"schema_version":99}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadMarker(dir); err == nil {
		t.Fatal("ReadMarker accepted a schema_version newer than this build understands")
	}
}

func TestStatusDriftedCoversNeverSyncedAndVersionMismatch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	never := Status{}
	if !never.Drifted("1.0.0") {
		t.Fatal("never-synced status must report drifted for a determined version")
	}
	if never.Drifted("unknown") || never.Drifted("(devel)") || never.Drifted("") {
		t.Fatal("an undetermined running version must never report drift")
	}

	if _, err := Sync(Options{Source: twoSkillSource(t), Dir: dir, WBVersion: "1.0.0"}); err != nil {
		t.Fatal(err)
	}
	status, err := ReadStatus(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || status.SyncedWBVersion != "1.0.0" {
		t.Fatalf("status = %+v, want Installed with 1.0.0", status)
	}
	if status.Drifted("1.0.0") {
		t.Fatal("matching versions must not report drift")
	}
	if !status.Drifted("1.1.0") {
		t.Fatal("a version bump since the last sync must report drift")
	}
}
