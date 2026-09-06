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
	open     func(string) error
	localURL func(context.Context, string) (string, error)
}

func defaultDashboardCommandDependencies() dashboardCommandDependencies {
	return dashboardCommandDependencies{
		open: openBrowser,
		localURL: func(ctx context.Context, root string) (string, error) {
			result, err := newDaemonController(defaultDaemonDependencies(), root).Start(ctx, daemonDefaultListen)
			if err != nil {
				return "", err
			}
			address := result.State.Listen
			if address == "" {
				address = daemonDefaultListen
			}
			return (&url.URL{Scheme: "http", Host: address, Path: "/"}).String(), nil
		},
	}
}

func newDashboardCmd() *cobra.Command {
	return newDashboardCmdWithDependencies(defaultDashboardCommandDependencies())
}

func newDashboardCmdWithDependencies(deps dashboardCommandDependencies) *cobra.Command {
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
				target, err = deps.localURL(command.Context(), projectsRoot)
				if err != nil {
					return err
				}
				scope = "local"
			}
			result := dashboardOpenResult{URL: target, Scope: scope}
			if format == "text" && !nonInteractive {
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
