package fleet

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/hubaddress"
)

const vmBearer = "vm-machine-credential"

// hubRequest is what a fake hub saw of one request.
type hubRequest struct {
	method, path, query, host, authorization, cookie string
}

// fakeHub is an httptest server standing for another machine's daemon-hosted
// hub. It records every request and answers with handler.
type fakeHub struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []hubRequest
}

func newFakeHub(t *testing.T, handler http.HandlerFunc) *fakeHub {
	t.Helper()
	hub := &fakeHub{}
	hub.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hub.mu.Lock()
		hub.requests = append(hub.requests, hubRequest{
			method: request.Method, path: request.URL.Path, query: request.URL.RawQuery, host: request.Host,
			authorization: request.Header.Get("Authorization"), cookie: request.Header.Get("Cookie"),
		})
		hub.mu.Unlock()
		handler(writer, request)
	}))
	t.Cleanup(hub.server.Close)
	return hub
}

func (h *fakeHub) seen() []hubRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]hubRequest(nil), h.requests...)
}

// address is the host:port the hub listens on.
func (h *fakeHub) address() string { return strings.TrimPrefix(h.server.URL, "http://") }

// tokenFile writes a private credential file holding content.
func tokenFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vm.token")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// serving answers the export route as a hub does: 401 without the bearer, the
// full envelope, or the metrics-only one for ?metrics_only=1.
func serving(full, only []byte) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != MachineExportPath || request.Header.Get("Authorization") != "Bearer "+vmBearer {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.RawQuery == "metrics_only=1" {
			_, _ = writer.Write(only)
			return
		}
		_, _ = writer.Write(full)
	}
}

func marshalled(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func httpTarget(hub *fakeHub, token string) RemoteTarget {
	return RemoteTarget{Machine: vmKey, HTTP: &HTTPRoute{URL: hub.server.URL, TokenFile: token}}
}

// TestConfiguredTargetAppearsLiveRemote proves
// cockpit-views#ac:configured-target-appears-live-remote end to end over the
// HTTP transport against a fake hub: a machine `vm` with an http route whose
// hub returns an envelope with 3 worktrees, 1 running agent and 360 samples, a
// second transport standing for SSH that counts its calls, and published-store
// entries for `vm` that are 25 days old. After one refresh interval on a fake
// clock the vm worktrees and agent are live-remote over http with the envelope's
// time, replacing the cached entries, the metrics answer is live-remote with
// fetched_at and the history, the fallback transport was never started, no
// request to the Cockpit caused an outbound call, and nothing the browser is
// served names the vm's address or credential.
func TestConfiguredTargetAppearsLiveRemote(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 360, false)
	only := exportOf(t, vmOwnName, vmSources(), 360, true)
	hub := newFakeHub(t, serving(marshalled(t, full), marshalled(t, only)))
	fallback := &fakeExporter{answer: failing(errBoom)}
	sources := oneRepoSources("/repos/widgets")
	sources.remote = append(sources.remote, cachedVM("alex"))
	var clock *manualClock
	snapshotter, clock := newSnapshotter(sources.collectors(), func(options *Options) {
		options.Login = testLogin
		options.Remotes = []RemoteTarget{httpTarget(hub, tokenFile(t, vmBearer+"\n"))}
		options.Transports = []RemoteTransport{
			{Name: TransportHTTP, Exporter: NewHTTPExporter(func() time.Time { return clock.Now() })},
			{Name: TransportSSH, Exporter: fallback},
		}
	})
	refreshAndSettle(t, snapshotter)
	clock.advance(2 * time.Second)
	pollAndSettle(t, snapshotter)
	for range DefaultInterval / remoteStep {
		clock.advance(remoteStep)
		pollAndSettle(t, snapshotter)
	}
	refreshAndSettle(t, snapshotter)

	requests := hub.seen()
	if len(requests) != 2 {
		t.Fatalf("the hub saw %d requests in one refresh interval, want the first export and one more: %+v", len(requests), requests)
	}
	for _, request := range requests {
		if request.method != http.MethodGet || request.path != MachineExportPath || request.query != "" || request.authorization != "Bearer "+vmBearer || request.host != hub.address() || request.cookie != "" {
			t.Errorf("a request to the hub = %+v", request)
		}
	}
	server := newCockpitServer(t, snapshotter)
	document := server.fleet()
	requireUniqueIDs(t, document)
	vm, found := machineNamed(document, vmKey)
	if !found || vm.Route != RouteLiveRemote || vm.Transport != TransportHTTP || vm.RemoteError != "" || !vm.ObservedAt.Equal(full.Fleet.SnapshotAt) {
		t.Fatalf("vm = %+v", vm)
	}
	live := entriesOf(document, vm.ID)
	if len(live["worktrees"]) != 3 || len(live["agents"]) != 1 || len(live["pull_requests"]) != 1 || len(live["repositories"]) != 1 {
		t.Fatalf("vm's entries = %v", live)
	}
	for _, worktree := range document.Worktrees {
		if worktree.MachineID != vm.ID {
			continue
		}
		if worktree.Route != RouteLiveRemote || !worktree.ObservedAt.Equal(full.Fleet.SnapshotAt) || worktree.OwnerState == "" || worktree.Ahead == nil || worktree.HasUpstream == nil {
			t.Errorf("a vm worktree = %+v", worktree)
		}
	}
	for _, agent := range document.Agents {
		if agent.MachineID == vm.ID && (agent.Route != RouteLiveRemote || agent.State != "running" || !agent.ObservedAt.Equal(full.Fleet.SnapshotAt)) {
			t.Errorf("the vm agent = %+v", agent)
		}
	}
	for _, pull := range document.PullRequests {
		if pull.MachineID == vm.ID && (pull.Number != 41 || pull.URL == "" || pull.Worktree == "") {
			t.Errorf("the vm pull request = %+v", pull)
		}
	}
	body := server.get(cockpit.APIPrefix+FleetRoute, nil).Body.String()
	metrics := server.get(metricsURL+vm.ID, nil).Body.String()
	for _, absent := range []string{"stale-task", "pull/9", hub.address(), hub.server.URL, vmBearer, vmOwnName, "token"} {
		if strings.Contains(body, absent) || strings.Contains(metrics, absent) {
			t.Errorf("what the browser is served carries %q", absent)
		}
	}
	answer := routeOf(t, server, vm.ID)
	if answer.Route != RouteLiveRemote || answer.FetchedAt == nil || len(answer.Samples) != 360 || answer.Machine != vm.ID {
		t.Fatalf("vm's metrics = route %s, fetched %v, %d samples", answer.Route, answer.FetchedAt, len(answer.Samples))
	}
	if fallback.count() != 0 {
		t.Errorf("the fallback transport was started %d times", fallback.count())
	}
	if after := hub.seen(); len(after) != len(requests) {
		t.Errorf("a request to the Cockpit caused %d outbound calls", len(after)-len(requests))
	}
}

// TestBearerStaysWithTheConfiguredHost proves
// cockpit-views#ac:bearer-stays-with-the-configured-host: a redirect to another
// host is not followed and that host receives nothing; an http:// address off
// loopback and an address with user information, a path or a query are refused
// by ValidateHubURL with no request at all; a loopback http:// address is used;
// every request carries the bearer to the configured host only; and a response
// over 8 MiB is refused without being read past the cap.
func TestBearerStaysWithTheConfiguredHost(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	now := newClock().Now
	exporter := NewHTTPExporter(now)
	token := tokenFile(t, vmBearer)

	other := newFakeHub(t, serving(marshalled(t, full), nil))
	redirecting := newFakeHub(t, func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, other.server.URL+MachineExportPath, http.StatusFound)
	})
	_, err := exporter.Export(t.Context(), httpTarget(redirecting, token), false)
	var failure *RemoteError
	if !errors.As(err, &failure) || failure.Code != RemoteErrorHTTPUnavailable || !failure.Fallback {
		t.Fatalf("a redirect = %v, want http_unavailable", err)
	}
	if got := redirecting.seen(); len(got) != 1 || got[0].authorization != "Bearer "+vmBearer {
		t.Errorf("the configured host saw %+v", got)
	}
	if got := other.seen(); len(got) != 0 {
		t.Fatalf("the redirect was followed: the other host saw %+v", got)
	}

	// The addresses the rule refuses name the fake hub where they can, so a
	// request, were one made, would be seen.
	hub := newFakeHub(t, serving(marshalled(t, full), nil))
	for name, address := range map[string]string{
		"http off loopback": "http://vm.example",
		"user information":  "http://user:secret@" + hub.address(),
		"a path":            hub.server.URL + "/v0/workbench",
		"a query":           hub.server.URL + "?machine=mac",
		"a fragment":        hub.server.URL + "#x",
		"no scheme":         hub.address(),
		"empty":             "",
	} {
		_, err := exporter.Export(t.Context(), RemoteTarget{Machine: vmKey, HTTP: &HTTPRoute{URL: address, TokenFile: token}}, false)
		if !errors.Is(err, ErrNoRoute) {
			t.Errorf("%s: %q = %v, want no route", name, address, err)
		}
	}
	if _, err := exporter.Export(t.Context(), RemoteTarget{Machine: vmKey}, false); !errors.Is(err, ErrNoRoute) {
		t.Errorf("a target with no http route = %v", err)
	}
	if got := hub.seen(); len(got) != 0 {
		t.Fatalf("a refused address was requested: %+v", got)
	}

	// The loopback http address is used, with a trailing slash or without.
	for _, address := range []string{hub.server.URL, hub.server.URL + "/"} {
		envelope, err := exporter.Export(t.Context(), RemoteTarget{Machine: vmKey, HTTP: &HTTPRoute{URL: address, TokenFile: token}}, false)
		if err != nil || envelope.Machine != vmOwnName || len(envelope.Fleet.Worktrees) != 3 {
			t.Fatalf("the loopback address %q = %v", address, err)
		}
	}
	for _, request := range hub.seen() {
		if request.host != hub.address() || request.path != MachineExportPath || request.authorization != "Bearer "+vmBearer {
			t.Errorf("a request = %+v", request)
		}
	}
	// The metrics-only export is the same path with its query.
	onlyHub := newFakeHub(t, serving(nil, marshalled(t, exportOf(t, vmOwnName, vmSources(), 2, true))))
	if envelope, err := exporter.Export(t.Context(), httpTarget(onlyHub, token), true); err != nil || envelope.Fleet != nil || len(envelope.Metrics.Samples) != 2 {
		t.Fatalf("the metrics-only export = %v", err)
	}
	if got := onlyHub.seen(); len(got) != 1 || got[0].query != "metrics_only=1" {
		t.Errorf("the metrics-only request = %+v", got)
	}

	// A response over the cap is refused, and the decoder never reads past it.
	padded := append(bytes.Repeat([]byte(" "), 9<<20), marshalled(t, full)...)
	large := newFakeHub(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write(padded) })
	_, err = exporter.Export(t.Context(), httpTarget(large, token), false)
	var refused *BadEnvelopeError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "larger than") {
		t.Fatalf("a 9 MiB response = %v, want it refused for its size", err)
	}
	reader := &countingReader{reader: bytes.NewReader(padded)}
	if _, err := DecodeEnvelope(reader, false, now()); err == nil || reader.read > MaxEnvelopeBytes+1 {
		t.Errorf("the decoder read %d bytes of a 9 MiB body, want at most %d", reader.read, MaxEnvelopeBytes+1)
	}
}

type countingReader struct {
	reader io.Reader
	read   int
}

func (c *countingReader) Read(buffer []byte) (int, error) {
	n, err := c.reader.Read(buffer)
	c.read += n
	return n, err
}

// TestHTTPFailureIsTheFallbackRule is the pure decision of which HTTP failures
// let the next transport be tried (cockpit-views#req:remote-exporter-transports)
// and which code each is (cockpit-views#req:remote-error-is-visible): no answer,
// 401, 403, 404, 429, any 5xx and a redirect fall back; any other status does
// not.
func TestHTTPFailureIsTheFallbackRule(t *testing.T) {
	t.Parallel()
	for status, want := range map[int]RemoteError{
		0:   {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		401: {Code: RemoteErrorHTTPAuthFailed, Fallback: true},
		403: {Code: RemoteErrorHTTPAuthFailed, Fallback: true},
		404: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		429: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		500: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		502: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		503: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		599: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		301: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		302: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		307: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		308: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		204: {Code: RemoteErrorHTTPUnavailable},
		400: {Code: RemoteErrorHTTPUnavailable},
		405: {Code: RemoteErrorHTTPUnavailable},
		410: {Code: RemoteErrorHTTPUnavailable},
		418: {Code: RemoteErrorHTTPUnavailable},
	} {
		if got := *httpFailure(status); got != want {
			t.Errorf("status %d: %+v, want %+v", status, got, want)
		}
	}
}

// TestHTTPExportFailuresAreCodesAndNeverTheResponsesText runs the failures
// against real connections: each status, a refused connection, a response that
// overruns the total timeout, a body that breaks off and a body that is not an
// envelope. Each is its code, and no text of the response reaches the error.
func TestHTTPExportFailuresAreCodesAndNeverTheResponsesText(t *testing.T) {
	t.Parallel()
	token := tokenFile(t, vmBearer)
	now := newClock().Now
	exporter := NewHTTPExporter(now)
	leak := sentinel + "response-body"
	for status, want := range map[int]RemoteError{
		http.StatusUnauthorized:        {Code: RemoteErrorHTTPAuthFailed, Fallback: true},
		http.StatusForbidden:           {Code: RemoteErrorHTTPAuthFailed, Fallback: true},
		http.StatusNotFound:            {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		http.StatusTooManyRequests:     {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		http.StatusInternalServerError: {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		http.StatusTemporaryRedirect:   {Code: RemoteErrorHTTPUnavailable, Fallback: true},
		http.StatusBadRequest:          {Code: RemoteErrorHTTPUnavailable},
		http.StatusAccepted:            {Code: RemoteErrorHTTPUnavailable},
	} {
		hub := newFakeHub(t, func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Location", "http://127.0.0.1:1/"+leak)
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(leak))
		})
		_, err := exporter.Export(t.Context(), httpTarget(hub, token), false)
		var failure *RemoteError
		if !errors.As(err, &failure) || *failure != want {
			t.Errorf("status %d = %v, want %+v", status, err, want)
		}
		if err == nil || strings.Contains(err.Error(), sentinel) || len(hub.seen()) != 1 {
			t.Errorf("status %d: error %v after %d requests", status, err, len(hub.seen()))
		}
	}
	unavailable := RemoteError{Code: RemoteErrorHTTPUnavailable, Fallback: true}

	closed := newFakeHub(t, func(http.ResponseWriter, *http.Request) {})
	closed.server.Close()
	var failure *RemoteError
	if _, err := exporter.Export(t.Context(), httpTarget(closed, token), false); !errors.As(err, &failure) || *failure != unavailable {
		t.Errorf("a refused connection = %v", err)
	}

	release := make(chan struct{})
	slow := newFakeHub(t, func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-release:
		case <-request.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	started := time.Now()
	if _, err := newHTTPExporter(time.Second, 50*time.Millisecond, now, nil).Export(t.Context(), httpTarget(slow, token), false); !errors.As(err, &failure) || *failure != unavailable {
		t.Errorf("a response over the total timeout = %v", err)
	}
	if waited := time.Since(started); waited > 5*time.Second {
		t.Errorf("the timeout took %v", waited)
	}

	broken := newFakeHub(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Length", "1000")
		_, _ = writer.Write([]byte(`{"schema_version":1,`))
	})
	if _, err := exporter.Export(t.Context(), httpTarget(broken, token), false); !errors.As(err, &failure) || *failure != unavailable {
		t.Errorf("a body that breaks off = %v", err)
	}

	var refused *BadEnvelopeError
	page := newFakeHub(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("<html>" + leak + "</html>"))
	})
	if _, err := exporter.Export(t.Context(), httpTarget(page, token), false); !errors.As(err, &refused) || strings.Contains(err.Error(), sentinel) {
		t.Errorf("a body that is not an envelope = %v", err)
	}
}

// TestHTTPExporterHasTheTransportsTimeoutsAndFollowsNothing pins the client:
// 3 seconds to connect, 10 in all, no redirect, no cookie jar, and the
// environment's proxy as the hub client has it.
func TestHTTPExporterHasTheTransportsTimeoutsAndFollowsNothing(t *testing.T) {
	t.Parallel()
	exporter := NewHTTPExporter(nil)
	if exporter.now == nil || exporter.now().IsZero() {
		t.Error("the exporter has no clock")
	}
	client := exporter.client
	transport, isTransport := client.Transport.(*http.Transport)
	if !isTransport || remoteConnectTimeout != 3*time.Second || remoteTotalTimeout != 10*time.Second ||
		client.Timeout != 10*time.Second || transport.TLSHandshakeTimeout != 3*time.Second || transport.ResponseHeaderTimeout != 10*time.Second {
		t.Fatalf("timeouts: client %v, transport %+v", client.Timeout, transport)
	}
	if client.Jar != nil {
		t.Error("the client keeps cookies")
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("a redirect = %v, want it not followed", err)
	}
	if reflect.ValueOf(transport.Proxy).Pointer() != reflect.ValueOf(hubaddress.Proxy).Pointer() {
		t.Error("the proxy policy is not the one of a client that sends a machine credential")
	}
	if tlsConfig := transport.TLSClientConfig; tlsConfig == nil || tlsConfig.InsecureSkipVerify || tlsConfig.MinVersion != tls.VersionTLS12 || tlsConfig.RootCAs != nil {
		t.Errorf("TLS: %+v, want verification on, the system roots and TLS 1.2 at least", transport.TLSClientConfig)
	}
}

// TestACredentialThatCannotBeUsedSendsNoRequest proves the credential rule of
// cockpit-views#req:remote-http-fetch: the token file must be an absolute path
// to a regular, private file holding one token; anything else is
// http_auth_failed with no request, and a rotated file is read on the next
// export.
func TestACredentialThatCannotBeUsedSendsNoRequest(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 2, false)
	hub := newFakeHub(t, serving(marshalled(t, full), nil))
	exporter := NewHTTPExporter(newClock().Now)
	directory := t.TempDir()
	write := func(name, content string, mode os.FileMode) string {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(content), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	for name, path := range map[string]string{
		"no file named":      "",
		"a relative path":    "vm.token",
		"a missing file":     filepath.Join(directory, "absent.token"),
		"a directory":        directory,
		"readable by others": write("shared.token", vmBearer, 0o644),
		"readable by group":  write("group.token", vmBearer, 0o640),
		"empty":              write("empty.token", "\n", 0o600),
		"two tokens":         write("two.token", "one two", 0o600),
		"a line break":       write("lines.token", "one\ntwo", 0o600),
		"too long":           write("long.token", strings.Repeat("t", maxBearerBytes+1), 0o600),
		"far too long":       write("huge.token", strings.Repeat("t", 4*maxBearerBytes), 0o600),
		"not ascii":          write("unicode.token", "tøken", 0o600),
		"a control byte":     write("control.token", "to\x00ken", 0o600),
	} {
		_, err := exporter.Export(t.Context(), RemoteTarget{Machine: vmKey, HTTP: &HTTPRoute{URL: hub.server.URL, TokenFile: path}}, false)
		var failure *RemoteError
		if !errors.As(err, &failure) || failure.Code != RemoteErrorHTTPAuthFailed || !failure.Fallback {
			t.Errorf("%s = %v, want http_auth_failed", name, err)
		}
		if _, err := readBearer(path); !errors.Is(err, errBearer) || strings.Contains(err.Error(), directory) {
			t.Errorf("%s: readBearer = %v", name, err)
		}
	}
	if got := hub.seen(); len(got) != 0 {
		t.Fatalf("a request was sent without a usable credential: %+v", got)
	}
	if !privateFile(0o600) || !privateFile(0o400) || privateFile(0o604) || privateFile(0o660) {
		t.Error("the private-file rule is wrong")
	}
	longest := write("longest.token", strings.Repeat("t", maxBearerBytes)+"\n", 0o600)
	if token, err := readBearer(longest); err != nil || len(token) != maxBearerBytes {
		t.Errorf("a token of the longest length = %d bytes, %v", len(token), err)
	}
	// The file is read on every export: a wrong credential is refused by the hub,
	// and the right one written to the same file is used by the next export.
	rotated := write("rotated.token", "an-old-credential", 0o600)
	target := RemoteTarget{Machine: vmKey, HTTP: &HTTPRoute{URL: hub.server.URL, TokenFile: rotated}}
	var failure *RemoteError
	if _, err := exporter.Export(t.Context(), target, false); !errors.As(err, &failure) || failure.Code != RemoteErrorHTTPAuthFailed {
		t.Fatalf("an old credential = %v", err)
	}
	write("rotated.token", "  "+vmBearer+"\n", 0o600)
	if _, err := exporter.Export(t.Context(), target, false); err != nil {
		t.Fatalf("the rotated credential = %v", err)
	}
}

// hostilePayloads is the refused bodies of
// cockpit-views#ac:hostile-payload-is-refused, each made from the valid
// envelope: 9 MiB, an unknown top-level field, a 10,000-byte task name, a
// worktree carrying a path, a negative count, a NaN, a sample 5 minutes after
// later and 361 samples.
func hostilePayloads(t *testing.T, valid []byte, later time.Time) map[string][]byte {
	t.Helper()
	edited := func(change func(root map[string]any)) []byte {
		var root map[string]any
		if err := json.Unmarshal(valid, &root); err != nil {
			t.Fatal(err)
		}
		change(root)
		return marshalled(t, root)
	}
	fleet := func(root map[string]any, collection string) map[string]any {
		return root["fleet"].(map[string]any)[collection].([]any)[0].(map[string]any)
	}
	samples := func(root map[string]any) []any {
		return root["metrics"].(map[string]any)["samples"].([]any)
	}
	cases := map[string][]byte{
		"9 MiB":                  append(bytes.Repeat([]byte(" "), 9<<20), valid...),
		"an unknown field":       edited(func(root map[string]any) { root["commands"] = []string{"rm -rf /"} }),
		"a 10,000-byte task":     edited(func(root map[string]any) { fleet(root, "worktrees")["task"] = strings.Repeat("t", 10_000) }),
		"a worktree with a path": edited(func(root map[string]any) { fleet(root, "worktrees")["path"] = "/home/ai/projects/secret" }),
		"a negative count":       edited(func(root map[string]any) { fleet(root, "repositories")["worktree_count"] = -1 }),
		"a NaN":                  bytes.Replace(valid, []byte(`"worktree_count":3`), []byte(`"worktree_count":NaN`), 1),
		"a sample in the future": edited(func(root map[string]any) {
			list := samples(root)
			list[len(list)-1].(map[string]any)["sampled_at"] = later.Add(5 * time.Minute).Format(time.RFC3339)
		}),
		"361 samples": edited(func(root map[string]any) {
			list := samples(root)
			extra := map[string]any{"sampled_at": later.Add(-10 * time.Second).Format(time.RFC3339)}
			root["metrics"].(map[string]any)["samples"] = append(list, extra)
		}),
	}
	if bytes.Equal(cases["a NaN"], valid) {
		t.Fatal("the NaN case changed nothing")
	}
	return cases
}

// TestHostilePayloadIsRefused proves cockpit-views#ac:hostile-payload-is-refused
// over the HTTP transport, through the whole daemon path: a 9 MiB body, an
// unknown top-level field, a 10,000-byte task name, a worktree carrying a path,
// a negative count, a NaN, a sample 5 minutes in the future and 361 samples are
// each refused as bad_payload; nothing of them is rendered (the machine's
// published entries stay, with the code); and the fallback transport is not
// tried, because a refused payload is not a transport failure.
func TestHostilePayloadIsRefused(t *testing.T) {
	t.Parallel()
	full := exportOf(t, vmOwnName, vmSources(), 360, false)
	valid := marshalled(t, full)
	// The daemon's clock is a minute past the export's, so a 361st sample is
	// refused for the cap and not for being in the future.
	later := newClock().Now().Add(time.Minute)
	cases := hostilePayloads(t, valid, later)
	for name, body := range cases {
		hub := newFakeHub(t, serving(body, nil))
		fallback := &fakeExporter{answer: answering(full, full)}
		sources := &fakeSources{}
		sources.remote = append(sources.remote, cachedVM("alex"))
		var clock *manualClock
		snapshotter, clock := newSnapshotter(sources.collectors(), func(options *Options) {
			options.Login = testLogin
			options.Remotes = []RemoteTarget{httpTarget(hub, tokenFile(t, vmBearer))}
			options.Transports = []RemoteTransport{
				{Name: TransportHTTP, Exporter: NewHTTPExporter(func() time.Time { return clock.Now() })},
				{Name: TransportSSH, Exporter: fallback},
			}
		})
		clock.advance(time.Minute)
		refreshAndSettle(t, snapshotter)
		pollAndSettle(t, snapshotter)
		document := snapshotter.Document()
		vm, found := machineNamed(document, vmKey)
		if !found || vm.Route != RouteCached || vm.RemoteError != RemoteErrorBadPayload || vm.Transport != "" {
			t.Errorf("%s: the machine = %+v, want its published entry with bad_payload", name, vm)
		}
		rendered := marshalled(t, document)
		for _, absent := range []string{"vm-task", "agt-vm", "rm -rf", "/home/ai", RouteLiveRemote} {
			if bytes.Contains(rendered, []byte(absent)) {
				t.Errorf("%s: the document renders %q of the refused payload", name, absent)
			}
		}
		if len(hub.seen()) != 1 || fallback.count() != 0 {
			t.Errorf("%s: %d requests and %d fallback exports, want 1 and 0", name, len(hub.seen()), fallback.count())
		}
		server := newCockpitServer(t, snapshotter)
		if got := routeOf(t, server, vm.ID); got.Route != RouteNone || len(got.Samples) != 0 {
			t.Errorf("%s: the metrics of the refused payload are served: %+v", name, got)
		}
	}
	// The valid body, on the same path, is accepted: the cases above are refused
	// for what was changed in them.
	hub := newFakeHub(t, serving(valid, nil))
	if _, err := NewHTTPExporter(func() time.Time { return later }).Export(t.Context(), httpTarget(hub, tokenFile(t, vmBearer)), false); err != nil {
		t.Fatalf("the unchanged body is refused: %v", err)
	}
}
