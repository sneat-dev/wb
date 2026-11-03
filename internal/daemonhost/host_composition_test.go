package daemonhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	daemonv1 "github.com/sneat-dev/wb/internal/gen/wb/daemon/v1"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
	"github.com/sneat-dev/wb/internal/repositoryevents"
	"github.com/sneat-dev/wb/internal/worktrees"
)

type deliveryStatusFixture struct{ delivery *hub.StatusDelivery }

func (fixture deliveryStatusFixture) IdentityRepositoryEventStatus(_ context.Context, identity string) (*hub.StatusDelivery, []hub.PendingRefresh, []hub.StatusError, error) {
	if identity != localIdentityID {
		return nil, nil, nil, errors.New("delivery status requested for another identity")
	}
	return fixture.delivery, nil, nil, nil
}

func TestHubHealthAndPeerReadsExposeTheirConfiguredStores(t *testing.T) {
	t.Parallel()
	mount, err := mountHub(t.Context(), memoryHubConfig(t), "127.0.0.1:0", narrate.Writer{}, nil)
	if err != nil || mount == nil {
		t.Fatalf("mount=%v error=%v", mount, err)
	}
	t.Cleanup(func() { _ = mount.Close() })
	at := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	mount.status.Events = deliveryStatusFixture{delivery: &hub.StatusDelivery{
		LastReceived:     &hub.StatusDeliveryMarker{DeliveryID: "received", Event: "push", OccurredAt: at},
		LastAcknowledged: &hub.StatusDeliveryMarker{DeliveryID: "acknowledged", Event: "repository", OccurredAt: at.Add(-time.Minute)},
	}}
	health := mount.hubHealth()(t.Context())
	if !health.Mounted || health.LastEventReceived == nil || health.LastEventAcknowledged == nil {
		t.Fatalf("health omitted stored delivery markers: %+v", health)
	}
	if health.LastEventReceived.ID != "received" || health.LastEventReceived.Event != "push" || !health.LastEventReceived.OccurredAt.Equal(at) ||
		health.LastEventAcknowledged.ID != "acknowledged" || health.LastEventAcknowledged.Event != "repository" || !health.LastEventAcknowledged.OccurredAt.Equal(at.Add(-time.Minute)) {
		t.Fatalf("health changed stored delivery markers: %+v %+v", health.LastEventReceived, health.LastEventAcknowledged)
	}
	record := peerRecordFixture("composition-peer")
	if err := mount.PeerAdmin.Trust.CreatePeer(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	source := mount.peersSource()
	if source == nil {
		t.Fatal("configured hub omitted its peer read source")
	}
	peers, err := source.ListPeers(t.Context())
	if err != nil || len(peers) != 1 || peers[0].Name != record.Name {
		t.Fatalf("peer read source=%+v error=%v", peers, err)
	}
}

func TestPeerAdminInviteReturnsThePersistedCredential(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	gitContext, cancelGit := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancelGit()
	command := exec.CommandContext(gitContext, "git", "-C", root, "init", "--quiet")
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize private persistent store: %v: %s", err, output)
	}
	store, closer, err := hubstore.Open(t.Context(), hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	trust, stats := hub.NewPeerStores(store)
	pepper := bytes.Repeat([]byte{'p'}, 32)
	at := time.Date(2026, 11, 3, 1, 0, 0, 0, time.UTC)
	admin := &hub.PeerAdminService{Backend: store, Trust: trust, Stats: stats, Pepper: pepper, HubMachineName: "composition-hub", Now: func() time.Time { return at }}
	handler := daemonruntime.AuthenticatedHandler("owner-token", newPeerAdminHTTPHandler(&hubMount{PeerAdmin: admin}))
	response := peerAdminRequest(t, handler, "owner-token", peersRPCPrefix+"invite", peerInviteRequest{Name: "composition-peer"})
	var invite peerInviteResponse
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &invite) != nil || invite.Token == "" || invite.Name != "composition-peer" || invite.Rotated || !invite.CreatedAt.Equal(at) {
		t.Fatalf("invite response status=%d body=%s", response.Code, response.Body.String())
	}
	reopened, reopenedCloser, err := hubstore.Open(t.Context(), hubconfig.Store{Engine: hubconfig.EngineInGitDB, Path: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopenedCloser.Close() })
	reopenedTrust, reopenedStats := hub.NewPeerStores(reopened)
	peer, found, err := reopenedTrust.GetPeer(t.Context(), invite.PeerID)
	if err != nil || !found || peer.Name != invite.Name || peer.Trust != hub.PeerTrustActive || !peer.CreatedAt.Equal(invite.CreatedAt) {
		t.Fatalf("persisted invite peer=%+v found=%v error=%v", peer, found, err)
	}
	if _, found, err := reopenedStats.GetStats(t.Context(), invite.PeerID); err != nil || !found {
		t.Fatalf("persisted invite statistics found=%v error=%v", found, err)
	}
	digest, err := hub.DigestMachineToken(invite.Token, pepper)
	if err != nil {
		t.Fatal(err)
	}
	_, resolver, _ := hub.NewMachineStores(reopened)
	binding, err := resolver.ResolveMachineCredential(t.Context(), digest)
	if err != nil || binding.MachineID != invite.PeerID || binding.MachineName != invite.Name {
		t.Fatalf("returned token does not resolve to persisted invite: %+v error=%v", binding, err)
	}
}

func TestServeRefusesAnEndpointThatIsAlreadyHeld(t *testing.T) {
	t.Parallel()
	h, request, store := nativeServeFixture(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	request.Listen = held.Addr().String()
	err = h.Serve(t.Context(), request, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "already held by another process") || !strings.Contains(err.Error(), request.Listen) {
		t.Fatalf("held endpoint refusal=%v", err)
	}
	if _, found, err := store.Load(); err != nil || found {
		t.Fatalf("refused endpoint wrote lifecycle state: found=%v error=%v", found, err)
	}
}

type observedAddressListener struct {
	net.Listener
	bound net.Addr
}

func (listener observedAddressListener) Addr() net.Addr { return listener.bound }

func TestServeEarlyPreparationFailuresReleaseTheDashboardListener(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"provenance", "resolved non-loopback address"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			h, request, store := nativeServeFixture(t)
			sentinel := errors.New("private executable observation failed")
			if stage == "provenance" {
				h.deps.Runtime.Executable = func() (string, error) { return "", sentinel }
			}
			var listener net.Listener
			h.deps.Listen = func(network, address string) (net.Listener, error) {
				var err error
				listener, err = net.Listen(network, address)
				if err == nil && stage == "resolved non-loopback address" {
					// Only the observed address is simulated; cleanup still owns
					// the real loopback listener created above.
					return observedAddressListener{Listener: listener, bound: &net.TCPAddr{IP: net.ParseIP("192.0.2.10"), Port: 8799}}, nil
				}
				return listener, err
			}
			err := h.servePrepared(request.ProjectsRoot, t.Context(), io.Discard, io.Discard, h.runtimeDependencies(), request.Listen, store, "private-stage-owner", false, false)
			if (stage == "provenance" && !errors.Is(err, sentinel)) || (stage == "resolved non-loopback address" && (err == nil || !strings.Contains(err.Error(), "192.0.2.10:8799, which is not a loopback address"))) {
				t.Fatalf("%s refusal=%v", stage, err)
			}
			if listener == nil {
				t.Fatal("dashboard listener did not bind before the refusal")
			}
			rebound, err := net.Listen("tcp", listener.Addr().String())
			if err != nil {
				t.Fatalf("preparation refusal retained dashboard endpoint: %v", err)
			}
			_ = rebound.Close()
			if _, found, err := store.Load(); err != nil || found {
				t.Fatalf("failed preparation wrote state: found=%v error=%v", found, err)
			}
		})
	}
}

type acceptingHostListener struct {
	net.Listener
	ready     chan struct{}
	once      sync.Once
	closeMu   sync.Mutex
	closes    int
	completed chan error
}

func (listener *acceptingHostListener) Accept() (net.Conn, error) {
	listener.once.Do(func() { close(listener.ready) })
	return listener.Listener.Accept()
}

func (listener *acceptingHostListener) Close() error {
	listener.closeMu.Lock()
	listener.closes++
	listener.closeMu.Unlock()
	err := listener.Listener.Close()
	select {
	case listener.completed <- err:
	default:
	}
	return err
}

// Only this fixture uses this pool. Ports outside the kernel's automatic range
// cannot be claimed by another test's :0/connect between Close and rebind.
func listenCompositionDashboard(network, address string) (net.Listener, error) {
	if runtime.GOOS != "linux" {
		return net.Listen(network, address)
	}
	rangeText, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return nil, err
	}
	startText, err := os.ReadFile("/proc/sys/net/ipv4/ip_unprivileged_port_start")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(rangeText))
	if len(rangeText) > 128 || len(startText) > 128 || len(fields) != 2 {
		return nil, errors.New("invalid Linux automatic port bounds")
	}
	first, firstErr := strconv.Atoi(fields[0])
	last, lastErr := strconv.Atoi(fields[1])
	unprivileged, startErr := strconv.Atoi(strings.TrimSpace(string(startText)))
	if firstErr != nil || lastErr != nil || startErr != nil || first < 1 || last < first || last > 65535 || unprivileged < 0 || unprivileged > 65535 {
		return nil, errors.New("invalid Linux automatic port bounds")
	}
	if network != "tcp" || address != "127.0.0.1:0" {
		return nil, fmt.Errorf("unexpected dashboard fixture address: %s %s", network, address)
	}
	attempts := 0
	for port := max(1024, unprivileged); port <= 65535 && attempts < 32; port++ {
		if port >= first && port <= last {
			port = last
			continue
		}
		attempts++
		listener, err := net.Listen(network, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			return listener, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("no dashboard fixture port outside automatic range after %d candidates", attempts)
}

func TestServeKeepsRawCommandsDeniedWhenTheRepositoryReceiverIsDisabled(t *testing.T) {
	t.Parallel()
	h, request, store := nativeServeFixture(t)
	config := h.deps.Runtime.HubConfigPath()
	write(t, config, "hub:\n  store:\n    engine: memory\n    path: "+t.TempDir()+"\n")
	invalidRemote := filepath.Join(t.TempDir(), "invalid-remote.yaml")
	write(t, invalidRemote, "remote:\n  provider: invalid\n  machine: composition-machine\n")
	configReads := 0
	h.deps.Runtime.HubConfigPath = func() string {
		configReads++
		if configReads == 4 {
			return invalidRemote
		}
		return config
	}
	policy := filepath.Join(t.TempDir(), "raw-policy.json")
	write(t, policy, `{"version":1,"allow_raw_daemon_execution":false}`)
	h.effects.rawPolicyPath = func() (string, error) { return policy, nil }
	ready := make(chan struct{})
	var listener net.Listener
	var observed *acceptingHostListener
	h.deps.Listen = func(network, address string) (net.Listener, error) {
		var err error
		listener, err = listenCompositionDashboard(network, address)
		if err != nil {
			return nil, err
		}
		observed = &acceptingHostListener{Listener: listener, ready: ready, completed: make(chan error, 1)}
		return observed, nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	var diagnostics bytes.Buffer
	go func() { done <- h.Serve(ctx, request, io.Discard, &diagnostics) }()
	exited := false
	stop := sync.OnceFunc(func() {
		cancel()
		select {
		case err := <-done:
			exited = true
			if err != nil {
				t.Errorf("daemon shutdown=%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("daemon did not stop after cancellation")
		}
	})
	t.Cleanup(stop)
	waitListenerEvent(t, ready, "dashboard server began accepting")
	client, err := daemonruntime.LocalHTTPClient(request.ProjectsRoot, "private-stage-owner")
	if err != nil {
		t.Fatal(err)
	}
	client.Timeout = 5 * time.Second
	t.Cleanup(client.CloseIdleConnections)
	rpc := daemonv1connect.NewDaemonServiceClient(client, daemonruntime.RPCBaseURL)
	marker := filepath.Join(request.ProjectsRoot, "must-not-run")
	_, err = rpc.SubmitOperation(t.Context(), connect.NewRequest(&daemonv1.SubmitOperationRequest{
		WorkingDirectory: request.ProjectsRoot,
		Argv:             []string{"/bin/sh", "-c", "touch must-not-run"},
		LocalRawCommand:  true,
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "raw daemon execution is disabled") {
		t.Fatalf("raw command admission=%v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused raw command created a file: %v", err)
	}
	stop()
	if !exited {
		t.Fatal("cannot inspect daemon outputs before it has stopped")
	}
	observed.closeMu.Lock()
	closeCalls := observed.closes
	observed.closeMu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("underlying dashboard Close calls=%d, want 1", closeCalls)
	}
	select {
	case err := <-observed.completed:
		if err != nil {
			t.Fatalf("underlying dashboard Close=%v", err)
		}
	default:
		t.Fatal("Serve returned before underlying dashboard Close completed")
	}
	if configReads != 4 || !strings.Contains(diagnostics.String(), "repository event receiver disabled:") || !strings.Contains(diagnostics.String(), "WB hub: engine=memory") {
		t.Fatalf("hub narration or receiver diagnostic missing: config reads=%d diagnostics=%s", configReads, diagnostics.String())
	}
	state, found, err := store.Load()
	if err != nil || !found || state.Status != daemon.StatusStopped {
		t.Fatalf("stopped lifecycle state=%+v found=%v error=%v", state, found, err)
	}
	rebound, err := net.Listen("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("daemon shutdown retained its dashboard endpoint: %v", err)
	}
	_ = rebound.Close()
}

type repositoryProgressWriter struct {
	cancel context.CancelFunc
	lines  chan string
}

func (writer repositoryProgressWriter) Write(data []byte) (int, error) {
	writer.cancel()
	select {
	case writer.lines <- string(data):
	default:
	}
	return len(data), nil
}

func TestRepositoryReceiverReportsAQueuedLocalFailure(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	canonical, err := worktrees.CanonicalRepositoryPath(root, "acme/composition")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, canonical, "not a repository directory")
	queue, err := repositoryevents.NewQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	event := repositoryevent.Event{Version: repositoryevent.ContractVersion, ID: "composition-event", Repository: "github.com/acme/composition", Ref: "refs/heads/main", Reason: repositoryevent.ReasonDefaultBranchUpdated}
	if _, err := queue.Enqueue(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "wb.yaml")
	write(t, config, "{}\n")
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	lines := make(chan string, 1)
	if err := startRepositoryEventReceiver(ctx, root, config, repositoryProgressWriter{cancel: cancel, lines: lines}); err != nil {
		t.Fatal(err)
	}
	select {
	case message := <-lines:
		if message != "repository event composition-event: retry pending\n" {
			t.Fatalf("repository progress=%q", message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("repository receiver did not report the queued failure")
	}
	jobs := filepath.Join(root, ".wb", "runtime", "daemon", "repository-events", "jobs")
	entries, err := os.ReadDir(jobs)
	if err != nil || len(entries) != 1 {
		t.Fatalf("retained event jobs=%v error=%v", entries, err)
	}
	raw, err := os.ReadFile(filepath.Join(jobs, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	var job struct {
		State    string `json:"state"`
		Attempts int    `json:"attempts"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(raw, &job); err != nil || job.State != "queued" || job.Attempts != 1 || job.Error != "canonical repository path is not a directory" {
		t.Fatalf("durable failed event=%+v error=%v", job, err)
	}
}
