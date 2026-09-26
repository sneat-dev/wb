package hub

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/quality"
)

type mockArtifactDownloader struct {
	download func(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error)
}

func (m mockArtifactDownloader) DownloadWorkflowArtifact(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error) {
	if m.download != nil {
		return m.download(ctx, installationID, owner, repo, runID, artifactName)
	}
	return nil, ErrArtifactNotFound
}

func createTestZip(t *testing.T, filename string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWorkflowRunHarvester_IgnoredCases(t *testing.T) {
	t.Parallel()

	store := NewRepositoryCoverageStore(newFirestoreMemoryBackend())
	downloader := mockArtifactDownloader{}
	harvester := WorkflowRunHarvester{Store: store, Downloader: downloader}
	ctx := context.Background()

	// 1. Non workflow_run event
	delivery := WebhookDelivery{ID: "d1", Event: "push", Payload: []byte(`{}`)}
	if err := harvester.HarvestWorkflowRun(ctx, delivery); err != nil {
		t.Errorf("expected nil for push event, got %v", err)
	}

	// 2. Action != completed
	payload := `{"action":"in_progress","workflow_run":{"conclusion":null}}`
	delivery = WebhookDelivery{ID: "d2", Event: "workflow_run", Payload: []byte(payload)}
	if err := harvester.HarvestWorkflowRun(ctx, delivery); err != nil {
		t.Errorf("expected nil for in_progress action, got %v", err)
	}

	// 3. Conclusion != success
	payload = `{"action":"completed","workflow_run":{"conclusion":"failure"}}`
	delivery = WebhookDelivery{ID: "d3", Event: "workflow_run", Payload: []byte(payload)}
	if err := harvester.HarvestWorkflowRun(ctx, delivery); err != nil {
		t.Errorf("expected nil for failed conclusion, got %v", err)
	}

	// 4. Non-default branch
	payload = `{"action":"completed","workflow_run":{"conclusion":"success","head_branch":"feature/x","repository":{"default_branch":"main"}}}`
	delivery = WebhookDelivery{ID: "d4", Event: "workflow_run", Payload: []byte(payload)}
	if err := harvester.HarvestWorkflowRun(ctx, delivery); err != nil {
		t.Errorf("expected nil for non-default branch, got %v", err)
	}
}

func TestWorkflowRunHarvester_ArtifactNotFound(t *testing.T) {
	t.Parallel()

	store := NewRepositoryCoverageStore(newFirestoreMemoryBackend())
	downloader := mockArtifactDownloader{
		download: func(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error) {
			return nil, ErrArtifactNotFound
		},
	}
	var narrated []narrate.Line
	harvester := WorkflowRunHarvester{
		Store:      store,
		Downloader: downloader,
		Narrate: func(l narrate.Line) {
			narrated = append(narrated, l)
		},
	}

	payload := `{
		"action": "completed",
		"workflow_run": {
			"id": 123,
			"conclusion": "success",
			"head_branch": "main",
			"repository": {"default_branch": "main", "full_name": "sneat-dev/wb"}
		},
		"repository": {"default_branch": "main", "full_name": "sneat-dev/wb"}
	}`
	delivery := WebhookDelivery{ID: "d-notfound", Event: "workflow_run", Payload: []byte(payload)}

	if err := harvester.HarvestWorkflowRun(context.Background(), delivery); err != nil {
		t.Fatalf("HarvestWorkflowRun failed: %v", err)
	}

	if len(narrated) == 0 || !strings.Contains(narrated[0].Action, "no coverage artifact found") {
		t.Errorf("expected narration of missing artifact, got: %+v", narrated)
	}
}

func TestWorkflowRunHarvester_SuccessfulHarvest(t *testing.T) {
	t.Parallel()

	store := NewRepositoryCoverageStore(newFirestoreMemoryBackend())
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

	summary := quality.CoverageSummary{
		SchemaVersion:  1,
		Repository:     "sneat-dev/wb",
		SHA:            "abcdef0123456789abcdef0123456789abcdef01",
		Ref:            "refs/heads/main",
		WorkflowRunID:  999,
		WorkflowRunURL: "https://github.com/sneat-dev/wb/actions/runs/999",
		ReportedAt:     now,
		Status:         quality.StatusPassed,
		Statements:     2000,
		Covered:        1800,
		Percentage:     90.0,
		Modules: []quality.ModuleCoverageSummary{
			{Path: ".", Statements: 2000, Covered: 1800, Percentage: 90.0},
		},
	}
	rawSummary, _ := json.Marshal(summary)
	zipBytes := createTestZip(t, "wb-coverage-summary.json", rawSummary)

	downloader := mockArtifactDownloader{
		download: func(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error) {
			if owner != "sneat-dev" || repo != "wb" || runID != 999 || artifactName != "wb-coverage-summary" {
				t.Fatalf("unexpected download args: %s/%s run=%d artifact=%s", owner, repo, runID, artifactName)
			}
			return zipBytes, nil
		},
	}

	var narrated []narrate.Line
	harvester := WorkflowRunHarvester{
		Store:      store,
		Downloader: downloader,
		Now:        func() time.Time { return now },
		Narrate: func(l narrate.Line) {
			narrated = append(narrated, l)
		},
	}

	payload := `{
		"action": "completed",
		"workflow_run": {
			"id": 999,
			"conclusion": "success",
			"head_branch": "main",
			"head_sha": "abcdef0123456789abcdef0123456789abcdef01",
			"html_url": "https://github.com/sneat-dev/wb/actions/runs/999",
			"repository": {"default_branch": "main", "full_name": "sneat-dev/wb"}
		},
		"repository": {"default_branch": "main", "full_name": "sneat-dev/wb"},
		"installation": {"id": 42}
	}`
	delivery := WebhookDelivery{ID: "d-harvest", Event: "workflow_run", Payload: []byte(payload)}

	if err := harvester.HarvestWorkflowRun(context.Background(), delivery); err != nil {
		t.Fatalf("HarvestWorkflowRun failed: %v", err)
	}

	// Verify persisted record
	record, found, err := store.GetCoverage(context.Background(), "sneat-dev/wb")
	if err != nil || !found {
		t.Fatalf("GetCoverage(sneat-dev/wb) = %v, found=%t, want found", err, found)
	}

	if record.Statements != 2000 || record.Covered != 1800 || record.Percentage != 90.0 {
		t.Errorf("record metrics = %d/%d (%.2f%%), want 1800/2000 (90%%)", record.Covered, record.Statements, record.Percentage)
	}
	if record.SHA != "abcdef0123456789abcdef0123456789abcdef01" {
		t.Errorf("record.SHA = %q, want abcdef...", record.SHA)
	}

	// Verify narration
	if len(narrated) == 0 || !strings.Contains(narrated[len(narrated)-1].Action, "coverage recorded: 90.00%") {
		t.Errorf("expected narration of coverage recorded, got: %+v", narrated)
	}
}

func TestWorkflowRunHarvester_Unconfigured(t *testing.T) {
	t.Parallel()

	h := WorkflowRunHarvester{}
	delivery := WebhookDelivery{ID: "d", Event: "workflow_run", Payload: []byte(`{}`)}
	if err := h.HarvestWorkflowRun(context.Background(), delivery); !errors.Is(err, ErrCoverageHarvesterClosed) {
		t.Errorf("expected ErrCoverageHarvesterClosed, got %v", err)
	}
}

func TestHTTPArtifactDownloader(t *testing.T) {
	t.Parallel()

	zipData := []byte("PK-test-zip-content")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if strings.HasSuffix(r.URL.Path, "/artifacts") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{
				"total_count": 1,
				"artifacts": [
					{"id": 1001, "name": "wb-coverage-summary", "expired": false}
				]
			}`)
			return
		}

		if strings.HasSuffix(r.URL.Path, "/artifacts/1001/zip") {
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipData)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	downloader := HTTPArtifactDownloader{
		Client:     server.Client(),
		APIBaseURL: server.URL,
		TokenSource: func(ctx context.Context, installationID int64) (string, error) {
			return "test-token", nil
		},
	}

	data, err := downloader.DownloadWorkflowArtifact(context.Background(), 1, "owner", "repo", 555, "wb-coverage-summary")
	if err != nil {
		t.Fatalf("DownloadWorkflowArtifact failed: %v", err)
	}
	if string(data) != string(zipData) {
		t.Errorf("downloaded data = %q, want %q", string(data), string(zipData))
	}

	// Artifact name not found
	_, err = downloader.DownloadWorkflowArtifact(context.Background(), 1, "owner", "repo", 555, "other-artifact")
	if !errors.Is(err, ErrArtifactNotFound) {
		t.Errorf("expected ErrArtifactNotFound, got %v", err)
	}
}

func TestWebhookWithCoverageHarvester(t *testing.T) {
	t.Parallel()

	secret := []byte(strings.Repeat("s", 32))
	store := NewRepositoryCoverageStore(newFirestoreMemoryBackend())

	harvested := false
	mockHarvester := mockCoverageHarvester{
		harvest: func(ctx context.Context, delivery WebhookDelivery) error {
			harvested = true
			return nil
		},
	}

	repoEvents := &RepositoryEventService{}

	handler := NewHandler(HandlerOptions{
		WebhookSecret:     secret,
		Coverage:          store,
		CoverageHarvester: mockHarvester,
		RepositoryEvents:  repoEvents,
	})

	payload := []byte(`{"action":"completed","workflow_run":{"id":123,"conclusion":"success","head_branch":"main"}}`)
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Delivery", "deliv-12345")
	req.Header.Set("X-GitHub-Event", "workflow_run")
	req.Header.Set("X-Hub-Signature-256", sig)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if !harvested {
		t.Error("expected CoverageHarvester to be called")
	}
}

type mockCoverageHarvester struct {
	harvest func(ctx context.Context, delivery WebhookDelivery) error
}

func (m mockCoverageHarvester) HarvestWorkflowRun(ctx context.Context, delivery WebhookDelivery) error {
	if m.harvest != nil {
		return m.harvest(ctx, delivery)
	}
	return nil
}

type errStoreForHarvester struct {
	RepositoryCoverageStore
	saveErr error
}

func (e errStoreForHarvester) SaveCoverage(ctx context.Context, record StoredRepositoryCoverage) error {
	return e.saveErr
}

func TestWorkflowRunHarvester_ErrorBranches(t *testing.T) {
	t.Parallel()
	store := NewRepositoryCoverageStore(newFirestoreMemoryBackend())

	// 1. Invalid JSON payload
	h := WorkflowRunHarvester{Store: store, Downloader: mockArtifactDownloader{}}
	err := h.HarvestWorkflowRun(context.Background(), WebhookDelivery{ID: "d", Event: "workflow_run", Payload: []byte("{invalid-json")})
	if !errors.Is(err, ErrInvalidWorkflowPayload) {
		t.Errorf("expected ErrInvalidWorkflowPayload, got %v", err)
	}

	// 2. Repo with no owner
	payload := `{"action":"completed","workflow_run":{"conclusion":"success","head_branch":"main"},"repository":{"full_name":"noslash","default_branch":"main"}}`
	err = h.HarvestWorkflowRun(context.Background(), WebhookDelivery{ID: "d", Event: "workflow_run", Payload: []byte(payload)})
	if err == nil || !strings.Contains(err.Error(), "has no owner") {
		t.Errorf("expected 'has no owner' error, got %v", err)
	}

	// 3. Downloader generic error
	hDownloaderErr := WorkflowRunHarvester{
		Store: store,
		Downloader: mockArtifactDownloader{
			download: func(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error) {
				return nil, errors.New("network failure")
			},
		},
	}
	payloadValid := `{"action":"completed","workflow_run":{"id":1,"conclusion":"success","head_branch":"main"},"repository":{"full_name":"o/r","default_branch":"main"}}`
	err = hDownloaderErr.HarvestWorkflowRun(context.Background(), WebhookDelivery{ID: "d", Event: "workflow_run", Payload: []byte(payloadValid)})
	if err == nil || !strings.Contains(err.Error(), "download workflow artifact") {
		t.Errorf("expected download workflow artifact error, got %v", err)
	}

	// 4. Corrupted zip extraction error
	hCorruptZip := WorkflowRunHarvester{
		Store: store,
		Downloader: mockArtifactDownloader{
			download: func(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error) {
				return []byte("not-a-zip"), nil
			},
		},
	}
	err = hCorruptZip.HarvestWorkflowRun(context.Background(), WebhookDelivery{ID: "d", Event: "workflow_run", Payload: []byte(payloadValid)})
	if err == nil || !strings.Contains(err.Error(), "extract coverage summary") {
		t.Errorf("expected extract coverage summary error, got %v", err)
	}

	// 5. Store save error
	validSummary := quality.CoverageSummary{Statements: 10, Covered: 10, Percentage: 100}
	summaryJSON, _ := json.Marshal(validSummary)
	zipBytes := createTestZip(t, "coverage-summary.json", summaryJSON)
	hSaveErr := WorkflowRunHarvester{
		Store: errStoreForHarvester{saveErr: errors.New("store disk full")},
		Downloader: mockArtifactDownloader{
			download: func(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error) {
				return zipBytes, nil
			},
		},
	}
	err = hSaveErr.HarvestWorkflowRun(context.Background(), WebhookDelivery{ID: "d", Event: "workflow_run", Payload: []byte(payloadValid)})
	if err == nil || !strings.Contains(err.Error(), "persist coverage") {
		t.Errorf("expected persist coverage error, got %v", err)
	}
}

func TestWorkflowRunHarvester_DefaultsApplied(t *testing.T) {
	t.Parallel()
	store := NewRepositoryCoverageStore(newFirestoreMemoryBackend())

	// Summary without SHA, Ref, WorkflowRunID, etc.
	minimalSummary := quality.CoverageSummary{Statements: 50, Covered: 40, Percentage: 80.0}
	summaryJSON, _ := json.Marshal(minimalSummary)
	zipBytes := createTestZip(t, "wb-coverage-summary.json", summaryJSON)

	h := WorkflowRunHarvester{
		Store: store,
		Downloader: mockArtifactDownloader{
			download: func(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error) {
				return zipBytes, nil
			},
		},
	}

	payload := `{
		"action": "completed",
		"workflow_run": {
			"id": 777,
			"conclusion": "success",
			"head_branch": "main",
			"head_sha": "sha-from-payload",
			"html_url": "https://github.com/o/r/actions/runs/777"
		},
		"repository": {
			"full_name": "o/r",
			"default_branch": "main"
		}
	}`
	delivery := WebhookDelivery{ID: "d-defaults", Event: "workflow_run", Payload: []byte(payload)}
	if err := h.HarvestWorkflowRun(context.Background(), delivery); err != nil {
		t.Fatalf("HarvestWorkflowRun failed: %v", err)
	}

	record, found, err := store.GetCoverage(context.Background(), "o/r")
	if err != nil || !found {
		t.Fatalf("GetCoverage failed: %v", err)
	}
	if record.SHA != "sha-from-payload" {
		t.Errorf("record.SHA = %q, want 'sha-from-payload'", record.SHA)
	}
	if record.Ref != "refs/heads/main" {
		t.Errorf("record.Ref = %q, want 'refs/heads/main'", record.Ref)
	}
	if record.WorkflowRunID != 777 {
		t.Errorf("record.WorkflowRunID = %d, want 777", record.WorkflowRunID)
	}
	if record.WorkflowRunURL != "https://github.com/o/r/actions/runs/777" {
		t.Errorf("record.WorkflowRunURL = %q, want 'https://github.com/o/r/actions/runs/777'", record.WorkflowRunURL)
	}
	if record.Status != quality.StatusPassed {
		t.Errorf("record.Status = %v, want StatusPassed", record.Status)
	}
}

func TestExtractCoverageSummaryFromZip_Errors(t *testing.T) {
	t.Parallel()

	// 1. Invalid zip
	if _, err := extractCoverageSummaryFromZip([]byte("bad-zip")); err == nil {
		t.Error("expected error for bad zip")
	}

	// 2. Zip with no json
	noJSONZip := createTestZip(t, "readme.txt", []byte("hello"))
	if _, err := extractCoverageSummaryFromZip(noJSONZip); err == nil {
		t.Error("expected error for zip without json")
	}

	// 3. Zip with invalid json
	badJSONZip := createTestZip(t, "coverage.json", []byte("invalid-json"))
	if _, err := extractCoverageSummaryFromZip(badJSONZip); err == nil {
		t.Error("expected error for zip with bad json")
	}
}

func TestHTTPArtifactDownloader_Errors(t *testing.T) {
	t.Parallel()

	// 1. TokenSource error
	downloaderTokenErr := HTTPArtifactDownloader{
		TokenSource: func(ctx context.Context, installationID int64) (string, error) {
			return "", errors.New("token acquisition failed")
		},
	}
	_, err := downloaderTokenErr.DownloadWorkflowArtifact(context.Background(), 1, "o", "r", 123, "art")
	if err == nil || !strings.Contains(err.Error(), "acquire token") {
		t.Errorf("expected acquire token error, got %v", err)
	}

	// 2. List artifacts non-200
	server500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer server500.Close()

	d500 := HTTPArtifactDownloader{
		APIBaseURL: server500.URL,
	}
	_, err = d500.DownloadWorkflowArtifact(context.Background(), 1, "o", "r", 123, "art")
	if err == nil || !strings.Contains(err.Error(), "list artifacts status 500") {
		t.Errorf("expected list artifacts status 500, got %v", err)
	}

	// 3. List artifacts bad json
	serverBadJSON := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "bad-json")
	}))
	defer serverBadJSON.Close()

	dBadJSON := HTTPArtifactDownloader{
		APIBaseURL: serverBadJSON.URL,
	}
	_, err = dBadJSON.DownloadWorkflowArtifact(context.Background(), 1, "o", "r", 123, "art")
	if err == nil || !strings.Contains(err.Error(), "decode artifacts list") {
		t.Errorf("expected decode artifacts list error, got %v", err)
	}

	// 4. Download artifact non-200
	serverDownload500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/artifacts") {
			_, _ = io.WriteString(w, `{"artifacts":[{"id":10,"name":"art","expired":false}]}`)
			return
		}
		http.Error(w, "download error", http.StatusBadGateway)
	}))
	defer serverDownload500.Close()

	dDownload500 := HTTPArtifactDownloader{
		APIBaseURL: serverDownload500.URL,
	}
	_, err = dDownload500.DownloadWorkflowArtifact(context.Background(), 1, "o", "r", 123, "art")
	if err == nil || !strings.Contains(err.Error(), "download artifact zip status 502") {
		t.Errorf("expected download artifact zip status 502, got %v", err)
	}

	// 5. Network error on list artifacts
	dNetErr := HTTPArtifactDownloader{
		APIBaseURL: "http://127.0.0.1:0", // unreachable
	}
	_, err = dNetErr.DownloadWorkflowArtifact(context.Background(), 1, "o", "r", 123, "art")
	if err == nil {
		t.Error("expected network error on list artifacts")
	}

	// 6. Network error on download artifact
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/artifacts") {
			_, _ = io.WriteString(w, `{"artifacts":[{"id":10,"name":"art","expired":false}]}`)
			return
		}
		// Redirect to dead address
		http.Redirect(w, r, "http://127.0.0.1:0/dead", http.StatusFound)
	}))
	defer redirectServer.Close()

	dDownloadNetErr := HTTPArtifactDownloader{
		APIBaseURL: redirectServer.URL,
	}
	_, err = dDownloadNetErr.DownloadWorkflowArtifact(context.Background(), 1, "o", "r", 123, "art")
	if err == nil {
		t.Error("expected network error on download artifact")
	}

	// 7. Invalid URL error
	dInvalidURL := HTTPArtifactDownloader{
		APIBaseURL: "http://example.com/\x7f",
	}
	_, err = dInvalidURL.DownloadWorkflowArtifact(context.Background(), 1, "o", "r", 123, "art")
	if err == nil {
		t.Error("expected error for invalid URL")
	}

	// 8. Download body read error
	bodyErrServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/artifacts") {
			_, _ = io.WriteString(w, `{"artifacts":[{"id":10,"name":"art","expired":false}]}`)
			return
		}
		// Announce 1000 bytes but close connection
		w.Header().Set("Content-Length", "1000")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		hj, ok := w.(http.Hijacker)
		if ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
	}))
	defer bodyErrServer.Close()

	dBodyErr := HTTPArtifactDownloader{
		APIBaseURL: bodyErrServer.URL,
	}
	_, err = dBodyErr.DownloadWorkflowArtifact(context.Background(), 1, "o", "r", 123, "art")
	if err == nil {
		t.Error("expected body read error")
	}
}

func TestWorkflowRunHarvester_DefaultBranchFallbackAndNow(t *testing.T) {
	t.Parallel()
	store := NewRepositoryCoverageStore(newFirestoreMemoryBackend())

	fixedTime := time.Date(2026, 9, 26, 15, 30, 0, 0, time.UTC)
	summary := quality.CoverageSummary{Statements: 10, Covered: 10, Percentage: 100}
	summaryJSON, _ := json.Marshal(summary)
	zipBytes := createTestZip(t, "wb-coverage-summary.json", summaryJSON)

	h := WorkflowRunHarvester{
		Store: store,
		Downloader: mockArtifactDownloader{
			download: func(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error) {
				return zipBytes, nil
			},
		},
		Now: func() time.Time { return fixedTime },
	}

	// Payload with NO default_branch in repository or workflow_run.repository, head_branch="main"
	payload := `{"action":"completed","workflow_run":{"conclusion":"success","head_branch":"main"},"repository":{"full_name":"o/r"}}`
	delivery := WebhookDelivery{ID: "d-main-fallback", Event: "workflow_run", Payload: []byte(payload)}
	if err := h.HarvestWorkflowRun(context.Background(), delivery); err != nil {
		t.Fatalf("HarvestWorkflowRun failed: %v", err)
	}

	record, found, err := store.GetCoverage(context.Background(), "o/r")
	if err != nil || !found {
		t.Fatalf("GetCoverage failed: %v", err)
	}
	if !record.ReportedAt.Equal(fixedTime) {
		t.Errorf("record.ReportedAt = %v, want %v", record.ReportedAt, fixedTime)
	}
	if record.Ref != "refs/heads/main" {
		t.Errorf("record.Ref = %q, want 'refs/heads/main'", record.Ref)
	}
}

func TestExtractCoverageSummaryFromZip_UnsupportedAlgorithm(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("coverage.json")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("data"))
	_ = zw.Close()

	data := buf.Bytes()
	cdIdx := bytes.Index(data, []byte("\x50\x4b\x01\x02"))
	if cdIdx != -1 && len(data) > cdIdx+11 {
		data[cdIdx+10] = 0xFE
	}

	_, err = extractCoverageSummaryFromZip(data)
	if err == nil || !strings.Contains(err.Error(), "open file coverage.json in zip") {
		t.Errorf("expected open file in zip error, got: %v", err)
	}
}
