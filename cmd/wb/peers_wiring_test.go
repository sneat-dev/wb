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
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	server, _ := newPeerAdminTestServer(t)
	deps.LocalClient = func(string, string) (*http.Client, error) {
		return fakeDaemonHTTPClient(server.Listener.Addr().String(), "owner-token"), nil
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
	// The existing controller fixture owns launch/state effects. This proves the
	// actual constructor and authenticated RPC, not platform-default launchd.
	bad := deps
	sentinel := errors.New("launch refused")
	bad.Start = func(string, []string, string) (int, error) { return 0, sentinel }
	_, err = newPeerAdminOperations(context.Background(), bad, daemonTestRoot(t))
	if !errors.Is(err, sentinel) {
		t.Fatalf("builder failure=%v", err)
	}
}
