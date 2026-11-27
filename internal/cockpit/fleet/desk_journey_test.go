package fleet_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/cockpit/fleet"
	"github.com/sneat-dev/wb/internal/deskpublish"
	"github.com/sneat-dev/wb/internal/deskrun"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

const journeyMachineID = "0123456789abcdef0123456789abcdef"

// journeyDesk is an in-process Desk that approves the first grant on the
// second poll and records everything the machine sends.
type journeyDesk struct {
	mu        sync.Mutex
	polls     int
	revoked   bool
	snapshots []desk.SnapshotRequest
	beats     int
	requests  int
}

func (d *journeyDesk) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.requests++
		reply := func(status int, body any) {
			writer.WriteHeader(status)
			if body != nil {
				_ = json.NewEncoder(writer).Encode(body)
			}
		}
		route := request.Method + " " + request.URL.Path
		if !strings.HasPrefix(request.URL.Path, "/v0/desk/connect/") && (d.revoked || request.Header.Get("Authorization") != "Bearer cred_journey") {
			reply(http.StatusUnauthorized, nil)
			return
		}
		switch route {
		case "POST " + desk.PathConnectStart:
			reply(http.StatusOK, desk.ConnectStartResponse{DeviceCode: "dev_journey", UserCode: "BCDF-GHJK",
				VerificationURI: "https://desk.example/connect", VerificationURIComplete: "https://desk.example/connect?c=BCDF-GHJK", ExpiresIn: 600, Interval: 1})
		case "POST " + desk.PathConnectToken:
			d.polls++
			if d.polls == 1 {
				reply(http.StatusBadRequest, desk.ConnectTokenResponse{Error: desk.GrantPending})
				return
			}
			reply(http.StatusOK, desk.ConnectTokenResponse{Credential: "cred_journey", Machine: desk.MachineRef{ID: journeyMachineID, Name: "macbook"},
				PersonalSpaceID: "personal1", Scopes: desk.Scopes()})
		case "GET " + desk.PathMachineSelf:
			reply(http.StatusOK, desk.MachineSelf{Machine: desk.MachineRef{ID: journeyMachineID, Name: "macbook"}, PersonalSpaceID: "personal1", Detail: desk.LevelRepositories})
		case "PUT " + desk.PathSnapshot:
			reader, err := gzip.NewReader(request.Body)
			if err != nil {
				t.Errorf("snapshot not gzip: %v", err)
				reply(http.StatusBadRequest, nil)
				return
			}
			var snapshot desk.SnapshotRequest
			if err := desk.Decode(reader, desk.MaxDecodedSnapshotBytes, &snapshot); err != nil {
				t.Errorf("snapshot invalid: %v", err)
				reply(http.StatusUnprocessableEntity, nil)
				return
			}
			d.snapshots = append(d.snapshots, snapshot)
			reply(http.StatusOK, desk.SnapshotReceipt{Hash: snapshot.Hash, AcceptedAt: snapshot.ObservedAt})
		case "POST " + desk.PathHeartbeat:
			var beat desk.HeartbeatRequest
			if err := desk.Decode(request.Body, desk.MaxResponseBytes, &beat); err != nil {
				t.Errorf("heartbeat invalid: %v", err)
			}
			d.beats++
			reply(http.StatusNoContent, nil)
		case "DELETE " + desk.PathMachineSelf:
			d.revoked = true
			reply(http.StatusNoContent, nil)
		default:
			reply(http.StatusNotFound, nil)
		}
	})
}

func (d *journeyDesk) counts() (snapshots, beats, requests int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.snapshots), d.beats, d.requests
}

type journeySource struct {
	mu       sync.Mutex
	envelope fleet.Envelope
}

func (s *journeySource) Export(bool) fleet.Envelope {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.envelope
}
func (s *journeySource) LocalThroughput() *fleet.Throughput { return &fleet.Throughput{WindowDays: 7} }

// TestDeskJourneyConnectPublishRevokeDisconnect walks desk-connect end to end
// over real HTTP against an in-process Desk (desk-connect#ac:one-command-connect,
// #ac:publish-on-change-with-heartbeat, #ac:detail-level-enforced-locally,
// #ac:reconnect-and-disconnect).
func TestDeskJourneyConnectPublishRevokeDisconnect(t *testing.T) {
	t.Parallel()
	recorder := &journeyDesk{}
	server := httptest.NewServer(recorder.handler(t))
	t.Cleanup(server.Close)
	dir := t.TempDir()
	configPath := filepath.Join(dir, "wb.yaml")
	if err := os.WriteFile(configPath, []byte("remote:\n  provider: git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := deskrun.New(deskrun.DefaultPlatform(deskrun.Dependencies{
		ConfigPath:  func() string { return configPath },
		NewClient:   deskrun.NewDeskClient,
		MachineName: func(string) string { return "macbook" },
		MachineID:   func(string) (string, error) { return journeyMachineID, nil },
		OpenBrowser: func(string) error { return nil },
		Restart:     func(context.Context, string) error { return nil },
		Sleep:       func(context.Context, time.Duration) error { return nil },
		Now:         time.Now,
		Random:      strings.NewReader(strings.Repeat("j", deskpublish.TaskKeyBytes)),
		WBVersion:   "0.211.0+journey",
	}), func(code int, message string) error { t.Fatalf("exit %d: %s", code, message); return nil })

	// 1. Connect: one command, approved on the second poll, credential installed.
	var notice bytes.Buffer
	connected, err := service.Connect(context.Background(), deskrun.ConnectRequest{ProjectsRoot: dir, DeskURL: server.URL, Notice: &notice, Input: strings.NewReader("")})
	if err != nil || !strings.Contains(notice.String(), "BCDF-GHJK") {
		t.Fatalf("connect = %+v, %v; notice %q", connected, err, notice.String())
	}
	config, err := wbconfig.LoadDesk(configPath)
	if err != nil || !config.Connected() {
		t.Fatalf("after connect, desk: = %+v, %v", config, err)
	}
	if raw, _ := os.ReadFile(configPath); strings.Contains(string(raw), "cred_journey") || !strings.Contains(string(raw), "provider: git") {
		t.Fatalf("wb.yaml after connect:\n%s", raw)
	}

	// 2. The daemon's publisher, built from that configuration, publishes once.
	key, _, err := deskpublish.LoadOrCreateTaskKey(deskrun.TaskKeyPath(configPath), nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := deskrun.NewDeskClient(config.URL, config.CredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	clock := struct {
		sync.Mutex
		now time.Time
	}{now: time.Now()}
	now := func() time.Time { clock.Lock(); defer clock.Unlock(); return clock.now }
	advance := func(d time.Duration) { clock.Lock(); clock.now = clock.now.Add(d); clock.Unlock() }
	beat := make(chan struct{}, 8)
	publisher := deskpublish.NewPublisher(deskpublish.PublisherOptions{Client: client.(deskpublish.Client), Detail: config.Detail, Key: key,
		MinInterval: config.PublishMinInterval, HeartbeatInterval: config.HeartbeatInterval, WBVersion: "0.211.0+journey", Now: now,
		Wait: func(ctx context.Context, _ time.Duration) error {
			beat <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	source := &journeySource{envelope: fleet.SentinelExport(t)}
	publisher.Publish(ctx, source)
	<-beat
	if snapshots, beats, _ := recorder.counts(); snapshots != 1 || beats != 1 || publisher.Diagnostic() != "" {
		t.Fatalf("first pass: %d snapshots, %d heartbeats, %q", snapshots, beats, publisher.Diagnostic())
	}

	// 3. Nothing at repositories detail names a task or a branch.
	recorder.mu.Lock()
	first := recorder.snapshots[0]
	recorder.mu.Unlock()
	worktree := source.envelope.Fleet.Worktrees[0]
	for _, name := range []string{worktree.Task, worktree.Branch, fleet.Sentinel} {
		if strings.Contains(string(first.Envelope), `"`+name+`"`) {
			t.Fatalf("the published envelope names %q", name)
		}
	}
	if first.Detail != desk.LevelRepositories || first.TaskKeyID != key.ID() || !strings.Contains(string(first.Envelope), `"window_days":7`) {
		t.Fatalf("published %s / %s", first.Detail, first.TaskKeyID)
	}

	// 4. Unchanged: nothing sent. Changed, after the interval: sent again.
	advance(2 * config.PublishMinInterval)
	publisher.Publish(ctx, source)
	if snapshots, _, _ := recorder.counts(); snapshots != 1 {
		t.Fatalf("an unchanged pass sent a snapshot (%d)", snapshots)
	}
	source.mu.Lock()
	changed := *source.envelope.Fleet
	changed.Repositories = append([]fleet.Repository(nil), changed.Repositories...)
	changed.Repositories[0].Name = "acme/gadgets"
	source.envelope.Fleet = &changed
	source.mu.Unlock()
	publisher.Publish(ctx, source)
	if snapshots, _, _ := recorder.counts(); snapshots != 2 {
		t.Fatalf("a changed pass sent %d snapshots in all, want 2", snapshots)
	}

	// 5. Revoked at Desk: the next publish is refused and traffic stops.
	recorder.mu.Lock()
	recorder.revoked = true
	recorder.mu.Unlock()
	source.mu.Lock()
	again := *source.envelope.Fleet
	again.Repositories = append([]fleet.Repository(nil), again.Repositories...)
	again.Repositories[0].Name = "acme/sprockets"
	source.envelope.Fleet = &again
	source.mu.Unlock()
	advance(2 * config.PublishMinInterval)
	publisher.Publish(ctx, source)
	_, _, before := recorder.counts()
	advance(time.Hour)
	publisher.Publish(ctx, source)
	if _, _, after := recorder.counts(); after != before || publisher.Diagnostic() != string(desk.ErrorRevoked) {
		t.Fatalf("after revocation: %d requests then %d, diagnostic %q", before, after, publisher.Diagnostic())
	}

	// 6. Disconnect: the credential and the desk fields are gone, other settings stay.
	recorder.mu.Lock()
	recorder.revoked = false
	recorder.mu.Unlock()
	result, err := service.Disconnect(context.Background(), dir, true)
	if err != nil || !result.Revoked {
		t.Fatalf("disconnect = %+v, %v", result, err)
	}
	if _, err := os.Stat(config.CredentialFile); !os.IsNotExist(err) {
		t.Fatal("the credential remains after disconnect")
	}
	raw, _ := os.ReadFile(configPath)
	if strings.Contains(string(raw), "desk") || !strings.Contains(string(raw), "provider: git") {
		t.Fatalf("wb.yaml after disconnect:\n%s", raw)
	}
	if after, _ := wbconfig.LoadDesk(configPath); after.Connected() {
		t.Fatal("still connected after disconnect")
	}
}
