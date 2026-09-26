package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/githubapp"
	"github.com/sneat-dev/wb/internal/quality"
)

const repositoryCoverageCollection = "workbench_repository_coverage"

var errRepositoryCoverageStoreUnavailable = errors.New("workbench repository coverage store is unavailable")

// StoredRepositoryCoverage is the persisted coverage record for one repository.
type StoredRepositoryCoverage struct {
	Repository     string                            `json:"repository"`
	Owner          string                            `json:"owner"`
	Name           string                            `json:"name"`
	Ref            string                            `json:"ref"`
	SHA            string                            `json:"sha"`
	WorkflowRunID  int64                             `json:"workflow_run_id,omitempty"`
	WorkflowRunURL string                            `json:"workflow_run_url,omitempty"`
	ReportedAt     time.Time                         `json:"reported_at"`
	Status         quality.Status                    `json:"status"`
	Statements     int                               `json:"statements"`
	Covered        int                               `json:"covered"`
	Percentage     float64                           `json:"percentage"`
	Modules        []quality.ModuleCoverageSummary   `json:"modules,omitempty"`
	Packages       map[string]quality.PackageSummary `json:"packages,omitempty"`
}

// RepositoryCoverageStore persists and lists repository test coverage reports.
type RepositoryCoverageStore interface {
	SaveCoverage(ctx context.Context, record StoredRepositoryCoverage) error
	GetCoverage(ctx context.Context, repository string) (StoredRepositoryCoverage, bool, error)
	ListCoverage(ctx context.Context) ([]StoredRepositoryCoverage, error)
}

type repositoryCoverageStore struct {
	backend githubapp.DocumentStore
}

// NewRepositoryCoverageStore binds the repository coverage store to backend.
func NewRepositoryCoverageStore(backend githubapp.DocumentStore) RepositoryCoverageStore {
	return repositoryCoverageStore{backend: backend}
}

func repositoryCoverageDocumentID(repository string) string {
	digest := sha256.Sum256([]byte(canonicalRepository(repository)))
	return hex.EncodeToString(digest[:])
}

func (store repositoryCoverageStore) SaveCoverage(ctx context.Context, record StoredRepositoryCoverage) error {
	if store.backend == nil {
		return errRepositoryCoverageStoreUnavailable
	}
	canonical := canonicalRepository(record.Repository)
	if canonical == "" || canonical == "github.com/" {
		return errors.New("repository is required for coverage record")
	}
	record.Repository = canonical
	parts := strings.Split(strings.TrimPrefix(canonical, "github.com/"), "/")
	if len(parts) == 2 {
		if record.Owner == "" {
			record.Owner = parts[0]
		}
		if record.Name == "" {
			record.Name = parts[1]
		}
	}
	id := repositoryCoverageDocumentID(canonical)
	if err := store.backend.Set(ctx, repositoryCoverageCollection, id, record); err != nil {
		return fmt.Errorf("save repository coverage for %s: %w", canonical, err)
	}
	return nil
}

func (store repositoryCoverageStore) GetCoverage(ctx context.Context, repository string) (StoredRepositoryCoverage, bool, error) {
	if store.backend == nil {
		return StoredRepositoryCoverage{}, false, errRepositoryCoverageStoreUnavailable
	}
	canonical := canonicalRepository(repository)
	id := repositoryCoverageDocumentID(canonical)
	var record StoredRepositoryCoverage
	found, err := store.backend.Get(ctx, repositoryCoverageCollection, id, &record)
	if err != nil {
		return StoredRepositoryCoverage{}, false, fmt.Errorf("read repository coverage for %s: %w", canonical, err)
	}
	return record, found, nil
}

func (store repositoryCoverageStore) ListCoverage(ctx context.Context) ([]StoredRepositoryCoverage, error) {
	if store.backend == nil {
		return nil, errRepositoryCoverageStoreUnavailable
	}
	var records []StoredRepositoryCoverage
	if err := store.backend.Query(ctx, repositoryCoverageCollection, nil, 0, &records); err != nil {
		return nil, fmt.Errorf("list repository coverage records: %w", err)
	}
	return records, nil
}

var _ RepositoryCoverageStore = repositoryCoverageStore{}
