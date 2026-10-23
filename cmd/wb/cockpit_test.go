package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/sneat-dev/wb/internal/cli/cmdcockpit"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/cockpitrun"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/spf13/pflag"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCockpitExportThroughTheRootCommand(t *testing.T) {
	root := daemonTestRoot(t)
	var stdout, stderr bytes.Buffer
	code := runWithStdin([]string{"--projects-root", root, "cockpit", "export", "--format", "json"}, strings.NewReader(""), &stdout, &stderr)
	if code != exitFindings || stdout.String() != `{"schema_version":1,"error":"daemon_not_running"}`+"\n" {
		t.Fatalf("code = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), root) {
		t.Errorf("stderr names the projects root: %q", stderr.String())
	}
}

func TestCockpitFlagsAreExactlyTheCapabilityRowFlags(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "ai", "capabilities.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Capabilities []struct {
			Surfaces struct {
				Runtime struct {
					Commands []struct {
						Path  string   `json:"path"`
						Flags []string `json:"flags"`
					} `json:"commands"`
				} `json:"runtime"`
			} `json:"surfaces"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, capability := range manifest.Capabilities {
		for _, command := range capability.Surfaces.Runtime.Commands {
			if command.Path == "wb cockpit" {
				want = command.Flags
			}
		}
	}
	var got []string
	newCockpitCmd(&invocation{}).Flags().VisitAll(func(flag *pflag.Flag) { got = append(got, "--"+flag.Name) })
	if len(want) == 0 || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("command flags = %v, capability row flags = %v", got, want)
	}
}

func TestCockpitLocalFromDaemonMintsOverTheOwnerChannel(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	// Start the fake daemon once to learn the stored owner token.
	controller := newDaemonController(deps, root)
	if _, err := controller.Start(context.Background(), daemonruntime.DefaultListen); err != nil {
		t.Fatal(err)
	}
	state, _, err := controller.LoadState()
	if err != nil || state.OwnerToken == "" {
		t.Fatalf("state = %+v, err = %v", state, err)
	}
	var presented []string
	server := cockpit.New(cockpit.Options{CanonicalHost: "127.0.0.1"})
	mux := http.NewServeMux()
	mux.Handle(cockpit.LoginCodeRPCPath, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		presented = append(presented, request.Header.Get("Authorization"))
		daemonruntime.AuthenticatedHandler(state.OwnerToken, server.LoginCodeHandler()).ServeHTTP(writer, request)
	}))
	mux.Handle(cockpit.PagePrefix, server.Mounts()[cockpit.PagePrefix])
	test := httptest.NewServer(mux)
	defer test.Close()
	deps.LocalClient = func(_, ownerToken string) (*http.Client, error) {
		return &http.Client{Transport: daemonruntime.WithOwnerToken(ownerToken, cockpitRewriteTransport{target: test.URL})}, nil
	}
	command := cmdcockpit.New(newCLIRuntime(&invocation{projectsRoot: root}), cmdcockpit.Dependencies{Local: cockpitrun.NewLocalService(deps.Dependencies, requestCockpitLogin).Local, IsTerminal: func(any) bool { return false }})
	command.SetArgs([]string{"--print-url"})
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(presented) != 1 || presented[0] != "Bearer "+state.OwnerToken {
		t.Fatalf("presented = %v, want the stored owner token once", presented)
	}
	login, err := url.Parse(strings.TrimSpace(strings.TrimPrefix(stdout.String(), "cockpit: ")))
	if err != nil || login.Path != cockpit.LoginPath || login.Query().Get("code") == "" {
		t.Fatalf("stdout = %q, err = %v", stdout.String(), err)
	}
	// The key the daemon minted beside the code is in the fragment and nowhere
	// the server is sent.
	key, isKey := strings.CutPrefix(login.Fragment, cockpit.LoginKeyFragment+"=")
	if !isKey || len(key) < 43 || key == login.Query().Get("code") || strings.Contains(login.RequestURI(), key) {
		t.Fatalf("the login URL's fragment is %q, want the session key and only there", login.Fragment)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(test.URL + login.Path + "?code=" + login.Query().Get("code"))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusSeeOther || len(response.Cookies()) == 0 {
		t.Fatalf("login exchange = %s, cookies = %v", response.Status, response.Cookies())
	}
	// The printed key is the one that session is bound to: with the cookie it
	// is the owner, and the cookie alone is no session (this server has
	// anonymous metadata off, so it is refused).
	principal := func(withKey bool) string {
		request := httptest.NewRequest(http.MethodGet, cockpit.APIPrefix+"session", nil)
		request.Host = "127.0.0.1:8766"
		request.AddCookie(&http.Cookie{Name: "wb_cockpit_session_8766", Value: response.Cookies()[0].Value})
		if withKey {
			request.Header.Set(cockpit.SessionKeyHeader, key)
		}
		recorder := httptest.NewRecorder()
		server.Mounts()[cockpit.APIPrefix].ServeHTTP(recorder, request)
		return recorder.Body.String()
	}
	if with, without := principal(true), principal(false); !strings.Contains(with, `"principal":"owner"`) || !strings.Contains(without, "an owner session is required") {
		t.Fatalf("with the printed key: %s; with the cookie alone: %s", with, without)
	}
}

func TestSideEffectFreeCommandsRecordNoHeartbeat(t *testing.T) {
	t.Parallel()
	for id, want := range map[string]bool{"version": true, "cockpit export": true, "cockpit": false, "daemon status": false, "": false} {
		if got := sideEffectFreeCommand(id); got != want {
			t.Errorf("sideEffectFreeCommand(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestCockpitLocalFromDaemonRefusesBadMintResponses(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	status, body := http.StatusInternalServerError, `{"error":"boom"}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	}))
	defer server.Close()
	deps.LocalClient = func(string, string) (*http.Client, error) {
		return &http.Client{Transport: cockpitRewriteTransport{target: server.URL}}, nil
	}
	if _, err := cockpitrun.NewLocalService(deps.Dependencies, requestCockpitLogin).Local(context.Background(), cockpitrun.LocalRequest{Root: root, Listen: daemonruntime.DefaultListen, Mint: true}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the daemon's refusal", err)
	}
	status, body = http.StatusOK, `{}`
	if _, err := cockpitrun.NewLocalService(deps.Dependencies, requestCockpitLogin).Local(context.Background(), cockpitrun.LocalRequest{Root: root, Listen: daemonruntime.DefaultListen, Mint: true}); err == nil || !strings.Contains(err.Error(), "no code") {
		t.Fatalf("err = %v, want a no-code error", err)
	}
	// A daemon still running from before an update mints a code and no key: a
	// session it starts would make the cookie alone an owner, so the login is
	// refused with the advice to restart it, and nothing is printed.
	status, body = http.StatusOK, `{"code":"c","path":"`+cockpit.LoginPath+`"}`
	if _, err := cockpitrun.NewLocalService(deps.Dependencies, requestCockpitLogin).Local(context.Background(), cockpitrun.LocalRequest{Root: root, Listen: daemonruntime.DefaultListen, Mint: true}); !errors.Is(err, cockpitrun.ErrNoSessionKey) || !strings.Contains(err.Error(), "wb daemon restart") || exitCodeFor(err, true) != 1 {
		t.Fatalf("err = %v (exit %d), want the refusal that names `wb daemon restart`, exit 1", err, exitCodeFor(err, true))
	}
	status, body = http.StatusOK, `{"code":"c","key":"k","path":"/elsewhere"}`
	if _, err := cockpitrun.NewLocalService(deps.Dependencies, requestCockpitLogin).Local(context.Background(), cockpitrun.LocalRequest{Root: root, Listen: daemonruntime.DefaultListen, Mint: true}); err == nil || !strings.Contains(err.Error(), "/elsewhere") {
		t.Fatalf("err = %v, want a login-path error", err)
	}
	deps.LocalClient = func(string, string) (*http.Client, error) { return nil, errors.New("no socket") }
	if _, err := cockpitrun.NewLocalService(deps.Dependencies, requestCockpitLogin).Local(context.Background(), cockpitrun.LocalRequest{Root: root, Listen: daemonruntime.DefaultListen, Mint: true}); err == nil || !strings.Contains(err.Error(), "no socket") {
		t.Fatalf("err = %v", err)
	}
}

type cockpitRewriteTransport struct{ target string }

func (transport cockpitRewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme, clone.URL.Host = "http", strings.TrimPrefix(transport.target, "http://")
	return http.DefaultTransport.RoundTrip(clone)
}
