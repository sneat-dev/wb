package deskclient

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/desk"
)

const (
	machineID = "0123456789abcdef0123456789abcdef"
	hash      = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

var observed = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

func credentialFile(t *testing.T, token string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "desk.token")
	if err := os.WriteFile(path, []byte(token+"\n"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeDesk answers each route with the handler registered for "METHOD path".
func fakeDesk(t *testing.T, routes map[string]http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler, ok := routes[request.Method+" "+request.URL.Path]
		if !ok {
			http.NotFound(writer, request)
			return
		}
		handler(writer, request)
	}))
	t.Cleanup(server.Close)
	client, err := New(server.URL, Options{CredentialFile: credentialFile(t, "cred_1", 0o600)})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func answer(status int, body string) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, body)
	}
}

func snapshotRequest(envelope string) desk.SnapshotRequest {
	return desk.SnapshotRequest{SchemaVersion: desk.SchemaVersion, Detail: desk.LevelFull, ObservedAt: observed,
		Hash: hash, Envelope: json.RawMessage(envelope)}
}

func wantCode(t *testing.T, err error, code desk.ErrorCode) {
	t.Helper()
	if CodeOf(err) != code {
		t.Fatalf("error = %v, want desk code %s", err, code)
	}
}

func TestNewAcceptsOnlyAddressesACredentialMaySeek(t *testing.T) {
	t.Parallel()
	for _, address := range []string{"http://desk.sneat.dev", "https://desk.sneat.dev/v0", "ftp://x", "https://user@desk.sneat.dev"} {
		if _, err := New(address, Options{}); !errors.Is(err, ErrInvalidURL) {
			t.Errorf("New(%q) = %v", address, err)
		}
	}
	client, err := New("https://Desk.Sneat.dev/", Options{})
	if err != nil || client.origin != "https://desk.sneat.dev" || client.http.Timeout != DefaultTotalTimeout {
		t.Fatalf("client = %+v, %v", client, err)
	}
	custom, _ := New("http://127.0.0.1:9", Options{Total: time.Second, Connect: time.Second})
	if custom.http.Timeout != time.Second {
		t.Fatalf("timeout = %v", custom.http.Timeout)
	}
	if (&Error{Code: desk.ErrorRevoked}).Error() != "desk: revoked" || CodeOf(errors.New("other")) != "" {
		t.Fatal("an Error names only its code")
	}
}

func TestConnectFlowSendsNoBearerAndSeparatesGrantErrors(t *testing.T) {
	t.Parallel()
	grant := `{"device_code":"dev_1","user_code":"BCDF-GHJK","verification_uri":"https://desk.sneat.dev/connect",` +
		`"verification_uri_complete":"https://desk.sneat.dev/connect?c=BCDF-GHJK","expires_in":600,"interval":5}`
	issued := `{"credential":"cred_2","machine":{"id":"` + machineID + `","name":"macbook"},"personal_space_id":"p1",` +
		`"scopes":["desk_snapshot:publish","desk_heartbeat:write","desk_machine:read_self"]}`
	tokenAnswers := []http.HandlerFunc{answer(400, `{"error":"authorization_pending"}`), answer(200, issued),
		answer(200, `{"error":"slow_down"}`), answer(400, issued)}
	calls := 0
	client, _ := fakeDesk(t, map[string]http.HandlerFunc{
		"POST " + desk.PathConnectStart: func(writer http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Authorization") != "" {
				t.Error("the connect routes carry no bearer")
			}
			answer(200, grant)(writer, request)
		},
		"POST " + desk.PathConnectToken: func(writer http.ResponseWriter, request *http.Request) {
			tokenAnswers[calls](writer, request)
			calls++
		},
	})
	start := desk.ConnectStartRequest{SchemaVersion: desk.SchemaVersion, Machine: desk.MachineRef{ID: machineID, Name: "macbook"},
		WBVersion: "0.211.0", OS: "linux", Arch: "amd64", Detail: desk.LevelRepositories}
	opened, err := client.Start(t.Context(), start)
	if err != nil || opened.UserCode != "BCDF-GHJK" {
		t.Fatalf("Start() = %+v, %v", opened, err)
	}
	if _, err := client.Start(t.Context(), desk.ConnectStartRequest{}); !errors.Is(err, desk.ErrInvalid) {
		t.Fatalf("an invalid start request is refused before sending: %v", err)
	}
	pending, err := client.Token(t.Context(), "dev_1")
	if err != nil || pending.Error != desk.GrantPending {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	done, err := client.Token(t.Context(), "dev_1")
	if err != nil || done.Credential != "cred_2" {
		t.Fatalf("issued = %+v, %v", done, err)
	}
	_, err = client.Token(t.Context(), "dev_1")
	wantCode(t, err, desk.ErrorRejectedPayload) // a grant error must not come with 200
	_, err = client.Token(t.Context(), "dev_1")
	wantCode(t, err, desk.ErrorRejectedPayload) // a credential must not come with 400
	if _, err := client.Token(t.Context(), ""); !errors.Is(err, desk.ErrInvalid) {
		t.Fatalf("an empty device code is refused: %v", err)
	}
}

func TestAuthenticatedRoutesCarryThePrivateCredential(t *testing.T) {
	t.Parallel()
	self := `{"machine":{"id":"` + machineID + `","name":"macbook"},"personal_space_id":"p1","detail":"full"}`
	var published []byte
	client, server := fakeDesk(t, map[string]http.HandlerFunc{
		"GET " + desk.PathMachineSelf: func(writer http.ResponseWriter, request *http.Request) {
			if request.Header.Get("Authorization") != "Bearer cred_1" {
				t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
			}
			answer(200, self)(writer, request)
		},
		"DELETE " + desk.PathMachineSelf: answer(204, ""),
		"PUT " + desk.PathSnapshot: func(writer http.ResponseWriter, request *http.Request) {
			reader, err := gzip.NewReader(request.Body)
			if err != nil || request.Header.Get("Content-Encoding") != "gzip" {
				t.Errorf("the snapshot must be gzip-encoded: %v", err)
				return
			}
			published, _ = io.ReadAll(reader)
			answer(200, `{"hash":"`+hash+`","accepted_at":"2026-11-27T12:00:01Z"}`)(writer, request)
		},
		"POST " + desk.PathHeartbeat: answer(204, ""),
	})
	record, err := client.Self(t.Context())
	if err != nil || record.Machine.Name != "macbook" {
		t.Fatalf("Self() = %+v, %v", record, err)
	}
	if err := client.Revoke(t.Context()); err != nil {
		t.Fatalf("Revoke() = %v", err)
	}
	gone, _ := fakeDesk(t, map[string]http.HandlerFunc{"DELETE " + desk.PathMachineSelf: answer(404, "")})
	if err := gone.Revoke(t.Context()); err != nil {
		t.Fatalf("revoking a machine Desk no longer has is done: %v", err)
	}
	receipt, err := client.Publish(t.Context(), snapshotRequest(`{"schema_version":1}`))
	if err != nil || receipt.Hash != hash || !strings.Contains(string(published), `"envelope":{"schema_version":1}`) {
		t.Fatalf("Publish() = %+v, %v; sent %s", receipt, err, published)
	}
	heartbeat := desk.HeartbeatRequest{SchemaVersion: desk.SchemaVersion, ObservedAt: observed, WBVersion: "0.211.0"}
	if err := client.Heartbeat(t.Context(), heartbeat); err != nil {
		t.Fatalf("Heartbeat() = %v", err)
	}
	if err := client.Heartbeat(t.Context(), desk.HeartbeatRequest{}); !errors.Is(err, desk.ErrInvalid) {
		t.Fatalf("an invalid heartbeat is refused: %v", err)
	}
	if _, err := client.Publish(t.Context(), desk.SnapshotRequest{}); !errors.Is(err, desk.ErrInvalid) {
		t.Fatalf("an invalid snapshot is refused: %v", err)
	}
	for name, mode := range map[string]os.FileMode{"readable": 0o644} {
		unprotected, _ := New(server.URL, Options{CredentialFile: credentialFile(t, "cred_1", mode)})
		if _, err := unprotected.Self(t.Context()); !errors.Is(err, ErrCredential) {
			t.Errorf("%s credential = %v", name, err)
		}
	}
	for name, token := range map[string]string{"empty": "", "spaced": "a b", "long": strings.Repeat("a", maxCredentialBytes+1)} {
		bad, _ := New(server.URL, Options{CredentialFile: credentialFile(t, token, 0o600)})
		if _, err := bad.Self(t.Context()); !errors.Is(err, ErrCredential) {
			t.Errorf("%s credential = %v", name, err)
		}
	}
	missing, _ := New(server.URL, Options{CredentialFile: filepath.Join(t.TempDir(), "none")})
	if err := missing.Revoke(t.Context()); !errors.Is(err, ErrCredential) {
		t.Errorf("missing credential = %v", err)
	}
}

func TestPublishRefusesOversizedSnapshotsLocally(t *testing.T) {
	t.Parallel()
	sent := false
	client, _ := fakeDesk(t, map[string]http.HandlerFunc{"PUT " + desk.PathSnapshot: func(http.ResponseWriter, *http.Request) { sent = true }})
	huge := `{"x":"` + strings.Repeat("a", desk.MaxDecodedSnapshotBytes) + `"}`
	_, err := client.Publish(t.Context(), snapshotRequest(huge))
	wantCode(t, err, desk.ErrorTooLarge)
	noise := make([]byte, 2<<20)
	_, _ = rand.Read(noise)
	incompressible := `{"x":"` + base64.StdEncoding.EncodeToString(noise) + `"}`
	_, err = client.Publish(t.Context(), snapshotRequest(incompressible))
	wantCode(t, err, desk.ErrorTooLarge)
	if sent {
		t.Fatal("an oversized snapshot must never be sent")
	}
}

func TestPublishRejectsAReceiptForAnotherSnapshot(t *testing.T) {
	t.Parallel()
	other := "sha256:" + strings.Repeat("f", 64)
	client, _ := fakeDesk(t, map[string]http.HandlerFunc{
		"PUT " + desk.PathSnapshot: answer(200, `{"hash":"`+other+`","accepted_at":"2026-11-27T12:00:01Z"}`)})
	_, err := client.Publish(t.Context(), snapshotRequest(`{}`))
	wantCode(t, err, desk.ErrorRejectedPayload)
}

func TestStatusesMapToTypedCodesWithoutResponseText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		handler http.HandlerFunc
		code    desk.ErrorCode
		check   func(*Error) bool
	}{
		{"unauthorized", answer(401, "token revoked by admin"), desk.ErrorRevoked, nil},
		{"forbidden by a proxy", answer(403, ""), desk.ErrorUnreachable, nil},
		{"too large", answer(413, ""), desk.ErrorTooLarge, nil},
		{"bad request", answer(400, "secret detail"), desk.ErrorRejectedPayload, nil},
		{"unprocessable", answer(422, ""), desk.ErrorRejectedPayload, nil},
		{"upgrade", answer(426, `{"minimum_schema_version":2}`), desk.ErrorSchemaUnsupported,
			func(e *Error) bool { return e.MinimumSchemaVersion == 2 }},
		{"upgrade without body", answer(426, "update wb"), desk.ErrorSchemaUnsupported,
			func(e *Error) bool { return e.MinimumSchemaVersion == 0 }},
		{"rate limited", func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Retry-After", "120")
			answer(429, "")(writer, request)
		}, desk.ErrorRateLimited, func(e *Error) bool { return e.RetryAfter == 2*time.Minute }},
		{"rate limited without hint", answer(429, ""), desk.ErrorRateLimited, func(e *Error) bool { return e.RetryAfter == 0 }},
		{"server error", answer(503, ""), desk.ErrorUnreachable, nil},
		{"redirect", func(writer http.ResponseWriter, request *http.Request) {
			http.Redirect(writer, request, "https://elsewhere.example/", http.StatusFound)
		}, desk.ErrorUnreachable, nil},
		{"malformed success", answer(200, `{"machine":`), desk.ErrorRejectedPayload, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client, _ := fakeDesk(t, map[string]http.HandlerFunc{"GET " + desk.PathMachineSelf: tc.handler})
			_, err := client.Self(t.Context())
			wantCode(t, err, tc.code)
			if strings.Contains(err.Error(), "admin") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("response text leaked into %q", err)
			}
			var typed *Error
			if errors.As(err, &typed); tc.check != nil && !tc.check(typed) {
				t.Fatalf("error = %+v", typed)
			}
		})
	}
}

func TestTransportFailuresAreUnreachableOrTimeout(t *testing.T) {
	t.Parallel()
	slow := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(slow.Close)
	client, _ := New(slow.URL, Options{CredentialFile: credentialFile(t, "cred_1", 0o600), Total: 50 * time.Millisecond})
	_, err := client.Self(t.Context())
	wantCode(t, err, desk.ErrorTimeout)

	closed := httptest.NewServer(http.NotFoundHandler())
	address := closed.URL
	closed.Close()
	gone, _ := New(address, Options{CredentialFile: credentialFile(t, "cred_1", 0o600)})
	_, err = gone.Self(t.Context())
	wantCode(t, err, desk.ErrorUnreachable)

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = gone.Self(cancelled)
	wantCode(t, err, desk.ErrorUnreachable)

	broken := &Client{origin: "http://%zz", http: http.DefaultClient}
	if err := broken.Revoke(t.Context()); err == nil || CodeOf(err) != "" {
		t.Fatalf("an unbuildable request is a local error, got %v", err)
	}
}
