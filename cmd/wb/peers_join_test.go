package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

type fixedMachineBearerResolver struct {
	machine hub.Machine
	err     error
}

func (resolver fixedMachineBearerResolver) ResolveMachineBearer(*http.Request) (hub.Machine, error) {
	return resolver.machine, resolver.err
}

// hubPeersConnectTestServer serves a real hub.NewHandler whose machine
// bearer always resolves to a fixed peer (isPeer) or enrollment (!isPeer)
// credential, so verifyPeerConnectProbe can be exercised against the actual
// route it targets.
func hubPeersConnectTestServer(t *testing.T, isPeer bool) *httptest.Server {
	t.Helper()
	scopes := []hub.MachineScope{hub.ScopePeerSession}
	if !isPeer {
		scopes = []hub.MachineScope{hub.ScopeSnapshotPublish, hub.ScopeSnapshotRead, hub.ScopeEventsPoll, hub.ScopeEventsAck}
	}
	machine := hub.Machine{ID: "machine_1", Name: "laptop", IdentityID: "local", Scopes: scopes}
	handler := hub.NewHandler(hub.HandlerOptions{MachineBearer: fixedMachineBearerResolver{machine: machine}})
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func testPeersJoinDeps(verifyErr, restartErr error) (peersJoinDeps, *int, *int) {
	verifyCalls, restartCalls := 0, 0
	return peersJoinDeps{
		configPath: func() string { return "" }, // overwritten per test
		verify: func(context.Context, string, string) error {
			verifyCalls++
			return verifyErr
		},
		restart: func(context.Context, string) error {
			restartCalls++
			return restartErr
		},
	}, &verifyCalls, &restartCalls
}

// TestPeersJoinWritesConfigAndCredentialLeavingRemoteByteIdentical covers
// wb.peers.join's core contract: it verifies, writes a private credential,
// writes peers.upstream, and leaves remote: untouched.
func TestPeersJoinWritesConfigAndCredentialLeavingRemoteByteIdentical(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	original := "parallel: 3\nremote:\n  provider: git\n  repo: acme/state\n"
	if err := os.WriteFile(configPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	deps, verifyCalls, restartCalls := testPeersJoinDeps(nil, nil)
	deps.configPath = func() string { return configPath }

	var out bytes.Buffer
	if err := runPeersJoin(context.Background(), deps, t.TempDir(), "https://vm1.sneat.dev", "", true, true, false, strings.NewReader("the-token\n"), &out, &out); err != nil {
		t.Fatal(err)
	}
	if *verifyCalls != 1 || *restartCalls != 1 {
		t.Fatalf("verify calls = %d, restart calls = %d, want 1 and 1", *verifyCalls, *restartCalls)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "remote:\n  provider: git\n  repo: acme/state\n") {
		t.Fatalf("remote: block changed:\n%s", text)
	}
	if !strings.Contains(text, "peers:") || !strings.Contains(text, "url: https://vm1.sneat.dev") {
		t.Fatalf("peers.upstream missing:\n%s", text)
	}
	upstream, found, err := wbconfig.LoadPeersUpstream(configPath)
	if err != nil || !found || upstream.URL != "https://vm1.sneat.dev" {
		t.Fatalf("LoadPeersUpstream = %+v, %t, %v", upstream, found, err)
	}
	info, err := os.Stat(upstream.TokenFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file = %v, %v; want mode 0600", info, err)
	}
	credential, err := os.ReadFile(upstream.TokenFile)
	if err != nil || strings.TrimSpace(string(credential)) != "the-token" {
		t.Fatalf("credential file content = %q, %v", credential, err)
	}
	if !strings.Contains(out.String(), "Joined https://vm1.sneat.dev") {
		t.Fatalf("join text output = %q", out.String())
	}
}

func TestPeersJoinRefusesSameOriginHubRemote(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	original := "remote:\n  provider: hub\n  url: https://vm1.sneat.dev\n  machine: laptop\n  token_file: /abs/token\n"
	if err := os.WriteFile(configPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	deps, verifyCalls, restartCalls := testPeersJoinDeps(nil, nil)
	deps.configPath = func() string { return configPath }

	var out bytes.Buffer
	err := runPeersJoin(context.Background(), deps, t.TempDir(), "https://vm1.sneat.dev", "", true, false, false, strings.NewReader("the-token\n"), &out, &out)
	if err == nil {
		t.Fatal("expected a refusal for a same-origin remote.provider: hub")
	}
	if *verifyCalls != 0 || *restartCalls != 0 {
		t.Fatalf("same-origin refusal must short-circuit before verify/restart: verify=%d restart=%d", *verifyCalls, *restartCalls)
	}
}

func TestPeersJoinRefusesABadToken(t *testing.T) {
	deps, _, restartCalls := testPeersJoinDeps(errors.New("unauthorized"), nil)
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	deps.configPath = func() string { return configPath }
	var out bytes.Buffer
	err := runPeersJoin(context.Background(), deps, t.TempDir(), "https://vm1.sneat.dev", "", true, false, false, strings.NewReader("bad-token\n"), &out, &out)
	if err == nil {
		t.Fatal("expected a refusal for a bad token")
	}
	if *restartCalls != 0 {
		t.Fatal("a failed verification must never reach the restart step")
	}
	if _, err := os.Stat(deps.configPath()); err == nil {
		t.Fatal("a failed verification must never write configuration")
	}
}

func TestPeersJoinRequiresExactlyOneTokenSource(t *testing.T) {
	deps, _, _ := testPeersJoinDeps(nil, nil)
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	deps.configPath = func() string { return configPath }
	var out bytes.Buffer
	if err := runPeersJoin(context.Background(), deps, t.TempDir(), "https://vm1.sneat.dev", "", false, false, false, strings.NewReader(""), &out, &out); err == nil {
		t.Fatal("expected a usage error when neither --token-stdin nor --token-file is given")
	}
	if err := runPeersJoin(context.Background(), deps, t.TempDir(), "https://vm1.sneat.dev", "/abs/token", true, false, false, strings.NewReader("x"), &out, &out); err == nil {
		t.Fatal("expected a usage error when both --token-stdin and --token-file are given")
	}
}

func TestPeersJoinRejectsANonAbsoluteTokenFile(t *testing.T) {
	deps, _, _ := testPeersJoinDeps(nil, nil)
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	deps.configPath = func() string { return configPath }
	var out bytes.Buffer
	if err := runPeersJoin(context.Background(), deps, t.TempDir(), "https://vm1.sneat.dev", "relative/token", false, false, false, strings.NewReader(""), &out, &out); err == nil {
		t.Fatal("expected a usage error for a relative --token-file")
	}
}

func TestPeersJoinRejectsAnInvalidHubURL(t *testing.T) {
	deps, _, _ := testPeersJoinDeps(nil, nil)
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	deps.configPath = func() string { return configPath }
	var out bytes.Buffer
	if err := runPeersJoin(context.Background(), deps, t.TempDir(), "http://vm1.sneat.dev", "", true, false, false, strings.NewReader("token"), &out, &out); err == nil {
		t.Fatal("expected a usage error for a non-loopback http:// hub URL")
	}
}

// TestPeersJoinReportsJSON proves the machine-readable output shape.
func TestPeersJoinReportsJSON(t *testing.T) {
	deps, _, _ := testPeersJoinDeps(nil, nil)
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	deps.configPath = func() string { return configPath }
	var out bytes.Buffer
	if err := runPeersJoin(context.Background(), deps, t.TempDir(), "https://vm1.sneat.dev", "", true, false, true, strings.NewReader("the-token\n"), &out, &out); err != nil {
		t.Fatal(err)
	}
	var result peersJoinResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.Verified || result.DaemonRestart {
		t.Fatalf("join JSON output = %q, %v", out.String(), err)
	}
}

// TestPeersJoinNodeIdentityWarningGoesToStderr is M1: a failure to create
// the node identity file is reported to stderr, not mixed into stdout's join
// result, and does not fail the join itself.
func TestPeersJoinNodeIdentityWarningGoesToStderr(t *testing.T) {
	deps, _, _ := testPeersJoinDeps(nil, nil)
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	deps.configPath = func() string { return configPath }

	// A projects root that is itself a regular file makes nodeidentity.Load
	// fail deterministically — the same portable fault
	// internal/nodeidentity's own test uses, rather than fighting
	// HOME/USERPROFILE across platforms.
	root := t.TempDir()
	blocker := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if err := runPeersJoin(context.Background(), deps, blocker, "https://vm1.sneat.dev", "", true, false, false, strings.NewReader("the-token\n"), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if errOut.Len() == 0 || !strings.Contains(errOut.String(), "node identity unavailable") {
		t.Fatalf("stderr = %q, want the node identity warning", errOut.String())
	}
	if strings.Contains(out.String(), "node identity") {
		t.Fatalf("stdout leaked the node identity warning: %q", out.String())
	}
	if !strings.Contains(out.String(), "Joined https://vm1.sneat.dev") {
		t.Fatalf("stdout = %q, want the ordinary join result despite the node identity warning", out.String())
	}
}

// TestSameOriginNormalizesSchemeHostPortAndTrailingDot is M2: two URLs that
// name the same origin under case, a trailing DNS root dot, or an explicit
// default port must compare equal.
func TestSameOriginNormalizesSchemeHostPortAndTrailingDot(t *testing.T) {
	for _, pair := range [][2]string{
		{"https://VM1.sneat.dev", "https://vm1.sneat.dev"},
		{"https://vm1.sneat.dev.", "https://vm1.sneat.dev"},
		{"https://vm1.sneat.dev:443", "https://vm1.sneat.dev"},
		{"http://vm1.sneat.dev:80", "http://vm1.sneat.dev"},
	} {
		if !sameOrigin(pair[0], pair[1]) {
			t.Fatalf("sameOrigin(%q, %q) = false, want true", pair[0], pair[1])
		}
	}
	if sameOrigin("https://vm1.sneat.dev:8443", "https://vm1.sneat.dev") {
		t.Fatal("sameOrigin must not ignore a non-default port")
	}
	if sameOrigin("https://vm1.sneat.dev", "https://vm2.sneat.dev") {
		t.Fatal("sameOrigin must not equate two different hosts")
	}
}

// TestPeersJoinRefusesOnAnUnparsableRemoteConfig is M2's second half: a
// wb.yaml that exists but fails to parse must refuse the join, rather than
// silently skipping the same-origin check the way "no remote configured"
// (an *remotestate.UnconfiguredError, the ordinary fresh-install case) is
// allowed to.
func TestPeersJoinRefusesOnAnUnparsableRemoteConfig(t *testing.T) {
	deps, verifyCalls, _ := testPeersJoinDeps(nil, nil)
	configPath := filepath.Join(t.TempDir(), "wb.yaml")
	if err := os.WriteFile(configPath, []byte("not: [valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps.configPath = func() string { return configPath }
	var out bytes.Buffer
	err := runPeersJoin(context.Background(), deps, t.TempDir(), "https://vm1.sneat.dev", "", true, false, false, strings.NewReader("the-token\n"), &out, &out)
	if err == nil {
		t.Fatal("expected a refusal for an unparsable wb.yaml")
	}
	if *verifyCalls != 0 {
		t.Fatal("an unparsable config must refuse before ever verifying the token")
	}
}

// TestVerifyPeerConnectProbeRefusesARedirect is M10: the probe client must
// never follow a redirect, since the peer's bearer token travels in a
// header a redirect target would also receive.
func TestVerifyPeerConnectProbeRefusesARedirect(t *testing.T) {
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("verifyPeerConnectProbe must never follow a redirect to a second server")
	}))
	t.Cleanup(redirectTarget.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+hub.PeersConnectPath, http.StatusFound)
	}))
	t.Cleanup(server.Close)
	if err := verifyPeerConnectProbe(context.Background(), server.URL, "the-token"); err == nil {
		t.Fatal("expected verifyPeerConnectProbe to refuse a redirect")
	}
}

// TestVerifyPeerConnectProbeAgainstTheRealHandler runs the default verify
// implementation against a real hub handler serving PeersConnectPath, so the
// join↔probe contract is exercised end to end (not just through a mocked
// verify function).
func TestVerifyPeerConnectProbeAgainstTheRealHandler(t *testing.T) {
	server := hubPeersConnectTestServer(t, true)
	if err := verifyPeerConnectProbe(context.Background(), server.URL, "the-token"); err != nil {
		t.Fatalf("verifyPeerConnectProbe against a valid peer token = %v", err)
	}
}

func TestVerifyPeerConnectProbeRefusesANonPeerCredential(t *testing.T) {
	server := hubPeersConnectTestServer(t, false)
	if err := verifyPeerConnectProbe(context.Background(), server.URL, "the-token"); err == nil {
		t.Fatal("expected verifyPeerConnectProbe to refuse a non-peer credential")
	}
}
