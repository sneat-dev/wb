package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/daemon"
)

// cockpitExportDependencies is everything `wb cockpit export` touches. It
// deliberately has no seam that starts a daemon, mints a login code or opens a
// browser (cockpit-views#req:cockpit-export-verb): the verb reads the daemon
// record and makes anonymous loopback reads, and a test proves that no start
// path is reachable from it.
type cockpitExportDependencies struct {
	// loadRecord reads this machine's daemon record for a projects root.
	loadRecord func(root string) (daemon.State, bool, error)
	// alive reports whether a process id is a running process.
	alive func(int) bool
	// client is the HTTP client for the daemon's loopback listener.
	client func() *http.Client
	now    func() time.Time
}

func defaultCockpitExportDependencies() cockpitExportDependencies {
	return cockpitExportDependencies{
		loadRecord: func(root string) (daemon.State, bool, error) {
			return newDaemonController(daemonDependencies{}, root).store.Load()
		},
		alive:  defaultDaemonDependencies().alive,
		client: cockpitExportClient,
		now:    time.Now,
	}
}

// cockpitExportClient is a client with no proxy, no cookie jar and no
// redirect, and a 10 second limit: the daemon is on this machine.
func cockpitExportClient() *http.Client {
	return &http.Client{
		Timeout:       10 * time.Second,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// The two reads are bounded so that the envelope built from them stays within
// cockpitfleet.MaxEnvelopeBytes: 360 samples of metrics fit well inside 128 KiB.
const (
	cockpitMetricsLimit  = 128 << 10
	cockpitDocumentLimit = cockpitfleet.MaxEnvelopeBytes - cockpitMetricsLimit - 4096
)

// errExportRefused and errDaemonNotRunning are the two typed reasons the verb
// reports with exit code 1.
var (
	errExportRefused    = errors.New(cockpitfleet.ErrorExportRefused)
	errDaemonNotRunning = errors.New(cockpitfleet.ErrorDaemonNotRunning)
)

// cockpitExportEnvelope builds this machine's export envelope from the running
// daemon's own fleet and machine-metrics routes, read as anonymous-local, and
// returns it encoded with its trailing newline. It never starts anything.
func cockpitExportEnvelope(ctx context.Context, deps cockpitExportDependencies, root string, metricsOnly bool) ([]byte, error) {
	record, found, err := deps.loadRecord(root)
	if err != nil {
		return nil, fmt.Errorf("read the local daemon record: %w", err)
	}
	if !found || record.Status == daemon.StatusStopped || record.PID <= 0 || !deps.alive(record.PID) {
		return nil, errDaemonNotRunning
	}
	if err := requireLoopbackAddress(record.Listen); err != nil {
		return nil, fmt.Errorf("the daemon record names %q: %w", record.Listen, errDaemonNotRunning)
	}
	_, port, _ := net.SplitHostPort(record.Listen)
	base := url.URL{Scheme: "http", Host: net.JoinHostPort(cockpit.CanonicalHost(record.Listen), port), Path: cockpit.APIPrefix}
	client := deps.client()

	var document cockpitfleet.Document
	if err := cockpitExportGet(ctx, client, base, cockpitfleet.FleetRoute, nil, cockpitDocumentLimit, &document); err != nil {
		return nil, err
	}
	metrics := cockpitfleet.MetricsResponse{Route: cockpitfleet.RouteNone, Reason: cockpitfleet.ReasonNoSource}
	for _, machine := range document.Machines {
		if machine.Route != cockpitfleet.RouteLocal {
			continue
		}
		query := url.Values{"machine": {machine.ID}}
		if err := cockpitExportGet(ctx, client, base, cockpitfleet.MetricsRoute, query, cockpitMetricsLimit, &metrics); err != nil {
			return nil, err
		}
		break
	}
	// What is printed is held to the strict decoder every reader of it uses, so
	// this binary never emits an envelope that its own readers would refuse, and
	// the 8 MiB bound is enforced on the bytes themselves.
	// The envelope types cannot fail to marshal.
	body, _ := json.Marshal(cockpitfleet.NewEnvelope(document, metrics, deps.now(), metricsOnly))
	if _, err := cockpitfleet.DecodeEnvelope(bytes.NewReader(body), metricsOnly, deps.now()); err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// cockpitExportGet makes one anonymous GET of a Cockpit route and decodes its
// JSON body into target, strictly and within the envelope's size bound. A
// connection that fails means the recorded daemon is not serving; 401 or 403
// means it refuses anonymous reads.
func cockpitExportGet(ctx context.Context, client *http.Client, base url.URL, route string, query url.Values, limit int, target any) error {
	address := base
	address.Path += route
	address.RawQuery = query.Encode()
	// The method is a constant and the address is built from parts that were
	// checked, so the request cannot fail to be made.
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("read the daemon's %s route: %w", route, errDaemonNotRunning)
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return errExportRefused
	case response.StatusCode != http.StatusOK:
		return fmt.Errorf("the daemon's %s route answered status %d", route, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return fmt.Errorf("read the daemon's %s route: %w", route, err)
	}
	if len(body) > limit {
		return fmt.Errorf("the daemon's %s route answered more than %d bytes", route, limit)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode the daemon's %s route: %w", route, err)
	}
	return nil
}

func newCockpitExportCmd(inv *invocation, deps cockpitExportDependencies) *cobra.Command {
	var format string
	var metricsOnly bool
	command := &cobra.Command{
		Use:   "export",
		Short: "Print this machine's Cockpit export envelope as JSON",
		Long: "Print this machine's export envelope {schema_version, machine, exported_at, fleet, metrics} as JSON, " +
			"read from this machine's running daemon over its loopback listener as the anonymous-local principal, " +
			"within 8 MiB and limited to the metadata an anonymous local reader may see. " +
			"Another machine's daemon runs this over SSH to read this one. " +
			"It never starts a daemon, opens a browser or mints a login code. " +
			"--metrics-only omits the cockpitfleet. With no running daemon, or one that refuses anonymous reads " +
			"(cockpit.anonymous_metadata: false), it prints {schema_version, error} with error daemon_not_running " +
			"or export_refused and exits 1.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireOutputFormat(format, "json"); err != nil {
				return usageError(err.Error())
			}
			body, err := cockpitExportEnvelope(command.Context(), deps, inv.projectsRoot, metricsOnly)
			if err != nil {
				for _, typed := range []error{errDaemonNotRunning, errExportRefused} {
					if errors.Is(err, typed) {
						if encodeErr := json.NewEncoder(command.OutOrStdout()).Encode(cockpitfleet.NewExportError(typed.Error())); encodeErr != nil {
							return encodeErr
						}
						return &exitError{code: exitFindings, message: "wb cockpit export: " + typed.Error()}
					}
				}
				return err
			}
			_, err = command.OutOrStdout().Write(body)
			return err
		},
	}
	command.Flags().StringVar(&format, "format", "json", "stdout format: json")
	command.Flags().BoolVar(&metricsOnly, "metrics-only", false, "omit the fleet and print only the machine's metrics")
	return command
}
