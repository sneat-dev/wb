package daemonhost

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	cockpitfleet "github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/nodeidentity"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// Each native stage owns a short private root; no platform supervisor is started.
func nativeServeFixture(t *testing.T) (*Host, Request, daemon.Store) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("native local owner-channel listener is unavailable on Windows")
	}
	root, err := os.MkdirTemp("/tmp", "wb-host-stage-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	config := filepath.Join(root, "wb.yaml")
	write(t, config, "{}\n")
	deps := daemonruntime.DefaultDependencies(usageError)
	deps.HubConfigPath = func() string { return config }
	deps.Token = func() (string, error) { return "private-stage-owner", nil }
	h := New(Dependencies{Runtime: deps, FleetOptions: func(root, home, _ string, _ wbconfig.CockpitConfig, _ io.Writer, _ func() (string, error)) cockpitfleet.Options {
		return cockpitfleet.Options{Machine: "private-stage", ProjectsRoot: root, Collectors: (cockpitfleet.LocalCollectors{ProjectsRoot: root, Home: home}).Collectors(nil)}
	}})
	statePath, err := daemonruntime.StatePath(root)
	if err != nil {
		t.Fatal(err)
	}
	return h, Request{ProjectsRoot: root, Listen: "127.0.0.1:0"}, daemon.Store{Path: statePath}
}

func seedManagedHost(t *testing.T, h *Host, r *Request, store daemon.Store) {
	t.Helper()
	provenance, err := daemonruntime.NewController(h.deps.Runtime, r.ProjectsRoot).Provenance()
	if err != nil {
		t.Fatal(err)
	}
	location, err := daemonruntime.ResolveLocation(r.ProjectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	state := daemon.NewStartingAt(nil, r.Listen, provenance, "private-stage-owner", location.Home, location.StatePath, h.deps.Runtime.Now())
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	r.ManagedStart = true
}

func replaceStateLockWithDirectory(t *testing.T, root string) {
	t.Helper()
	dir, err := daemon.RuntimeDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "daemon.state.lock")
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err = os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestServeSelectedPrivateStateFailuresDelegateAllOtherWrites(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("selected private storage observation")
	for _, stage := range []string{"load preparation", "load locked", "save starting", "managed load current"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			h, r, store := nativeServeFixture(t)
			failLoad := 0
			if stage == "load preparation" {
				failLoad = 1
			}
			if stage == "load locked" {
				failLoad = 2
			}
			if stage == "managed load current" {
				failLoad = 3
				seedManagedHost(t, h, &r, store)
			}
			loads, saves := 0, 0
			h.effects.loadState = func(s daemon.Store) (daemon.State, bool, error) {
				loads++
				if s.Path != store.Path {
					t.Errorf("state path %q != %q", s.Path, store.Path)
				}
				if loads == failLoad {
					return daemon.State{}, false, sentinel
				}
				return s.Load()
			}
			h.effects.saveState = func(s daemon.Store, state daemon.State) error {
				saves++
				if stage == "save starting" {
					return sentinel
				}
				return s.Save(state)
			}
			var out bytes.Buffer
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := h.Serve(ctx, r, &out, io.Discard)
			if !errors.Is(err, sentinel) || out.Len() != 0 {
				t.Fatalf("Serve=%v output=%q", err, out.String())
			}
			if stage == "managed load current" {
				state, ok, err := store.Load()
				if err != nil || !ok || state.PID != os.Getpid() || state.Status != daemon.StatusStarting {
					t.Fatalf("actual starting receipt=%+v,%v,%v", state, ok, err)
				}
			} else if _, ok, err := store.Load(); err != nil || ok {
				t.Fatalf("unexpected persisted receipt found=%v err=%v", ok, err)
			}
			if stage == "load preparation" && saves != 0 {
				t.Fatal("saved after initial read failure")
			}
			if stage != "load preparation" {
				release, err := daemonruntime.NewController(h.deps.Runtime, r.ProjectsRoot).AcquireStateLock()
				if err != nil {
					t.Fatalf("state lock not released: %v", err)
				}
				release()
			}
		})
	}
}

func TestServeManagedOwnershipAndRealLockRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"missing managed state", "superseded first lock", "first lock path", "second lock path", "superseded current"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			h, r, store := nativeServeFixture(t)
			r.ManagedStart = true
			if stage != "missing managed state" {
				seedManagedHost(t, h, &r, store)
			}
			loads := 0
			h.effects.loadState = func(s daemon.Store) (daemon.State, bool, error) {
				loads++
				if stage == "first lock path" && loads == 1 {
					replaceStateLockWithDirectory(t, r.ProjectsRoot)
				}
				if (stage == "superseded first lock" && loads == 2) || (stage == "superseded current" && loads == 3) {
					state, _, err := s.Load()
					if err != nil {
						return daemon.State{}, false, err
					}
					state.OwnerToken = "replacement-owner"
					if err = s.Save(state); err != nil {
						return daemon.State{}, false, err
					}
				}
				return s.Load()
			}
			if stage == "second lock path" {
				h.effects.operationsDir = func(root string) (string, error) {
					dir, err := daemon.OperationsDir(root)
					if err == nil {
						replaceStateLockWithDirectory(t, root)
					}
					return dir, err
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := h.Serve(ctx, r, io.Discard, io.Discard)
			want := "superseded"
			if stage == "missing managed state" {
				want = "no longer owns"
			}
			if strings.Contains(stage, "lock path") {
				want = "state lock"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Serve=%v want %q", err, want)
			}
			if strings.HasPrefix(stage, "superseded") {
				state, found, err := store.Load()
				if err != nil || !found || state.OwnerToken != "replacement-owner" {
					t.Fatalf("replacement receipt overwritten: %+v,%v,%v", state, found, err)
				}
			}
		})
	}
}

func TestServePrivateResolverFailuresCloseOwnedListeners(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("simulated OS resolver observation")
	for _, stage := range []string{"raw policy", "operations"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			h, r, store := nativeServeFixture(t)
			if stage == "raw policy" {
				h.effects.rawPolicyPath = func() (string, error) { return "", sentinel }
			} else {
				h.effects.operationsDir = func(string) (string, error) { return "", sentinel }
			}
			var listener net.Listener
			h.deps.Listen = func(network, address string) (net.Listener, error) {
				var err error
				listener, err = net.Listen(network, address)
				return listener, err
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := h.Serve(ctx, r, io.Discard, io.Discard)
			if !errors.Is(err, sentinel) {
				t.Fatalf("Serve=%v", err)
			}
			if listener == nil {
				t.Fatal("native TCP listener never bound")
			}
			if _, err := listener.Accept(); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("listener not closed: %v", err)
			}
			state, ok, err := store.Load()
			if err != nil || !ok || state.PID != os.Getpid() {
				t.Fatalf("actual prior lifecycle write missing: %+v %v %v", state, ok, err)
			}
			socket, err := daemon.SocketPath(r.ProjectsRoot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = os.Lstat(socket); !os.IsNotExist(err) {
				t.Fatalf("local listener path survives: %v", err)
			}
		})
	}
}

func TestServeRealPrivateLatePreparationRefusals(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"local path", "queue record", "cockpit config", "hub config", "bridge directory", "stdout"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			h, r, store := nativeServeFixture(t)
			config := h.deps.Runtime.HubConfigPath()
			want := ""
			switch stage {
			case "local path":
				want = "non-socket"
				h.effects.saveState = func(s daemon.Store, state daemon.State) error {
					if err := s.Save(state); err != nil {
						return err
					}
					socket, err := daemon.SocketPath(r.ProjectsRoot)
					if err != nil {
						return err
					}
					return os.WriteFile(socket, []byte("owned non-socket fixture"), 0o600)
				}
			case "queue record":
				want = "load durable daemon queue"
				dir, err := daemon.OperationsDir(r.ProjectsRoot)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(dir, "invalid.json"), "{")
			case "cockpit config":
				want = "load the cockpit configuration"
				write(t, config, "cockpit: [broken]\n")
			case "hub config":
				want = "mount the bench hub"
				write(t, config, "hub: [broken]\n")
			case "bridge directory":
				want = "prepare daemon file bridge"
				runtimeDir, err := daemon.RuntimeDir(r.ProjectsRoot)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.MkdirAll(runtimeDir, 0o700); err != nil {
					t.Fatal(err)
				}
				write(t, filepath.Join(runtimeDir, "file-bridge"), "blocked")
			case "stdout":
				want = "selected stdout write refusal"
			}
			out := io.Writer(io.Discard)
			sentinel := errors.New(want)
			if stage == "stdout" {
				out = serveFailWriter{sentinel}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := h.Serve(ctx, r, out, io.Discard)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Serve=%v want %q", err, want)
			}
			if stage == "stdout" && !errors.Is(err, sentinel) {
				t.Fatal("stdout error identity changed")
			}
			state, ok, err := store.Load()
			if err != nil || !ok {
				t.Fatalf("prior durable state missing: %+v %v %v", state, ok, err)
			}
		})
	}
}

type serveFailWriter struct{ err error }

func (w serveFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestServeNativeManagedAndUnmanagedShutdownUsesActualState(t *testing.T) {
	t.Parallel()
	for _, managed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmanaged", true: "managed"}[managed], func(t *testing.T) {
			t.Parallel()
			h, r, store := nativeServeFixture(t)
			if managed {
				seedManagedHost(t, h, &r, store)
			}
			// Nil optional observer callbacks retain the genuine process defaults.
			h.deps.Runtime.Getpid = nil
			h.deps.Runtime.Getppid = nil
			h.deps.Runtime.ObservedCgroupUnit = nil
			location, err := daemonruntime.ResolveLocation(r.ProjectsRoot)
			if err != nil {
				t.Fatal(err)
			}
			path := nodeidentity.PathFromHome(location.Home)
			if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			write(t, path, "not a node identity")
			write(t, h.deps.Runtime.HubConfigPath(), "remote: [broken]\n")
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			var out, errOut bytes.Buffer
			writer := &serveCancelWriter{out: &out, cancel: cancel}
			if err = h.Serve(ctx, r, writer, &errOut); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "WB dashboard: http://127.0.0.1:") || !strings.Contains(errOut.String(), "node identity unavailable") || !strings.Contains(errOut.String(), "repository event receiver disabled") {
				t.Fatalf("output=%q diagnostics=%q", out.String(), errOut.String())
			}
			state, ok, err := store.Load()
			if err != nil || !ok || state.Status != daemon.StatusStopped || state.OwnerToken != "private-stage-owner" {
				t.Fatalf("native shutdown receipt=%+v %v %v", state, ok, err)
			}
		})
	}
}

type serveCancelWriter struct {
	out    io.Writer
	cancel context.CancelFunc
}

func (w *serveCancelWriter) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	w.cancel()
	return n, err
}

func TestServePinnedStateStillRefusesAnUnresolvableProjectsRoot(t *testing.T) {
	t.Parallel()
	h, r, store := nativeServeFixture(t)
	seedManagedHost(t, h, &r, store)
	r.LifecycleState = store.Path
	blocked := filepath.Join(r.ProjectsRoot, "blocked")
	write(t, blocked, "private file")
	r.ProjectsRoot = filepath.Join(blocked, "child")
	var out bytes.Buffer
	if err := h.Serve(t.Context(), r, &out, io.Discard); err == nil || out.Len() != 0 {
		t.Fatalf("Serve=%v output=%q", err, out.String())
	}
	state, ok, err := store.Load()
	if err != nil || !ok || state.Status != daemon.StatusStarting || state.PID != 0 {
		t.Fatalf("pinned receipt changed: %+v %v %v", state, ok, err)
	}
}

func TestServeRuntimeGuardReturnsActualRemovedLifecycleError(t *testing.T) {
	t.Parallel()
	h, r, store := nativeServeFixture(t)
	ticks := make(chan time.Time, 1)
	stopped := make(chan struct{})
	h.deps.Runtime.GuardTicker = func(interval time.Duration) (<-chan time.Time, func()) {
		if interval != 10*time.Second {
			t.Errorf("actual heartbeat interval=%s", interval)
		}
		return ticks, func() { close(stopped) }
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var diagnostics bytes.Buffer
	writer := serveRemoveStateWriter{t: t, path: store.Path, ticks: ticks}
	err := h.Serve(ctx, r, writer, &diagnostics)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "lifecycle state") || !strings.Contains(diagnostics.String(), "daemon stopping:") {
		t.Fatalf("Serve=%v diagnostic=%q", err, diagnostics.String())
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("runtime guard timer not stopped")
	}
	state, found, loadErr := store.Load()
	if loadErr != nil || !found || state.Status != daemon.StatusStopped || state.PID != 0 || state.OwnerToken != "private-stage-owner" || state.StoppedReason != err.Error() {
		t.Fatalf("actual guard stop receipt=%+v found=%v loadErr=%v guardErr=%v", state, found, loadErr, err)
	}
}

type serveRemoveStateWriter struct {
	t     *testing.T
	path  string
	ticks chan<- time.Time
}

func (w serveRemoveStateWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	if err := os.Remove(w.path); err != nil {
		return 0, err
	}
	w.ticks <- time.Now()
	return len(p), nil
}

func TestServePreparationReturnsCurrentPathRecordAndTokenErrors(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"state path", "state record", "token"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			h, r, store := nativeServeFixture(t)
			sentinel := errors.New("selected token dependency refusal")
			switch stage {
			case "state path":
				blocked := filepath.Join(r.ProjectsRoot, "blocked")
				write(t, blocked, "private file")
				r.ProjectsRoot = filepath.Join(blocked, "child")
			case "state record":
				if err := os.MkdirAll(filepath.Dir(store.Path), 0o700); err != nil {
					t.Fatal(err)
				}
				write(t, store.Path, "{")
			case "token":
				h.deps.Runtime.Token = func() (string, error) { return "", sentinel }
			}
			var out bytes.Buffer
			err := h.Serve(t.Context(), r, &out, io.Discard)
			if err == nil || out.Len() != 0 {
				t.Fatalf("Serve=%v output=%q", err, out.String())
			}
			if stage == "token" && !errors.Is(err, sentinel) {
				t.Fatalf("token identity=%v", err)
			}
		})
	}
}
