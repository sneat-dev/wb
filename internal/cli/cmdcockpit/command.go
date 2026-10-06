// Package cmdcockpit binds Cockpit arguments and output to narrow operations.
package cmdcockpit

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/cockpitrun"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/spf13/cobra"
	"net"
	"net/url"
)

type Dependencies struct {
	Local      func(context.Context, cockpitrun.LocalRequest) (cockpitrun.LocalSession, error)
	HostedURL  func() (string, error)
	Export     func(context.Context, cockpitrun.ExportRequest) cockpitrun.ExportResult
	Open       func(string) error
	IsTerminal func(any) bool
}

// cockpitOpenResult is the one JSON object `wb cockpit --format json` prints.
// URL never carries a login code or a session key: it has no query string and
// no fragment. LoginURL is the credential, and is there only when --print-url
// asked for it.
type cockpitOpenResult struct {
	URL      string `json:"url"`
	LoginURL string `json:"login_url,omitempty"`
	Scope    string `json:"scope"`
	// Opened is always false: only text output opens a browser, and text
	// output prints a line rather than this object.
	Opened bool `json:"opened"`
}

// cockpitLoginURLHint is what the command says, on stderr, when it prints the
// plain address because stdout is not a terminal.
const cockpitLoginURLHint = "the login URL is a credential and is printed only on a terminal; pass --print-url to print it here"

// cockpitOrigin renders the canonical origin for a daemon listen address.
func cockpitOrigin(listen string) (url.URL, error) {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return url.URL{}, fmt.Errorf("daemon listen address %q: %w", listen, err)
	}
	return url.URL{Scheme: "http", Host: net.JoinHostPort(cockpit.CanonicalHost(listen), port)}, nil
}

func New(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var hosted, jsonOut, printURL bool
	var format, listen string
	command := &cobra.Command{
		Use:   "cockpit",
		Short: "Open the Cockpit web workspace for this machine's fleet",
		Long: "Open Cockpit. By default it starts or reuses this machine's loopback daemon, requests a " +
			"single-use login code (valid for 60 seconds) and opens the login URL on the canonical origin. " +
			"The login URL is a credential: its fragment carries the session key, which the page keeps and sends with " +
			"each request, because the session cookie alone, which every server on the loopback host receives, does " +
			"not make an owner. It is printed only when stdout is a terminal, or with --print-url; otherwise the " +
			"plain Cockpit URL is printed and no login code is requested. Do not paste it into a log or a transcript. " +
			"A running daemon too old to issue a session key is refused: restart it with `wb daemon restart`. " +
			"--listen names the loopback address of the daemon to start (default 127.0.0.1:8766); without it a daemon " +
			"already running here is used wherever it listens. A running daemon on another address is never replaced: " +
			"the command refuses and names it. Non-loopback addresses are refused before anything starts. " +
			"--hosted opens the configured cockpit.hosted_url and starts nothing. " +
			"JSON output and non-interactive invocations print the URL without launching a browser; " +
			"JSON output carries a login code and a session key only in `login_url`, and only with --print-url.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			result := cockpitOpenResult{Scope: "hosted"}
			target := ""
			// The login URL is a credential. It is requested, and so printed,
			// only where a person will read it: on a terminal, or where
			// --print-url asked for it. JSON never has it unless asked.
			login := printURL || (format == "text" && deps.IsTerminal(command.OutOrStdout()))
			if hosted {
				if printURL {
					return runtime.ExitError(shared.ExitUsage, "--print-url prints the local login URL and cannot be used with --hosted")
				}
				hostedURL, err := deps.HostedURL()
				if err != nil {
					return fmt.Errorf("load the cockpit configuration: %w", err)
				}
				result.URL, target = hostedURL, hostedURL
			} else {
				if command.Flags().Changed("listen") {
					if err := daemonruntime.RequireLoopbackAddress(listen); err != nil {
						return runtime.ExitError(shared.ExitUsage, err.Error())
					}
				}
				session, err := deps.Local(command.Context(), cockpitrun.LocalRequest{Root: runtime.Flags().ProjectsRoot, Listen: listen, Mint: login})
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
					// The key goes in the fragment, which a browser sends to no
					// server: the page takes it from there
					// (cockpit#req:session-key).
					origin.Path, origin.RawQuery = session.Path, url.Values{"code": {session.Code}}.Encode()
					origin.Fragment = cockpit.LoginKeyFragment + "=" + session.Key
					target = origin.String()
					if format == "json" {
						result.LoginURL = target
					}
				} else if format == "text" {
					_, _ = fmt.Fprintln(command.ErrOrStderr(), "wb:", cockpitLoginURLHint)
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
			if !runtime.Flags().NonInteractive && deps.IsTerminal(command.OutOrStdout()) {
				if err := deps.Open(target); err != nil {
					_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: could not open the browser: %v; use the URL above\n", err)
				}
			}
			return nil
		},
	}
	command.Flags().StringVar(&listen, "listen", "", "loopback address of the daemon to start (default 127.0.0.1:8766, or the running daemon's address)")
	command.Flags().BoolVar(&hosted, "hosted", false, "open the hosted Cockpit at cockpit.hosted_url and start no daemon")
	command.Flags().BoolVar(&printURL, "print-url", false, "print the login URL, which is a credential, though stdout is not a terminal (in JSON: as login_url)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	command.AddCommand(newExportCmd(runtime, deps.Export))
	return command
}
