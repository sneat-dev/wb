package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/cli/cmdpeers"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/peersrun"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

type peerCommandFixture struct {
	ops        peersrun.Dependencies
	configPath func() string
	now        func() time.Time
}

func testPeersDeps(t *testing.T, adminServer, readServer *httptest.Server) peerCommandFixture {
	t.Helper()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	deps := peersrun.Dependencies{ConfigPath: func() string { return config }, Abs: filepath.Abs, Do: (&http.Client{Timeout: 5 * time.Second}).Do, Admin: func(context.Context, string) (peersrun.AdminOperations, error) {
		t.Fatal("test reached owner RPC without fixture")
		return peersrun.AdminOperations{}, nil
	}, ListenAddress: func(string) (string, error) { return "", os.ErrNotExist }}
	if adminServer != nil {
		if err := os.WriteFile(config, []byte("hub:\n  store:\n    engine: memory\n"), 0600); err != nil {
			t.Fatal(err)
		}
		client := &peerAdminClient{httpClient: fakeDaemonHTTPClient(adminServer.Listener.Addr().String(), "owner-token")}
		deps.Admin = func(context.Context, string) (peersrun.AdminOperations, error) {
			return peerAdminOperations(client), nil
		}
	}
	if readServer != nil {
		deps.ListenAddress = func(string) (string, error) { return strings.TrimPrefix(readServer.URL, "http://"), nil }
	}
	return peerCommandFixture{ops: deps, configPath: deps.ConfigPath, now: func() time.Time { return time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC) }}
}
func executePeerFixture(t *testing.T, ctx context.Context, deps peerCommandFixture, root string, args []string, in io.Reader, out, errOut io.Writer) error {
	inv := testInvocation(t, root)
	service := peersrun.New(deps.ops, peersrun.JoinDependencies{})
	command := cmdpeers.New(newCLIRuntime(inv), cmdpeers.Dependencies{Invite: service.Invite, List: service.List, Get: service.Get, TrustChange: service.TrustChange, Disconnect: service.Disconnect, Now: deps.now, SetDiscoveryTerms: setDiscoveryTerms})
	command.SetContext(ctx)
	command.SetArgs(args)
	command.SetIn(in)
	command.SetOut(out)
	command.SetErr(errOut)
	command.SilenceUsage = true
	command.SilenceErrors = true
	return command.Execute()
}
func fakeDaemonHTTPClient(serverAddr, token string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", serverAddr)
		},
	}
	return &http.Client{Transport: daemonruntime.WithOwnerToken(token, transport)}
}

type fixedPeerSource struct {
	list   []peers.Record
	detail peers.Detail
	found  bool
}

func newPeerAdminTestServer(t *testing.T) (*httptest.Server, *hubMount) {
	t.Helper()
	mount, _ := peerAdminTestMount(t)
	server := httptest.NewServer(daemonruntime.AuthenticatedHandler("owner-token", newPeerAdminHTTPHandler(mount)))
	t.Cleanup(server.Close)
	return server, mount
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

func TestPeersInviteTokenFileWriteFailureStillReportsTheToken(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the read-only directory permission this test relies on")
	}
	adminServer, mount := newPeerAdminTestServer(t)
	mount.PeerAdmin.MemoryEngine = false
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
		t.Fatal(err)
	}
	deps := testPeersDeps(t, adminServer, nil)

	readOnlyDir := t.TempDir()
	if err := os.Chmod(readOnlyDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnlyDir, 0o700) })
	tokenFile := filepath.Join(readOnlyDir, "token.txt")

	var out bytes.Buffer
	err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"invite", "laptop", "--rotate=" + strconv.FormatBool(true), "--token-file=" + tokenFile, "--json=" + strconv.FormatBool(false)}, nil, &out, &out)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitFindings {
		t.Fatalf("error = %v, want an exitFindings *exitError", err)
	}
	if !strings.Contains(out.String(), "could not write token file") || !strings.Contains(out.String(), "Token (copy this now") {
		t.Fatalf("text output = %q, want a write-failure warning and the rescued token", out.String())
	}

	out.Reset()
	err = executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"invite", "laptop", "--rotate=" + strconv.FormatBool(true), "--token-file=" + tokenFile + ".json", "--json=" + strconv.FormatBool(true)}, nil, &out, &out)
	if !errors.As(err, &exit) || exit.code != exitFindings {
		t.Fatalf("json-mode error = %v, want an exitFindings *exitError", err)
	}
	var jsonResult peersrun.InviteOutput
	if jsonErr := json.Unmarshal(out.Bytes(), &jsonResult); jsonErr != nil {
		t.Fatalf("json-mode output is not valid JSON: %q, %v", out.String(), jsonErr)
	}
	if jsonResult.Token == "" || jsonResult.TokenFile != "" {
		t.Fatalf("json-mode result = %+v, want the rescued token and no token file", jsonResult)
	}
}
func TestPeersBlockUnblockDisconnectCallTheOwnerPath(t *testing.T) {
	adminServer, mount := newPeerAdminTestServer(t)
	if err := mount.PeerAdmin.Trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
		t.Fatal(err)
	}
	deps := testPeersDeps(t, adminServer, nil)

	var out bytes.Buffer
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"block", "laptop", "--json=" + strconv.FormatBool(false)}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Blocked laptop") || !strings.Contains(out.String(), "trust=blocked") {
		t.Fatalf("block output = %q", out.String())
	}

	out.Reset()
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"unblock", "laptop", "--json=" + strconv.FormatBool(false)}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Unblocked laptop") || !strings.Contains(out.String(), "trust=active") {
		t.Fatalf("unblock output = %q", out.String())
	}

	out.Reset()
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"disconnect", "laptop", "--json=" + strconv.FormatBool(false)}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no live session") {
		t.Fatalf("disconnect output = %q, want it to name Task 2's not-yet-implemented session", out.String())
	}
}
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
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"invite", "laptop", "--rotate=" + strconv.FormatBool(true), "--token-file=" + "", "--json=" + strconv.FormatBool(false)}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Rotated laptop") || !strings.Contains(out.String(), "Token (copy this now") {
		t.Fatalf("invite text output = %q", out.String())
	}

	tokenFile := filepath.Join(t.TempDir(), "token.txt")
	out.Reset()
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"invite", "laptop", "--rotate=" + strconv.FormatBool(true), "--token-file=" + tokenFile, "--json=" + strconv.FormatBool(false)}, nil, &out, &out); err != nil {
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
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"invite", "laptop", "--rotate=" + strconv.FormatBool(true), "--token-file=" + "", "--json=" + strconv.FormatBool(true)}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	var jsonResult peersrun.InviteOutput
	if err := json.Unmarshal(out.Bytes(), &jsonResult); err != nil || jsonResult.Token == "" || jsonResult.PeerID == "" {
		t.Fatalf("invite JSON output = %q, %v", out.String(), err)
	}
}

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
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"list", "--json=" + strconv.FormatBool(false)}, nil, &out, &errOut); err != nil {
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
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"list", "--json=" + strconv.FormatBool(true)}, nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var jsonResult peers.ListResponse
	if err := json.Unmarshal(out.Bytes(), &jsonResult); err != nil || len(jsonResult.Peers) != 2 {
		t.Fatalf("list JSON output = %q, %v", out.String(), err)
	}
}
func TestPeersGetRendersAPeerDetailOrTheUpstream(t *testing.T) {
	readServer := httptest.NewServer(peers.NewHandler("/api/v1/peers", fixedPeerSource{
		detail: peers.Detail{Record: peers.Record{SchemaVersion: peers.SchemaVersion, ID: "machine_1", Name: "laptop", Role: "downstream", Status: "offline"}}, found: true,
	}, nil))
	t.Cleanup(readServer.Close)
	deps := testPeersDeps(t, nil, readServer)

	var out bytes.Buffer
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"get", "laptop", "--json=" + strconv.FormatBool(false)}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "laptop") || !strings.Contains(out.String(), "SESSION") {
		t.Fatalf("get text output = %q", out.String())
	}

	out.Reset()
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"get", "no-such-peer", "--json=" + strconv.FormatBool(false)}, nil, &out, &out); err == nil {
		t.Fatal("expected an error for an unknown peer")
	}

	if err := wbconfig.SetPeersUpstream(deps.configPath(), "https://vm1.sneat.dev", filepath.Join(t.TempDir(), "token")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := executePeerFixture(t, context.Background(), deps, t.TempDir(), []string{"get", "upstream", "--json=" + strconv.FormatBool(true)}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	var detail peers.Detail
	if err := json.Unmarshal(out.Bytes(), &detail); err != nil || detail.Role != "upstream" {
		t.Fatalf("get upstream JSON output = %q, %v", out.String(), err)
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
