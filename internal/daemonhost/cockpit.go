package daemonhost

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"sync/atomic"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/hubaddress"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

type machineExportSource struct {
	snapshotter atomic.Pointer[cockpitfleet.Snapshotter]
	refused     atomic.Bool
}

func newCockpitServer(address string, config wbconfig.CockpitConfig) *cockpit.Server {
	return cockpit.New(cockpit.Options{CanonicalHost: cockpit.CanonicalHost(address), Config: config})
}

func registerCockpitFleet(server *cockpit.Server, options cockpitfleet.Options) *cockpitfleet.Snapshotter {
	snapshotter := cockpitfleet.New(options)
	cockpitfleet.Register(server, snapshotter)
	return snapshotter
}

func withoutOwnAddress(targets []cockpitfleet.RemoteTarget, address string, logf func(string, ...any)) []cockpitfleet.RemoteTarget {
	_, ownPort, err := net.SplitHostPort(address)
	if err != nil {
		return targets
	}
	kept := make([]cockpitfleet.RemoteTarget, 0, len(targets))
	for _, target := range targets {
		if target.HTTP != nil {
			if parsed, parseErr := url.Parse(hubaddress.Origin(target.HTTP.URL)); parseErr == nil && (parsed.Host == address || (hubaddress.IsLoopbackHost(parsed.Hostname()) && parsed.Port() == ownPort)) {
				logf("cockpit fleet: %s is not read over http: its http url is this daemon's own address", target.Machine)
				if target.SSH == nil {
					continue
				}
				target.HTTP = nil
			}
		}
		kept = append(kept, target)
	}
	return kept
}

func (source *machineExportSource) serve(writer http.ResponseWriter, request *http.Request, metricsOnly bool) {
	reason := func(status int, code string) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		// The error type cannot fail to marshal.
		body, _ := json.Marshal(map[string]string{"error": code})
		_, _ = writer.Write(append(body, '\n'))
	}
	if source.refused.Load() {
		reason(http.StatusForbidden, cockpitfleet.ErrorExportRefused)
		return
	}
	snapshotter := source.snapshotter.Load()
	if snapshotter == nil {
		reason(http.StatusServiceUnavailable, cockpitfleet.ErrorWarmingUp)
		return
	}
	payload, failure := snapshotter.ExportPayload(metricsOnly)
	if failure != "" {
		reason(http.StatusServiceUnavailable, failure)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	// An export is credentialed and is never to be kept by a client or a proxy.
	cockpit.ServePrivatePayload(writer, request, payload)
}
