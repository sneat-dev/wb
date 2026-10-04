package cockpitrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/loopbackhost"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type ExportRequest struct {
	Root        string
	MetricsOnly bool
}
type ExportResult struct {
	Body    []byte
	Drops   cockpitfleet.ExportDrops
	Failure ExportFailure
}

// ExportDependencies is everything `wb cockpit export` touches. It
// deliberately has no seam that starts a daemon, mints a login code or opens a
// browser (cockpit-views#req:cockpit-export-verb): the verb reads the daemon
// record and makes anonymous loopback reads, and tests prove that no start path
// is reachable from it (a structural test of this struct and a call-graph test
// of this file).
type ExportDependencies struct {
	// LoadRecord reads this machine's daemon record for a projects root.
	LoadRecord func(root string) (daemon.State, bool, error)
	// Alive reports whether a process id is a running process. On macOS it asks
	// launchd (`launchctl print`, which changes nothing), so a daemon started in
	// the foreground by hand is reported as not running there.
	Alive func(int) bool
	// ProcessStart observes when a process started, to tell the daemon the record
	// was written for from a process that Now holds its recycled id. It reads
	// /proc on Linux and reports false elsewhere.
	ProcessStart func(int) (time.Time, bool)
	// Client is the HTTP Client for the daemon's loopback listener.
	Client func() *http.Client
	Now    func() time.Time
}

func DefaultExportDependencies() ExportDependencies {
	return ExportDependencies{
		LoadRecord: func(root string) (daemon.State, bool, error) {
			return daemonruntime.NewController(daemonruntime.Dependencies{}, root).LoadState()
		},
		Alive:        daemonruntime.ProcessAlive,
		ProcessStart: daemon.ProcessStartTime,
		Client:       exportClient,
		Now:          time.Now,
	}
}

// exportClient is a Client with no proxy, no cookie jar and no
// redirect, and a 10 second limit: the daemon is on this machine.
func exportClient() *http.Client {
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

// ExportFailure is one of the three typed reasons the verb reports on stdout
// with exit code 1, or empty for none. It is its code and nothing else: no error
// text of a dependency, no path and no response body reaches an operator or a
// pipe.
type ExportFailure string

const (
	errDaemonNotRunning = ExportFailure(cockpitfleet.ErrorDaemonNotRunning)
	errExportRefused    = ExportFailure(cockpitfleet.ErrorExportRefused)
	errExportFailed     = ExportFailure(cockpitfleet.ErrorExportFailed)
	errWarmingUp        = ExportFailure(cockpitfleet.ErrorWarmingUp)
)

// cockpitExportEnvelope builds this machine's export envelope from the running
// daemon's own fleet and machine-metrics routes, read as anonymous-local, and
// returns it encoded with its trailing newline, and what it left out. It never
// starts anything and every failure is one of the ExportFailure values. A
// daemon whose first pass has not ended has a partial fleet, which is not
// exported: the full export is warming_up until it has (the metrics-only one
// needs no fleet).
func Export(ctx context.Context, deps ExportDependencies, request ExportRequest) ExportResult {
	root, metricsOnly := request.Root, request.MetricsOnly
	fail := func(reason ExportFailure) ExportResult {
		return ExportResult{Failure: reason}
	}
	record, found, err := deps.LoadRecord(root)
	switch {
	case err != nil:
		return fail(errExportFailed)
	case !found || record.Status == daemon.StatusStopped || record.PID <= 0 || !deps.Alive(record.PID):
		return fail(errDaemonNotRunning)
	}
	// A record whose recorded process start differs from the process that Now holds
	// its id is a stale record of a daemon that is gone; a record that cannot say
	// is trusted as far as the process being Alive.
	if started, observed := deps.ProcessStart(record.PID); observed {
		if match, known := record.ProcessGenerationMatches(started, observed); known && !match {
			return fail(errDaemonNotRunning)
		}
	}
	base, ok := cockpitLoopbackBase(record.Listen)
	if !ok {
		return fail(errExportFailed)
	}
	Client := deps.Client()

	// The verb asks the daemon for this machine's part of the fleet document
	// alone: its own entries for a full export, and nothing but its machine entry
	// for a metrics-only one. A machine that shows many other machines would
	// otherwise fail its own export on the size of entries it never exports. A
	// daemon that does not know the parameter answers the whole document, which
	// is read as before.
	scope := cockpitfleet.ScopeOwn
	if metricsOnly {
		scope = cockpitfleet.ScopeMachine
	}
	var document cockpitfleet.Document
	header, failure := cockpitExportGet(ctx, Client, base, cockpitfleet.FleetRoute, url.Values{"scope": {scope}}, cockpitDocumentLimit, &document)
	if failure != "" {
		return fail(failure)
	}
	if document.WarmingUp && !metricsOnly {
		return fail(errWarmingUp)
	}
	// A daemon that could not list its repositories and holds none cannot say
	// what this machine has: an empty fleet is not exported in its place.
	if cockpitfleet.Unlistable(document) && !metricsOnly {
		return fail(errExportFailed)
	}
	metrics := cockpitfleet.MetricsResponse{Route: cockpitfleet.RouteNone, Reason: cockpitfleet.ReasonNoSource}
	for _, machine := range document.Machines {
		if machine.Route != cockpitfleet.RouteLocal {
			continue
		}
		query := url.Values{"machine": {machine.ID}}
		if _, failure := cockpitExportGet(ctx, Client, base, cockpitfleet.MetricsRoute, query, cockpitMetricsLimit, &metrics); failure != "" {
			return fail(failure)
		}
		break
	}
	// What is printed is held to the strict decoder every reader of it uses, so
	// this binary never emits an envelope that its own readers would refuse, and
	// the 8 MiB bound is enforced on the bytes themselves.
	// The envelope types cannot fail to marshal.
	envelope, drops := cockpitfleet.NewEnvelope(document, metrics, deps.Now(), metricsOnly)
	if !metricsOnly {
		// What the daemon left out of its answer counts with what this pass did.
		drops = drops.Plus(cockpitfleet.ParseExportDrops(header.Get(cockpitfleet.ExportDropsHeader)))
		envelope.Dropped = drops.Total()
	}
	body, _ := json.Marshal(envelope)
	if _, err := cockpitfleet.DecodeEnvelope(bytes.NewReader(body), metricsOnly, deps.Now()); err != nil {
		return fail(errExportFailed)
	}
	return ExportResult{Body: append(body, '\n'), Drops: drops}
}

// cockpitLoopbackBase is the Cockpit API address of a daemon recorded as
// listening on host:port, dialled exactly as recorded: only a host that
// loopbackhost.Named accepts (the rule the daemon's --listen check and the
// Cockpit Host guard share), so a record naming any other address is refused
// rather than rewritten to the canonical one. The port must be a port number: a record
// with any other text there names no address.
func cockpitLoopbackBase(listen string) (url.URL, bool) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || !loopbackhost.Named(host) {
		return url.URL{}, false
	}
	if number, err := strconv.ParseUint(port, 10, 16); err != nil || number == 0 {
		return url.URL{}, false
	}
	return url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: cockpit.APIPrefix}, true
}

// cockpitExportGet makes one anonymous GET of a Cockpit route and decodes its
// JSON body into target, strictly and within limit bytes, and returns the
// response's header. A connection that is refused means the recorded daemon is
// not serving; 401 or 403 means it refuses anonymous reads; anything else, a
// request that cannot be made and a daemon that does not answer in time
// included, is a failed export.
func cockpitExportGet(ctx context.Context, Client *http.Client, base url.URL, route string, query url.Values, limit int, target any) (http.Header, ExportFailure) {
	address := base
	address.Path += route
	address.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return nil, errExportFailed
	}
	// This read is another machine's daemon reading this one, not a person
	// looking at the Cockpit: it must not count as demand for this daemon's own
	// reads of other machines.
	request.Header.Set(cockpitfleet.ExportReaderHeader, "1")
	response, err := Client.Do(request)
	if err != nil {
		return nil, exportTransportFailure(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return nil, errExportRefused
	case response.StatusCode != http.StatusOK:
		return nil, errExportFailed
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil || len(body) > limit {
		return nil, errExportFailed
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return nil, errExportFailed
	}
	return response.Header, ""
}

// exportTransportFailure is the reason of a request that got no response. A
// daemon that is there and did not answer in time, or a read that was
// cancelled, is a failed export: only a connection that could not be made says
// that no daemon is serving at the recorded address.
func exportTransportFailure(ctx context.Context, err error) ExportFailure {
	var timeout interface{ Timeout() bool }
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return errExportFailed
	}
	return errDaemonNotRunning
}
