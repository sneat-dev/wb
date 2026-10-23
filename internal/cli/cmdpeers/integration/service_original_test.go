package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/cmdpeers"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/peersrun"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/spf13/cobra"
)

type fixtureExitError struct {
	code    int
	message string
}

func (e *fixtureExitError) Error() string { return e.message }

type peerCommandFixture struct {
	ops        peersrun.Dependencies
	configPath func() string
	now        func() time.Time
}

func testPeersDeps(t *testing.T, _, _ *httptest.Server) peerCommandFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wb.yaml")
	deps := peersrun.Dependencies{ConfigPath: func() string { return path }, Abs: filepath.Abs, Do: (&http.Client{Timeout: 5 * time.Second}).Do, Admin: func(context.Context, string) (peersrun.AdminOperations, error) {
		t.Fatal("unexpected admin call")
		return peersrun.AdminOperations{}, nil
	}, ListenAddress: func(string) (string, error) { return "", os.ErrNotExist }}
	return peerCommandFixture{ops: deps, configPath: deps.ConfigPath, now: time.Now}
}
func executePeerFixture(ctx context.Context, deps peerCommandFixture, root string, args []string, in io.Reader, out, errOut io.Writer) error {
	service := peersrun.New(deps.ops, peersrun.JoinDependencies{})
	command := cmdpeers.New(shared.Runtime{Flags: func() shared.Flags { return shared.Flags{ProjectsRoot: root} }, ExitError: func(code int, message string) error { return &fixtureExitError{code: code, message: message} }}, cmdpeers.Dependencies{Invite: service.Invite, List: service.List, Get: service.Get, TrustChange: service.TrustChange, Disconnect: service.Disconnect, Now: deps.now, SetDiscoveryTerms: func(*cobra.Command, string) {}})
	command.SetContext(ctx)
	command.SetArgs(args)
	command.SetIn(in)
	command.SetOut(out)
	command.SetErr(errOut)
	command.SilenceUsage = true
	command.SilenceErrors = true
	return command.Execute()
}
func TestHubOnlyVerbsRefuseWithoutAHubConfig(t *testing.T) {
	t.Parallel()
	deps := testPeersDeps(t, nil, nil)
	var out bytes.Buffer

	assertUsageRefusal := func(t *testing.T, err error) {
		t.Helper()
		var exit *fixtureExitError
		if !errors.As(err, &exit) || exit.code != shared.ExitUsage {
			t.Fatalf("error = %v, want an shared.ExitUsage *fixtureExitError", err)
		}
	}

	err := executePeerFixture(context.Background(), deps, t.TempDir(), []string{"invite", "laptop", "--rotate=" + strconv.FormatBool(false), "--token-file=" + "", "--json=" + strconv.FormatBool(false)}, nil, &out, &out)
	if err == nil {
		t.Fatal("expected invite to refuse without a hub config")
	}
	assertUsageRefusal(t, err)

	err = executePeerFixture(context.Background(), deps, t.TempDir(), []string{"disconnect", "laptop", "--json=" + strconv.FormatBool(false)}, nil, &out, &out)
	if err == nil {
		t.Fatal("expected disconnect to refuse without a hub config")
	}
	assertUsageRefusal(t, err)

	err = executePeerFixture(context.Background(), deps, t.TempDir(), []string{"block", "laptop", "--json=" + strconv.FormatBool(false)}, nil, &out, &out)
	if err == nil {
		t.Fatal("expected block of a non-upstream name to refuse without a hub config")
	}
	assertUsageRefusal(t, err)
}
func TestPeersListIsForgivingWithoutADaemonOrUpstream(t *testing.T) {
	t.Parallel()
	deps := testPeersDeps(t, nil, nil)
	var out, errOut bytes.Buffer
	if err := executePeerFixture(context.Background(), deps, t.TempDir(), []string{"list", "--json=" + strconv.FormatBool(true)}, nil, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	var jsonResult peers.ListResponse
	if err := json.Unmarshal(out.Bytes(), &jsonResult); err != nil || len(jsonResult.Peers) != 0 {
		t.Fatalf("empty list output = %q, %v", out.String(), err)
	}
}
func TestPeersListEscalatesToAFindingWhenAHubIsConfiguredButUnreachable(t *testing.T) {
	t.Parallel()
	deps := testPeersDeps(t, nil, nil)
	hubConfigPath := deps.configPath()
	if err := os.WriteFile(hubConfigPath, []byte("hub:\n  store:\n    engine: memory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	err := executePeerFixture(context.Background(), deps, t.TempDir(), []string{"list", "--json=" + strconv.FormatBool(true)}, nil, &out, &errOut)
	if err == nil {
		t.Fatal("expected a finding when a hub is configured but the daemon is unreachable")
	}
	var exit *fixtureExitError
	if !errors.As(err, &exit) || exit.code != shared.ExitFindings {
		t.Fatalf("runPeersList error = %v, want an shared.ExitFindings *fixtureExitError", err)
	}
	if errOut.Len() == 0 {
		t.Fatal("expected a stderr note naming the downstream failure")
	}
}
func TestPeersGetUpstreamWithoutConfigurationIsAFinding(t *testing.T) {
	t.Parallel()
	deps := testPeersDeps(t, nil, nil)
	var out bytes.Buffer
	if err := executePeerFixture(context.Background(), deps, t.TempDir(), []string{"get", "upstream", "--json=" + strconv.FormatBool(false)}, nil, &out, &out); err == nil {
		t.Fatal("expected an error when no upstream is configured")
	}
}
func TestPeersBlockActsLocallyOnTheUpstream(t *testing.T) {
	t.Parallel()
	deps := testPeersDeps(t, nil, nil)
	if err := wbconfig.SetPeersUpstream(deps.configPath(), "https://vm1.sneat.dev", filepath.Join(t.TempDir(), "token")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()

	var out bytes.Buffer
	if err := executePeerFixture(context.Background(), deps, root, []string{"block", "upstream", "--json=" + strconv.FormatBool(false)}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Blocked upstream vm1.sneat.dev") {
		t.Fatalf("upstream block output = %q", out.String())
	}
	service := peersrun.New(deps.ops, peersrun.JoinDependencies{})
	upstream, err := service.Get(context.Background(), peersrun.GetRequest{Root: root, Peer: "upstream"})
	found := err == nil
	if err != nil || !found || upstream.Status != "blocked" {
		t.Fatalf("upstream row after local block = %+v, %t, %v", upstream, found, err)
	}

	out.Reset()
	if err := executePeerFixture(context.Background(), deps, root, []string{"unblock", "vm1.sneat.dev", "--json=" + strconv.FormatBool(false)}, nil, &out, &out); err != nil {
		t.Fatal(err)
	}
	upstream, err = service.Get(context.Background(), peersrun.GetRequest{Root: root, Peer: "upstream"})
	found = err == nil
	if err != nil || !found || upstream.Status != "offline" {
		t.Fatalf("upstream row after local unblock = %+v, %t, %v", upstream, found, err)
	}
}
