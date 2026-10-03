package wbskills

import (
	"strings"

	"github.com/strongo/cli-helpers/skillsync"
)

type AnnouncementDependencies struct {
	SkillsDir  func() (string, error)
	ReadStatus func(string) (skillsync.Status, error)
	Version    func() string
}

// sessionStartAnnouncement never returns an error and never panics: it is
// injected into every Claude Code session's opening context, so every
// failure along the way -- no resolvable home directory, no marker yet, a
// corrupt one -- degrades to the registration reminder alone rather than
// producing nothing or crashing the hook.
func Announcement(deps AnnouncementDependencies) string {
	lines := []string{
		"WB session start: if this session has not already run `wb session register` for itself, run it now with --pid $PPID from your own tool-call shell (see `wb session register --help`).",
	}
	dir, err := deps.SkillsDir()
	if err != nil {
		return strings.Join(lines, "\n")
	}
	status, err := deps.ReadStatus(dir)
	if err != nil {
		return strings.Join(lines, "\n")
	}
	current := deps.Version()
	synced, installed := SyncedWBVersion(status)
	if current != "" && current != "unknown" && current != "(devel)" && (!installed || synced != current) {
		lines = append(lines, DriftMessage(dir, status, current))
	}
	return strings.Join(lines, "\n")
}
