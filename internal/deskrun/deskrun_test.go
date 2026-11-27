package deskrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/cockpitrun"
	"github.com/sneat-dev/wb/internal/deskclient"
	"github.com/sneat-dev/wb/internal/deskpublish"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

const machineID = "0123456789abcdef0123456789abcdef"

type coded struct {
	code    int
	message string
}

func (c *coded) Error() string { return c.message }

func exitFactory(code int, message string) error { return &coded{code: code, message: message} }

func exitCode(err error) int {
	var c *coded
	if errors.As(err, &c) {
		return c.code
	}
	return -1
}

// fakeClient scripts Desk: Token answers in order, the rest by field.
type fakeClient struct {
	startErr   error
	tokens     []desk.ConnectTokenResponse
	tokenErr   error
	selfErr    error
	revokeErr  error
	self       desk.MachineSelf
	started    *desk.ConnectStartRequest
	credential string
	publishErr error
	beatErr    error
	pushed     []desk.SnapshotRequest
	beats      []desk.HeartbeatRequest
}

func (f *fakeClient) Start(_ context.Context, request desk.ConnectStartRequest) (desk.ConnectStartResponse, error) {
	f.started = &request
	return desk.ConnectStartResponse{DeviceCode: "dev_1", UserCode: "BCDF-GHJK", VerificationURI: "https://desk.example/connect",
		VerificationURIComplete: "https://desk.example/connect?c=BCDF-GHJK", ExpiresIn: 600, Interval: 5}, f.startErr
}

func (f *fakeClient) Token(context.Context, string) (desk.ConnectTokenResponse, error) {
	if f.tokenErr != nil {
		return desk.ConnectTokenResponse{}, f.tokenErr
	}
	answer := f.tokens[0]
	if len(f.tokens) > 1 {
		f.tokens = f.tokens[1:]
	}
	return answer, nil
}

func (f *fakeClient) Self(context.Context) (desk.MachineSelf, error) { return f.self, f.selfErr }
func (f *fakeClient) Revoke(context.Context) error                   { return f.revokeErr }

func (f *fakeClient) Publish(_ context.Context, request desk.SnapshotRequest) (desk.SnapshotReceipt, error) {
	f.pushed = append(f.pushed, request)
	return desk.SnapshotReceipt{Hash: request.Hash, AcceptedAt: request.ObservedAt}, f.publishErr
}

func (f *fakeClient) Heartbeat(_ context.Context, request desk.HeartbeatRequest) error {
	f.beats = append(f.beats, request)
	return f.beatErr
}

func issued() desk.ConnectTokenResponse {
	return desk.ConnectTokenResponse{Credential: "cred_1", Machine: desk.MachineRef{ID: machineID, Name: "macbook"},
		PersonalSpaceID: "personal1", Shares: []desk.Share{{SpaceID: "team1", Level: desk.ShareRepositories, State: desk.SharePending}},
		Scopes: desk.Scopes()}
}

type harness struct {
	t          *testing.T
	dir        string
	configPath string
	client     *fakeClient
	clientErr  []error
	opened     []string
	openErr    error
	restarts   int
	restartErr error
	sleeps     []time.Duration
	sleepErr   error
	now        time.Time
	machineErr error
	notice     bytes.Buffer
	exported   cockpitrun.ExportResult
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	return &harness{t: t, dir: dir, configPath: filepath.Join(dir, "wb.yaml"),
		client: &fakeClient{tokens: []desk.ConnectTokenResponse{issued()}}, now: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)}
}

func (h *harness) service() *Service {
	return New(Dependencies{
		ConfigPath: func() string { return h.configPath },
		NewClient: func(_, credentialFile string) (Client, error) {
			if len(h.clientErr) > 0 {
				err := h.clientErr[0]
				h.clientErr = h.clientErr[1:]
				if err != nil {
					return nil, err
				}
			}
			if credentialFile != "" {
				raw, _ := os.ReadFile(credentialFile)
				h.client.credential = strings.TrimSpace(string(raw))
			}
			return h.client, nil
		},
		MachineName: func(string) string { return "macbook" },
		MachineID:   func(string) (string, error) { return machineID, h.machineErr },
		OpenBrowser: func(address string) error { h.opened = append(h.opened, address); return h.openErr },
		Restart:     func(context.Context, string) error { h.restarts++; return h.restartErr },
		Sleep: func(_ context.Context, d time.Duration) error {
			h.sleeps = append(h.sleeps, d)
			h.now = h.now.Add(d)
			return h.sleepErr
		},
		Now:       func() time.Time { return h.now },
		Random:    strings.NewReader(strings.Repeat("r", 4*deskpublish.TaskKeyBytes)),
		Export:    func(context.Context, string) cockpitrun.ExportResult { return h.exported },
		WBVersion: "0.211.0", OS: "linux", Arch: "amd64",
	}, exitFactory)
}

func (h *harness) connect(change func(*ConnectRequest)) (ConnectResult, error) {
	request := ConnectRequest{ProjectsRoot: h.dir, CanOpen: true, Notice: &h.notice, Input: strings.NewReader("")}
	if change != nil {
		change(&request)
	}
	return h.service().Connect(context.Background(), request)
}

func (h *harness) writeConfig(body string) {
	h.t.Helper()
	if err := os.WriteFile(h.configPath, []byte(body), 0o600); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) connected() string {
	h.t.Helper()
	credential := filepath.Join(h.dir, "credentials", "desk-macbook.token")
	if err := os.MkdirAll(filepath.Dir(credential), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(credential, []byte("cred_0\n"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	h.writeConfig("remote:\n  provider: git\ndesk:\n  url: https://desk.example\n  machine_id: " + machineID +
		"\n  space_id: personal1\n  credential_file: " + credential + "\n  detail: repositories\n")
	return credential
}

func TestConnectApprovesInstallsAndRestarts(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.client.tokens = []desk.ConnectTokenResponse{{Error: desk.GrantPending}, {Error: desk.GrantSlowDown}, issued()}
	result, err := h.connect(func(r *ConnectRequest) { r.SpaceHint = "team1" })
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(h.sleeps) != "[5s 5s 10s]" {
		t.Fatalf("slow_down adds 5s to the interval: %v", h.sleeps)
	}
	if !strings.Contains(h.notice.String(), "BCDF-GHJK") || len(h.opened) != 1 || strings.Contains(h.notice.String(), "dev_1") {
		t.Fatalf("notice %q, opened %v", h.notice.String(), h.opened)
	}
	if h.client.started.SpaceHint != "team1" || h.client.started.Detail != desk.LevelRepositories || h.client.credential != "cred_1" {
		t.Fatalf("start %+v; verified with %q", h.client.started, h.client.credential)
	}
	info, err := os.Stat(result.CredentialFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("credential file: %v", err)
	}
	if _, err := os.Stat(result.CredentialFile + ".pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the staged credential must not remain")
	}
	config, _ := wbconfig.LoadDesk(h.configPath)
	if !config.Connected() || config.SpaceID != "personal1" || strings.Join(config.SharedSpaceIDs, ",") != "team1" {
		t.Fatalf("config = %+v", config)
	}
	raw, _ := os.ReadFile(h.configPath)
	if strings.Contains(string(raw), "cred_1") {
		t.Fatal("the credential must never be in wb.yaml")
	}
	if !result.TaskKeyCreated || result.TaskKeyID == "" || h.restarts != 1 || !result.DaemonRestarted || len(result.Shares) != 1 {
		t.Fatalf("result = %+v, restarts %d", result, h.restarts)
	}
}

func TestConnectHonoursBrowserAndRestartChoices(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if _, err := h.connect(func(r *ConnectRequest) { r.NoBrowser, r.NoRestart, r.DeskURL = true, true, "https://other.example" }); err != nil {
		t.Fatal(err)
	}
	if len(h.opened) != 0 || h.restarts != 0 {
		t.Fatalf("opened %v, restarts %d", h.opened, h.restarts)
	}
	if config, _ := wbconfig.LoadDesk(h.configPath); config.URL != "https://other.example" {
		t.Fatalf("url = %s", config.URL)
	}
	failing := newHarness(t)
	failing.openErr = errors.New("no display")
	if _, err := failing.connect(nil); err != nil || !strings.Contains(failing.notice.String(), "open the address above yourself") {
		t.Fatalf("a browser failure is a notice, not an error: %v %q", err, failing.notice.String())
	}
}

func TestConnectGrantOutcomes(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		change func(*harness)
		code   int
		text   string
	}{
		"denied":        {func(h *harness) { h.client.tokens = []desk.ConnectTokenResponse{{Error: desk.GrantAccessDenied}} }, 1, "denied"},
		"expired":       {func(h *harness) { h.client.tokens = []desk.ConnectTokenResponse{{Error: desk.GrantExpired}} }, 1, "expired"},
		"never decided": {func(h *harness) { h.client.tokens = []desk.ConnectTokenResponse{{Error: desk.GrantPending}} }, 1, "expired"},
		"token failure": {func(h *harness) { h.client.tokenErr = &deskclient.Error{Code: desk.ErrorUnreachable} }, 1, "wait for approval"},
		"start failure": {func(h *harness) { h.client.startErr = &deskclient.Error{Code: desk.ErrorTimeout} }, 1, "open a Desk connection"},
		"bad desk url":  {func(h *harness) { h.clientErr = []error{deskclient.ErrInvalidURL} }, 2, "desk url"},
		"bad machine":   {func(h *harness) {}, 2, "pass a valid --machine"},
		"not verified":  {func(h *harness) { h.client.selfErr = &deskclient.Error{Code: desk.ErrorRevoked} }, 1, "verify the Desk credential"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			tc.change(h)
			_, err := h.connect(func(r *ConnectRequest) {
				if name == "bad machine" {
					r.Machine = "not a name"
				}
			})
			if exitCode(err) != tc.code || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("error = %v (code %d)", err, exitCode(err))
			}
			if _, statErr := os.Stat(h.configPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatal("a failed connect must not write the configuration")
			}
		})
	}
	cancelled := newHarness(t)
	cancelled.sleepErr = context.Canceled
	if _, err := cancelled.connect(nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("an interrupted wait = %v", err)
	}
}

func TestConnectRefusesBadInputsBeforeContactingDesk(t *testing.T) {
	t.Parallel()
	malformed := newHarness(t)
	malformed.writeConfig("desk:\n  detail: everything\n")
	if _, err := malformed.connect(nil); exitCode(err) != 2 {
		t.Fatalf("malformed config = %v", err)
	}
	badDetail := newHarness(t)
	if _, err := badDetail.connect(func(r *ConnectRequest) { r.Detail, r.DetailChanged = "summary", true }); exitCode(err) != 2 {
		t.Fatalf("summary detail = %v", err)
	}
	noID := newHarness(t)
	noID.machineErr = errors.New("no state dir")
	if _, err := noID.connect(nil); err == nil || !strings.Contains(err.Error(), "machine id") {
		t.Fatalf("machine id failure = %v", err)
	}
	for _, h := range []*harness{malformed, badDetail, noID} {
		if h.client.started != nil {
			t.Fatal("Desk must not be contacted")
		}
	}
}

func TestConnectAdoptsAPipedTaskKey(t *testing.T) {
	t.Parallel()
	key, _ := deskpublish.NewTaskKey(strings.NewReader(strings.Repeat("k", deskpublish.TaskKeyBytes)))
	h := newHarness(t)
	result, err := h.connect(func(r *ConnectRequest) { r.TaskKeyStdin, r.Input = true, strings.NewReader(key.Text()+"\n") })
	if err != nil || result.TaskKeyID != key.ID() || result.TaskKeyCreated {
		t.Fatalf("adopted key = %+v, %v", result, err)
	}
	other, _ := deskpublish.NewTaskKey(strings.NewReader(strings.Repeat("o", deskpublish.TaskKeyBytes)))
	_, err = h.connect(func(r *ConnectRequest) { r.TaskKeyStdin, r.Input = true, strings.NewReader(other.Text()) })
	if exitCode(err) != 1 || !strings.Contains(err.Error(), "different Desk task key") || strings.Contains(err.Error(), "--token-file") {
		t.Fatalf("a different key = %v", err)
	}
	for name, input := range map[string]string{"empty": "", "garbage": "not-a-key"} {
		fresh := newHarness(t)
		if _, err := fresh.connect(func(r *ConnectRequest) { r.TaskKeyStdin, r.Input = true, strings.NewReader(input) }); exitCode(err) != 2 {
			t.Errorf("%s key = %v", name, err)
		}
	}
	exposed := newHarness(t)
	keyPath := TaskKeyPath(exposed.configPath)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte(key.Text()), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exposed.connect(nil); err == nil || !strings.Contains(err.Error(), "other users") {
		t.Fatalf("a readable key file = %v", err)
	}
}

func TestConnectLocalWriteFailures(t *testing.T) {
	t.Parallel()
	verifierFails := newHarness(t)
	verifierFails.clientErr = []error{nil, errors.New("verifier")}
	if _, err := verifierFails.connect(nil); err == nil || !strings.Contains(err.Error(), "verifier") {
		t.Fatalf("verifier construction failure = %v", err)
	}
	occupied := newHarness(t)
	if err := os.MkdirAll(filepath.Join(occupied.dir, "credentials", "desk-macbook.token", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := occupied.connect(nil); err == nil || !strings.Contains(err.Error(), "install the Desk credential") {
		t.Fatalf("an occupied credential path = %v", err)
	}
	unstageable := newHarness(t)
	if err := os.MkdirAll(filepath.Join(unstageable.dir, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := deskpublish.LoadOrCreateTaskKey(TaskKeyPath(unstageable.configPath), strings.NewReader(strings.Repeat("s", 32))); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(unstageable.dir, "credentials", "desk-macbook.token.pending"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unstageable.dir, "credentials", "desk-macbook.token.pending", "x"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := unstageable.connect(nil); err == nil {
		t.Fatal("a staging path that cannot be replaced must fail")
	}
	unwritable := newHarness(t)
	if err := os.MkdirAll(filepath.Join(unwritable.dir, "credentials"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unwritable.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unwritable.dir, 0o700) })
	if os.Getuid() != 0 {
		if _, err := unwritable.connect(nil); err == nil || !strings.Contains(err.Error(), "config") {
			t.Fatalf("an unwritable configuration directory = %v", err)
		}
	}
	restartFails := newHarness(t)
	restartFails.restartErr = errors.New("daemon")
	if _, err := restartFails.connect(nil); err == nil || !strings.Contains(err.Error(), "daemon restart failed") {
		t.Fatalf("restart failure = %v", err)
	}
}

func TestConnectWithOnlyDetailChangesTheLevelInPlace(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connected()
	result, err := h.connect(func(r *ConnectRequest) { r.Detail, r.DetailChanged = desk.LevelFull, true })
	if err != nil || !result.DetailOnly || result.Detail != desk.LevelFull || h.client.started != nil || h.restarts != 1 {
		t.Fatalf("detail change = %+v, %v; started %v", result, err, h.client.started)
	}
	if config, _ := wbconfig.LoadDesk(h.configPath); config.Detail != desk.LevelFull || !config.Connected() {
		t.Fatalf("config = %+v", config)
	}
	restartFails := newHarness(t)
	restartFails.connected()
	restartFails.restartErr = errors.New("daemon")
	if _, err := restartFails.connect(func(r *ConnectRequest) { r.Detail, r.DetailChanged = desk.LevelFull, true }); err == nil {
		t.Fatal("a failed restart is reported")
	}
	exposed := newHarness(t)
	exposed.connected()
	if err := os.WriteFile(TaskKeyPath(exposed.configPath), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exposed.connect(func(r *ConnectRequest) { r.Detail, r.DetailChanged = desk.LevelFull, true }); err == nil {
		t.Fatal("a readable key file is refused")
	}
	unwritable := newHarness(t)
	unwritable.connected()
	if _, _, err := deskpublish.LoadOrCreateTaskKey(TaskKeyPath(unwritable.configPath), strings.NewReader(strings.Repeat("u", 32))); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unwritable.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unwritable.dir, 0o700) })
	if os.Getuid() != 0 {
		if _, err := unwritable.connect(func(r *ConnectRequest) { r.Detail, r.DetailChanged = desk.LevelFull, true }); err == nil {
			t.Fatal("an unwritable configuration is reported")
		}
	}
	reconnect := newHarness(t)
	reconnect.connected()
	if result, err := reconnect.connect(func(r *ConnectRequest) { r.Detail, r.DetailChanged, r.Machine = desk.LevelFull, true, "macbook" }); err != nil || result.DetailOnly {
		t.Fatalf("--detail with another flag reconnects: %+v, %v", result, err)
	}
}

func TestDisconnectRevokesThenRemovesLocally(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	credential := h.connected()
	result, err := h.service().Disconnect(context.Background(), h.dir, false)
	if err != nil || !result.Revoked || !result.DaemonRestarted {
		t.Fatalf("disconnect = %+v, %v", result, err)
	}
	if _, err := os.Stat(credential); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the credential must be removed")
	}
	raw, _ := os.ReadFile(h.configPath)
	if strings.Contains(string(raw), "desk") || !strings.Contains(string(raw), "provider: git") {
		t.Fatalf("config after disconnect:\n%s", raw)
	}
	for name, revoke := range map[string]error{"already revoked": &deskclient.Error{Code: desk.ErrorRevoked}, "unreachable": &deskclient.Error{Code: desk.ErrorUnreachable}} {
		other := newHarness(t)
		other.connected()
		other.client.revokeErr = revoke
		result, err := other.service().Disconnect(context.Background(), other.dir, true)
		if err != nil || result.Revoked != (name == "already revoked") || result.DaemonRestarted {
			t.Errorf("%s: %+v, %v", name, result, err)
		}
	}
	noClient := newHarness(t)
	noClient.connected()
	noClient.clientErr = []error{errors.New("no client")}
	if result, err := noClient.service().Disconnect(context.Background(), noClient.dir, true); err != nil || result.Revoked {
		t.Fatalf("no client = %+v, %v", result, err)
	}
}

func TestDisconnectFailures(t *testing.T) {
	t.Parallel()
	notConnected := newHarness(t)
	if _, err := notConnected.service().Disconnect(context.Background(), notConnected.dir, true); exitCode(err) != 1 {
		t.Fatalf("not connected = %v", err)
	}
	malformed := newHarness(t)
	malformed.writeConfig("desk: [\n")
	if _, err := malformed.service().Disconnect(context.Background(), malformed.dir, true); exitCode(err) != 2 {
		t.Fatalf("malformed = %v", err)
	}
	stuck := newHarness(t)
	credential := stuck.connected()
	if err := os.Remove(credential); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(credential, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := stuck.service().Disconnect(context.Background(), stuck.dir, true); err == nil || !strings.Contains(err.Error(), "remove the Desk credential") {
		t.Fatalf("an unremovable credential = %v", err)
	}
	gone := newHarness(t)
	if err := os.Remove(gone.connected()); err != nil {
		t.Fatal(err)
	}
	if _, err := gone.service().Disconnect(context.Background(), gone.dir, true); err != nil {
		t.Fatalf("an already removed credential is fine: %v", err)
	}
	restartFails := newHarness(t)
	restartFails.connected()
	restartFails.restartErr = errors.New("daemon")
	if _, err := restartFails.service().Disconnect(context.Background(), restartFails.dir, false); err == nil {
		t.Fatal("a failed restart is reported")
	}
	unwritable := newHarness(t)
	unwritable.connected()
	if err := os.Chmod(unwritable.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unwritable.dir, 0o700) })
	if os.Getuid() != 0 {
		if _, err := unwritable.service().Disconnect(context.Background(), unwritable.dir, true); err == nil {
			t.Fatal("an unwritable configuration is reported")
		}
	}
}

func TestStatusReportsConnectionAndDeskErrors(t *testing.T) {
	t.Parallel()
	none := newHarness(t)
	result, err := none.service().Status(context.Background())
	if err != nil || result.Connected || result.Fix != "wb desk connect" {
		t.Fatalf("not connected = %+v, %v", result, err)
	}
	malformed := newHarness(t)
	malformed.writeConfig("desk: [\n")
	if _, err := malformed.service().Status(context.Background()); exitCode(err) != 2 {
		t.Fatalf("malformed = %v", err)
	}
	h := newHarness(t)
	h.connected()
	at := time.Date(2026, 11, 27, 11, 59, 0, 0, time.UTC)
	h.client.self = desk.MachineSelf{PersonalSpaceID: "personal1", Shares: []desk.Share{{SpaceID: "team1", Level: desk.ShareFull, State: desk.ShareActive}},
		LastSnapshotAt: at, LastHeartbeatAt: at}
	result, err = h.service().Status(context.Background())
	if err != nil || !result.Connected || result.DeskError != "" || result.LastSnapshotAt == nil || len(result.Shares) != 1 {
		t.Fatalf("connected = %+v, %v", result, err)
	}
	h.client.self = desk.MachineSelf{PersonalSpaceID: "personal1"}
	if result, _ = h.service().Status(context.Background()); result.LastSnapshotAt != nil || result.LastHeartbeatAt != nil {
		t.Fatalf("never published = %+v", result)
	}
	h.client.selfErr = &deskclient.Error{Code: desk.ErrorRevoked}
	if result, _ = h.service().Status(context.Background()); result.DeskError != desk.ErrorRevoked || result.Fix != "wb desk connect" {
		t.Fatalf("revoked = %+v", result)
	}
	h.client.selfErr = errors.New("credential file is readable by others")
	if result, _ = h.service().Status(context.Background()); result.DeskError != desk.ErrorUnreachable || result.Fix != "" {
		t.Fatalf("local failure = %+v", result)
	}
	h.clientErr = []error{errors.New("no client")}
	if result, _ = h.service().Status(context.Background()); result.DeskError != desk.ErrorUnreachable {
		t.Fatalf("no client = %+v", result)
	}
}

func TestTaskKeyExportOnlyToAPipe(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	var out bytes.Buffer
	if err := h.service().ExportTaskKey(&out, true); exitCode(err) != 2 || out.Len() != 0 {
		t.Fatalf("terminal = %v", err)
	}
	if err := h.service().ExportTaskKey(&out, false); exitCode(err) != 1 {
		t.Fatalf("no key = %v", err)
	}
	key, _, err := deskpublish.LoadOrCreateTaskKey(TaskKeyPath(h.configPath), strings.NewReader(strings.Repeat("e", 32)))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.service().ExportTaskKey(&out, false); err != nil || strings.TrimSpace(out.String()) != key.Text() {
		t.Fatalf("export = %q, %v", out.String(), err)
	}
	if err := os.Chmod(TaskKeyPath(h.configPath), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.service().ExportTaskKey(&out, false); err == nil {
		t.Fatal("a readable key file is refused")
	}
}

func TestDefaultsSleepAndClient(t *testing.T) {
	t.Parallel()
	if deps := DefaultPlatform(Dependencies{}); deps.OS == "" || deps.Arch == "" {
		t.Fatalf("platform = %+v", deps)
	}
	if err := Sleep(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Sleep(cancelled, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sleep = %v", err)
	}
	if _, err := NewDeskClient("http://desk.example", ""); err == nil {
		t.Fatal("plain http off loopback is refused")
	}
	if client, err := NewDeskClient(desk.DefaultURL, ""); err != nil || client == nil {
		t.Fatalf("default client = %v", err)
	}
}

func exportOf(t *testing.T) cockpitrun.ExportResult {
	t.Helper()
	envelope := fleet.Envelope{SchemaVersion: fleet.ExportSchemaVersion, Machine: "macbook", Fleet: &fleet.Document{
		RepositoriesTotal: 1, Repositories: []fleet.Repository{{Entry: fleet.Entry{ID: "rp-1"}, Name: "acme/widgets"}},
		Worktrees: []fleet.Worktree{{Entry: fleet.Entry{ID: "wt-a"}, Repository: "rp-1", Task: "task-a", Branch: "feature/a"}}}}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return cockpitrun.ExportResult{Body: body, Throughput: &fleet.Throughput{WindowDays: 7}}
}

func TestPushPublishesTheDaemonsStateReducedLikeTheDaemon(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connected()
	h.exported = exportOf(t)
	result, err := h.service().Push(context.Background(), h.dir)
	if err != nil || !result.Heartbeat || result.Detail != desk.LevelRepositories || len(h.client.pushed) != 1 {
		t.Fatalf("push = %+v, %v", result, err)
	}
	sent := string(h.client.pushed[0].Envelope)
	if strings.Contains(sent, "task-a") || strings.Contains(sent, "feature/a") || !strings.Contains(sent, `"window_days":7`) {
		t.Fatalf("the pushed envelope is the repositories reduction with throughput: %s", sent)
	}
	if h.client.beats[0].LastSnapshotHash != result.Hash || h.client.credential != "cred_0" {
		t.Fatalf("heartbeat %+v, credential %q", h.client.beats[0], h.client.credential)
	}
	h.client.beatErr = &deskclient.Error{Code: desk.ErrorTimeout}
	if result, err := h.service().Push(context.Background(), h.dir); err != nil || result.Heartbeat {
		t.Fatalf("a failed heartbeat after a publish is reported, not fatal: %+v, %v", result, err)
	}
	full := newHarness(t)
	full.connected()
	full.writeConfig(strings.Replace(mustRead(t, full.configPath), "detail: repositories", "detail: full", 1))
	full.exported = exportOf(t)
	if _, err := full.service().Push(context.Background(), full.dir); err != nil || !strings.Contains(string(full.client.pushed[0].Envelope), "task-a") {
		t.Fatalf("full detail push = %v", err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPushFailures(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		setup func(*harness)
		code  int
		text  string
	}{
		"not connected": {func(h *harness) {}, 1, "not connected"},
		"malformed":     {func(h *harness) { h.writeConfig("desk: [\n") }, 2, ""},
		"no daemon": {func(h *harness) {
			h.connected()
			h.exported = cockpitrun.ExportResult{Failure: cockpitrun.ExportFailure(fleet.ErrorDaemonNotRunning)}
		}, 1, "wb daemon start"},
		"export failed": {func(h *harness) { h.connected(); h.exported = cockpitrun.ExportResult{Failure: "warming_up"} }, 1, "warming_up"},
		"bad body":      {func(h *harness) { h.connected(); h.exported = cockpitrun.ExportResult{Body: []byte("{")} }, -1, "read the daemon's export"},
		"metrics only":  {func(h *harness) { h.connected(); h.exported = cockpitrun.ExportResult{Body: []byte("{}")} }, 1, "nothing to publish"},
		"no client": {func(h *harness) {
			h.connected()
			h.exported = exportOf(t)
			h.clientErr = []error{errors.New("bad url")}
		}, 2, "bad url"},
		"desk refuses": {func(h *harness) {
			h.connected()
			h.exported = exportOf(t)
			h.client.publishErr = &deskclient.Error{Code: desk.ErrorRevoked}
		}, 1, "revoked"},
		"exposed key": {func(h *harness) {
			h.connected()
			h.exported = exportOf(t)
			_ = os.WriteFile(TaskKeyPath(h.configPath), []byte("x"), 0o644)
		}, -1, "other users"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			tc.setup(h)
			_, err := h.service().Push(context.Background(), h.dir)
			if err == nil || exitCode(err) != tc.code || !strings.Contains(err.Error(), tc.text) {
				t.Fatalf("error = %v (code %d)", err, exitCode(err))
			}
		})
	}
}

func TestExportFromDaemonFindsNoDaemonUnderAnEmptyRoot(t *testing.T) {
	t.Parallel()
	if got := ExportFromDaemon(context.Background(), t.TempDir()); got.Failure != cockpitrun.ExportFailure(fleet.ErrorDaemonNotRunning) {
		t.Fatalf("export under an empty root = %+v", got.Failure)
	}
}

func TestADeniedConnectLeavesNoTaskKeyBehind(t *testing.T) {
	t.Parallel()
	key, _ := deskpublish.NewTaskKey(strings.NewReader(strings.Repeat("k", deskpublish.TaskKeyBytes)))
	for name, piped := range map[string]bool{"created": false, "piped": true} {
		h := newHarness(t)
		h.client.tokens = []desk.ConnectTokenResponse{{Error: desk.GrantAccessDenied}}
		_, err := h.connect(func(r *ConnectRequest) {
			if piped {
				r.TaskKeyStdin, r.Input = true, strings.NewReader(key.Text())
			}
		})
		if exitCode(err) != 1 {
			t.Fatalf("%s: denied = %v", name, err)
		}
		if _, statErr := os.Stat(TaskKeyPath(h.configPath)); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("%s: a denied connect wrote the task key", name)
		}
	}
}

func TestAPausedMachineCanStillBeDisconnectedAndShowsPaused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	credential := h.connected()
	h.writeConfig(mustRead(t, h.configPath) + "  enabled: false\n")
	status, err := h.service().Status(context.Background())
	if err != nil || !status.Connected || !status.Paused {
		t.Fatalf("paused status = %+v, %v", status, err)
	}
	result, err := h.service().Disconnect(context.Background(), h.dir, true)
	if err != nil || !result.Revoked {
		t.Fatalf("disconnect of a paused machine = %+v, %v", result, err)
	}
	if _, err := os.Stat(credential); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the credential of a paused machine must be removed too")
	}
}

func TestStatusReportsALocalCredentialProblemAsLocal(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connected()
	h.client.selfErr = fmt.Errorf("read: %w", deskclient.ErrCredential)
	status, err := h.service().Status(context.Background())
	if err != nil || status.DeskError != "" || !strings.Contains(status.Problem, "credential file") || !strings.Contains(status.Fix, "chmod 600") {
		t.Fatalf("credential problem = %+v, %v", status, err)
	}
}

func TestConnectReportsATaskKeyThatCannotBeInstalled(t *testing.T) {
	t.Parallel()
	key, _ := deskpublish.NewTaskKey(strings.NewReader(strings.Repeat("k", deskpublish.TaskKeyBytes)))
	h := newHarness(t)
	if err := os.MkdirAll(TaskKeyPath(h.configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := h.connect(func(r *ConnectRequest) { r.TaskKeyStdin, r.Input = true, strings.NewReader(key.Text()) })
	if err == nil || exitCode(err) != -1 || !strings.Contains(err.Error(), "task key") {
		t.Fatalf("an uninstallable key = %v", err)
	}
}

func TestPushRefusesWhilePublishingIsPaused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.connected()
	h.writeConfig(mustRead(t, h.configPath) + "  enabled: false\n")
	h.exported = exportOf(t)
	if _, err := h.service().Push(context.Background(), h.dir); exitCode(err) != 1 || !strings.Contains(err.Error(), "paused") || len(h.client.pushed) != 0 {
		t.Fatalf("paused push = %v", err)
	}
}
