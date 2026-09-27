package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbconfig"
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
			deps := peersDeps{
				listenAddress: func(daemonDependencies, string) (string, error) {
					return "127.0.0.1:7777", nil
				},
				httpClient: client, now: time.Now,
			}
			var stdout bytes.Buffer
			err := runPeersGet(context.Background(), deps, t.TempDir(), "laptop", true, &stdout)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			var exit *exitError
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
	deps := peersDeps{listenAddress: func(daemonDependencies, string) (string, error) { return "", want }}
	var stdout bytes.Buffer
	err := runPeersGet(context.Background(), deps, t.TempDir(), "laptop", false, &stdout)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitFindings || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("failed lookup wrote a result: %q", stdout.String())
	}
}

func TestPeersUpstreamTrustChangeKeepsUnrelatedPeersUntouchedAndReportsJSON(t *testing.T) {
	t.Parallel()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	if err := wbconfig.SetPeersUpstream(config, "https://hub.example.test", filepath.Join(t.TempDir(), "token")); err != nil {
		t.Fatal(err)
	}
	deps := peersDeps{configPath: func() string { return config }}
	root := t.TempDir()
	var output bytes.Buffer
	handled, err := runPeersUpstreamTrustChange(deps, root, "Blocked", "another-peer", true, &output)
	if err != nil || handled || output.Len() != 0 {
		t.Fatalf("unrelated peer handled=%t error=%v output=%q", handled, err, output.String())
	}
	for _, tc := range []struct{ verb, peer, want string }{
		{"Blocked", "upstream", "blocked"},
		{"Unblocked", "hub.example.test", "active"},
	} {
		output.Reset()
		handled, err = runPeersUpstreamTrustChange(deps, root, tc.verb, tc.peer, true, &output)
		if err != nil || !handled {
			t.Fatalf("%s handled=%t error=%v", tc.verb, handled, err)
		}
		var response peerTrustResponse
		if err := json.Unmarshal(output.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.PeerID != "hub.example.test" || response.Trust != tc.want {
			t.Fatalf("%s response = %+v", tc.verb, response)
		}
	}
}
