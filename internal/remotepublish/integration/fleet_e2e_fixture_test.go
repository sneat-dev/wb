//go:build e2e

package integration

import (
	"net/http"
	"net/http/httptest"

	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
)

// fleetBody is the fleet document as a request without Accept-Encoding would
// receive it, from the snapshotter's prepared bytes.
func fleetBody(snapshotter *cockpitfleet.Snapshotter) []byte {
	recorder := httptest.NewRecorder()
	cockpit.ServePayload(recorder, httptest.NewRequest(http.MethodGet, "/", nil), snapshotter.Payload())
	return recorder.Body.Bytes()
}
