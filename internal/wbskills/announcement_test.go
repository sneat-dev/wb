package wbskills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/buildinfo" // TestSessionStartAnnouncementAlwaysRemindsRegistration covers a fresh home
	// with no marker at all. A SessionStart hook only ever runs because Claude
	// Code itself is launching a session, so -- unlike the general drift banner
	// in main.go, which must stay silent on a machine with no harness present
	// at all -- this announcement always reports "never synced" as exactly the
	// drift it is: the reminder this whole feature exists to give.
	// no ~/.claude/skills at all
	// a go test binary otherwise reports an undetermined version, which never counts as drifted
)

func TestSessionStartAnnouncementAlwaysRemindsRegistration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	buildinfo.Set("1.2.3")
	t.Cleanup(func() { buildinfo.Set("") })
	announcement := Announcement(testAnnouncementDependencies())
	if !strings.Contains(announcement, "wb session register") {
		t.Errorf("announcement = %q, want the registration reminder", announcement)
	}
	if !strings.Contains(announcement, "wb skills sync") {
		t.Errorf("announcement = %q, want a drift warning: skills were never synced under this home", announcement)
	}
}
func TestSessionStartAnnouncementWarnsWhenSkillsAreStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	buildinfo.Set("1.2.3")
	t.Cleanup(func() { buildinfo.Set("") })
	skillsDir := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, ".wb-skills-sync.json"),
		[]byte(`{"schema_version":1,"wb_version":"0.0.1-old","skills":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	announcement := Announcement(testAnnouncementDependencies())
	if !strings.Contains(announcement, "wb skills sync") {
		t.Errorf("announcement = %q, want a drift warning when the marker predates the running wb", announcement)
	}
}
