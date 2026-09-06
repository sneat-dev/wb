package githubapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	ProjectionCollection  = "workbench_projections"
	SeriesCollection      = "workbench_series"
	LeaderboardCollection = "workbench_leaderboards"
	MergeCollection       = "workbench_latest_merges"
)

var ErrProjectionNotFound = errors.New("workbench projection not found")

// ProjectionDocument is the durable, privacy-classified summary written by a
// Workbench-owned projector. Public responses require PublicOptIn; private
// responses require the host membership resolver below.
type ProjectionDocument struct {
	Scope             Scope              `json:"scope" firestore:"scope"`
	ID                string             `json:"id" firestore:"id"`
	DisplayName       string             `json:"display_name" firestore:"display_name"`
	Summary           Summary            `json:"summary" firestore:"summary"`
	UpdatedAt         time.Time          `json:"updated_at" firestore:"updated_at"`
	PublicOptIn       bool               `json:"public_opt_in" firestore:"public_opt_in"`
	PublicEligibility *PublicEligibility `json:"public_eligibility,omitempty" firestore:"public_eligibility,omitempty"`
}

type SeriesDocument struct {
	Scope  Scope         `json:"scope" firestore:"scope"`
	ID     string        `json:"id" firestore:"id"`
	Metric string        `json:"metric" firestore:"metric"`
	Points []SeriesPoint `json:"points" firestore:"points"`
}

type LeaderboardDocument struct {
	Metric     string             `json:"metric" firestore:"metric"`
	Entries    []LeaderboardEntry `json:"entries" firestore:"entries"`
	PublicOnly bool               `json:"public_only" firestore:"public_only"`
}

type PublicLatestMerges struct {
	Entries []LatestMerge `json:"entries" firestore:"entries"`
}

// ProjectionStore is the narrow durable persistence adapter supplied by the
// host. Implementations map these operations to Firestore or another durable
// store; aggregation and disclosure stay in this package.
type ProjectionStore interface {
	ListProjections(context.Context, Scope) ([]ProjectionDocument, error)
	GetProjection(context.Context, Scope, string) (ProjectionDocument, error)
	ListSeries(context.Context, Scope, string, string) (SeriesDocument, error)
	GetLeaderboard(context.Context, string) (LeaderboardDocument, error)
	ListPublicLatestMerges(context.Context, int) (PublicLatestMerges, error)
}

// MembershipResolver proves GitHub installation membership for one canonical
// subject. A Firebase-authenticated host maps Viewer.UserID to GitHub identity;
// the provider never trusts browser headers or public GitHub visibility.
type MembershipResolver interface {
	Member(context.Context, Viewer, Scope, string) (bool, error)
}

// ProjectionKey is the stable document key: scope plus a SHA-256 digest of the
// canonical subject ID. The digest prevents slashes in github.com/org/repo IDs
// from changing collection hierarchy while preserving the original ID in the
// document body for audit and display.
func ProjectionKey(scope Scope, id string) string {
	sum := sha256.Sum256([]byte(string(scope) + "\x00" + id))
	return string(scope) + "_" + hex.EncodeToString(sum[:])
}

func ValidateProjectionDocument(document ProjectionDocument) error {
	if document.Scope != ScopeRepository && document.Scope != ScopeOrganization && document.Scope != ScopeUser {
		return fmt.Errorf("invalid projection scope %q", document.Scope)
	}
	if strings.TrimSpace(document.ID) == "" {
		return errors.New("projection ID is required")
	}
	if strings.TrimSpace(document.DisplayName) == "" {
		return errors.New("projection display name is required")
	}
	if document.UpdatedAt.IsZero() {
		return errors.New("projection updated_at is required")
	}
	if document.PublicOptIn {
		if document.Scope != ScopeRepository {
			return errors.New("only repository projections may be publicly opted in")
		}
		if document.PublicEligibility == nil {
			return errors.New("public projection eligibility evidence is required")
		}
		if document.PublicEligibility.Repository != document.ID {
			return errors.New("public projection eligibility repository must match projection ID")
		}
		if err := ValidatePublicEligibility(*document.PublicEligibility); err != nil {
			return fmt.Errorf("invalid public projection eligibility: %w", err)
		}
	}
	return nil
}

type StoreReadModel struct {
	Store      ProjectionStore
	Membership MembershipResolver
}

func (model StoreReadModel) Dashboard(ctx context.Context, viewer Viewer) (Access[Dashboard], error) {
	if model.Store == nil {
		return Access[Dashboard]{}, ErrNoReadModel
	}
	documents, err := model.Store.ListProjections(ctx, ScopeRepository)
	if err != nil {
		return Access[Dashboard]{}, err
	}
	var summary Summary
	var updated time.Time
	visibility := VisibilityPublic
	for _, document := range documents {
		if err := ValidateProjectionDocument(document); err != nil {
			return Access[Dashboard]{}, err
		}
		visible, err := model.visible(ctx, viewer, document)
		if err != nil {
			return Access[Dashboard]{}, err
		}
		if !visible {
			continue
		}
		if !document.PublicOptIn {
			visibility = VisibilityPrivate
		}
		summary.Repositories += document.Summary.Repositories
		summary.OpenPulls += document.Summary.OpenPulls
		summary.MergedPulls += document.Summary.MergedPulls
		summary.OpenIssues += document.Summary.OpenIssues
		summary.Releases += document.Summary.Releases
		if document.UpdatedAt.After(updated) {
			updated = document.UpdatedAt
		}
	}
	return Access[Dashboard]{Visibility: visibility, Value: Dashboard{GeneratedAt: updated, Summary: summary}}, nil
}

func (model StoreReadModel) Stats(ctx context.Context, viewer Viewer, scope Scope, id string) (Access[Stat], error) {
	document, err := model.projection(ctx, scope, id)
	if err != nil {
		return Access[Stat]{}, err
	}
	visible, err := model.visible(ctx, viewer, document)
	if err != nil {
		return Access[Stat]{}, err
	}
	if !visible {
		return Access[Stat]{}, ErrPrivateData
	}
	return Access[Stat]{Visibility: model.documentVisibility(document), Value: Stat{Scope: scope, ID: document.ID, DisplayName: document.DisplayName, Summary: document.Summary, UpdatedAt: document.UpdatedAt}}, nil
}

func (model StoreReadModel) Series(ctx context.Context, viewer Viewer, scope Scope, id, metric string) (Access[Series], error) {
	document, err := model.projection(ctx, scope, id)
	if err != nil {
		return Access[Series]{}, err
	}
	visible, err := model.visible(ctx, viewer, document)
	if err != nil {
		return Access[Series]{}, err
	}
	if !visible {
		return Access[Series]{}, ErrPrivateData
	}
	series, err := model.Store.ListSeries(ctx, scope, id, metric)
	if err != nil {
		return Access[Series]{}, err
	}
	return Access[Series]{Visibility: model.documentVisibility(document), Value: Series{Scope: scope, ID: id, Metric: metric, Points: series.Points}}, nil
}

func (model StoreReadModel) Leaderboard(ctx context.Context, viewer Viewer, metric string) (Access[Leaderboard], error) {
	if model.Store == nil {
		return Access[Leaderboard]{}, ErrNoReadModel
	}
	document, err := model.Store.GetLeaderboard(ctx, metric)
	if err != nil {
		return Access[Leaderboard]{}, err
	}
	if !document.PublicOnly {
		return Access[Leaderboard]{}, ErrPrivateData
	}
	return Access[Leaderboard]{Visibility: VisibilityPublic, Value: Leaderboard{Metric: document.Metric, Entries: document.Entries}}, nil
}

func (model StoreReadModel) LatestMerges(ctx context.Context, viewer Viewer, limit int) (Access[[]LatestMerge], error) {
	if model.Store == nil {
		return Access[[]LatestMerge]{}, ErrNoReadModel
	}
	merges, err := model.Store.ListPublicLatestMerges(ctx, limit)
	if err != nil {
		return Access[[]LatestMerge]{}, err
	}
	return Access[[]LatestMerge]{Visibility: VisibilityPublic, Value: merges.Entries}, nil
}

func (model StoreReadModel) projection(ctx context.Context, scope Scope, id string) (ProjectionDocument, error) {
	if model.Store == nil {
		return ProjectionDocument{}, ErrNoReadModel
	}
	if strings.TrimSpace(id) == "" {
		return ProjectionDocument{}, ErrProjectionNotFound
	}
	document, err := model.Store.GetProjection(ctx, scope, id)
	if err != nil {
		return ProjectionDocument{}, err
	}
	if err := ValidateProjectionDocument(document); err != nil {
		return ProjectionDocument{}, err
	}
	return document, nil
}

func (model StoreReadModel) visible(ctx context.Context, viewer Viewer, document ProjectionDocument) (bool, error) {
	visibility, err := model.classify(ctx, viewer, document)
	return visibility != "", err
}

func (model StoreReadModel) documentVisibility(document ProjectionDocument) Visibility {
	if document.PublicOptIn {
		return VisibilityPublic
	}
	return VisibilityPrivate
}

func (model StoreReadModel) classify(ctx context.Context, viewer Viewer, document ProjectionDocument) (Visibility, error) {
	if document.PublicOptIn {
		return VisibilityPublic, nil
	}
	if model.Membership == nil || !viewer.Authenticated || viewer.UserID == "" {
		return "", nil
	}
	member, err := model.Membership.Member(ctx, viewer, document.Scope, document.ID)
	if err != nil {
		return "", err
	}
	if !member {
		return "", nil
	}
	return VisibilityPrivate, nil
}

var _ ReadModel = StoreReadModel{}
