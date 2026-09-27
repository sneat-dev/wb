package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestCoverageEndpoints(t *testing.T) {
	t.Parallel()

	// 1. Coverage unavailable when Coverage is nil
	handlerWithoutCoverage := NewHandler(HandlerOptions{})
	req := httptest.NewRequest(http.MethodGet, CoveragePath, nil)
	rec := httptest.NewRecorder()
	handlerWithoutCoverage.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET %s without store status = %d, want %d", CoveragePath, rec.Code, http.StatusServiceUnavailable)
	}

	// 2. Configure with in-memory store
	backend := newFirestoreMemoryBackend()
	coverageStore := NewRepositoryCoverageStore(backend)
	handler := NewHandler(HandlerOptions{Coverage: coverageStore})

	// GET empty list
	req = httptest.NewRequest(http.MethodGet, CoveragePath, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", CoveragePath, rec.Code, http.StatusOK)
	}
	var emptyList []StoredRepositoryCoverage
	if err := json.NewDecoder(rec.Body).Decode(&emptyList); err != nil {
		t.Fatalf("decode empty list: %v", err)
	}
	if len(emptyList) != 0 {
		t.Errorf("empty list len = %d, want 0", len(emptyList))
	}

	// POST invalid JSON
	req = httptest.NewRequest(http.MethodPost, CoveragePath, bytes.NewReader([]byte("{invalid-json")))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST %s invalid JSON status = %d, want %d", CoveragePath, rec.Code, http.StatusBadRequest)
	}

	// POST valid coverage record
	record := StoredRepositoryCoverage{
		Repository: "sneat-dev/wb",
		SHA:        "1234567890abcdef",
		Ref:        "refs/heads/main",
		ReportedAt: time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC),
		Status:     quality.StatusPassed,
		Statements: 200,
		Covered:    180,
		Percentage: 90.0,
	}
	payload, _ := json.Marshal(record)
	req = httptest.NewRequest(http.MethodPost, CoveragePath, bytes.NewReader(payload))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST %s status = %d, want %d; body: %s", CoveragePath, rec.Code, http.StatusCreated, rec.Body.String())
	}

	// GET single existing coverage
	req = httptest.NewRequest(http.MethodGet, CoveragePath+"/sneat-dev/wb", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s/sneat-dev/wb status = %d, want %d; body: %s", CoveragePath, rec.Code, http.StatusOK, rec.Body.String())
	}
	var got StoredRepositoryCoverage
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode coverage record: %v", err)
	}
	if got.Repository != "github.com/sneat-dev/wb" || got.Statements != 200 || got.Covered != 180 {
		t.Errorf("decoded coverage record mismatch: %+v", got)
	}

	// GET non-existent coverage -> 404
	req = httptest.NewRequest(http.MethodGet, CoveragePath+"/sneat-dev/missing", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET %s/sneat-dev/missing status = %d, want %d", CoveragePath, rec.Code, http.StatusNotFound)
	}

	// GET list with 1 item
	req = httptest.NewRequest(http.MethodGet, CoveragePath, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", CoveragePath, rec.Code, http.StatusOK)
	}
	var list []StoredRepositoryCoverage
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].Repository != "github.com/sneat-dev/wb" {
		t.Errorf("list mismatch: %+v", list)
	}
}

type testMachineBearer struct {
	valid bool
}

func (b testMachineBearer) ResolveMachineBearer(r *http.Request) (Machine, error) {
	if !b.valid {
		return Machine{}, errors.New("invalid bearer")
	}
	return Machine{ID: "machine-1", Name: "laptop", IdentityID: "local"}, nil
}

func TestCoverageAuth(t *testing.T) {
	t.Parallel()

	backend := newFirestoreMemoryBackend()
	coverageStore := NewRepositoryCoverageStore(backend)

	// Machine bearer unauthorized
	handlerUnauthorized := NewHandler(HandlerOptions{
		Coverage:      coverageStore,
		MachineBearer: testMachineBearer{valid: false},
	})
	req := httptest.NewRequest(http.MethodGet, CoveragePath, nil)
	rec := httptest.NewRecorder()
	handlerUnauthorized.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthorized status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	// Machine bearer authorized
	handlerAuthorized := NewHandler(HandlerOptions{
		Coverage:      coverageStore,
		MachineBearer: testMachineBearer{valid: true},
	})
	req = httptest.NewRequest(http.MethodGet, CoveragePath, nil)
	rec = httptest.NewRecorder()
	handlerAuthorized.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("authorized status = %d, want %d", rec.Code, http.StatusOK)
	}

	// ViewerResolver authorized
	handlerViewer := NewHandler(HandlerOptions{
		Coverage: coverageStore,
		ViewerResolver: testViewerResolver{
			resolve: func(r *http.Request) (Viewer, error) {
				return Viewer{Authenticated: true, IdentityID: "user-1"}, nil
			},
		},
	})
	req = httptest.NewRequest(http.MethodGet, CoveragePath, nil)
	rec = httptest.NewRecorder()
	handlerViewer.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("viewer authorized status = %d, want %d", rec.Code, http.StatusOK)
	}

	// Unauthorized GET single and POST
	req = httptest.NewRequest(http.MethodGet, CoveragePath+"/o/r", nil)
	rec = httptest.NewRecorder()
	handlerUnauthorized.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthorized get status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	req = httptest.NewRequest(http.MethodPost, CoveragePath, bytes.NewReader([]byte("{}")))
	rec = httptest.NewRecorder()
	handlerUnauthorized.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthorized post status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

type testViewerResolver struct {
	resolve func(r *http.Request) (Viewer, error)
}

func (v testViewerResolver) Viewer(r *http.Request) (Viewer, error) {
	if v.resolve != nil {
		return v.resolve(r)
	}
	return Viewer{}, errors.New("unauthorized")
}

type mockErrCoverageStore struct {
	listErr error
	getErr  error
	saveErr error
}

func (m mockErrCoverageStore) SaveCoverage(ctx context.Context, record StoredRepositoryCoverage) error {
	return m.saveErr
}
func (m mockErrCoverageStore) GetCoverage(ctx context.Context, repository string) (StoredRepositoryCoverage, bool, error) {
	return StoredRepositoryCoverage{}, false, m.getErr
}
func (m mockErrCoverageStore) ListCoverage(ctx context.Context) ([]StoredRepositoryCoverage, error) {
	return nil, m.listErr
}

func TestCoverageEndpoints_StoreErrors(t *testing.T) {
	t.Parallel()

	// 1. List error -> 503
	hListErr := NewHandler(HandlerOptions{Coverage: mockErrCoverageStore{listErr: errors.New("list failed")}})
	req := httptest.NewRequest(http.MethodGet, CoveragePath, nil)
	rec := httptest.NewRecorder()
	hListErr.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}

	// 1b. List returns nil records slice
	hListNil := NewHandler(HandlerOptions{Coverage: mockErrCoverageStore{}})
	req = httptest.NewRequest(http.MethodGet, CoveragePath, nil)
	rec = httptest.NewRecorder()
	hListNil.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}

	// 2. Get error -> 503
	hGetErr := NewHandler(HandlerOptions{Coverage: mockErrCoverageStore{getErr: errors.New("get failed")}})
	req = httptest.NewRequest(http.MethodGet, CoveragePath+"/o/r", nil)
	rec = httptest.NewRecorder()
	hGetErr.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}

	// 2b. Direct call to apiHandler.getCoverage without path value
	hDirect := apiHandler{options: HandlerOptions{Coverage: mockErrCoverageStore{}}}
	reqDirect := httptest.NewRequest(http.MethodGet, CoveragePath+"/o/r", nil)
	recDirect := httptest.NewRecorder()
	hDirect.getCoverage(recDirect, reqDirect)
	if recDirect.Code != http.StatusNotFound {
		t.Errorf("direct status = %d, want 404", recDirect.Code)
	}

	// 3. Save error -> 400
	hSaveErr := NewHandler(HandlerOptions{Coverage: mockErrCoverageStore{saveErr: errors.New("save failed")}})
	validRecord := StoredRepositoryCoverage{Repository: "o/r", Statements: 10, Covered: 10}
	data, _ := json.Marshal(validRecord)
	req = httptest.NewRequest(http.MethodPost, CoveragePath, bytes.NewReader(data))
	rec = httptest.NewRecorder()
	hSaveErr.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}

	// 4. GET and POST without Coverage configured -> 503
	hNoStore := NewHandler(HandlerOptions{})
	req = httptest.NewRequest(http.MethodGet, CoveragePath+"/o/r", nil)
	rec = httptest.NewRecorder()
	hNoStore.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, CoveragePath, bytes.NewReader(data))
	rec = httptest.NewRecorder()
	hNoStore.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}
