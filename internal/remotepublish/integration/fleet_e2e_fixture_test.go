//go:build e2e

package integration

import (
	"net/http"
	"net/http/httptest"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/cockpitoptions"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// fleetBody is the fleet document as a request without Accept-Encoding would
// receive it, from the snapshotter's prepared bytes.
func fleetBody(snapshotter *cockpitfleet.Snapshotter) []byte {
	recorder := httptest.NewRecorder()
	cockpit.ServePayload(recorder, httptest.NewRequest(http.MethodGet, "/", nil), snapshotter.Payload())
	return recorder.Body.Bytes()
}

func publishConfig(publish remotestate.PublishConfig) remotestate.Config {
	return remotestate.Config{Provider: "git", Repo: "acme/wb-state", Machine: "mac", Publish: publish}
}
func testCockpitDependencies(host string) cockpitoptions.Dependencies {
	deps := cockpitoptions.DefaultDependencies("", testExitFactory)
	deps.Hostname = func() (string, error) { return host, nil }
	return deps
}
