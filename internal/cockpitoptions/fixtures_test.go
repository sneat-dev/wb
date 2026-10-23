package cockpitoptions

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func testError(_ int, s string) error { return testErrorText(s) }

type testErrorText string

func (e testErrorText) Error() string { return string(e) }
func testOptions(root, home, path string, config wbconfig.CockpitConfig, logs io.Writer, hostname func() (string, error), ssh SSH) cockpitfleet.Options {
	deps := DefaultDependencies(path, testError)
	deps.Hostname, deps.SSH = hostname, ssh
	return Options(Request{ProjectsRoot: root, Home: home, ConfigPath: path, Config: config, Logs: logs}, deps)
}
func testDefaultOptions(root, home, path string, config wbconfig.CockpitConfig, logs io.Writer, hostname func() (string, error)) cockpitfleet.Options {
	deps := DefaultDependencies(path, testError)
	deps.Hostname = hostname
	return Options(Request{ProjectsRoot: root, Home: home, ConfigPath: path, Config: config, Logs: logs}, deps)
}
func cockpitConfigFile(t *testing.T, content string) func() string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() string { return path }
}
