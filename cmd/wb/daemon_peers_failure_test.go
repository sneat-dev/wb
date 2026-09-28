package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPeerAdminRoutesRejectMalformedJSONBeforeChangingTrust(t *testing.T) {
	mount, handler := peerAdminTestMount(t)
	for _, path := range []string{"invite", "block", "unblock", "disconnect", "enroll"} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, peersRPCPrefix+path, strings.NewReader("{"))
			request.Header.Set("Authorization", "Bearer owner-token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "decode request") {
				t.Fatalf("%s malformed request = %d %s, want JSON decode refusal", path, response.Code, response.Body.String())
			}
		})
	}
	if records, err := mount.PeerAdmin.Trust.ListPeers(context.Background()); err != nil || len(records) != 0 {
		t.Fatalf("malformed requests changed peer trust: records=%v error=%v", records, err)
	}
}

func TestPeerAdminRoutesRejectUnknownPeersWithoutChangingTrust(t *testing.T) {
	mount, handler := peerAdminTestMount(t)
	for _, path := range []string{"unblock", "disconnect"} {
		t.Run(path, func(t *testing.T) {
			response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+path, peerNameOrIDRequest{Peer: "no-such-peer"})
			if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "peer record not found") {
				t.Fatalf("%s unknown peer = %d %s, want peer-not-found refusal", path, response.Code, response.Body.String())
			}
		})
	}
	if records, err := mount.PeerAdmin.Trust.ListPeers(context.Background()); err != nil || len(records) != 0 {
		t.Fatalf("unknown-peer requests changed trust: records=%v error=%v", records, err)
	}
}

type peerAdminErrorReader struct{}

func (peerAdminErrorReader) Read([]byte) (int, error) { return 0, errors.New("response read failed") }
func (peerAdminErrorReader) Close() error             { return nil }

type peerAdminRoundTripFunc func(*http.Request) (*http.Response, error)

func (f peerAdminRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func peerAdminHTTPResponse(status int, statusText string, body io.ReadCloser) *http.Response {
	return &http.Response{StatusCode: status, Status: statusText, Header: make(http.Header), Body: body}
}

func TestPeerAdminClientReportsEncodingURLTransportAndBodyFailures(t *testing.T) {
	const path = peersRPCPrefix + "block"
	for _, test := range []struct {
		name, path, want string
		request          any
		transport        func(*http.Request) (*http.Response, error)
	}{
		{name: "request cannot be encoded", path: path, request: make(chan int), want: "encode request"},
		{name: "path is not a URL", path: path + "\n", request: map[string]string{"peer": "laptop"}, want: "invalid control character"},
		{name: "transport fails", path: path, request: map[string]string{"peer": "laptop"}, want: "call local daemon", transport: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("socket unavailable")
		}},
		{name: "response cannot be read", path: path, request: map[string]string{"peer": "laptop"}, want: "read daemon response", transport: func(*http.Request) (*http.Response, error) {
			return peerAdminHTTPResponse(http.StatusOK, "200 OK", peerAdminErrorReader{}), nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			transportCalled := false
			client := &peerAdminClient{httpClient: &http.Client{Transport: peerAdminRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				transportCalled = true
				if test.transport == nil {
					t.Fatal("invalid request reached transport")
				}
				return test.transport(request)
			})}}
			err := client.call(context.Background(), test.path, test.request, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("call error = %v, want %q", err, test.want)
			}
			if (test.transport != nil) != transportCalled {
				t.Fatalf("transport called=%t, want %t", transportCalled, test.transport != nil)
			}
		})
	}
}

func TestPeerAdminClientPreservesDaemonRefusalsAndDecodingErrors(t *testing.T) {
	for _, test := range []struct {
		name, statusText, body, want string
		status                       int
		response                     any
		wantExit                     int
	}{
		{name: "JSON refusal", status: http.StatusForbidden, statusText: "403 Forbidden", body: `{"error":"peer is blocked"}`, want: "peer is blocked", wantExit: exitFindings},
		{name: "plain-text refusal", status: http.StatusBadGateway, statusText: "502 Bad Gateway", body: "upstream unavailable", want: "upstream unavailable", wantExit: exitFindings},
		{name: "empty refusal uses HTTP status", status: http.StatusTeapot, statusText: "418 I'm a teapot", want: "418 I'm a teapot", wantExit: exitFindings},
		{name: "successful malformed response", status: http.StatusOK, statusText: "200 OK", body: "{oops", response: &map[string]string{}, want: "decode daemon response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := &peerAdminClient{httpClient: &http.Client{Transport: peerAdminRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
					t.Errorf("request = %s %s, content type %q", request.Method, request.URL, request.Header.Get("Content-Type"))
				}
				return peerAdminHTTPResponse(test.status, test.statusText, io.NopCloser(strings.NewReader(test.body))), nil
			})}}
			err := client.call(context.Background(), peersRPCPrefix+"block", map[string]string{"peer": "laptop"}, test.response)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("call error = %v, want %q", err, test.want)
			}
			if test.wantExit != 0 {
				var exit *exitError
				if !errors.As(err, &exit) || exit.code != test.wantExit {
					t.Fatalf("refusal = %v, want exit %d", err, test.wantExit)
				}
			}
		})
	}
	client := &peerAdminClient{httpClient: &http.Client{Transport: peerAdminRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return peerAdminHTTPResponse(http.StatusOK, "200 OK", io.NopCloser(strings.NewReader("not JSON"))), nil
	})}}
	if err := client.call(context.Background(), peersRPCPrefix+"block", map[string]string{"peer": "laptop"}, nil); err != nil {
		t.Fatalf("a successful response with no requested value should be discarded: %v", err)
	}
}
