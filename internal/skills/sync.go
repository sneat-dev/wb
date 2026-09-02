package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Action classifies what Sync did (or, in a dry run, would do) with one
// skill.
type Action string

const (
	// Added means the skill was not installed and now is.
	Added Action = "added"
	// Updated means an installed skill's content hash changed and was
	// replaced.
	Updated Action = "updated"
	// Unchanged means the installed skill already matches the embedded one;
	// nothing was written.
	Unchanged Action = "unchanged"
	// Removed means a skill this wb build no longer ships was previously
	// installed by wb and has been deleted.
	Removed Action = "removed"
	// Conflict means a directory already occupies a shipped skill's name but
	// was never recorded as wb-installed. Sync never overwrites or deletes
	// it; the caller must report it and leave it alone.
	Conflict Action = "conflict"
)

// Change is one skill's outcome from a Sync call.
type Change struct {
	Name   string
	Action Action
}

// Report is the full outcome of one Sync call, in the shape `wb skills
// sync` prints and tests assert against.
type Report struct {
	// Dir is the harness skills directory Sync targeted.
	Dir string
	// PriorWBVersion is the wb_version recorded in the marker before this
	// run, or "" when there was no marker (a first sync).
	PriorWBVersion string
	// WBVersion is the wb build that performed this sync, now recorded in
	// the marker (unless DryRun).
	WBVersion string
	// Changes covers every shipped skill plus every previously-installed
	// skill this build no longer ships, sorted by name.
	Changes []Change
	// DryRun reports whether this Report describes a plan rather than an
	// applied sync.
	DryRun bool
}

// Names returns the skill names classified with action, in Report order.
func (r Report) Names(action Action) []string {
	var names []string
	for _, change := range r.Changes {
		if change.Action == action {
			names = append(names, change.Name)
		}
	}
	return names
}

// Changed reports whether this sync (or plan) wrote, or would write,
// anything at all -- the idempotency signal: a second run with nothing new
// to ship reports Changed() == false.
func (r Report) Changed() bool {
	return len(r.Names(Added)) > 0 || len(r.Names(Updated)) > 0 || len(r.Names(Removed)) > 0
}

// Options configures one Sync call.
type Options struct {
	// Source is the embedded skill tree, rooted so each skill's files sit at
	// "<name>/...". Ordinarily fs.Sub(ai.SkillsFS, "skills").
	Source fs.FS
	// Dir is the harness skills directory to install into, e.g.
	// ~/.claude/skills.
	Dir string
	// WBVersion is the running wb build's version, recorded in the marker.
	WBVersion string
	// DryRun computes and returns the Report without writing, removing, or
	// touching the marker.
	DryRun bool
}

// Sync installs every skill in opts.Source into opts.Dir, and removes any
// skill this build no longer ships that a previous Sync installed there.
//
// It is idempotent and always safe to re-run: a directory that already
// matches the embedded content is left untouched (Unchanged), and a
// directory Sync did not itself install -- because it predates any sync, or
// because its name collides with a shipped skill it never recorded owning --
// is never overwritten or deleted (Conflict). Only names this exact function
// previously wrote, per the marker, are ever candidates for Removed.
func Sync(opts Options) (Report, error) {
	report := Report{Dir: opts.Dir, WBVersion: opts.WBVersion, DryRun: opts.DryRun}

	discovered, err := Discover(opts.Source)
	if err != nil {
		return report, err
	}

	marker, err := ReadMarker(opts.Dir)
	switch {
	case err == nil:
		report.PriorWBVersion = marker.WBVersion
	case errors.Is(err, fs.ErrNotExist):
		marker = Marker{Skills: map[string]string{}}
	default:
		return report, err
	}
	if marker.Skills == nil {
		marker.Skills = map[string]string{}
	}

	present := make(map[string]bool, len(discovered))
	nextSkills := make(map[string]string, len(discovered))
	var changes []Change

	for _, skill := range discovered {
		present[skill.Name] = true
		priorHash, known := marker.Skills[skill.Name]
		installed := skillInstalled(opts.Dir, skill.Name)

		var action Action
		switch {
		case installed && !known:
			action = Conflict
		case !installed:
			action = Added
		case priorHash == skill.Hash:
			action = Unchanged
		default:
			action = Updated
		}
		changes = append(changes, Change{Name: skill.Name, Action: action})

		if action == Conflict {
			continue
		}
		nextSkills[skill.Name] = skill.Hash
		if action == Unchanged || opts.DryRun {
			continue
		}
		if err := writeSkill(opts.Source, opts.Dir, skill.Name); err != nil {
			return report, fmt.Errorf("install skill %s: %w", skill.Name, err)
		}
	}

	for name := range marker.Skills {
		if present[name] {
			continue
		}
		changes = append(changes, Change{Name: name, Action: Removed})
		if opts.DryRun {
			continue
		}
		if err := os.RemoveAll(filepath.Join(opts.Dir, name)); err != nil {
			return report, fmt.Errorf("remove stale skill %s: %w", name, err)
		}
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	report.Changes = changes

	if opts.DryRun {
		return report, nil
	}
	// A pure no-op -- same version, no skill content changed -- leaves the
	// marker file untouched rather than rewriting it with a fresh SyncedAt.
	// A version bump alone (skills happen to be byte-identical between
	// releases) still rewrites it: the marker's wb_version is what the drift
	// banner compares against, and it must record the version that actually
	// ran this sync even when there was nothing else to do.
	if !report.Changed() && report.PriorWBVersion == opts.WBVersion {
		return report, nil
	}
	return report, writeMarker(opts.Dir, Marker{
		WBVersion: opts.WBVersion,
		SyncedAt:  time.Now().UTC(),
		Skills:    nextSkills,
	})
}

// skillInstalled reports whether dir/name looks like an installed skill:
// specifically, whether its SKILL.md is present. A directory that exists but
// lacks SKILL.md (partially written, or cleared out from under wb) is
// treated as not installed, so Sync repairs it instead of leaving it
// half-present.
func skillInstalled(dir, name string) bool {
	info, err := os.Stat(filepath.Join(dir, name, "SKILL.md"))
	return err == nil && info.Mode().IsRegular()
}

// writeSkill replaces dir/name with a fresh copy of source's name subtree.
// It removes any existing directory first rather than overwriting file by
// file, so a file the embedded skill no longer ships (a renamed or deleted
// reference) does not linger on disk after an update.
func writeSkill(source fs.FS, dir, name string) error {
	target := filepath.Join(dir, name)
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return fs.WalkDir(source, name, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		destination := filepath.Join(dir, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o644)
	})
}
