package peers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type fakeSource struct {
	list        []Record
	listErr     error
	detail      Detail
	detailFound bool
	detailErr   error
}

func (source fakeSource) ListPeers(context.Context) ([]Record, error) {
	return source.list, source.listErr
}
func (source fakeSource) GetPeer(context.Context, string) (Detail, bool, error) {
	return source.detail, source.detailFound, source.detailErr
}

func request(t *testing.T, handler http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

func TestNewHandlerListsPeers(t *testing.T) {
	now := time.Now().UTC()
	source := fakeSource{list: []Record{{SchemaVersion: SchemaVersion, ID: "machine_1", Name: "laptop", Role: "downstream", Status: "offline", CreatedAt: now}}}
	handler := NewHandler("/api/v1/peers", source)
	response := request(t, handler, http.MethodGet, "/api/v1/peers")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var body ListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SchemaVersion != SchemaVersion || len(body.Peers) != 1 || body.Peers[0].Name != "laptop" {
		t.Fatalf("list body = %+v", body)
	}
	// The bare prefix, with no trailing slash, is the same list route.
	if response := request(t, handler, http.MethodGet, "/api/v1/peers/"); response.Code != http.StatusOK {
		t.Fatalf("trailing-slash list status = %d, want 200", response.Code)
	}
}

func TestNewHandlerGetsAPeerDetail(t *testing.T) {
	detail := Detail{Record: Record{SchemaVersion: SchemaVersion, ID: "machine_1", Name: "laptop"}, Counters: Counters{}}
	source := fakeSource{detail: detail, detailFound: true}
	handler := NewHandler("/api/v1/peers", source)
	response := request(t, handler, http.MethodGet, "/api/v1/peers/machine_1")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.Code)
	}
	var body Detail
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ID != "machine_1" || body.Session != nil || body.AdminAvailable {
		t.Fatalf("detail body = %+v", body)
	}
}

func TestNewHandlerReportsNotFound(t *testing.T) {
	handler := NewHandler("/api/v1/peers", fakeSource{})
	if response := request(t, handler, http.MethodGet, "/api/v1/peers/no-such-peer"); response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", response.Code)
	}
}

func TestNewHandlerRejectsNonGetMethods(t *testing.T) {
	handler := NewHandler("/api/v1/peers", fakeSource{})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/peers", nil))
	if response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", response.Code)
	}
}

func TestNewHandlerReportsSourceFailures(t *testing.T) {
	handler := NewHandler("/api/v1/peers", fakeSource{listErr: errors.New("boom")})
	if response := request(t, handler, http.MethodGet, "/api/v1/peers"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("list failure status = %d, want 503", response.Code)
	}
	handler = NewHandler("/api/v1/peers", fakeSource{detailErr: errors.New("boom")})
	if response := request(t, handler, http.MethodGet, "/api/v1/peers/machine_1"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("detail failure status = %d, want 503", response.Code)
	}
}

func TestNewHandlerWithoutASourceIsUnavailable(t *testing.T) {
	handler := NewHandler("/api/v1/peers", nil)
	if response := request(t, handler, http.MethodGet, "/api/v1/peers"); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.Code)
	}
}

// TestIdenticalJSONFromTwoMountPrefixes is the AC's "same data on every
// mount" assertion: the same source, mounted at two different prefixes,
// produces byte-identical JSON once the mount-specific prefix is accounted
// for.
func TestIdenticalJSONFromTwoMountPrefixes(t *testing.T) {
	source := fakeSource{list: []Record{{SchemaVersion: SchemaVersion, ID: "machine_1", Name: "laptop", Role: "downstream", Status: "offline"}}}
	local := NewHandler("/api/v1/peers", source)
	hub := NewHandler("/v0/workbench/peers", source)
	localResponse := request(t, local, http.MethodGet, "/api/v1/peers")
	hubResponse := request(t, hub, http.MethodGet, "/v0/workbench/peers")
	if localResponse.Body.String() != hubResponse.Body.String() {
		t.Fatalf("mount JSON differs:\nlocal: %s\nhub:   %s", localResponse.Body.String(), hubResponse.Body.String())
	}
}
