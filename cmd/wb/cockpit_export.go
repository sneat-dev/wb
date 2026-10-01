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
// record and makes anonymous loopback reads, and tests prove that no start path
// is reachable from it (a structural test of this struct and a call-graph test
// of this file).
type cockpitExportDependencies struct {
	// loadRecord reads this machine's daemon record for a projects root.
	loadRecord func(root string) (daemon.State, bool, error)
	// alive reports whether a process id is a running process. On macOS it asks
	// launchd (`launchctl print`, which changes nothing), so a daemon started in
	// the foreground by hand is reported as not running there.
	alive func(int) bool
	// processStart observes when a process started, to tell the daemon the record
	// was written for from a process that now holds its recycled id. It reads
	// /proc on Linux and reports false elsewhere.
	processStart func(int) (time.Time, bool)
	// client is the HTTP client for the daemon's loopback listener.
	client func() *http.Client
	now    func() time.Time
}

func defaultCockpitExportDependencies() cockpitExportDependencies {
	return cockpitExportDependencies{
		loadRecord: func(root string) (daemon.State, bool, error) {
			return newDaemonController(daemonDependencies{}, root).store.Load()
		},
		alive:        daemonProcessAlive,
		processStart: daemon.ProcessStartTime,
		client:       cockpitExportClient,
		now:          time.Now,
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

// exportFailure is one of the three typed reasons the verb reports on stdout
// with exit code 1, or empty for none. It is its code and nothing else: no error
// text of a dependency, no path and no response body reaches an operator or a
// pipe.
type exportFailure string

const (
	errDaemonNotRunning = exportFailure(cockpitfleet.ErrorDaemonNotRunning)
	errExportRefused    = exportFailure(cockpitfleet.ErrorExportRefused)
	errExportFailed     = exportFailure(cockpitfleet.ErrorExportFailed)
	errWarmingUp        = exportFailure(cockpitfleet.ErrorWarmingUp)
)

// cockpitExportEnvelope builds this machine's export envelope from the running
// daemon's own fleet and machine-metrics routes, read as anonymous-local, and
// returns it encoded with its trailing newline, and what it left out. It never
// starts anything and every failure is one of the exportFailure values. A
// daemon whose first pass has not ended has a partial fleet, which is not
// exported: the full export is warming_up until it has (the metrics-only one
// needs no fleet).
func cockpitExportEnvelope(ctx context.Context, deps cockpitExportDependencies, root string, metricsOnly bool) ([]byte, cockpitfleet.ExportDrops, exportFailure) {
	fail := func(reason exportFailure) ([]byte, cockpitfleet.ExportDrops, exportFailure) {
		return nil, cockpitfleet.ExportDrops{}, reason
	}
	record, found, err := deps.loadRecord(root)
	switch {
	case err != nil:
		return fail(errExportFailed)
	case !found || record.Status == daemon.StatusStopped || record.PID <= 0 || !deps.alive(record.PID):
		return fail(errDaemonNotRunning)
	}
	// A record whose recorded process start differs from the process that now holds
	// its id is a stale record of a daemon that is gone; a record that cannot say
	// is trusted as far as the process being alive.
	if started, observed := deps.processStart(record.PID); observed {
		if match, known := record.ProcessGenerationMatches(started, observed); known && !match {
			return fail(errDaemonNotRunning)
		}
	}
	base, ok := cockpitLoopbackBase(record.Listen)
	if !ok {
		return fail(errExportFailed)
	}
	client := deps.client()

	var document cockpitfleet.Document
	if failure := cockpitExportGet(ctx, client, base, cockpitfleet.FleetRoute, nil, cockpitDocumentLimit, &document); failure != "" {
		return fail(failure)
	}
	if document.WarmingUp && !metricsOnly {
		return fail(errWarmingUp)
	}
	metrics := cockpitfleet.MetricsResponse{Route: cockpitfleet.RouteNone, Reason: cockpitfleet.ReasonNoSource}
	for _, machine := range document.Machines {
		if machine.Route != cockpitfleet.RouteLocal {
			continue
		}
		query := url.Values{"machine": {machine.ID}}
		if failure := cockpitExportGet(ctx, client, base, cockpitfleet.MetricsRoute, query, cockpitMetricsLimit, &metrics); failure != "" {
			return fail(failure)
		}
		break
	}
	// What is printed is held to the strict decoder every reader of it uses, so
	// this binary never emits an envelope that its own readers would refuse, and
	// the 8 MiB bound is enforced on the bytes themselves.
	// The envelope types cannot fail to marshal.
	envelope, drops := cockpitfleet.NewEnvelope(document, metrics, deps.now(), metricsOnly)
	body, _ := json.Marshal(envelope)
	if _, err := cockpitfleet.DecodeEnvelope(bytes.NewReader(body), metricsOnly, deps.now()); err != nil {
		return fail(errExportFailed)
	}
	return append(body, '\n'), drops, ""
}

// cockpitLoopbackBase is the Cockpit API address of a daemon recorded as
// listening on host:port, dialled exactly as recorded: only `127.0.0.1`, `::1`
// and `localhost` are accepted (the three names the Cockpit Host guard serves),
// so a record naming any other address, 127.0.0.2 included, is refused rather
// than rewritten to the canonical one.
func cockpitLoopbackBase(listen string) (url.URL, bool) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		return url.URL{}, false
	}
	return url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: cockpit.APIPrefix}, true
}

// cockpitExportGet makes one anonymous GET of a Cockpit route and decodes its
// JSON body into target, strictly and within limit bytes. A connection that
// fails means the recorded daemon is not serving; 401 or 403 means it refuses
// anonymous reads; anything else is a failed export.
func cockpitExportGet(ctx context.Context, client *http.Client, base url.URL, route string, query url.Values, limit int, target any) exportFailure {
	address := base
	address.Path += route
	address.RawQuery = query.Encode()
	// The method is a constant and the address is built from parts that were
	// checked, so the request cannot fail to be made.
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	response, err := client.Do(request)
	if err != nil {
		return errDaemonNotRunning
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return errExportRefused
	case response.StatusCode != http.StatusOK:
		return errExportFailed
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil || len(body) > limit {
		return errExportFailed
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return errExportFailed
	}
	return ""
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
			"It never starts a daemon, opens a browser or mints a login code, and writes nothing. " +
			"--metrics-only omits the fleet. When no daemon is running, or one refuses anonymous reads " +
			"(cockpit.anonymous_metadata: false), or its first scan has not finished (the fleet is still partial; " +
			"--metrics-only is not affected), or the export fails otherwise, it prints {schema_version, error} " +
			"with error daemon_not_running, export_refused, warming_up or export_failed and exits 1. " +
			"An entry of this machine that the envelope's rules refuse is left out and counted in the envelope's dropped field. " +
			"On macOS a daemon is found through launchd, so one started by hand in the foreground is reported as not running.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireOutputFormat(format, "json"); err != nil {
				return usageError(err.Error())
			}
			body, drops, failure := cockpitExportEnvelope(command.Context(), deps, inv.projectsRoot, metricsOnly)
			if failure != "" {
				if _, writeErr := command.OutOrStdout().Write(append(mustMarshalExportError(failure), '\n')); writeErr != nil {
					return errors.New("wb cockpit export: could not write to stdout")
				}
				return &exitError{code: exitFindings, message: "wb cockpit export: " + string(failure)}
			}
			if _, err := command.OutOrStdout().Write(body); err != nil {
				return errors.New("wb cockpit export: could not write to stdout")
			}
			if drops.Total() > 0 {
				// Numbers only: what was left out is never named, here or anywhere.
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb cockpit export: left out %d entries the envelope's rules refuse (repositories %d, worktrees %d, pull requests %d, agents %d)\n",
					drops.Total(), drops.Repositories, drops.Worktrees, drops.PullRequests, drops.Agents)
			}
			return nil
		},
	}
	command.Flags().StringVar(&format, "format", "json", "stdout format: json")
	command.Flags().BoolVar(&metricsOnly, "metrics-only", false, "omit the fleet and print only the machine's metrics")
	return command
}

// mustMarshalExportError is the stdout form of a failure; the type cannot fail
// to marshal.
func mustMarshalExportError(failure exportFailure) []byte {
	body, _ := json.Marshal(cockpitfleet.NewExportError(string(failure)))
	return body
}
