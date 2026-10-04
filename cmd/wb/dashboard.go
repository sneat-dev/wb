package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cockpit"
)

const hostedDashboardURL = "https://sneat.work/bench/dashboard/"

// dashboardLocalDeprecation is what `wb dashboard --local` says on stderr: the
// operations pages this flag once opened are retired, and Cockpit replaces
// them. The flag still starts or reuses the daemon and opens Cockpit, without
// the sign-in that `wb cockpit` performs.
const dashboardLocalDeprecation = "--local is deprecated: the local operations dashboard is retired and Cockpit replaces it; run `wb cockpit`, which also signs you in"

type dashboardOpenResult struct {
	URL    string `json:"url"`
	Scope  string `json:"scope"`
	Opened bool   `json:"opened"`
}

type dashboardCommandDependencies struct {
	open func(string) error
	// localURL is the address the local daemon listens at; the command opens
	// Cockpit under it. It also returns a non-fatal warning (sneat-dev/wb#622 review
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
			result, err := newDaemonController(defaultDaemonDependencies(), root).Start(ctx, daemonruntime.DefaultListen)
			if err != nil {
				return "", "", err
			}
			address := result.State.Listen
			if address == "" {
				address = daemonruntime.DefaultListen
			}
			return (&url.URL{Scheme: "http", Host: address, Path: "/"}).String(), result.Warning, nil
		},
	}
}

// localCockpitURL is the plain Cockpit address under base, the address the
// daemon's listener answers at.
func localCockpitURL(base string) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("daemon address %q: %w", base, err)
	}
	parsed.Path = cockpit.PagePrefix
	return parsed.String(), nil
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
		Long: "Open the hosted cross-machine Workbench dashboard. This machine's own web interface is Cockpit: " +
			"run `wb cockpit`. The local operations pages that --local, --metrics and --coverage used to open " +
			"are retired; --local now opens Cockpit and is deprecated in favour of `wb cockpit`.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			target, scope := hostedDashboardURL, "hosted"
			if local {
				_, _ = fmt.Fprintln(command.ErrOrStderr(), "wb:", dashboardLocalDeprecation)
				var warning string
				target, warning, err = deps.localURL(command.Context(), inv.projectsRoot)
				if err != nil {
					return err
				}
				if warning != "" {
					_, _ = fmt.Fprintln(command.ErrOrStderr(), "wb:", warning)
				}
				if target, err = localCockpitURL(target); err != nil {
					return err
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
	command.Flags().BoolVar(&local, "local", false, "deprecated: open this machine's Cockpit (use `wb cockpit`)")
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
