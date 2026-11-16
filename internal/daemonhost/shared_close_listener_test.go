package daemonhost

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Separate parity pools keep the dashboard and descriptor fixtures from
// claiming each other's ports between Close and rebind.
const (
	dashboardRebindPortPool = iota
	sharedCloseRebindPortPool
)

type automaticPortRange struct{ first, last int }

func listenRebindFixtureTCP(pool, candidates int) (*net.TCPListener, error) {
	endpoint := net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)}
	first, automatic, err := rebindFixturePortPolicy()
	if err != nil {
		return nil, err
	}
	if first == 0 {
		return net.ListenTCP("tcp4", &endpoint)
	}
	for port := first; port <= 65535 && candidates > 0; port++ {
		if port%2 != pool {
			continue
		}
		reserved := false
		for _, r := range automatic {
			reserved = reserved || (port >= r.first && port <= r.last)
		}
		if reserved {
			continue
		}
		endpoint.Port = port
		listener, err := net.ListenTCP("tcp4", &endpoint)
		if candidates == 1 || !errors.Is(err, syscall.EADDRINUSE) {
			return listener, err
		}
		candidates--
	}
	return nil, fmt.Errorf("no fixture port outside automatic ranges for pool %d", pool)
}

func TestSharedListenerCloseWaitsForHTTPShutdownToReleaseDescriptor(t *testing.T) {
	t.Parallel()
	tcp, err := listenRebindFixtureTCP(sharedCloseRebindPortPool, 1)
	if err != nil {
		t.Fatal(err)
	}
	address := tcp.Addr().String()
	observed := &descriptorHeldListener{
		TCPListener: tcp, accepting: make(chan struct{}), acceptClosed: make(chan struct{}),
	}
	listener := &closeEntryListener{Listener: shareListenerClose(observed), secondClose: make(chan struct{})}
	server := &http.Server{}
	release := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() {
		releaseOnce()
		_ = server.Close()
		_ = tcp.Close()
	})
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	waitListenerEvent(t, observed.accepting, "HTTP Accept entered")
	raw, err := tcp.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{})
	controlDone := make(chan error, 1)
	go func() {
		controlDone <- raw.Control(func(uintptr) {
			close(held)
			<-release
		})
	}()
	waitListenerEvent(t, held, "RawConn.Control held the descriptor")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- server.Shutdown(ctx) }()
	waitListenerEvent(t, observed.acceptClosed, "Accept observed the closing state")
	// The closing bit alone does not establish OS descriptor release.
	if err := tcp.SetDeadline(time.Time{}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("held closing listener deadline error = %v, want net.ErrClosed", err)
	}
	probe, err := net.Listen("tcp", address)
	if probe != nil {
		_ = probe.Close()
	}
	if err == nil {
		t.Fatal("held original descriptor unexpectedly released its address")
	}
	cleanupDone := make(chan error, 1)
	cleanup := sync.OnceFunc(func() { cleanupDone <- listener.Close() })
	go cleanup()
	waitListenerEvent(t, listener.secondClose, "owner cleanup entered the shared Close")
	select {
	case err := <-cleanupDone:
		t.Fatalf("owner cleanup returned before the original descriptor was released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	releaseOnce()
	if err := waitListenerError(t, controlDone, "descriptor control released"); err != nil {
		t.Fatal(err)
	}
	if err := waitListenerError(t, shutdownDone, "HTTP shutdown completed"); err != nil {
		t.Fatal(err)
	}
	if err := waitListenerError(t, serveDone, "HTTP Serve completed"); !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("HTTP Serve error = %v, want http.ErrServerClosed", err)
	}
	if err := waitListenerError(t, cleanupDone, "owner cleanup completed"); err != nil {
		t.Fatal(err)
	}
	if calls := observed.closes.Load(); calls != 1 {
		t.Fatalf("underlying Close calls = %d, want 1", calls)
	}
	rebound, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("address remains bound after all Close calls returned: %v", err)
	}
	_ = rebound.Close()
}

func TestSharedListenerCloseRetainsTheOriginalCloseError(t *testing.T) {
	t.Parallel()
	want := errors.New("original close failure")
	original := &closeErrorListener{err: want}
	listener := shareListenerClose(original)
	for range 2 {
		if err := listener.Close(); err != want {
			t.Fatalf("Close error = %v, want the original error instance", err)
		}
	}
	if original.calls != 1 {
		t.Fatalf("underlying Close calls = %d, want 1", original.calls)
	}
}

type descriptorHeldListener struct {
	*net.TCPListener
	accepting, acceptClosed chan struct{}
	acceptOnce, closedOnce  sync.Once
	closes                  atomic.Int32
}

func (listener *descriptorHeldListener) Accept() (net.Conn, error) {
	listener.acceptOnce.Do(func() { close(listener.accepting) })
	connection, err := listener.TCPListener.Accept()
	if errors.Is(err, net.ErrClosed) {
		listener.closedOnce.Do(func() { close(listener.acceptClosed) })
	}
	return connection, err
}

func (listener *descriptorHeldListener) Close() error {
	listener.closes.Add(1)
	return listener.TCPListener.Close()
}

type closeEntryListener struct {
	net.Listener
	calls       atomic.Int32
	secondClose chan struct{}
}

func (listener *closeEntryListener) Close() error {
	if listener.calls.Add(1) == 2 {
		close(listener.secondClose)
	}
	return listener.Listener.Close()
}

type closeErrorListener struct {
	net.Listener
	err   error
	calls int
}

func (listener *closeErrorListener) Close() error {
	listener.calls++
	return listener.err
}

func waitListenerEvent(t *testing.T, event <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-event:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitListenerError(t *testing.T, result <-chan error, label string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return nil
	}
}
