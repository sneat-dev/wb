package cmdskills

import "github.com/strongo/cli-helpers/skillsync"

type SyncDependencies struct {
	Config func() (skillsync.Config, error)
	Home   func() (string, error)
	Getenv func(string) string
}

type HookDependencies struct {
	Quote         func(string) string
	Executable    func() string
	Home          func() (string, error)
	MergeSettings func(string, string) ([]byte, bool, error)
	WriteSettings func(string, []byte) error
	Announcement  func() string
}
