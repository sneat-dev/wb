package githubapp

import (
	"context"
	"errors"
	"testing"
	"time"
)

type providerStore struct {
	documents []ProjectionDocument
	series    SeriesDocument
	board     LeaderboardDocument
	merges    PublicLatestMerges
	err       error
}

type seriesErrorStore struct{ providerStore }

func (store seriesErrorStore) ListSeries(context.Context, Scope, string, string) (SeriesDocument, error) {
	return SeriesDocument{}, errors.New("series unavailable")
}

func (store providerStore) ListProjections(context.Context, Scope) ([]ProjectionDocument, error) {
	return store.documents, store.err
}
func (store providerStore) GetProjection(_ context.Context, scope Scope, id string) (ProjectionDocument, error) {
	for _, document := range store.documents {
		if document.Scope == scope && document.ID == id {
			return document, store.err
		}
	}
	return ProjectionDocument{}, ErrProjectionNotFound
}
func (store providerStore) ListSeries(context.Context, Scope, string, string) (SeriesDocument, error) {
	return store.series, store.err
}
func (store providerStore) GetLeaderboard(context.Context, string) (LeaderboardDocument, error) {
	return store.board, store.err
}
func (store providerStore) ListPublicLatestMerges(context.Context, int) (PublicLatestMerges, error) {
	return store.merges, store.err
}

type providerMembership struct {
	member bool
	err    error
}

func (membership providerMembership) Member(context.Context, Viewer, Scope, string) (bool, error) {
	return membership.member, membership.err
}

func providerDocument(public bool) ProjectionDocument {
	return ProjectionDocument{Scope: ScopeRepository, ID: "github.com/acme/widgets", DisplayName: "acme/widgets", UpdatedAt: time.Unix(10, 0), PublicOptIn: public, Summary: Summary{Repositories: 1, OpenPulls: 2, MergedPulls: 3, OpenIssues: 4, Releases: 5}}
}

func TestProjectionKeyAndValidation(t *testing.T) {
	if ProjectionKey(ScopeRepository, "github.com/acme/widgets") != ProjectionKey(ScopeRepository, "github.com/acme/widgets") {
		t.Fatal("projection key is not stable")
	}
	valid := providerDocument(true)
	if err := ValidateProjectionDocument(valid); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []ProjectionDocument{{}, {Scope: Scope("bad"), ID: "x", DisplayName: "x", UpdatedAt: time.Now()}, {Scope: ScopeRepository, DisplayName: "x", UpdatedAt: time.Now()}, {Scope: ScopeRepository, ID: "x", UpdatedAt: time.Now()}, {Scope: ScopeRepository, ID: "x", DisplayName: "x"}} {
		if err := ValidateProjectionDocument(invalid); err == nil {
			t.Errorf("ValidateProjectionDocument(%#v) returned nil", invalid)
		}
	}
}

func TestStoreReadModelAggregatesAndDiscloses(t *testing.T) {
	store := providerStore{documents: []ProjectionDocument{providerDocument(true), {Scope: ScopeRepository, ID: "github.com/acme/private", DisplayName: "private", UpdatedAt: time.Unix(20, 0), Summary: Summary{Repositories: 2}}}, series: SeriesDocument{Points: []SeriesPoint{{Value: 7}}}, board: LeaderboardDocument{Metric: "landed", Entries: []LeaderboardEntry{{Rank: 1}}, PublicOnly: true}, merges: PublicLatestMerges{Entries: []LatestMerge{{Repository: "acme/widgets"}}}}
	model := StoreReadModel{Store: store, Membership: providerMembership{member: true}}
	public := Viewer{Authenticated: true, Member: true, UserID: "u1"}
	dashboard, err := model.Dashboard(context.Background(), public)
	if err != nil || dashboard.Visibility != VisibilityPrivate || dashboard.Value.Summary.Repositories != 3 || dashboard.Value.GeneratedAt != time.Unix(20, 0) {
		t.Fatalf("dashboard = %#v, err=%v", dashboard, err)
	}
	stat, err := model.Stats(context.Background(), public, ScopeRepository, "github.com/acme/widgets")
	if err != nil || stat.Value.DisplayName != "acme/widgets" {
		t.Fatalf("stat = %#v, err=%v", stat, err)
	}
	privateStat, err := model.Stats(context.Background(), public, ScopeRepository, "github.com/acme/private")
	if err != nil || privateStat.Visibility != VisibilityPrivate {
		t.Fatalf("private authorized stat = %#v, err=%v", privateStat, err)
	}
	series, err := model.Series(context.Background(), public, ScopeRepository, "github.com/acme/widgets", "landed")
	if err != nil || len(series.Value.Points) != 1 {
		t.Fatalf("series = %#v, err=%v", series, err)
	}
	board, err := model.Leaderboard(context.Background(), public, "landed")
	if err != nil || board.Value.Metric != "landed" {
		t.Fatalf("board = %#v, err=%v", board, err)
	}
	merges, err := model.LatestMerges(context.Background(), public, 5)
	if err != nil || len(merges.Value) != 1 {
		t.Fatalf("merges = %#v, err=%v", merges, err)
	}

	privateModel := StoreReadModel{Store: providerStore{documents: []ProjectionDocument{providerDocument(false)}}, Membership: providerMembership{member: false}}
	if _, err = privateModel.Stats(context.Background(), Viewer{Authenticated: true, UserID: "u1"}, ScopeRepository, providerDocument(false).ID); !errors.Is(err, ErrPrivateData) {
		t.Fatalf("private stat err = %v", err)
	}
	if _, err = privateModel.Series(context.Background(), Viewer{Authenticated: true, UserID: "u1"}, ScopeRepository, providerDocument(false).ID, "m"); !errors.Is(err, ErrPrivateData) {
		t.Fatalf("private series err = %v", err)
	}
	if _, err = privateModel.Dashboard(context.Background(), Viewer{}); err != nil {
		t.Fatal(err)
	}
	if anonymousBoard, err := model.Leaderboard(context.Background(), Viewer{}, "landed"); err != nil || anonymousBoard.Visibility != VisibilityPublic {
		t.Fatalf("anonymous public leaderboard = %#v, err=%v", anonymousBoard, err)
	}
	privateBoard := StoreReadModel{Store: providerStore{board: LeaderboardDocument{Metric: "landed", Entries: []LeaderboardEntry{{Rank: 1}}}}}
	if _, err = privateBoard.Leaderboard(context.Background(), Viewer{Authenticated: true, Member: true}, "landed"); !errors.Is(err, ErrPrivateData) {
		t.Fatalf("unvalidated leaderboard err = %v", err)
	}
}

func TestStoreReadModelFailsClosedAndPropagatesMembership(t *testing.T) {
	if _, err := (StoreReadModel{}).Dashboard(context.Background(), Viewer{}); !errors.Is(err, ErrNoReadModel) {
		t.Fatalf("nil dashboard err = %v", err)
	}
	if _, err := (StoreReadModel{}).Stats(context.Background(), Viewer{}, ScopeRepository, "x"); !errors.Is(err, ErrNoReadModel) {
		t.Fatalf("nil stats err = %v", err)
	}
	if _, err := (StoreReadModel{}).Series(context.Background(), Viewer{}, ScopeRepository, "x", "m"); !errors.Is(err, ErrNoReadModel) {
		t.Fatalf("nil series err = %v", err)
	}
	if _, err := (StoreReadModel{}).Leaderboard(context.Background(), Viewer{}, "m"); !errors.Is(err, ErrNoReadModel) {
		t.Fatalf("nil leaderboard err = %v", err)
	}
	if _, err := (StoreReadModel{}).LatestMerges(context.Background(), Viewer{}, 1); !errors.Is(err, ErrNoReadModel) {
		t.Fatalf("nil merges err = %v", err)
	}
	model := StoreReadModel{Store: providerStore{documents: []ProjectionDocument{providerDocument(false)}}, Membership: providerMembership{err: errors.New("membership unavailable")}}
	if _, err := model.Dashboard(context.Background(), Viewer{Authenticated: true, UserID: "u1"}); err == nil || err.Error() != "membership unavailable" {
		t.Fatalf("membership err = %v", err)
	}
	if _, err := model.Stats(context.Background(), Viewer{}, ScopeRepository, "missing"); !errors.Is(err, ErrProjectionNotFound) {
		t.Fatalf("missing stat err = %v", err)
	}
	if _, err := model.Stats(context.Background(), Viewer{}, ScopeRepository, " "); !errors.Is(err, ErrProjectionNotFound) {
		t.Fatalf("empty stat err = %v", err)
	}
	privateForMembership := providerDocument(false)
	if _, err := (StoreReadModel{Store: providerStore{documents: []ProjectionDocument{privateForMembership}}, Membership: providerMembership{err: errors.New("membership unavailable")}}).Stats(context.Background(), Viewer{Authenticated: true, UserID: "u1"}, ScopeRepository, privateForMembership.ID); err == nil {
		t.Fatal("stat membership error was swallowed")
	}
	invalidStat := providerDocument(true)
	invalidStat.DisplayName = ""
	if _, err := (StoreReadModel{Store: providerStore{documents: []ProjectionDocument{invalidStat}}}).Stats(context.Background(), Viewer{}, ScopeRepository, invalidStat.ID); err == nil {
		t.Fatal("invalid stat projection was accepted")
	}
	failed := providerStore{documents: []ProjectionDocument{providerDocument(true)}, err: errors.New("store unavailable")}
	if _, err := (StoreReadModel{Store: failed}).Dashboard(context.Background(), Viewer{}); err == nil {
		t.Fatal("store error was swallowed")
	}
	if _, err := (StoreReadModel{Store: failed}).Stats(context.Background(), Viewer{}, ScopeRepository, providerDocument(true).ID); err == nil {
		t.Fatal("stat store error was swallowed")
	}
	if _, err := (StoreReadModel{Store: failed}).Series(context.Background(), Viewer{}, ScopeRepository, providerDocument(true).ID, "m"); err == nil {
		t.Fatal("series projection error was swallowed")
	}
	if _, err := (StoreReadModel{Store: seriesErrorStore{providerStore{documents: []ProjectionDocument{providerDocument(true)}}}}).Series(context.Background(), Viewer{}, ScopeRepository, providerDocument(true).ID, "m"); err == nil {
		t.Fatal("series store error was swallowed")
	}
	if _, err := (StoreReadModel{Store: providerStore{documents: []ProjectionDocument{privateForMembership}}, Membership: providerMembership{err: errors.New("series membership unavailable")}}).Series(context.Background(), Viewer{Authenticated: true, UserID: "u1"}, ScopeRepository, privateForMembership.ID, "m"); err == nil {
		t.Fatal("series membership error was swallowed")
	}
	if _, err := (StoreReadModel{Store: providerStore{err: errors.New("leaderboard unavailable")}}).Leaderboard(context.Background(), Viewer{Authenticated: true, Member: true}, "m"); err == nil {
		t.Fatal("leaderboard store error was swallowed")
	}
	if _, err := (StoreReadModel{Store: providerStore{err: errors.New("merges unavailable")}}).LatestMerges(context.Background(), Viewer{}, 1); err == nil {
		t.Fatal("merges store error was swallowed")
	}
	if _, err := (StoreReadModel{Store: providerStore{documents: []ProjectionDocument{{Scope: ScopeRepository, ID: "x", DisplayName: "x", UpdatedAt: time.Now()}}}}).Dashboard(context.Background(), Viewer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := (StoreReadModel{Store: providerStore{documents: []ProjectionDocument{{Scope: ScopeRepository, ID: "x", DisplayName: "x", UpdatedAt: time.Now()}}}}).Stats(context.Background(), Viewer{}, ScopeRepository, "x"); !errors.Is(err, ErrPrivateData) {
		t.Fatalf("unopted stat err = %v", err)
	}
	invalid := providerDocument(true)
	invalid.DisplayName = ""
	if _, err := (StoreReadModel{Store: providerStore{documents: []ProjectionDocument{invalid}}}).Dashboard(context.Background(), Viewer{}); err == nil {
		t.Fatal("invalid dashboard projection was accepted")
	}
}
