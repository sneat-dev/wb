package wbskills

import (
	"os"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/strongo/cli-helpers/skillsync"
	skillscmd "github.com/strongo/cli-helpers/skillsync/cobracmd"
)

func testAnnouncementDependencies() AnnouncementDependencies {
	return AnnouncementDependencies{SkillsDir: func() (string, error) {
		home, e := os.UserHomeDir()
		if e != nil {
			return "", e
		}
		return skillscmd.DefaultHarnesses[0].SkillsDir(home, os.Getenv), nil
	}, ReadStatus: skillsync.ReadStatus, Version: buildinfo.Version}
}
