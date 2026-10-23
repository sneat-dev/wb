package cockpitrun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/cockpit"
	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"net/url"
	"testing"
)

func TestRecordedLoopbackOrigins(t *testing.T) {
	t.Parallel()
	for listen, want := range map[string]string{"127.0.0.1:8766": "127.0.0.1:8766", "[::1]:8766": "[::1]:8766", "localhost:8766": "localhost:8766", "127.0.0.2:8766": "127.0.0.2:8766", "[0:0:0:0:0:0:0:1]:8766": "[0:0:0:0:0:0:0:1]:8766"} {
		base, ok := cockpitLoopbackBase(listen)
		if !ok || base.Host != want || base.Scheme != "http" {
			t.Errorf("base for %s = %v %v", listen, base, ok)
		}
	}
}
func TestExportMalformedRequest(t *testing.T) {
	t.Parallel()
	// A request that cannot be made is a failed export too, whatever let the
	// address through.
	var document cockpitfleet.Document
	header, failure := cockpitExportGet(t.Context(), exportClient(), url.URL{Scheme: "http", Host: "127.0.0.1:http", Path: cockpit.APIPrefix}, cockpitfleet.FleetRoute, nil, cockpitDocumentLimit, &document)
	if failure != errExportFailed || header != nil {
		t.Fatalf("a request that cannot be made = %q", failure)
	}
}
func TestExportTransportFailureClassification(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	for name, test := range map[string]struct {
		ctx  context.Context
		err  error
		want ExportFailure
	}{
		"a refused connection":        {t.Context(), errors.New("dial tcp 127.0.0.1:1: connect: connection refused"), errDaemonNotRunning},
		"a deadline":                  {t.Context(), context.DeadlineExceeded, errExportFailed},
		"a cancelled request":         {t.Context(), context.Canceled, errExportFailed},
		"a cancelled command":         {cancelled, errors.New("anything"), errExportFailed},
		"a timeout of the client":     {t.Context(), &url.Error{Op: "Get", URL: "http://127.0.0.1", Err: timeoutError{}}, errExportFailed},
		"an error that is no timeout": {t.Context(), &url.Error{Op: "Get", URL: "http://127.0.0.1", Err: errors.New("EOF")}, errDaemonNotRunning},
	} {
		if got := exportTransportFailure(test.ctx, test.err); got != test.want {
			t.Errorf("%s: %q, want %q", name, got, test.want)
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string { return "i/o timeout" }
func (timeoutError) Timeout() bool { return true }
