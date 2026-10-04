package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/peersrun"
)

type peersReadRoundTrip func(*http.Request) (*http.Response, error)

func (roundTrip peersReadRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestPeersGetClassifiesDaemonReadFailuresWithoutPrintingAResult(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, body, want string
		status           int
		findings         bool
	}{
		{"unknown peer", "", `no peer named or with ID "laptop"`, http.StatusNotFound, true},
		{"daemon unavailable", "", "503 Service Unavailable", http.StatusServiceUnavailable, true},
		{"malformed response", "<html>not JSON</html>", "invalid character", http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := &http.Client{Transport: peersReadRoundTrip(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodGet || request.URL.EscapedPath() != "/api/v1/peers/laptop" {
					t.Errorf("unexpected daemon request: %s %s", request.Method, request.URL.EscapedPath())
				}
				return &http.Response{StatusCode: tc.status, Status: fmt.Sprintf("%d %s", tc.status, http.StatusText(tc.status)), Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header), Request: request}, nil
			})}
			deps := peerCommandFixture{ops: peersrun.Dependencies{ListenAddress: func(string) (string, error) { return "127.0.0.1:7777", nil }, Do: client.Do}, now: time.Now}
			var stdout bytes.Buffer
			err := executePeerFixture(context.Background(), deps, t.TempDir(), []string{"get", "laptop", "--json"}, nil, &stdout, &stdout)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			var exit *fixtureExitError
			if errors.As(err, &exit) != tc.findings {
				t.Fatalf("findings exit = %v, want %t", err, tc.findings)
			}
			if stdout.Len() != 0 {
				t.Fatalf("failed read wrote a result: %q", stdout.String())
			}
		})
	}
}

func TestPeersGetRefusesBeforeRequestWhenDaemonAddressUnavailable(t *testing.T) {
	t.Parallel()
	want := errors.New("daemon has no listener")
	deps := peerCommandFixture{ops: peersrun.Dependencies{ListenAddress: func(string) (string, error) { return "", want }}}
	var stdout bytes.Buffer
	err := executePeerFixture(context.Background(), deps, t.TempDir(), []string{"get", "laptop"}, nil, &stdout, &stdout)
	var exit *fixtureExitError
	if !errors.As(err, &exit) || exit.code != shared.ExitFindings || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed lookup wrote a result: %q", stdout.String())
	}
}
