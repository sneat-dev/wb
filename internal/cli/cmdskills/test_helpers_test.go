package cmdskills

import (
	"testing/fstest"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/wbskills"
	"github.com/strongo/cli-helpers/skillsync"
)

const (
	exitFindings = 1
	exitUsage    = 2
)

type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }
func testRuntime() shared.Runtime {
	return shared.Runtime{ExitError: func(code int, message string) error { return &exitError{code: code, message: message} }}
}
func testSyncDependencies() SyncDependencies {
	return SyncDependencies{Config: func() (skillsync.Config, error) {
		return wbskills.Config(fstest.MapFS{"skills/fixture/SKILL.md": {Data: []byte("---\nname: fixture\ndescription: fixture\n---\n# Fixture\n")}}, buildinfo.Report{Version: "1.2.3"})
	}, Home: func() (string, error) { return "/private/home", nil }, Getenv: func(string) string { return "" }}
}
func testHookDependencies() HookDependencies { return fakeHook() }

type failAfterWriter struct{ allowedWrites, calls int }
