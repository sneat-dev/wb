package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/sneat-dev/wb/internal/hubconfig"

	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func TestPeersRootDefaultBindingsAreLazyAndRefuseBeforeDaemonLaunch(t *testing.T) {
	// This actual default-composition test is serial: config is child-test-process state.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	inv := testInvocation(t, t.TempDir())
	command := newPeersCmd(inv)
	inv.projectsRoot = "\x00" // read lazily after construction; never resolves a usable machine root.
	command.SetArgs([]string{"invite", "laptop"})
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SilenceUsage = true
	command.SilenceErrors = true
	err := command.Execute()
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUsage || out.Len() != 0 {
		t.Fatalf("unconfigured refusal=%v out=%q", err, out.String())
	}
	path := wbconfig.DefaultPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("hub:\n  store:\n    engine: memory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	command = newPeersCmd(inv)
	command.SetArgs([]string{"invite", "laptop"})
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SilenceUsage = true
	command.SilenceErrors = true
	if err := command.Execute(); err == nil || out.Len() != 0 {
		t.Fatalf("invalid-root default start=%v out=%q", err, out.String())
	}
}
func TestPeersRootDefaultJoinAndReadBindingsUsePrivateState(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-peer-token" {
			t.Error("default probe lost token")
		}
		_, _ = io.WriteString(w, `{"schema_version":1,"peer_id":"peer"}`)
	}))
	t.Cleanup(server.Close)
	inv := testInvocation(t, root)
	command := newPeersCmd(inv)
	command.SetArgs([]string{"join", server.URL, "--token-stdin", "--json"})
	command.SetIn(bytes.NewBufferString("private-peer-token\n"))
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SilenceErrors = true
	command.SilenceUsage = true
	// The real default restart launches this verified test executable. Its
	// ordinary flag parser refuses CLI flags, so retain the actual child error
	// after persistence rather than pretending the platform daemon started.
	err := command.Execute()
	var childExit *exec.ExitError
	if !errors.As(err, &childExit) || !strings.Contains(err.Error(), "peer join saved, but daemon restart failed") || out.Len() != 0 || strings.Contains(err.Error(), "private-peer-token") || strings.Contains(errOut.String(), "private-peer-token") {
		t.Fatalf("saved join child failure=%v stdout=%q stderr=%q", err, out.String(), errOut.String())
	}
	upstream, found, err := wbconfig.LoadPeersUpstream(wbconfig.DefaultPath())
	if err != nil || !found || upstream.URL != server.URL {
		t.Fatalf("upstream=%+v,%v,%v", upstream, found, err)
	}
	if token, err := os.ReadFile(upstream.TokenFile); err != nil || string(token) != "private-peer-token\n" {
		t.Fatalf("private token=%q,%v", token, err)
	}
	out.Reset()
	errOut.Reset()
	command = newPeersCmd(inv)
	command.SetArgs([]string{"list"})
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SilenceErrors = true
	command.SilenceUsage = true
	if err := command.Execute(); err != nil || out.Len() == 0 || errOut.Len() == 0 {
		t.Fatalf("non-starting read=%v out=%q diagnostic=%q", err, out.String(), errOut.String())
	}
	sentinel := errAtWrite
	command = newPeersCmd(inv)
	command.SetArgs([]string{"list", "--json"})
	command.SetOut(&failAtCallWriter{failAt: 1})
	command.SetErr(io.Discard)
	command.SilenceErrors = true
	command.SilenceUsage = true
	if err := command.Execute(); !errors.Is(err, sentinel) {
		t.Fatalf("writer identity=%v", err)
	}
}
func TestPeersRootPrivateAdminBuilderPreservesRealClientSuccessAndRefusal(t *testing.T) {
	server, _ := newPeerAdminTestServerWithEngine(t, hubconfig.EngineMemory)
	// Keep the original injected lifecycle producer on its own private root.
	// The distinct Host root supplies actual native authenticated Unix transport;
	// this does not claim native platform startup or default-listen readiness.
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state, found, err := newDaemonController(server.deps, server.root).LoadState()
	if err != nil || !found || state.Status != daemon.StatusReady || state.OwnerToken == "" {
		t.Fatalf("native Host state=%+v, found=%t, err=%v", state, found, err)
	}
	deps.Token = func() (string, error) { return state.OwnerToken, nil }
	deps.LocalClient = func(requestedRoot, token string) (*http.Client, error) {
		if requestedRoot != root || token != state.OwnerToken {
			t.Fatalf("constructor transport requested root=%q token matches=%t", requestedRoot, token == state.OwnerToken)
		}
		return daemonruntime.LocalHTTPClient(server.root, token)
	}
	operations, err := newPeerAdminOperations(context.Background(), deps, root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = operations.Invite(context.Background(), peers.InviteRequest{Name: "private-peer"})
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitFindings {
		t.Fatalf("memory-engine authenticated refusal=%v", err)
	}
	// The original controller fixture owns simulated launch/state effects.
	// The authenticated RPC above uses the separately owned native Host.
	bad := deps
	sentinel := errors.New("launch refused")
	bad.Start = func(string, []string, string) (int, error) { return 0, sentinel }
	_, err = newPeerAdminOperations(context.Background(), bad, daemonTestRoot(t))
	if !errors.Is(err, sentinel) {
		t.Fatalf("builder failure=%v", err)
	}
}
