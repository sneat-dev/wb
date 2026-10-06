package main

import (
	"context"
	"net/url"

	"github.com/sneat-dev/wb/internal/cli/cmddashboard"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/spf13/cobra"
)

func newDashboardCmd(inv *invocation) *cobra.Command {
	return cmddashboard.New(newCLIRuntime(inv), defaultDashboardCommandDependencies())
}
func defaultDashboardCommandDependencies() cmddashboard.Operations {
	return cmddashboard.Operations{Open: openBrowser, LocalURL: func(ctx context.Context, root string) (string, string, error) {
		return dashboardLocalURL(ctx, root, defaultDaemonDependencies())
	}}
}
func dashboardLocalURL(ctx context.Context, root string, dependencies daemonDependencies) (string, string, error) {
	result, err := newDaemonController(dependencies, root).Start(ctx, daemonruntime.DefaultListen)
	if err != nil {
		return "", "", err
	}
	address, warning := dashboardStartedURL(result)
	return address, warning, nil
}
func dashboardStartedURL(result daemonruntime.Result) (address, warning string) {
	listen := result.State.Listen
	if listen == "" {
		listen = daemonruntime.DefaultListen
	}
	return (&url.URL{Scheme: "http", Host: listen, Path: "/"}).String(), result.Warning
}
