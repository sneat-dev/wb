package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// fakeDaemonHTTPClient dials directly to serverAddr regardless of the
// request's own host, exactly like daemonLocalHTTPClient dials a fixed unix
// socket path regardless of the request's host — the seam that lets a test
// exercise peerAdminClient.call's real JSON encoding/decoding against a real
// http.Handler without a live daemon process or a unix socket.
func fakeDaemonHTTPClient(serverAddr, token string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", serverAddr)
		},
	}
	return &http.Client{Transport: daemonAuthenticatedTransport{token: token, base: transport}}
}

// testPeersDeps wires a peersDeps whose admin writes go through server (a
// newPeerAdminHTTPHandler-backed httptest server) and whose reads go through
// readServer (an internal/peers.NewHandler-backed one), so both the CLI's
// write and read paths run their real HTTP encode/decode logic.
//
// It never leaves a path to the real daemon controller reachable:
// daemonDeps is always daemonTestDependencies's in-memory fake (never
// defaultDaemonDependencies's real startDaemonProcess/stopDaemonProcess,
// which can register a real launchd job on the founder's actual machine —
// see the incident this guards against), and when adminServer is nil,
// adminClient is a hard t.Fatal guard: no test that passes adminServer==nil
// expects the owner-RPC path to be reached at all (list/get are read-only,
// and the upstream-local block/unblock path returns before ever calling
// adminClient), so reaching it here is exactly the bug this guard exists to
// catch before it can start a process.
func testPeersDeps(t *testing.T, adminServer, readServer *httptest.Server) peersDeps {
	t.Helper()
	deps := defaultPeersDeps()
	deps.daemonDeps = daemonTestDependencies(t, t.TempDir())
	// t.TempDir() returns a fresh directory on every call, so the path must
	// be resolved once here — calling it inside the closure would give
	// wbconfig.SetPeersUpstream and resolveUpstreamRow two different files.
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	deps.configPath = func() string { return configPath }
	if adminServer != nil {
		// requireHubConfigured (M7) refuses invite/block/unblock/disconnect
		// as a usage error when this machine has no hub: section at all; a
		// wired adminServer means the test expects the owner-RPC path to be
		// reachable, so wb.yaml must say a hub exists for that to be true.
		if err := os.WriteFile(configPath, []byte("hub:\n  store:\n    engine: memory\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		client := fakeDaemonHTTPClient(adminServer.Listener.Addr().String(), "owner-token")
		deps.adminClient = func(context.Context, daemonDependencies, string) (*peerAdminClient, error) {
			return &peerAdminClient{httpClient: client}, nil
		}
	} else {
		deps.adminClient = func(context.Context, daemonDependencies, string) (*peerAdminClient, error) {
			t.Fatal("test reached the owner-token daemon RPC path with no adminServer fixture wired; this would fall through to a real daemon controller")
			return nil, nil
		}
	}
	if readServer != nil {
		listen := strings.TrimPrefix(readServer.URL, "http://")
		deps.listenAddress = func(daemonDependencies, string) (string, error) { return listen, nil }
	} else {
		deps.listenAddress = func(daemonDependencies, string) (string, error) { return "", os.ErrNotExist }
	}
	deps.now = func() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }
	return deps
}

// fixedPeerSource is a minimal internal/peers.Source for CLI-level tests
// that only need a canned list and/or detail response.
type fixedPeerSource struct {
	list   []peers.Record
	detail peers.Detail
	found  bool
}

func (source fixedPeerSource) ListPeers(context.Context) ([]peers.Record, error) {
	return source.list, nil
}
func (source fixedPeerSource) GetPeer(_ context.Context, id string) (peers.Detail, bool, error) {
	if !source.found || (source.detail.ID != id && source.detail.Name != id) {
		return peers.Detail{}, false, nil
	}
	return source.detail, true, nil
}

func newPeerAdminTestServer(t *testing.T) (*httptest.Server, *hubMount) {
	t.Helper()
	mount, _ := peerAdminTestMount(t)
	server := httptest.NewServer(authenticatedDaemonHandler("owner-token", newPeerAdminHTTPHandler(mount)))
	t.Cleanup(server.Close)
	return server, mount
}

// TestHubOnlyVerbsRefuseWithoutAHubConfig is M7: invite, disconnect, and
// block/unblock for a name that is not the configured upstream must refuse
// as a usage error when this machine has no hub: section, instead of
// starting a daemon just to learn that from a 503 over the owner RPC. Each
// deps here has adminClient set to testPeersDeps's t.Fatal guard (adminServer
// == nil), so reaching the RPC path at all would fail the test loudly.
func TestHubOnlyVerbsRefuseWithoutAHubConfig(t *testing.T) {
	deps := testPeersDeps(t, nil, nil)
	var out bytes.Buffer

	assertUsageRefusal := func(t *testing.T, err error) {
		t.Helper()
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitUsage {
			t.Fatalf("error = %v, want an exitUsage *exitError", err)
		}
	}

	err := runPeersInvite(context.Background(), deps, t.TempDir(), "laptop", false, "", false, &out)
	if err == nil {
		t.Fatal("expected invite to refuse without a hub config")
	}
	assertUsageRefusal(t, err)

	err = runPeersDisconnect(context.Background(), deps, t.TempDir(), "laptop", false, &out)
	if err == nil {
		t.Fatal("expected disconnect to refuse without a hub config")
	}
	assertUsageRefusal(t, err)

	err = runPeersTrustChange(context.Background(), deps, t.TempDir(), peersRPCPrefix+"block", "Blocked", "laptop", false, &out)
	if err == nil {
		t.Fatal("expected block of a non-upstream name to refuse without a hub config")
	}
	assertUsageRefusal(t, err)
}

// TestPeersInvitePrintsTokenOnceOrWritesTokenFile covers wb.peers.invite: by
// default the token is the one thing printed, and --token-file writes it
// privately instead and never echoes it.
func TestPeersInvitePrintsTokenOnceOrWritesTokenFile(t *testing.T) {
	adminServer, mount := newPeerAdminTestServer(t)
	// The fixture hub runs the memory engine, which invite refuses
	// unconditionally (rotate included) — that refusal is exercised directly
	// against the service in hub's own tests. This CLI-level test only cares
	// about the token's presentation, so it lifts the engine restriction.
	mount.PeerAdmin.MemoryEngine = false
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
		t.Fatal(err)
	}
	deps := testPeersDeps(t, adminServer, nil)

	var out bytes.Buffer
	if err := runPeersInvite(context.Background(), deps, t.TempDir(), "laptop", true, "", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Rotated laptop") || !strings.Contains(out.String(), "Token (copy this now") {
		t.Fatalf("invite text output = %q", out.String())
	}

	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	out.Reset()
	if err := runPeersInvite(context.Background(), deps, t.TempDir(), "laptop", true, tokenFile, false, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "Token (copy this now") {
		t.Fatalf("invite output leaked the token to stdout: %q", out.String())
	}
	if !strings.Contains(out.String(), tokenFile) {
		t.Fatalf("invite output does not name the token file: %q", out.String())
	}
	info, err := os.Stat(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %o, want 0600", info.Mode().Perm())
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil || strings.TrimSpace(string(raw)) == "" {
		t.Fatalf("token file content = %q, %v", raw, err)
	}

	out.Reset()
	if err := runPeersInvite(context.Background(), deps, t.TempDir(), "laptop", true, "", true, &out); err != nil {
		t.Fatal(err)
	}
	var jsonResult peersInviteResult
	if err := json.Unmarshal(out.Bytes(), &jsonResult); err != nil || jsonResult.Token == "" || jsonResult.PeerID == "" {
		t.Fatalf("invite JSON output = %q, %v", out.String(), err)
	}
}

func peerRecordFixture(name string) hub.PeerRecord {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	// MachineID must match hub.MachineID("local", name) exactly: that is the
	// key PeerAdminService.Invite/Block/etc. compute and look up by, and a
	// mismatched fixture ID would make every "existing peer" lookup silently
	// miss.
	return hub.PeerRecord{MachineID: hub.MachineID("local", name), Name: name, IdentityID: "local", Trust: hub.PeerTrustActive, CreatedAt: now, TrustChangedAt: now}
}

// TestPeersBlockUnblockDisconnectCallTheOwnerPath covers wb.peers.block,
// wb.peers.unblock and wb.peers.disconnect: each is a thin JSON round trip
// through the owner RPC.
func TestPeersBlockUnblockDisconnectCallTheOwnerPath(t *testing.T) {
	adminServer, mount := newPeerAdminTestServer(t)
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
		t.Fatal(err)
	}
	deps := testPeersDeps(t, adminServer, nil)

	var out bytes.Buffer
	if err := runPeersTrustChange(context.Background(), deps, t.TempDir(), peersRPCPrefix+"block", "Blocked", "laptop", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Blocked laptop") || !strings.Contains(out.String(), "trust=blocked") {
		t.Fatalf("block output = %q", out.String())
	}

	out.Reset()
	if err := runPeersTrustChange(context.Background(), deps, t.TempDir(), peersRPCPrefix+"unblock", "Unblocked", "laptop", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Unblocked laptop") || !strings.Contains(out.String(), "trust=active") {
		t.Fatalf("unblock output = %q", out.String())
	}

	out.Reset()
	if err := runPeersDisconnect(context.Background(), deps, t.TempDir(), "laptop", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no live session") {
		t.Fatalf("disconnect output = %q, want it to name Task 2's not-yet-implemented session", out.String())
	}
}

// TestPeersListRendersDownstreamAndUpstreamRows covers wb.peers.list: a
// downstream peer read from the local daemon API and an upstream row
// derived from peers.upstream configuration both appear.
func TestPeersListRendersDownstreamAndUpstreamRows(t *testing.T) {
	readServer := httptest.NewServer(peers.NewHandler("/api/v1/peers", fixedPeerSource{
		list: []peers.Record{{SchemaVersion: peers.SchemaVersion, ID: "machine_1", Name: "laptop", Role: "downstream", Status: "offline"}},
	}, nil))
	t.Cleanup(readServer.Close)
	deps := testPeersDeps(t, nil, readServer)
	if err := wbconfig.SetPeersUpstream(deps.configPath(), "https://vm1.sneat.dev", filepath.Join(t.TempDir(), "token")); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := runPeersList(context.Background(), deps, t.TempDir(), false, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "laptop") || !strings.Contains(text, "downstream") || !strings.Contains(text, "vm1.sneat.dev") || !strings.Contains(text, "upstream") {
		t.Fatalf("list text output = %q", text)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr = %q, want empty when the downstream read succeeds", errOut.String())
	}

	out.Reset()
	if err := runPeersList(context.Background(), deps, t.TempDir(), true, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var jsonResult peers.ListResponse
	if err := json.Unmarshal(out.Bytes(), &jsonResult); err != nil || len(jsonResult.Peers) != 2 {
		t.Fatalf("list JSON output = %q, %v", out.String(), err)
	}
}

// TestPeersListIsForgivingWithoutADaemonOrUpstream proves a fresh install
// (no hub, no upstream) lists cleanly rather than failing.
func TestPeersListIsForgivingWithoutADaemonOrUpstream(t *testing.T) {
	deps := testPeersDeps(t, nil, nil)
	var out, errOut bytes.Buffer
	if err := runPeersList(context.Background(), deps, t.TempDir(), true, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var jsonResult peers.ListResponse
	if err := json.Unmarshal(out.Bytes(), &jsonResult); err != nil || len(jsonResult.Peers) != 0 {
		t.Fatalf("empty list output = %q, %v", out.String(), err)
	}
}

// TestPeersListEscalatesToAFindingWhenAHubIsConfiguredButUnreachable covers
// M6: a stopped or unhealthy daemon on a self-hosted hub machine is a
// finding, not a silent empty list, distinguishing it from the ordinary
// fresh-laptop case above.
func TestPeersListEscalatesToAFindingWhenAHubIsConfiguredButUnreachable(t *testing.T) {
	deps := testPeersDeps(t, nil, nil)
	hubConfigPath := deps.configPath()
	if err := os.WriteFile(hubConfigPath, []byte("hub:\n  store:\n    engine: memory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err := runPeersList(context.Background(), deps, t.TempDir(), true, &out, &errOut)
	if err == nil {
		t.Fatal("expected a finding when a hub is configured but the daemon is unreachable")
	}
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitFindings {
		t.Fatalf("runPeersList error = %v, want an exitFindings *exitError", err)
	}
	if errOut.Len() == 0 {
		t.Fatal("expected a stderr note naming the downstream failure")
	}
}

// TestPeersListGoldenTextAndJSON is S5's golden-comparison upgrade over the
// earlier strings.Contains checks: an exact expected string, for both text
// and JSON, covering a downstream (connected-looking) row and an upstream
// (reset-pending) row together.
func TestPeersListGoldenTextAndJSON(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-2 * time.Hour)
	downstream := peers.Record{SchemaVersion: peers.SchemaVersion, ID: "machine_1", Name: "alex-macbook", Role: "downstream", Status: "connected", LastSeenAt: &lastSeen}
	upstream := peers.Record{SchemaVersion: peers.SchemaVersion, ID: "vm1.sneat.dev", Name: "vm1.sneat.dev", Role: "upstream", Status: "offline", ResetPending: true}
	rows := []peers.Record{downstream, upstream}

	var text bytes.Buffer
	writePeersTable(&text, now, rows)
	const rowFormat = "%-13s %-11s %-10s %-10s %-9s %-6s %s\n"
	wantText := fmt.Sprintf(rowFormat, "NAME", "ROLE", "STATUS", "LAST SEEN", "CONNECTED", "CURSOR", "LAG") +
		fmt.Sprintf(rowFormat, "alex-macbook", "downstream", "connected", "2h ago", "-", "-", "-") +
		fmt.Sprintf(rowFormat, "vm1.sneat.dev", "upstream", "offline", "-", "-", "-", "reset")
	if text.String() != wantText {
		t.Fatalf("golden text mismatch:\ngot:  %q\nwant: %q", text.String(), wantText)
	}

	var gotJSON bytes.Buffer
	if err := json.NewEncoder(&gotJSON).Encode(peers.ListResponse{SchemaVersion: peers.SchemaVersion, Peers: rows}); err != nil {
		t.Fatal(err)
	}
	wantJSON := `{"schema_version":1,"peers":[` +
		`{"schema_version":1,"id":"machine_1","name":"alex-macbook","role":"downstream","status":"connected","created_at":"0001-01-01T00:00:00Z","last_seen_at":"2026-09-18T10:00:00Z"},` +
		`{"schema_version":1,"id":"vm1.sneat.dev","name":"vm1.sneat.dev","role":"upstream","status":"offline","created_at":"0001-01-01T00:00:00Z","reset_pending":true}` +
		"]}\n"
	if gotJSON.String() != wantJSON {
		t.Fatalf("golden JSON mismatch:\ngot:  %s\nwant: %s", gotJSON.String(), wantJSON)
	}
}

// TestPeersGetGoldenTextAndJSON is writePeerDetail's golden-comparison
// counterpart, for a downstream peer (with a node ID) and the upstream role
// (no node ID, rendered as "-").
func TestPeersGetGoldenTextAndJSON(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	lastSeen := now.Add(-90 * time.Minute)
	downstream := peers.Detail{Record: peers.Record{
		SchemaVersion: peers.SchemaVersion, ID: "machine_1", Name: "alex-macbook", Role: "downstream",
		Status: "connected", NodeID: "abcd1234", LastSeenAt: &lastSeen,
	}}
	wantDownstreamText := "NAME           alex-macbook\n" +
		"ROLE           downstream\n" +
		"STATUS         connected\n" +
		"PEER ID        machine_1\n" +
		"NODE ID        abcd1234\n" +
		"LAST SEEN      1h ago\n" +
		"RESET PENDING  false\n" +
		"SESSION        none\n" +
		"COUNTERS       rx_events=0 tx_events=0 rx_bytes=0 tx_bytes=0\n" +
		"ADMIN          false\n"
	var downstreamText bytes.Buffer
	writePeerDetail(&downstreamText, now, downstream)
	if downstreamText.String() != wantDownstreamText {
		t.Fatalf("golden downstream detail text mismatch:\ngot:  %q\nwant: %q", downstreamText.String(), wantDownstreamText)
	}
	wantDownstreamJSON := `{"schema_version":1,"id":"machine_1","name":"alex-macbook","role":"downstream","status":"connected","node_id":"abcd1234","created_at":"0001-01-01T00:00:00Z","last_seen_at":"2026-09-18T10:30:00Z","session":null,"counters":{"rx_payload_bytes":0,"tx_payload_bytes":0,"rx_messages":0,"tx_messages":0,"rx_events":0,"tx_events":0},"admin_available":false}` + "\n"
	var downstreamJSON bytes.Buffer
	if err := json.NewEncoder(&downstreamJSON).Encode(downstream); err != nil {
		t.Fatal(err)
	}
	if downstreamJSON.String() != wantDownstreamJSON {
		t.Fatalf("golden downstream detail JSON mismatch:\ngot:  %s\nwant: %s", downstreamJSON.String(), wantDownstreamJSON)
	}

	upstream := peers.Detail{Record: peers.Record{
		SchemaVersion: peers.SchemaVersion, ID: "vm1.sneat.dev", Name: "vm1.sneat.dev", Role: "upstream", Status: "offline",
	}}
	wantUpstreamText := "NAME           vm1.sneat.dev\n" +
		"ROLE           upstream\n" +
		"STATUS         offline\n" +
		"PEER ID        vm1.sneat.dev\n" +
		"NODE ID        -\n" +
		"LAST SEEN      -\n" +
		"RESET PENDING  false\n" +
		"SESSION        none\n" +
		"COUNTERS       rx_events=0 tx_events=0 rx_bytes=0 tx_bytes=0\n" +
		"ADMIN          false\n"
	var upstreamText bytes.Buffer
	writePeerDetail(&upstreamText, now, upstream)
	if upstreamText.String() != wantUpstreamText {
		t.Fatalf("golden upstream detail text mismatch:\ngot:  %q\nwant: %q", upstreamText.String(), wantUpstreamText)
	}
}

// TestPeersGetRendersAPeerDetailOrTheUpstream covers wb.peers.get for both a
// downstream peer (read through the local daemon API) and the reserved
// "upstream" name.
func TestPeersGetRendersAPeerDetailOrTheUpstream(t *testing.T) {
	readServer := httptest.NewServer(peers.NewHandler("/api/v1/peers", fixedPeerSource{
		detail: peers.Detail{Record: peers.Record{SchemaVersion: peers.SchemaVersion, ID: "machine_1", Name: "laptop", Role: "downstream", Status: "offline"}}, found: true,
	}, nil))
	t.Cleanup(readServer.Close)
	deps := testPeersDeps(t, nil, readServer)

	var out bytes.Buffer
	if err := runPeersGet(context.Background(), deps, t.TempDir(), "laptop", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "laptop") || !strings.Contains(out.String(), "SESSION") {
		t.Fatalf("get text output = %q", out.String())
	}

	out.Reset()
	if err := runPeersGet(context.Background(), deps, t.TempDir(), "no-such-peer", false, &out); err == nil {
		t.Fatal("expected an error for an unknown peer")
	}

	if err := wbconfig.SetPeersUpstream(deps.configPath(), "https://vm1.sneat.dev", filepath.Join(t.TempDir(), "token")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runPeersGet(context.Background(), deps, t.TempDir(), "upstream", true, &out); err != nil {
		t.Fatal(err)
	}
	var detail peers.Detail
	if err := json.Unmarshal(out.Bytes(), &detail); err != nil || detail.Role != "upstream" {
		t.Fatalf("get upstream JSON output = %q, %v", out.String(), err)
	}
}

func TestPeersGetUpstreamWithoutConfigurationIsAFinding(t *testing.T) {
	deps := testPeersDeps(t, nil, nil)
	var out bytes.Buffer
	if err := runPeersGet(context.Background(), deps, t.TempDir(), "upstream", false, &out); err == nil {
		t.Fatal("expected an error when no upstream is configured")
	}
}

// TestPeersBlockActsLocallyOnTheUpstream covers "On a laptop, block and
// unblock act on its upstream locally" from peer-management-commands.
func TestPeersBlockActsLocallyOnTheUpstream(t *testing.T) {
	deps := testPeersDeps(t, nil, nil)
	if err := wbconfig.SetPeersUpstream(deps.configPath(), "https://vm1.sneat.dev", filepath.Join(t.TempDir(), "token")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	var out bytes.Buffer
	if err := runPeersTrustChange(context.Background(), deps, root, peersRPCPrefix+"block", "Blocked", "upstream", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Blocked upstream vm1.sneat.dev") {
		t.Fatalf("upstream block output = %q", out.String())
	}
	upstream, found, err := resolveUpstreamRow(deps, root)
	if err != nil || !found || upstream.Status != "blocked" {
		t.Fatalf("upstream row after local block = %+v, %t, %v", upstream, found, err)
	}

	out.Reset()
	if err := runPeersTrustChange(context.Background(), deps, root, peersRPCPrefix+"unblock", "Unblocked", "vm1.sneat.dev", false, &out); err != nil {
		t.Fatal(err)
	}
	upstream, found, err = resolveUpstreamRow(deps, root)
	if err != nil || !found || upstream.Status != "offline" {
		t.Fatalf("upstream row after local unblock = %+v, %t, %v", upstream, found, err)
	}
}
