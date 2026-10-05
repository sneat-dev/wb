package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonhost"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
	"github.com/sneat-dev/wb/internal/testenv"

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

func testPeersDeps(t *testing.T, adminServer *peerAdminHostFixture, readServer *httptest.Server) peerCommandFixture {
	t.Helper()
	config := filepath.Join(t.TempDir(), "wb.yaml")
	deps := peersrun.Dependencies{ConfigPath: func() string { return config }, Abs: filepath.Abs, Do: (&http.Client{Timeout: 5 * time.Second}).Do, Admin: func(context.Context, string) (peersrun.AdminOperations, error) {
		t.Fatal("test reached owner RPC without fixture")
		return peersrun.AdminOperations{}, nil
	}, ListenAddress: func(string) (string, error) { return "", os.ErrNotExist }}
	if adminServer != nil {
		deps.ConfigPath = func() string { return adminServer.configPath }
		deps.Admin = func(_ context.Context, _ string) (peersrun.AdminOperations, error) {
			state, found, err := newDaemonController(adminServer.deps, adminServer.root).LoadState()
			if err != nil || !found || state.Status != daemon.StatusReady {
				t.Fatalf("private host lifecycle: %+v, found=%t, err=%v", state, found, err)
			}
			httpClient, err := daemonruntime.LocalHTTPClient(adminServer.root, state.OwnerToken)
			if err != nil {
				return peersrun.AdminOperations{}, err
			}
			return peerAdminOperations(&peerAdminClient{httpClient: httpClient}), nil
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

type fixedPeerSource struct {
	list   []peers.Record
	detail peers.Detail
	found  bool
}

type peerAdminHostFixture struct {
	root, configPath string
	deps             daemonDependencies
}

// newPeerAdminTestServer owns an actual durable hub and native daemon host.
// The command/client/service assertions below use the production owner channel.
func newPeerAdminTestServer(t *testing.T) (*peerAdminHostFixture, hub.PeerTrustStore) {
	t.Helper()
	return newPeerAdminTestServerWithEngine(t, hubconfig.EngineInGitDB)
}

func newPeerAdminTestServerWithEngine(t *testing.T, engine string) (*peerAdminHostFixture, hub.PeerTrustStore) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native Host owner-channel fixture requires supported Unix local transport; Windows is compilation-only")
	}
	started := time.Now()
	root, err := os.MkdirTemp("/tmp", "wb-peer-host-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	storePath := filepath.Join(root, "hub-store")
	if err := os.Mkdir(storePath, 0700); err != nil {
		t.Fatal(err)
	}
	// Keep the real database transaction lock in this private project, even
	// when invoked by a managed Git hook with inherited GIT_DIR/configuration.
	if engine == hubconfig.EngineInGitDB {
		git := exec.CommandContext(t.Context(), "git", "-C", storePath, "init", "-q")
		git.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
		if raw, err := git.CombinedOutput(); err != nil {
			t.Fatalf("private hub Git init: %v: %s", err, raw)
		}
		testenv.ConfigureGitAutoMaintenanceOff(t, storePath)
	}

	backend, closer, err := hubstore.Open(t.Context(), hubconfig.Store{Engine: engine, Path: storePath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	trust, _ := hub.NewPeerStores(backend)
	configPath := filepath.Join(root, "wb.yaml")
	config := fmt.Sprintf("hub:\n  store:\n    engine: %s\n    path: %s\n", engine, storePath)
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	deps := daemonTestDependencies(t, root)
	deps.HubConfigPath = func() string { return configPath }
	deps.Token = func() (string, error) { return "owner-token", nil }
	deps.Alive = daemonruntime.ProcessAlive
	deps.Getpid, deps.Getppid = os.Getpid, os.Getppid
	deps.Now = func() time.Time { return time.Now().UTC() }
	deps.ProcessStartTime = daemon.ProcessStartTime
	deps.Health = defaultDaemonDependencies().Health
	deps.Start = func(string, []string, string) (int, error) {
		t.Error("ready host unexpectedly launched a second daemon")
		return 0, errors.New("unexpected daemon start")
	}
	address := freeLoopbackAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- newDaemonHost(deps).Serve(ctx, daemonhost.Request{ProjectsRoot: root, Listen: address, Quiet: true}, io.Discard, io.Discard)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("private peer host shutdown: %v", err)
			}
		case <-time.After(30 * time.Second):
			t.Error("private peer host did not join")
		}
	})
	// Wait for real private-host health; individual probes must tolerate an
	// instrumented scheduler without replacing the readiness handshake.
	deadline := time.Now().Add(30 * time.Second)
	for {
		probe, cancelProbe := context.WithDeadline(t.Context(), deadline)
		err := deps.Health(probe, address)
		cancelProbe()
		if err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("private peer host stopped before readiness: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("private peer host readiness: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("private durable host setup: %s", time.Since(started))
	return &peerAdminHostFixture{root: root, configPath: configPath, deps: deps}, trust
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
	adminServer, trust := newPeerAdminTestServer(t)
	if err := trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
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
	adminServer, trust := newPeerAdminTestServer(t)
	if err := trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
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
	adminServer, trust := newPeerAdminTestServer(t)
	// The private ingitdb host persists the real invitation through the
	// authenticated owner transport; this journey checks its token presentation.
	if err := trust.CreatePeer(context.Background(), peerRecordFixture("laptop")); err != nil {
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
