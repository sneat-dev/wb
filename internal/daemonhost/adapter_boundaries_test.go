package daemonhost

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/hub"
)

type boundarySnapshots struct {
	hub.MachineSnapshotStore
	records []hub.StoredMachineSnapshot
	err     error
}

func (s boundarySnapshots) ListLatest(context.Context) ([]hub.StoredMachineSnapshot, error) {
	return s.records, s.err
}

type boundaryTrust struct {
	hub.PeerTrustStore
	listErr, getErr, findErr error
	finds                    *int
}

func (s boundaryTrust) ListPeers(context.Context) ([]hub.PeerRecord, error) { return nil, s.listErr }
func (s boundaryTrust) GetPeer(context.Context, string) (hub.PeerRecord, bool, error) {
	return hub.PeerRecord{}, false, s.getErr
}
func (s boundaryTrust) FindPeerByName(context.Context, string) (hub.PeerRecord, bool, error) {
	if s.finds != nil {
		*s.finds++
	}
	return hub.PeerRecord{}, false, s.findErr
}

func TestSnapshotReadAdapterPreservesPayloadAndStoreError(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sentinel := errors.New("snapshot store unavailable")
	if _, err := (hubSnapshotReader{store: boundarySnapshots{err: sentinel}}).ListLatest(ctx); err != sentinel {
		t.Fatalf("store error=%v", err)
	}
	at := time.Date(2026, 9, 1, 2, 3, 4, 0, time.UTC)
	snapshot := machinesnapshot.Snapshot{Login: "local", Machine: "laptop"}
	got, err := (hubSnapshotReader{store: boundarySnapshots{records: []hub.StoredMachineSnapshot{{Snapshot: snapshot, ReceivedAt: at, Digest: "digest"}}}}).ListLatest(ctx)
	want := []machinesnapshot.StoredSnapshot{{Snapshot: snapshot, ReceivedAt: at, Digest: "digest"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot conversion=%+v,%v", got, err)
	}
	allowed, err := (localMachineAccess{}).CanViewMachine(ctx, githubapp.Viewer{}, "identity", "machine")
	if !allowed || err != nil {
		t.Fatalf("local owner read access=%t,%v", allowed, err)
	}
}

func TestPeerReadAdapterPreservesMissingAndFailureStages(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sentinel := errors.New("peer trust read failed")
	if got, err := (hubPeerReadSource{}).ListPeers(ctx); got != nil || err != nil {
		t.Fatalf("unconfigured list=%v,%v", got, err)
	}
	if _, found, err := (hubPeerReadSource{}).GetPeer(ctx, "missing"); found || err != nil {
		t.Fatalf("unconfigured detail=%t,%v", found, err)
	}
	if _, err := (hubPeerReadSource{trust: boundaryTrust{listErr: sentinel}}).ListPeers(ctx); err != sentinel {
		t.Fatalf("list error=%v", err)
	}
	for _, stage := range []string{"get", "find", "missing"} {
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			finds := 0
			trust := boundaryTrust{finds: &finds}
			if stage == "get" {
				trust.getErr = sentinel
			}
			if stage == "find" {
				trust.findErr = sentinel
			}
			_, found, err := (hubPeerReadSource{trust: trust}).GetPeer(t.Context(), "missing")
			if found {
				t.Fatal("unexpected peer")
			}
			if stage == "missing" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err != sentinel {
				t.Fatalf("%s error=%v", stage, err)
			}
			wantFinds := 1
			if stage == "get" {
				wantFinds = 0
			}
			if finds != wantFinds {
				t.Fatalf("find calls=%d,want%d", finds, wantFinds)
			}
		})
	}
	source := hubPeerReadSource{}
	record := peerRecordFixture("laptop")
	got := source.recordToRead(ctx, record)
	if got.Lag != nil || got.Name != record.Name {
		t.Fatalf("without queue enrichment=%+v", got)
	}
}

func TestEnrollmentHandlerPreservesUnavailableAndServiceRefusals(t *testing.T) {
	t.Parallel()
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "absent service", false: "unavailable store"}[missing], func(t *testing.T) {
			t.Parallel()
			h := peerAdminHandler{viewer: hub.Viewer{Authenticated: true, IdentityID: "local"}}
			if !missing {
				h.enrollment = &hub.MachineEnrollmentService{}
			}
			out := httptest.NewRecorder()
			h.enroll(out, httptest.NewRequest(http.MethodPost, peersRPCPrefix+"enroll", strings.NewReader(`{"name":"laptop"}`)))
			if out.Code != http.StatusServiceUnavailable || out.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("enrollment=%d %s", out.Code, out.Body.String())
			}
			if !strings.Contains(out.Body.String(), "unavailable") {
				t.Fatalf("refusal=%s", out.Body.String())
			}
		})
	}
	if got := peerAdminErrorStatus(errors.Join(errors.New("store"), hub.ErrUnavailable)); got != http.StatusServiceUnavailable {
		t.Fatalf("wrapped unavailable status=%d", got)
	}
}

func TestWorkbenchRoutingKeepsConnectWithHubAndDashboardWithReadModel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ path, want string }{{"/v0/workbench/peers/connect", "hub"}, {"/v0/workbench/dashboard", "read"}, {"/v0/workbench/stats/commits", "read"}, {"/v0/workbench/peers/laptop", "peers"}, {"/v0/workbench/machines/snapshot", "hub"}} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			calls := 0
			handler := func(name string) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					if r.URL.Path != tc.path {
						t.Fatalf("changed route=%s", r.URL.Path)
					}
					_, _ = w.Write([]byte(name))
				})
			}
			out := httptest.NewRecorder()
			composeWorkbenchAPI(handler("read"), handler("hub"), handler("peers")).ServeHTTP(out, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if out.Body.String() != tc.want || calls != 1 {
				t.Fatalf("route=%s,calls=%d", out.Body.String(), calls)
			}
		})
	}
}
