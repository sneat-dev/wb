package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/console"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// cockpitOpenResult is the one JSON object `wb cockpit --format json` prints.
// It never carries a login code: the URL has no query string.
type cockpitOpenResult struct {
	URL   string `json:"url"`
	Scope string `json:"scope"`
	// Opened is always false: only text output opens a browser, and text
	// output prints a line rather than this object.
	Opened bool `json:"opened"`
}

// cockpitLocalSession is what a local invocation learns from the daemon: its
// listen address, a non-fatal warning from the start-or-reuse path, and, when
// one was requested, the login code and the path it is redeemed at.
type cockpitLocalSession struct {
	Listen  string
	Warning string
	Code    string
	Path    string
}

type cockpitCommandDependencies struct {
	daemon daemonDependencies
	open   func(string) error
	// isTerminal reports whether stdout is a terminal: the browser opens only
	// for a person at one, never for a pipe or an agent transcript.
	isTerminal func(any) bool
	configPath func() string
	// local starts or reuses the daemon and, only when mint is true, requests
	// a login code over the owner channel. JSON output never mints one.
	local func(ctx context.Context, deps daemonDependencies, root, listen string, mint bool) (cockpitLocalSession, error)
}

func defaultCockpitCommandDependencies() cockpitCommandDependencies {
	return cockpitCommandDependencies{
		daemon:     defaultDaemonDependencies(),
		open:       openBrowser,
		isTerminal: console.IsTerminal,
		configPath: wbconfig.DefaultPath,
		local:      cockpitLocalFromDaemon,
	}
}

// cockpitLocalFromDaemon starts or reuses this machine's daemon the way
// `wb dashboard --local` does and, when mint is set, asks it for a login code
// on the owner channel (the unix socket behind the owner bearer token).
func cockpitLocalFromDaemon(ctx context.Context, deps daemonDependencies, root, listen string, mint bool) (cockpitLocalSession, error) {
	controller := newDaemonController(deps, root)
	// `wb cockpit` opens the daemon this machine has; it never moves one. A live
	// daemon recorded on another address is reused when --listen was not given
	// (listen is empty) and is a refusal when it was, because Start would
	// otherwise replace it (rewriting the launchd job on macOS, or overwriting
	// its record and spawning a second process elsewhere).
	recorded, found, err := controller.store.Load()
	if err != nil {
		return cockpitLocalSession{}, fmt.Errorf("read the local daemon record: %w", err)
	}
	switch {
	case found && recorded.Status != daemon.StatusStopped && recorded.PID > 0 && deps.alive(recorded.PID):
		if listen == "" {
			listen = recorded.Listen
		} else if listen != recorded.Listen {
			return cockpitLocalSession{}, fmt.Errorf(
				"a local daemon is already running on %s, not the requested %s; use `wb cockpit --listen %s`, or stop it first with `wb daemon stop`",
				recorded.Listen, listen, recorded.Listen)
		}
	case listen == "":
		listen = daemonDefaultListen
	}
	result, err := controller.Start(ctx, listen)
	if err != nil {
		return cockpitLocalSession{}, fmt.Errorf("start local daemon: %w", err)
	}
	session := cockpitLocalSession{Listen: result.State.Listen, Warning: result.Warning}
	if !mint {
		return session, nil
	}
	client, err := cockpitOwnerClient(result, controller.store.Load, deps, root)
	if err != nil {
		return cockpitLocalSession{}, err
	}
	var issued struct {
		Code string `json:"code"`
		Path string `json:"path"`
	}
	if err := client.call(ctx, cockpit.LoginCodeRPCPath, struct{}{}, &issued); err != nil {
		return cockpitLocalSession{}, fmt.Errorf("request a cockpit login code: %w", err)
	}
	if issued.Code == "" {
		return cockpitLocalSession{}, errors.New("request a cockpit login code: the daemon returned no code")
	}
	if issued.Path != cockpit.LoginPath {
		return cockpitLocalSession{}, fmt.Errorf("request a cockpit login code: the daemon named the login path %q, want %q", issued.Path, cockpit.LoginPath)
	}
	session.Code, session.Path = issued.Code, issued.Path
	return session, nil
}

// cockpitOwnerClient builds the owner-channel client against the daemon that
// Start just brought up, so the command starts it exactly once. It keeps the
// readiness check `wb peers` applies to the same state.
func cockpitOwnerClient(result daemonResult, load func() (daemon.State, bool, error), deps daemonDependencies, root string) (*peerAdminClient, error) {
	state, found, err := load()
	if err != nil {
		return nil, err
	}
	if !found || state.Status != daemon.StatusReady || !result.ProcessManagerRunning {
		return nil, errors.New("local daemon is not ready")
	}
	localClient := deps.localClient
	if localClient == nil {
		localClient = daemonLocalHTTPClient
	}
	httpClient, err := localClient(root, state.OwnerToken)
	if err != nil {
		return nil, fmt.Errorf("open the owner channel to the local daemon: %w", err)
	}
	return &peerAdminClient{httpClient: httpClient}, nil
}

// cockpitOrigin renders the canonical origin for a daemon listen address.
func cockpitOrigin(listen string) (url.URL, error) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return url.URL{}, fmt.Errorf("daemon listen address %q: %w", listen, err)
	}
	return url.URL{Scheme: "http", Host: net.JoinHostPort(cockpit.CanonicalHost(listen), port)}, nil
}

func newCockpitCmd(inv *invocation) *cobra.Command {
	return newCockpitCmdWithDependencies(inv, defaultCockpitCommandDependencies())
}

func newCockpitCmdWithDependencies(inv *invocation, deps cockpitCommandDependencies) *cobra.Command {
	var hosted, jsonOut bool
	var format, listen string
	command := &cobra.Command{
		Use:   "cockpit",
		Short: "Open the Cockpit web workspace for this machine's fleet",
		Long: "Open Cockpit. By default it starts or reuses this machine's loopback daemon, requests a " +
			"single-use login code (valid for 60 seconds) and opens the login URL on the canonical origin. " +
			"--listen names the loopback address of the daemon to start (default 127.0.0.1:8766); without it a daemon " +
			"already running here is used wherever it listens. A running daemon on another address is never replaced: " +
			"the command refuses and names it. Non-loopback addresses are refused before anything starts. " +
			"--hosted opens the configured cockpit.hosted_url and starts nothing. " +
			"JSON output and non-interactive invocations print the URL without launching a browser; " +
			"JSON output never carries a login code.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			result := cockpitOpenResult{Scope: "hosted"}
			target := ""
			if hosted {
				config, err := wbconfig.LoadCockpit(deps.configPath())
				if err != nil {
					return fmt.Errorf("load the cockpit configuration: %w", err)
				}
				result.URL, target = config.HostedURL, config.HostedURL
			} else {
				if command.Flags().Changed("listen") {
					if err := requireLoopbackAddress(listen); err != nil {
						return usageError(err.Error())
					}
				}
				session, err := deps.local(command.Context(), deps.daemon, inv.projectsRoot, listen, format == "text")
				if err != nil {
					return err
				}
				origin, err := cockpitOrigin(session.Listen)
				if err != nil {
					return err
				}
				if session.Warning != "" {
					_, _ = fmt.Fprintln(command.ErrOrStderr(), "wb:", session.Warning)
				}
				result.Scope = "local"
				plain := origin
				plain.Path = cockpit.PagePrefix
				result.URL, target = plain.String(), plain.String()
				if session.Code != "" {
					origin.Path, origin.RawQuery = session.Path, url.Values{"code": {session.Code}}.Encode()
					target = origin.String()
				}
			}
			if format == "json" {
				return json.NewEncoder(command.OutOrStdout()).Encode(result)
			}
			// Print before opening: a local login code is live for 60 seconds
			// and must reach the operator even when the browser cannot open.
			if _, err := fmt.Fprintf(command.OutOrStdout(), "cockpit: %s\n", target); err != nil {
				return err
			}
			if !inv.nonInteractive && deps.isTerminal(command.OutOrStdout()) {
				if err := deps.open(target); err != nil {
					_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: could not open the browser: %v; use the URL above\n", err)
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&listen, "listen", "", "loopback address of the daemon to start (default 127.0.0.1:8766, or the running daemon's address)")
	command.Flags().BoolVar(&hosted, "hosted", false, "open the hosted Cockpit at cockpit.hosted_url and start no daemon")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}
