package peersrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/credentialfile"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func boundaryService(t *testing.T, hub bool) (Service, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wb.yaml")
	if hub {
		if err := os.WriteFile(path, []byte("hub:\n  store:\n    engine: memory\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deps := Dependencies{ConfigPath: func() string { return path }, Abs: filepath.Abs, ListenAddress: func(string) (string, error) { return "127.0.0.1:1", nil }, Do: func(*http.Request) (*http.Response, error) { return nil, errors.New("unexpected HTTP") }, Admin: func(context.Context, string) (AdminOperations, error) {
		t.Fatal("unexpected admin")
		return AdminOperations{}, nil
	}}
	return New(deps, JoinDependencies{}), dir
}
func TestAdminFailuresPreserveIdentityAndValidationOrder(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"config", "abs", "existing", "admin", "rpc"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			s, root := boundaryService(t, true)
			sentinel := errors.New("operation refused")
			calls := 0
			s.deps.Admin = func(ctx context.Context, got string) (AdminOperations, error) {
				calls++
				if got != root || ctx.Value(boundaryContextKey{}) != "request" {
					t.Fatal("request changed")
				}
				if stage == "admin" {
					return AdminOperations{}, sentinel
				}
				return AdminOperations{Invite: func(_ context.Context, r peers.InviteRequest) (peers.InviteResponse, error) {
					if r.Name != "laptop" || !r.Rotate {
						t.Fatal(r)
					}
					return peers.InviteResponse{}, sentinel
				}, Trust: func(context.Context, TrustAction, string) (peers.TrustResponse, error) {
					return peers.TrustResponse{}, sentinel
				}, Disconnect: func(context.Context, string) (peers.DisconnectResponse, error) {
					return peers.DisconnectResponse{}, sentinel
				}}, nil
			}
			req := InviteRequest{Root: root, Name: "laptop", Rotate: true}
			switch stage {
			case "config":
				if err := os.WriteFile(s.deps.ConfigPath(), []byte("hub: ["), 0o600); err != nil {
					t.Fatal(err)
				}
			case "abs":
				req.TokenFile = "relative"
				s.deps.Abs = func(string) (string, error) { return "", sentinel }
			case "existing":
				req.TokenFile = filepath.Join(root, "existing")
				if err := os.WriteFile(req.TokenFile, []byte("retained"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.WithValue(context.Background(), boundaryContextKey{}, "request")
			_, err := s.Invite(ctx, req)
			if err == nil {
				t.Fatal("missing refusal")
			}
			if stage == "abs" || stage == "admin" || stage == "rpc" {
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			}
			want := 0
			if stage == "admin" || stage == "rpc" {
				want = 1
			}
			if calls != want {
				t.Fatalf("admin calls=%d want%d", calls, want)
			}
			if stage == "admin" || stage == "rpc" {
				_, err = s.TrustChange(ctx, TrustRequest{Root: root, Peer: "laptop", Action: Unblock})
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
				_, err = s.Disconnect(ctx, DisconnectRequest{Root: root, Peer: "laptop"})
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			}
		})
	}
}

type boundaryContextKey struct{}

func TestInviteRescueRetainsNormalizedAttemptedPathAndMintedToken(t *testing.T) {
	t.Parallel()
	s, root := boundaryService(t, true)
	blocker := filepath.Join(root, "blocker")
	attempted := filepath.Join(blocker, "credential")
	s.deps.Abs = func(p string) (string, error) {
		if p != "relative/token" {
			t.Fatal(p)
		}
		return attempted, nil
	}
	s.deps.Admin = func(context.Context, string) (AdminOperations, error) {
		return AdminOperations{Invite: func(context.Context, peers.InviteRequest) (peers.InviteResponse, error) {
			if err := os.WriteFile(blocker, []byte("retain"), 0o600); err != nil {
				t.Fatal(err)
			}
			return peers.InviteResponse{PeerID: "p", Token: "one-time"}, nil
		}}, nil
	}
	result, err := s.Invite(context.Background(), InviteRequest{Root: root, TokenFile: "relative/token"})
	if err != nil || result.TokenWriteError == nil || result.AttemptedTokenFile != attempted || result.Output.Token != "one-time" || result.Output.TokenFile != "" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
func TestLocalReadsClassifyFailuresAndJoinWarningBeforeConfigError(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"address", "request", "transport", "status", "decode", "success"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			s, root := boundaryService(t, false)
			closed := false
			sentinel := errors.New("transport refused")
			s.deps.ListenAddress = func(string) (string, error) {
				switch stage {
				case "address":
					return "", sentinel
				case "request":
					return "%", nil
				}
				return "127.0.0.1:1", nil
			}
			s.deps.Do = func(r *http.Request) (*http.Response, error) {
				if stage == "transport" {
					return nil, sentinel
				}
				status := 200
				body := `{"schema_version":1,"peers":[]}`
				switch stage {
				case "status":
					status = 503
				case "decode":
					body = "["
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: &boundaryReadCloser{Reader: strings.NewReader(body), closed: &closed}}, nil
			}
			result, err := s.readPeersList(context.Background(), root)
			if stage == "success" {
				if err != nil || result.SchemaVersion != 1 {
					t.Fatalf("%+v,%v", result, err)
				}
			} else if err == nil {
				t.Fatal("missing failure")
			}
			if stage == "status" || stage == "decode" || stage == "success" {
				if !closed {
					t.Fatal("response not closed")
				}
			}
			if stage == "request" || stage == "transport" {
				_, err = s.Get(context.Background(), GetRequest{Root: root, Peer: "laptop"})
				if err == nil {
					t.Fatal("malformed URL accepted")
				}
			}
		})
	}
	s, root := boundaryService(t, false)
	s.deps.ListenAddress = func(string) (string, error) { return "", os.ErrNotExist }
	warned := false
	result, err := s.List(context.Background(), ListRequest{Root: root}, func(error) {
		warned = true
		if err := os.WriteFile(s.deps.ConfigPath(), []byte("hub: ["), 0o600); err != nil {
			t.Fatal(err)
		}
	})
	if !warned || err == nil || result.Response.SchemaVersion != peers.SchemaVersion {
		t.Fatalf("warned=%v result=%+v err=%v", warned, result, err)
	}
}

type boundaryReadCloser struct {
	io.Reader
	closed *bool
}

func (r *boundaryReadCloser) Close() error { *r.closed = true; return nil }
func TestUpstreamStateRefusalsRemainLocalAndDoNotCallAdmin(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"config", "state-read", "state-parse", "state-write"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			s, root := boundaryService(t, false)
			if stage == "config" {
				if err := os.WriteFile(s.deps.ConfigPath(), []byte("peers: ["), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := wbconfig.SetPeersUpstream(s.deps.ConfigPath(), "https://hub.example.test", filepath.Join(root, "token")); err != nil {
					t.Fatal(err)
				}
				path, err := peerUpstreamStatePath(root)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				switch stage {
				case "state-read":
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
				case "state-parse":
					if err := os.WriteFile(path, []byte("["), 0o600); err != nil {
						t.Fatal(err)
					}
				case "state-write":
					if err := os.Remove(filepath.Dir(path)); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Dir(path), []byte("blocker"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err := s.Get(context.Background(), GetRequest{Root: root, Peer: "upstream"})
			if err == nil {
				t.Fatal("missing upstream read refusal")
			}
			_, err = s.TrustChange(context.Background(), TrustRequest{Root: root, Peer: "upstream", Action: Block})
			if err == nil {
				t.Fatal("missing upstream trust refusal")
			}
		})
	}
}
func TestPrivateStateAndTokenDirectoryCreationRefusals(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(parent, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := savePeerUpstreamState(filepath.Join(parent, "state"), peerUpstreamState{}); err == nil || !strings.Contains(err.Error(), "create upstream peer state directory") {
		t.Fatal(err)
	}
	if err := writeOneTimeToken(filepath.Join(parent, "token"), "secret"); err == nil || !strings.Contains(err.Error(), "create token directory") {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("retain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeOneTimeToken(path, "secret"); err == nil || !strings.Contains(err.Error(), "create token file") {
		t.Fatal(err)
	}
	if name := upstreamDisplayName("%bad"); name != "%bad" {
		t.Fatal(name)
	}
	if name := hostForFilename("%bad"); name != "hub" {
		t.Fatal(name)
	}
	if err := Verify(context.Background(), "%", "secret"); err == nil {
		t.Fatal("bad probe URL accepted")
	}
}

func TestInvalidRootFailsBeforeReadingOrChangingUpstreamState(t *testing.T) {
	t.Parallel()
	s, root := boundaryService(t, false)
	if err := wbconfig.SetPeersUpstream(s.deps.ConfigPath(), "https://hub.example.test", filepath.Join(root, "token")); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"get", "trust"} {
		var err error
		if operation == "get" {
			_, err = s.Get(context.Background(), GetRequest{Root: "\x00", Peer: "upstream"})
		} else {
			_, err = s.TrustChange(context.Background(), TrustRequest{Root: "\x00", Peer: "upstream", Action: Block})
		}
		if err == nil {
			t.Fatalf("%s accepted invalid root", operation)
		}
	}
}
func TestUpstreamSaveRefusalDoesNotReportSuccess(t *testing.T) {
	t.Parallel()
	s, root := boundaryService(t, false)
	if err := wbconfig.SetPeersUpstream(s.deps.ConfigPath(), "https://hub.example.test", filepath.Join(root, "token")); err != nil {
		t.Fatal(err)
	}
	path, err := peerUpstreamStatePath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(path), 0o700) })
	result, err := s.TrustChange(context.Background(), TrustRequest{Root: root, Peer: "upstream", Action: Block})
	if err == nil {
		t.Skip("filesystem permits writes despite read-only directory")
	}
	if result.Upstream || result.Response.Trust != "" || !strings.Contains(err.Error(), "stage upstream peer state") {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}
func TestJoinPersistenceFailureRemovesOnlyNewlyCreatedCredential(t *testing.T) {
	t.Parallel()
	for _, reused := range []bool{false, true} {
		t.Run(fmt.Sprint(reused), func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			config := filepath.Join(root, "wb.yaml")
			token := "secret"
			digest := sha256.Sum256([]byte(token))
			credential := filepath.Join(root, "credentials", fmt.Sprintf("peer-hub.example.test-%s.token", hex.EncodeToString(digest[:3])))
			if reused {
				if _, err := credentialfile.WritePrivate(credential, token); err != nil {
					t.Fatal(err)
				}
			}
			service := New(Dependencies{}, JoinDependencies{ConfigPath: func() string { return config }, Verify: func(context.Context, string, string) error {
				if err := os.Mkdir(config, 0o700); err != nil {
					t.Fatal(err)
				}
				return nil
			}, EnsureNodeIdentity: func(string) error { return nil }, Restart: func(context.Context, string) error { t.Fatal("restart before save"); return nil }})
			_, err := service.Join(context.Background(), JoinRequest{Root: root, HubURL: "https://hub.example.test", TokenStdin: true, RestartDaemon: true}, strings.NewReader(token), func(error) { t.Fatal("unexpected warning") })
			if err == nil {
				t.Fatal("config directory accepted")
			}
			raw, readErr := os.ReadFile(credential)
			if reused {
				if readErr != nil || string(raw) != token+"\n" {
					t.Fatalf("reused credential=%q,%v", raw, readErr)
				}
			} else if !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("created credential leaked: %q,%v", raw, readErr)
			}
		})
	}
}
func TestJoinCredentialAndSavedRestartFailuresPreserveStages(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"credential", "restart"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			config := filepath.Join(root, "wb.yaml")
			sentinel := errors.New("restart refused")
			restarts := 0
			deps := JoinDependencies{ConfigPath: func() string { return config }, Verify: func(context.Context, string, string) error {
				if stage == "credential" {
					if err := os.WriteFile(filepath.Join(root, "credentials"), []byte("retain"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				return nil
			}, EnsureNodeIdentity: func(string) error { return nil }, Restart: func(context.Context, string) error { restarts++; return sentinel }}
			_, err := New(Dependencies{}, deps).Join(context.Background(), JoinRequest{Root: root, HubURL: "https://hub.example.test", TokenStdin: true, RestartDaemon: true}, strings.NewReader("secret"), func(error) { t.Fatal("warning") })
			if err == nil {
				t.Fatal("missing refusal")
			}
			if stage == "credential" {
				if restarts != 0 {
					t.Fatal("restart after credential failure")
				}
			} else {
				if !errors.Is(err, sentinel) || restarts != 1 || !strings.Contains(err.Error(), "peer join saved") {
					t.Fatal(err)
				}
				up, found, loadErr := wbconfig.LoadPeersUpstream(config)
				if loadErr != nil || !found || up.URL != "https://hub.example.test" {
					t.Fatalf("saved=%+v,%v,%v", up, found, loadErr)
				}
			}
		})
	}
}
func TestJoinTokenFileRefusalsAndProbeProtocolBoundaries(t *testing.T) {
	t.Parallel()
	for _, content := range []string{"", "two tokens"} {
		path := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readPeersJoinToken(nil, path, false); err == nil {
			t.Fatalf("accepted %q", content)
		}
	}
	if _, err := readPeersJoinToken(nil, filepath.Join(t.TempDir(), "missing"), false); err == nil {
		t.Fatal("missing token accepted")
	}
	for _, body := range []string{"[", `{"schema_version":2,"peer_id":"p"}`, `{"schema_version":1,"peer_id":" "}`} {
		t.Run(body, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer secret" {
					t.Error("missing bearer")
				}
				_, _ = io.WriteString(w, body)
			}))
			t.Cleanup(server.Close)
			if err := Verify(context.Background(), server.URL, "secret"); err == nil {
				t.Fatal("invalid probe accepted")
			}
		})
	}
}

func TestSuccessfulPublicServiceResultsRetainSecretsSortingAndDecodePolicy(t *testing.T) {
	t.Parallel()
	s, root := boundaryService(t, true)
	s.deps.Admin = func(context.Context, string) (AdminOperations, error) {
		return AdminOperations{Invite: func(context.Context, peers.InviteRequest) (peers.InviteResponse, error) {
			return peers.InviteResponse{PeerID: "p", Token: "one-time"}, nil
		}}, nil
	}
	path := filepath.Join(root, "credentials", "one-time")
	result, err := s.Invite(context.Background(), InviteRequest{Root: root, TokenFile: path})
	if err != nil || result.TokenWriteError != nil || result.Output.Token != "" || result.Output.TokenFile != path || result.AttemptedTokenFile != path {
		t.Fatalf("invite=%+v,%v", result, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "one-time\n" {
		t.Fatalf("token=%q,%v", raw, err)
	}
	token, err := readPeersJoinToken(nil, path, false)
	if err != nil || token != "one-time" {
		t.Fatalf("read=%q,%v", token, err)
	}
	if got := (&Refusal{Kind: Usage, Message: "usage refused"}).Error(); got != "usage refused" {
		t.Fatal(got)
	}
	if err := wbconfig.SetPeersUpstream(s.deps.ConfigPath(), "https://hub.example.test", path); err != nil {
		t.Fatal(err)
	}
	s.deps.Do = func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/peers" {
			t.Fatal(r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"schema_version":1,"peers":[{"name":"zeta"},{"name":"alpha"}]}`))}, nil
	}
	list, err := s.List(context.Background(), ListRequest{Root: root}, func(error) { t.Fatal("unexpected warning") })
	if err != nil || len(list.Response.Peers) != 3 || list.Response.Peers[0].Name != "alpha" || list.Response.Peers[1].Name != "hub.example.test" || list.Response.Peers[2].Name != "zeta" {
		t.Fatalf("list=%+v,%v", list, err)
	}
	statePath, err := peerUpstreamStatePath(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(context.Background(), ListRequest{Root: root}, func(error) { t.Fatal("warning before upstream refusal") }); err == nil {
		t.Fatal("invalid upstream state accepted")
	}
	for _, body := range []string{"[", `{"name":"laptop"}`} {
		s.deps.Do = func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/api/v1/peers/laptop" {
				t.Fatal(r.URL)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
		detail, err := s.Get(context.Background(), GetRequest{Root: root, Peer: "laptop"})
		if body == "[" {
			if err == nil {
				t.Fatal("malformed detail accepted")
			}
		} else if err != nil || detail.Name != "laptop" {
			t.Fatalf("detail=%+v,%v", detail, err)
		}
	}
}
