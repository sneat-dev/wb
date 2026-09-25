package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/spf13/cobra"
)

const hostedDashboardURL = "https://sneat.work/bench/dashboard/"

type dashboardOpenResult struct {
	URL    string `json:"url"`
	Scope  string `json:"scope"`
	Opened bool   `json:"opened"`
}

type dashboardCommandDependencies struct {
	open func(string) error
	// localURL also returns a non-fatal warning (sneat-dev/wb#622 review
	// round 3, item M1): an implicit Start that found a live, healthy,
	// supervised daemon under a different binary than this invocation's own
	// returns that daemon with a warning rather than an error (review round
	// 2, item 9), and this command must not silently drop it.
	localURL func(context.Context, string) (url string, warning string, err error)
}

func defaultDashboardCommandDependencies() dashboardCommandDependencies {
	return dashboardCommandDependencies{
		open: openBrowser,
		localURL: func(ctx context.Context, root string) (string, string, error) {
			result, err := newDaemonController(defaultDaemonDependencies(), root).Start(ctx, daemonDefaultListen)
			if err != nil {
				return "", "", err
			}
			address := result.State.Listen
			if address == "" {
				address = daemonDefaultListen
			}
			return (&url.URL{Scheme: "http", Host: address, Path: "/"}).String(), result.Warning, nil
		},
	}
}

func newDashboardCmd(inv *invocation) *cobra.Command {
	return newDashboardCmdWithDependencies(inv, defaultDashboardCommandDependencies())
}

func newDashboardCmdWithDependencies(inv *invocation, deps dashboardCommandDependencies) *cobra.Command {
	var local, jsonOut bool
	var format string
	command := &cobra.Command{
		Use:   "dashboard",
		Short: "Open the hosted cross-machine Workbench dashboard",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			target, scope := hostedDashboardURL, "hosted"
			if local {
				var warning string
				target, warning, err = deps.localURL(command.Context(), projectsRoot)
				if err != nil {
					return err
				}
				if warning != "" {
					_, _ = fmt.Fprintln(command.ErrOrStderr(), "wb:", warning)
				}
				scope = "local"
			}
			result := dashboardOpenResult{URL: target, Scope: scope}
			if format == "text" && !inv.nonInteractive {
				if err := deps.open(target); err != nil {
					return fmt.Errorf("open %s dashboard %s: %w", scope, target, err)
				}
				result.Opened = true
			}
			return writeDashboardOpenResult(command.OutOrStdout(), format, result)
		},
	}
	command.Flags().BoolVar(&local, "local", false, "open this machine's loopback daemon dashboard")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func writeDashboardOpenResult(out io.Writer, format string, result dashboardOpenResult) error {
	if format == "json" {
		return json.NewEncoder(out).Encode(result)
	}
	action := "dashboard"
	if result.Opened {
		action = "opened dashboard"
	}
	_, err := fmt.Fprintf(out, "%s: %s\n", action, result.URL)
	return err
}
