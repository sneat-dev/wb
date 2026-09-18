package main

import (
	"bytes"
	"context"
	"encoding/json"
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
	if err := runPeersInvite(context.Background(), deps, "", "laptop", true, "", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Rotated laptop") || !strings.Contains(out.String(), "Token (copy this now") {
		t.Fatalf("invite text output = %q", out.String())
	}

	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	out.Reset()
	if err := runPeersInvite(context.Background(), deps, "", "laptop", true, tokenFile, false, &out); err != nil {
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
	if err := runPeersInvite(context.Background(), deps, "", "laptop", true, "", true, &out); err != nil {
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
	if err := runPeersTrustChange(context.Background(), deps, "", peersRPCPrefix+"block", "Blocked", "laptop", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Blocked laptop") || !strings.Contains(out.String(), "trust=blocked") {
		t.Fatalf("block output = %q", out.String())
	}

	out.Reset()
	if err := runPeersTrustChange(context.Background(), deps, "", peersRPCPrefix+"unblock", "Unblocked", "laptop", false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Unblocked laptop") || !strings.Contains(out.String(), "trust=active") {
		t.Fatalf("unblock output = %q", out.String())
	}

	out.Reset()
	if err := runPeersDisconnect(context.Background(), deps, "", "laptop", false, &out); err != nil {
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
	}))
	t.Cleanup(readServer.Close)
	deps := testPeersDeps(t, nil, readServer)
	if err := wbconfig.SetPeersUpstream(deps.configPath(), "https://vm1.sneat.dev", filepath.Join(t.TempDir(), "token")); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runPeersList(context.Background(), deps, t.TempDir(), false, &out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "laptop") || !strings.Contains(text, "downstream") || !strings.Contains(text, "vm1.sneat.dev") || !strings.Contains(text, "upstream") {
		t.Fatalf("list text output = %q", text)
	}

	out.Reset()
	if err := runPeersList(context.Background(), deps, t.TempDir(), true, &out); err != nil {
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
	var out bytes.Buffer
	if err := runPeersList(context.Background(), deps, t.TempDir(), true, &out); err != nil {
		t.Fatal(err)
	}
	var jsonResult peers.ListResponse
	if err := json.Unmarshal(out.Bytes(), &jsonResult); err != nil || len(jsonResult.Peers) != 0 {
		t.Fatalf("empty list output = %q, %v", out.String(), err)
	}
}

// TestPeersGetRendersAPeerDetailOrTheUpstream covers wb.peers.get for both a
// downstream peer (read through the local daemon API) and the reserved
// "upstream" name.
func TestPeersGetRendersAPeerDetailOrTheUpstream(t *testing.T) {
	readServer := httptest.NewServer(peers.NewHandler("/api/v1/peers", fixedPeerSource{
		detail: peers.Detail{Record: peers.Record{SchemaVersion: peers.SchemaVersion, ID: "machine_1", Name: "laptop", Role: "downstream", Status: "offline"}}, found: true,
	}))
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
