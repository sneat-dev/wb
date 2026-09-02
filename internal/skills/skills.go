// Package skills installs WB's Agent Skills (embedded from ai/skills, see
// package ai) into a harness's own skills directory -- for example Claude
// Code's ~/.claude/skills -- so they are discoverable in every project, not
// only inside a checkout of sneat-dev/wb.
//
// The gap this closes: WB ships agent-facing skills under ai/skills/ in its
// own repository, and Claude Code auto-discovers them there through
// .claude-plugin/plugin.json -- but only for a session working inside that
// repository. A session orchestrating a different repository, with wb
// installed globally (Homebrew, go install, self-update), has never had
// those skills at all. `wb skills sync` is the fix: it copies every shipped
// skill into the harness's own skills directory once, so it is available
// everywhere wb is.
package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
)

// Skill is one shipped skill discovered in the embedded source: its name
// (the directory under ai/skills/, and the directory it is installed as
// under a harness skills directory) and a deterministic content hash of
// every file the skill directory contains.
type Skill struct {
	Name string
	Hash string
}

// Discover lists every skill in source: an fs.FS rooted so that a skill's
// files sit at "<name>/SKILL.md", "<name>/references/...", and so on --
// ordinarily fs.Sub(ai.SkillsFS, "skills"). A top-level entry only counts as
// a skill when it is a directory containing SKILL.md; commands.json and any
// other loose file at the same level is ignored.
//
// Results are sorted by name so callers, and the marker file Sync writes,
// never depend on directory iteration order.
func Discover(source fs.FS) ([]Skill, error) {
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read embedded skills: %w", err)
	}
	var discovered []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if _, err := fs.Stat(source, name+"/SKILL.md"); err != nil {
			// A directory with no SKILL.md is not a skill by the harness
			// convention this installs into; skip it rather than fail the
			// whole discovery over an unrelated stray directory.
			continue
		}
		hash, err := hashSkill(source, name)
		if err != nil {
			return nil, fmt.Errorf("hash skill %s: %w", name, err)
		}
		discovered = append(discovered, Skill{Name: name, Hash: hash})
	}
	sort.Slice(discovered, func(i, j int) bool { return discovered[i].Name < discovered[j].Name })
	return discovered, nil
}

// hashSkill hashes every file under source/name, in the deterministic
// lexical order fs.WalkDir guarantees, over both each file's slash-separated
// relative path and its content. Including the path means a rename inside a
// skill (not just a content edit) changes the hash, so Sync classifies it as
// an update rather than silently missing it.
func hashSkill(source fs.FS, name string) (string, error) {
	digest := sha256.New()
	walkErr := fs.WalkDir(source, name, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return err
		}
		relative := path[len(name)+1:]
		if _, err := fmt.Fprintf(digest, "%s\x00", relative); err != nil {
			return err
		}
		if _, err := digest.Write(data); err != nil {
			return err
		}
		_, err = digest.Write([]byte{0})
		return err
	})
	if walkErr != nil {
		return "", walkErr
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}
