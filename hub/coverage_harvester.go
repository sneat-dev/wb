package hub

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/quality"
)

var (
	ErrArtifactNotFound        = errors.New("coverage artifact not found")
	ErrInvalidWorkflowPayload  = errors.New("invalid workflow_run webhook payload")
	ErrCoverageHarvesterClosed = errors.New("coverage harvester is closed or unconfigured")
)

// CoverageHarvester processes workflow_run webhook events and harvests published artifacts.
type CoverageHarvester interface {
	HarvestWorkflowRun(ctx context.Context, delivery WebhookDelivery) error
}

// ArtifactDownloader fetches build artifact archive bytes from GitHub.
type ArtifactDownloader interface {
	DownloadWorkflowArtifact(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error)
}

// WorkflowRunHarvester extracts and persists coverage summaries from successful default-branch workflow runs.
type WorkflowRunHarvester struct {
	Store      RepositoryCoverageStore
	Downloader ArtifactDownloader
	Now        func() time.Time
	Narrate    func(narrate.Line)
}

func (h WorkflowRunHarvester) narrateLine(event, subject, action string) {
	if h.Narrate == nil {
		return
	}
	at := time.Now()
	if h.Now != nil {
		at = h.Now()
	}
	h.Narrate(narrate.Line{At: at, Event: event, Subject: subject, Action: action})
}

type workflowRunPayload struct {
	Action      string `json:"action"`
	WorkflowRun struct {
		ID         int64     `json:"id"`
		Name       string    `json:"name"`
		HeadBranch string    `json:"head_branch"`
		HeadSHA    string    `json:"head_sha"`
		Conclusion string    `json:"conclusion"`
		HTMLURL    string    `json:"html_url"`
		UpdatedAt  time.Time `json:"updated_at"`
		Repository struct {
			ID            int64  `json:"id"`
			FullName      string `json:"full_name"`
			DefaultBranch string `json:"default_branch"`
		} `json:"repository"`
	} `json:"workflow_run"`
	Repository struct {
		ID            int64  `json:"id"`
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	Installation struct {
		ID int64 `json:"id"`
	} `json:"installation"`
}

func (h WorkflowRunHarvester) HarvestWorkflowRun(ctx context.Context, delivery WebhookDelivery) error {
	if h.Store == nil || h.Downloader == nil {
		return ErrCoverageHarvesterClosed
	}
	if delivery.Event != "workflow_run" {
		return nil
	}

	var payload workflowRunPayload
	if err := json.Unmarshal(delivery.Payload, &payload); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkflowPayload, err)
	}

	repoFullName := payload.Repository.FullName
	if repoFullName == "" {
		repoFullName = payload.WorkflowRun.Repository.FullName
	}
	if repoFullName == "" {
		repoFullName = delivery.Repository
	}

	if payload.Action != "completed" {
		h.narrateLine(delivery.Event, repoFullName, "ignored: run not completed")
		return nil
	}
	if payload.WorkflowRun.Conclusion != "success" {
		h.narrateLine(delivery.Event, repoFullName, "ignored: conclusion not success ("+payload.WorkflowRun.Conclusion+")")
		return nil
	}

	defaultBranch := payload.Repository.DefaultBranch
	if defaultBranch == "" {
		defaultBranch = payload.WorkflowRun.Repository.DefaultBranch
	}
	if defaultBranch == "" {
		defaultBranch = "main"
	}

	if payload.WorkflowRun.HeadBranch != defaultBranch {
		h.narrateLine(delivery.Event, repoFullName, "ignored: branch "+payload.WorkflowRun.HeadBranch+" != "+defaultBranch)
		return nil
	}

	owner, repoName, found := strings.Cut(repoFullName, "/")
	if !found {
		return fmt.Errorf("%w: repository %q has no owner", ErrInvalidWorkflowPayload, repoFullName)
	}

	zipBytes, err := h.Downloader.DownloadWorkflowArtifact(ctx, payload.Installation.ID, owner, repoName, payload.WorkflowRun.ID, "wb-coverage-summary")
	if err != nil {
		if errors.Is(err, ErrArtifactNotFound) {
			h.narrateLine(delivery.Event, repoFullName, "no coverage artifact found in workflow run")
			return nil
		}
		return fmt.Errorf("download workflow artifact: %w", err)
	}

	summary, err := extractCoverageSummaryFromZip(zipBytes)
	if err != nil {
		return fmt.Errorf("extract coverage summary: %w", err)
	}

	targetRepo := summary.Repository
	if targetRepo == "" {
		targetRepo = repoFullName
	}

	stored := StoredRepositoryCoverage{
		Repository:     targetRepo,
		Owner:          owner,
		Name:           repoName,
		Ref:            summary.Ref,
		SHA:            summary.SHA,
		WorkflowRunID:  summary.WorkflowRunID,
		WorkflowRunURL: summary.WorkflowRunURL,
		ReportedAt:     summary.ReportedAt,
		Status:         summary.Status,
		Statements:     summary.Statements,
		Covered:        summary.Covered,
		Percentage:     summary.Percentage,
		Modules:        summary.Modules,
		Packages:       summary.Packages,
	}
	if stored.SHA == "" {
		stored.SHA = payload.WorkflowRun.HeadSHA
	}
	if stored.Ref == "" {
		stored.Ref = "refs/heads/" + defaultBranch
	}
	if stored.WorkflowRunID == 0 {
		stored.WorkflowRunID = payload.WorkflowRun.ID
	}
	if stored.WorkflowRunURL == "" {
		stored.WorkflowRunURL = payload.WorkflowRun.HTMLURL
	}
	if stored.ReportedAt.IsZero() {
		at := time.Now().UTC()
		if h.Now != nil {
			at = h.Now().UTC()
		}
		stored.ReportedAt = at
	}
	if stored.Status == "" {
		stored.Status = quality.StatusPassed
	}

	if err := h.Store.SaveCoverage(ctx, stored); err != nil {
		return fmt.Errorf("persist coverage: %w", err)
	}

	h.narrateLine(delivery.Event, stored.Repository, fmt.Sprintf("coverage recorded: %.2f%% (%d/%d statements)", stored.Percentage, stored.Covered, stored.Statements))
	return nil
}

func extractCoverageSummaryFromZip(zipBytes []byte) (quality.CoverageSummary, error) {
	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return quality.CoverageSummary{}, fmt.Errorf("invalid zip archive: %w", err)
	}

	var jsonFile *zip.File
	for _, f := range reader.File {
		if strings.HasSuffix(f.Name, ".json") {
			jsonFile = f
			if f.Name == "coverage-summary.json" || f.Name == "wb-coverage-summary.json" {
				break
			}
		}
	}

	if jsonFile == nil {
		return quality.CoverageSummary{}, errors.New("no json summary file found in artifact archive")
	}

	rc, err := jsonFile.Open()
	if err != nil {
		return quality.CoverageSummary{}, fmt.Errorf("open file %s in zip: %w", jsonFile.Name, err)
	}
	defer func() { _ = rc.Close() }()

	var summary quality.CoverageSummary
	if err := json.NewDecoder(rc).Decode(&summary); err != nil {
		return quality.CoverageSummary{}, fmt.Errorf("decode coverage summary from %s: %w", jsonFile.Name, err)
	}
	return summary, nil
}

// HTTPArtifactDownloader fetches artifacts via the GitHub REST API.
type HTTPArtifactDownloader struct {
	Client      *http.Client
	APIBaseURL  string
	TokenSource func(ctx context.Context, installationID int64) (string, error)
}

func (d HTTPArtifactDownloader) DownloadWorkflowArtifact(ctx context.Context, installationID int64, owner, repo string, runID int64, artifactName string) ([]byte, error) {
	client := d.Client
	if client == nil {
		client = http.DefaultClient
	}
	baseURL := d.APIBaseURL
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	baseURL = strings.TrimRight(baseURL, "/")

	var token string
	if d.TokenSource != nil {
		t, err := d.TokenSource(ctx, installationID)
		if err != nil {
			return nil, fmt.Errorf("acquire token for installation %d: %w", installationID, err)
		}
		token = t
	}

	// 1. List artifacts for run
	endpoint := fmt.Sprintf("%s/repos/%s/%s/actions/runs/%d/artifacts", baseURL, owner, repo, runID)
	req, err := d.newRequest(ctx, endpoint, token)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("list artifacts status %d", resp.StatusCode)
	}

	var list struct {
		TotalCount int `json:"total_count"`
		Artifacts  []struct {
			ID                 int64  `json:"id"`
			Name               string `json:"name"`
			ArchiveDownloadURL string `json:"archive_download_url"`
			Expired            bool   `json:"expired"`
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode artifacts list: %w", err)
	}

	var targetArtifactID int64
	for _, a := range list.Artifacts {
		if a.Name == artifactName && !a.Expired {
			targetArtifactID = a.ID
			break
		}
	}

	if targetArtifactID == 0 {
		return nil, ErrArtifactNotFound
	}

	// 2. Download artifact zip
	downloadEndpoint := fmt.Sprintf("%s/repos/%s/%s/actions/artifacts/%d/zip", baseURL, owner, repo, targetArtifactID)
	downloadReq, _ := d.newRequest(ctx, downloadEndpoint, token)

	downloadResp, err := client.Do(downloadReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = downloadResp.Body.Close() }()

	if downloadResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download artifact zip status %d", downloadResp.StatusCode)
	}

	data, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		return nil, fmt.Errorf("read artifact zip body: %w", err)
	}
	return data, nil
}

func (d HTTPArtifactDownloader) newRequest(ctx context.Context, endpoint, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}
