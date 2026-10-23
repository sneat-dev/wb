package cmdinstall

import (
	"context"
	"net/http"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/wbupdate"
	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/cliinstall/cobracmd"
	"github.com/strongo/cli-helpers/selfupdate"
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
func newSelfUpdateConfig() selfupdate.Config { return wbupdate.Config("unknown") }
func newInstallCmd() *cobra.Command          { return NewInstall(testRuntime(), cobracmd.CommandOptions{}) }
func newUpgradeCmd() *cobra.Command          { return newUpgradeCmdWithConfig(newSelfUpdateConfig()) }
func newSelfUpdateCmd() *cobra.Command       { return newSelfUpdateCmdWithConfig(newSelfUpdateConfig()) }
func newUpgradeCmdWithConfig(cfg selfupdate.Config) *cobra.Command {
	return NewUpgrade(testRuntime(), cfg, testAfterUpdate)
}
func newSelfUpdateCmdWithConfig(cfg selfupdate.Config) *cobra.Command {
	return NewSelfUpdate(testRuntime(), cfg, testAfterUpdate)
}
func testAfterUpdate(_ context.Context, _ selfupdate.AfterUpdate, _ wbupdate.Output) error {
	return nil
}

type selfUpdateReleaseTransport func(*http.Request) (*http.Response, error)
