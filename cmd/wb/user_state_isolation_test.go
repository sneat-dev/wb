package main

import (
	"testing"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// Every path this binary resolves by default (the wb.yaml that names the
// fleet's claim store, the remote dependencies built from it, the agent
// configuration, and the projects root a command uses when given none) must
// sit under the private user TestMain created. A test that stubs half a
// command would otherwise act on the developer's real fleet with the rest.
func TestThisTestBinaryCannotReachARealUsersConfigurationOrFleet(t *testing.T) {
	t.Parallel()
	violations := testenv.UserStateViolations(
		wbconfig.DefaultPath(), defaultRemoteDeps().configPath, agentrun.DefaultDependencies().ConfigPath(), defaultProjectsRoot(),
	)
	if len(violations) != 0 {
		t.Fatalf("this test binary can reach real user state: %v", violations)
	}
}
