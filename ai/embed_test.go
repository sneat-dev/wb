package ai

import (
	"io/fs"
	"os"
	"testing"

	"github.com/strongo/cli-helpers/skillsync"
)

// TestSkillsFSEmbedsEveryOnDiskSkill guards against the failure mode that
// motivated embedding at all: `wb skills sync` running from an installed
// binary sees exactly what is checked into ai/skills/, not a stale or
// partial copy pinned at build time by an overly narrow //go:embed pattern.
func TestSkillsFSEmbedsEveryOnDiskSkill(t *testing.T) {
	onDisk, err := os.ReadDir("skills")
	if err != nil {
		t.Fatal(err)
	}
	var wantNames []string
	for _, entry := range onDisk {
		if !entry.IsDir() {
			continue
		}
		if _, err := os.Stat("skills/" + entry.Name() + "/SKILL.md"); err != nil {
			continue
		}
		wantNames = append(wantNames, entry.Name())
	}
	if len(wantNames) == 0 {
		t.Fatal("no on-disk skill directories found; test fixture assumption broke")
	}

	sub, err := fs.Sub(SkillsFS, "skills")
	if err != nil {
		t.Fatal(err)
	}
	discovered, err := skillsync.Discover(sub)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != len(wantNames) {
		t.Fatalf("embedded %d skills, on-disk has %d: %+v", len(discovered), len(wantNames), discovered)
	}
	for _, name := range wantNames {
		found := false
		for _, skill := range discovered {
			if skill.Name == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("on-disk skill %q is not embedded in SkillsFS", name)
		}
	}
}
