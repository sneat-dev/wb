package fleet

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/remotestate"
)

// This file is the HTTP transport of a remote machine's export
// (cockpit-views#req:remote-http-fetch). What it guarantees:
//
//   - The address and the credential come from the HTTPRoute it is handed, which
//     the daemon builds from local configuration only. Nothing of a response, a
//     snapshot or a request is ever used as an address.
//   - The address must pass remotestate.ValidateHubURL (https, or http on a
//     loopback host only), and the one request goes to that origin with the fixed
//     path below. No redirect is followed, so the bearer is never sent to any
//     other host.
//   - The body is read only through DecodeEnvelope, which reads at most
//     MaxEnvelopeBytes and scans the shape before decoding.
//   - A failure is a code. The response body of a failure is never read into an
//     error, a log or the document.
//
// Proxies: the client honours the proxy the environment names
// (http.ProxyFromEnvironment), which is the policy of the hub client this
// machine already sends the same credential through
// (internal/remotestate/hub uses the default transport). An https request goes
// through such a proxy as a CONNECT tunnel, so the proxy does not see the
// bearer, and a loopback address is never proxied.

// MachineExportPath is the path of the hub route that serves a machine's own
// export (cockpit-views#req:hub-export-route); hub.MachineExportPath is the same
// path, and a test holds the two together.
const MachineExportPath = "/v0/workbench/machines/export"

// metricsOnlyQuery asks the route for the metrics-only envelope.
const metricsOnlyQuery = "?metrics_only=1"

// The timeouts of the HTTP transport: connecting (and the TLS handshake) and
// the whole request, body included.
const (
	remoteConnectTimeout = 3 * time.Second
	remoteTotalTimeout   = 10 * time.Second
)

// maxBearerBytes bounds the credential file that is read; the hub refuses a
// token longer than 1024 bytes.
const maxBearerBytes = 1024

// HTTPRoute is one machine's HTTP route, from local configuration: the origin
// of its daemon-hosted hub and the private file that holds the machine bearer
// credential for it.
type HTTPRoute struct {
	URL       string
	TokenFile string
}

// HTTPExporter is the HTTP RemoteExporter.
type HTTPExporter struct {
	client *http.Client
	now    func() time.Time
}

// NewHTTPExporter is the HTTP exporter with the transport's timeouts, on the
// clock now (nil means time.Now).
func NewHTTPExporter(now func() time.Time) *HTTPExporter {
	return newHTTPExporter(remoteConnectTimeout, remoteTotalTimeout, now)
}

func newHTTPExporter(connect, total time.Duration, now func() time.Time) *HTTPExporter {
	if now == nil {
		now = time.Now
	}
	return &HTTPExporter{now: now, client: &http.Client{
		Timeout: total,
		Transport: &http.Transport{
			Proxy:                  http.ProxyFromEnvironment,
			DialContext:            (&net.Dialer{Timeout: connect}).DialContext,
			TLSHandshakeTimeout:    connect,
			ResponseHeaderTimeout:  total,
			MaxResponseHeaderBytes: 64 << 10,
			MaxIdleConnsPerHost:    1,
			IdleConnTimeout:        90 * time.Second,
			ForceAttemptHTTP2:      true,
		},
		// A redirect is never followed: the response is returned as it is and
		// reported as a failure, so the bearer reaches the configured host only.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Export reads target's envelope from its configured HTTP route. A target with
// no route, or whose address the hub address rule refuses, has no HTTP route and
// no request is made. A credential that is missing, not private or malformed is
// http_auth_failed, again with no request.
func (e *HTTPExporter) Export(ctx context.Context, target RemoteTarget, metricsOnly bool) (Envelope, error) {
	route := target.HTTP
	if route == nil || remotestate.ValidateHubURL(route.URL) != nil {
		return Envelope{}, ErrNoRoute
	}
	bearer, err := readBearer(route.TokenFile)
	if err != nil {
		return Envelope{}, &RemoteError{Code: RemoteErrorHTTPAuthFailed, Fallback: true}
	}
	address := strings.TrimRight(strings.TrimSpace(route.URL), "/") + MachineExportPath
	if metricsOnly {
		address += metricsOnlyQuery
	}
	// The method is a constant and the address passed the rule above, so the
	// request cannot fail to be made.
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+bearer)
	response, err := e.client.Do(request)
	if err != nil {
		return Envelope{}, httpFailure(0)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return Envelope{}, httpFailure(response.StatusCode)
	}
	envelope, err := DecodeEnvelope(response.Body, metricsOnly, e.now())
	var unread *ReadError
	if errors.As(err, &unread) {
		return Envelope{}, httpFailure(0)
	}
	return envelope, err
}

// httpFailure is the failure of an HTTP export by the status it was answered
// with, 0 standing for no answer at all (a connection error, a timeout, a body
// that broke off). It is the whole fallback rule of the HTTP transport
// (cockpit-views#req:remote-exporter-transports): no answer, 401, 403, 404, 429,
// any 5xx and a redirect (never followed) let the next transport be tried, as
// http_auth_failed for 401 and 403 and http_unavailable for the rest. Any other
// status is http_unavailable and does not fall back: the host answered, and not
// as a hub does.
func httpFailure(status int) *RemoteError {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &RemoteError{Code: RemoteErrorHTTPAuthFailed, Fallback: true}
	case status == 0, status == http.StatusNotFound, status == http.StatusTooManyRequests, status >= 500, status >= 300 && status < 400:
		return &RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}
	}
	return &RemoteError{Code: RemoteErrorHTTPUnavailable}
}

// errBearer is the one error of a credential file that cannot be used. It says
// nothing of the path or of what the file holds.
var errBearer = errors.New("the machine credential file cannot be used")

// privateFile reports whether a file's mode lets only its owner read it. Windows
// has no such mode bits; a credential there is protected by its directory.
func privateFile(mode os.FileMode) bool {
	return runtime.GOOS == "windows" || mode.Perm()&0o077 == 0
}

// readBearer reads the machine bearer credential from path: an absolute path to
// a regular, private file holding one token of at most maxBearerBytes of
// printable ASCII with no space in it. It is read on every export, so a rotated credential is picked up
// without a restart.
func readBearer(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errBearer
	}
	file, err := os.Open(path) //nolint:gosec // the path comes from the operator's own configuration.
	if err != nil {
		return "", errBearer
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !privateFile(info.Mode()) {
		return "", errBearer
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxBearerBytes+2))
	token := strings.TrimSpace(string(raw))
	if err != nil || token == "" || len(token) > maxBearerBytes || strings.ContainsFunc(token, func(character rune) bool { return character <= ' ' || character > '~' }) {
		return "", errBearer
	}
	return token, nil
}
