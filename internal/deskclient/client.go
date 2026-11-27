// Package deskclient is the WB daemon's HTTP client for Sneat Dev Desk
// (spec/features/desk-connect). Every failure is a typed desk.ErrorCode; the
// text of a Desk response is never kept.
package deskclient

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/hubaddress"
)

// Default timeouts.
const (
	DefaultConnectTimeout = 10 * time.Second
	DefaultTotalTimeout   = 30 * time.Second
	maxCredentialBytes    = desk.MaxCredentialLength
)

// Error is a typed Desk failure.
type Error struct {
	Code desk.ErrorCode
	// RetryAfter is Desk's Retry-After for rate_limited, zero when absent.
	RetryAfter time.Duration
	// MinimumSchemaVersion is the version Desk requires for schema_unsupported.
	MinimumSchemaVersion int
}

func (err *Error) Error() string { return "desk: " + string(err.Code) }

// CodeOf is the desk.ErrorCode of err, or "" when err is not a Desk failure.
func CodeOf(err error) desk.ErrorCode {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

// ErrInvalidURL is returned for a Desk address a credential may not be sent to.
var ErrInvalidURL = errors.New("desk url must be https, or http on a loopback host, with no path")

// ErrCredential is returned when the credential file is missing, not private,
// or not one token.
var ErrCredential = errors.New("desk credential file must be one private token")

// Options configures a Client.
type Options struct {
	// CredentialFile holds the machine credential; empty for the unauthenticated
	// connect routes only.
	CredentialFile string
	Connect        time.Duration
	Total          time.Duration
	// Transport replaces the default transport (tests).
	Transport http.RoundTripper
}

// Client talks to one Desk origin.
type Client struct {
	origin     string
	credential string
	http       *http.Client
}

// New returns a client for the Desk at rawURL.
func New(rawURL string, options Options) (*Client, error) {
	if !hubaddress.Valid(rawURL) {
		return nil, ErrInvalidURL
	}
	connect, total := options.Connect, options.Total
	if connect <= 0 {
		connect = DefaultConnectTimeout
	}
	if total <= 0 {
		total = DefaultTotalTimeout
	}
	transport := options.Transport
	if transport == nil {
		transport = &http.Transport{
			Proxy:                  hubaddress.Proxy,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			DialContext:            (&net.Dialer{Timeout: connect}).DialContext,
			TLSHandshakeTimeout:    connect,
			ResponseHeaderTimeout:  total,
			MaxResponseHeaderBytes: 64 << 10,
			MaxIdleConnsPerHost:    1,
			IdleConnTimeout:        90 * time.Second,
			ForceAttemptHTTP2:      true,
		}
	}
	return &Client{origin: hubaddress.Origin(rawURL), credential: options.CredentialFile, http: &http.Client{
		Timeout:   total,
		Transport: transport,
		// A redirect is never followed, so the bearer reaches the configured host only.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func readCredential(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return "", ErrCredential
	}
	raw, err := os.ReadFile(path)
	token := strings.TrimSpace(string(raw))
	if err != nil || token == "" || len(token) > maxCredentialBytes || strings.ContainsFunc(token, func(r rune) bool { return r <= ' ' || r > '~' }) {
		return "", ErrCredential
	}
	return token, nil
}

type call struct {
	method   string
	path     string
	body     []byte
	gzipped  bool
	auth     bool
	accept   []int
	response desk.Validator
}

func (client *Client) do(ctx context.Context, c call) (int, error) {
	var reader io.Reader
	if c.body != nil {
		reader = bytes.NewReader(c.body)
	}
	request, err := http.NewRequestWithContext(ctx, c.method, client.origin+c.path, reader)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/json")
	if c.body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.gzipped {
		request.Header.Set("Content-Encoding", "gzip")
	}
	if c.auth {
		token, err := readCredential(client.credential)
		if err != nil {
			return 0, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.http.Do(request)
	if err != nil {
		code := desk.ErrorUnreachable
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			code = desk.ErrorTimeout
		}
		return 0, &Error{Code: code}
	}
	defer func() { _ = response.Body.Close() }()
	for _, status := range c.accept {
		if response.StatusCode != status {
			continue
		}
		if c.response == nil {
			return status, nil
		}
		if err := desk.Decode(response.Body, desk.MaxResponseBytes, c.response); err != nil {
			return status, &Error{Code: desk.ErrorRejectedPayload}
		}
		return status, nil
	}
	return response.StatusCode, statusError(response)
}

func statusError(response *http.Response) error {
	switch status := response.StatusCode; {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &Error{Code: desk.ErrorRevoked}
	case status == http.StatusRequestEntityTooLarge:
		return &Error{Code: desk.ErrorTooLarge}
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		return &Error{Code: desk.ErrorRejectedPayload}
	case status == http.StatusUpgradeRequired:
		var body desk.SchemaUnsupported
		minimum := 0
		if desk.Decode(response.Body, desk.MaxResponseBytes, &body) == nil {
			minimum = body.MinimumSchemaVersion
		}
		return &Error{Code: desk.ErrorSchemaUnsupported, MinimumSchemaVersion: minimum}
	case status == http.StatusTooManyRequests:
		retry := time.Duration(0)
		if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil && seconds > 0 {
			retry = time.Duration(seconds) * time.Second
		}
		return &Error{Code: desk.ErrorRateLimited, RetryAfter: retry}
	default:
		return &Error{Code: desk.ErrorUnreachable}
	}
}

func encode(value desk.Validator) ([]byte, error) {
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

// Start opens a device grant (unauthenticated).
func (client *Client) Start(ctx context.Context, request desk.ConnectStartRequest) (desk.ConnectStartResponse, error) {
	var response desk.ConnectStartResponse
	body, err := encode(request)
	if err != nil {
		return response, err
	}
	_, err = client.do(ctx, call{method: http.MethodPost, path: desk.PathConnectStart, body: body,
		accept: []int{http.StatusOK}, response: &response})
	return response, err
}

// Token polls a device grant (unauthenticated). A grant error is a normal
// answer: it comes back in the response, not as an error.
func (client *Client) Token(ctx context.Context, deviceCode string) (desk.ConnectTokenResponse, error) {
	var response desk.ConnectTokenResponse
	body, err := encode(desk.ConnectTokenRequest{DeviceCode: deviceCode})
	if err != nil {
		return response, err
	}
	status, err := client.do(ctx, call{method: http.MethodPost, path: desk.PathConnectToken, body: body,
		accept: []int{http.StatusOK, http.StatusBadRequest}, response: &response})
	if err == nil && (status == http.StatusOK) != (response.Error == "") {
		return desk.ConnectTokenResponse{}, &Error{Code: desk.ErrorRejectedPayload}
	}
	return response, err
}

// Self reads Desk's record of this machine.
func (client *Client) Self(ctx context.Context) (desk.MachineSelf, error) {
	var response desk.MachineSelf
	_, err := client.do(ctx, call{method: http.MethodGet, path: desk.PathMachineSelf, auth: true,
		accept: []int{http.StatusOK}, response: &response})
	return response, err
}

// Revoke deletes this machine's credential at Desk.
func (client *Client) Revoke(ctx context.Context) error {
	_, err := client.do(ctx, call{method: http.MethodDelete, path: desk.PathMachineSelf, auth: true,
		accept: []int{http.StatusNoContent, http.StatusOK}})
	return err
}

// Publish sends a snapshot gzip-encoded, refusing locally one over the bounds
// (desk-connect#req:bounded-payload).
func (client *Client) Publish(ctx context.Context, request desk.SnapshotRequest) (desk.SnapshotReceipt, error) {
	var receipt desk.SnapshotReceipt
	body, err := encode(request)
	if err != nil {
		return receipt, err
	}
	if len(body) > desk.MaxDecodedSnapshotBytes {
		return receipt, &Error{Code: desk.ErrorTooLarge}
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	_, _ = writer.Write(body)
	_ = writer.Close()
	if compressed.Len() > desk.MaxCompressedSnapshotBytes {
		return receipt, &Error{Code: desk.ErrorTooLarge}
	}
	_, err = client.do(ctx, call{method: http.MethodPut, path: desk.PathSnapshot, body: compressed.Bytes(), gzipped: true,
		auth: true, accept: []int{http.StatusOK}, response: &receipt})
	if err == nil && receipt.Hash != request.Hash {
		return desk.SnapshotReceipt{}, &Error{Code: desk.ErrorRejectedPayload}
	}
	return receipt, err
}

// Heartbeat sends the periodic liveness signal.
func (client *Client) Heartbeat(ctx context.Context, request desk.HeartbeatRequest) error {
	body, err := encode(request)
	if err != nil {
		return err
	}
	_, err = client.do(ctx, call{method: http.MethodPost, path: desk.PathHeartbeat, body: body, auth: true,
		accept: []int{http.StatusNoContent, http.StatusOK}})
	return err
}
