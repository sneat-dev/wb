package githubapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type projectorDeliveryStore struct {
	mu         sync.Mutex
	seen       bool
	claimed    bool
	err        error
	committed  bool
	commitErr  error
	released   bool
	claimErr   error
	releaseErr error
}

func (s *projectorDeliveryStore) HasDelivery(context.Context, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen, s.err
}
func (s *projectorDeliveryStore) ClaimDelivery(context.Context, string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimErr != nil {
		return false, s.claimErr
	}
	if s.seen || s.claimed {
		return false, nil
	}
	s.claimed = true
	return true, nil
}
func (s *projectorDeliveryStore) ReleaseDelivery(context.Context, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.released = true
	if s.releaseErr != nil {
		return s.releaseErr
	}
	s.claimed = false
	return nil
}
func (s *projectorDeliveryStore) CommitDeliveryAndWakeup(_ context.Context, _ string, _ Wakeup) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitErr != nil {
		return false, s.commitErr
	}
	s.seen = true
	s.committed = true
	return true, nil
}

type projectorReader struct {
	snapshot ProjectionSnapshot
	err      error
}

func (r projectorReader) RefreshProjection(context.Context, WebhookDelivery) (ProjectionSnapshot, error) {
	return r.snapshot, r.err
}

type projectorRefresh struct{ err error }

func (r projectorRefresh) Refresh(context.Context, WebhookDelivery) error { return r.err }

type projectorWriter struct {
	err                                 error
	repositories, organizations, merges int
}

func (w *projectorWriter) WriteRepositories(_ context.Context, _ string, records []ProjectionDocument) error {
	w.repositories = len(records)
	return w.err
}
func (w *projectorWriter) WriteOrganizations(_ context.Context, _ string, records []ProjectionDocument) error {
	w.organizations = len(records)
	return w.err
}
func (w *projectorWriter) WriteLatestMerges(_ context.Context, _ string, records []LatestMerge) error {
	w.merges = len(records)
	return w.err
}

func projectorSignature(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func validProjectionSnapshot() ProjectionSnapshot {
	return ProjectionSnapshot{
		Repositories:  []ProjectionDocument{{Scope: ScopeRepository, ID: "github.com/acme/app", DisplayName: "app", UpdatedAt: time.Unix(1, 0)}},
		Organizations: []ProjectionDocument{{Scope: ScopeOrganization, ID: "github.com/acme", DisplayName: "acme", UpdatedAt: time.Unix(1, 0)}},
		LatestMerges:  []LatestMerge{{Repository: "github.com/acme/app", PullRequest: 1}},
	}
}

func newProjector() (ProjectionEngine, *projectorDeliveryStore, *projectorWriter) {
	store := &projectorDeliveryStore{}
	writer := &projectorWriter{}
	return ProjectionEngine{
		Deliveries: store, Reader: projectorReader{snapshot: validProjectionSnapshot()}, Writer: writer,
		AuthoritativeReader: projectorRefresh{}, WebhookSecret: []byte("secret"),
	}, store, writer
}

func TestProjectionEngineProcessAppliesValidatedSnapshotAndCommits(t *testing.T) {
	engine, store, writer := newProjector()
	delivery := WebhookDelivery{ID: "delivery-1", Event: "push", Repository: "github.com/acme/app", Payload: []byte(`{"action":"closed"}`)}
	queued, err := engine.Process(context.Background(), delivery, projectorSignature(engine.WebhookSecret, delivery.Payload))
	if err != nil || !queued || !store.committed {
		t.Fatalf("Process() = queued %v, err %v, committed %v", queued, err, store.committed)
	}
	if writer.repositories != 1 || writer.organizations != 1 || writer.merges != 1 {
		t.Fatalf("writes = %d/%d/%d", writer.repositories, writer.organizations, writer.merges)
	}
}

func TestProjectionEngineProcessUsesInstallationWakeupWhenRepositoryIsAbsent(t *testing.T) {
	engine, store, _ := newProjector()
	delivery := WebhookDelivery{ID: "delivery-installation", Event: "installation", Payload: []byte("payload")}
	queued, err := engine.Process(context.Background(), delivery, projectorSignature(engine.WebhookSecret, delivery.Payload))
	if err != nil || !queued || !store.committed {
		t.Fatalf("Process() = queued %v, err %v, committed %v", queued, err, store.committed)
	}
}

func TestProjectionEngineClaimsConcurrentDeliveryBeforeRefresh(t *testing.T) {
	engine, store, writer := newProjector()
	started := make(chan struct{})
	continueRefresh := make(chan struct{})
	engine.Reader = blockingProjectionReader{snapshot: validProjectionSnapshot(), started: started, proceed: continueRefresh}
	delivery := WebhookDelivery{ID: "delivery-race", Event: "push", Repository: "github.com/acme/app", Payload: []byte("payload")}
	results := make(chan error, 2)
	go func() {
		_, err := engine.Process(context.Background(), delivery, projectorSignature(engine.WebhookSecret, delivery.Payload))
		results <- err
	}()
	<-started
	go func() {
		_, err := engine.Process(context.Background(), delivery, projectorSignature(engine.WebhookSecret, delivery.Payload))
		results <- err
	}()
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	close(continueRefresh)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if writer.repositories != 1 || !store.committed {
		t.Fatalf("writes = %d, committed = %v", writer.repositories, store.committed)
	}
}

type blockingProjectionReader struct {
	snapshot ProjectionSnapshot
	started  chan struct{}
	proceed  chan struct{}
}

func (r blockingProjectionReader) RefreshProjection(context.Context, WebhookDelivery) (ProjectionSnapshot, error) {
	close(r.started)
	<-r.proceed
	return r.snapshot, nil
}

func TestProjectionEngineProcessRejectsDuplicateAndMissingDependencies(t *testing.T) {
	engine, store, _ := newProjector()
	store.seen = true
	delivery := WebhookDelivery{ID: "delivery-1", Event: "push", Payload: []byte("payload")}
	if queued, err := engine.Process(context.Background(), delivery, projectorSignature(engine.WebhookSecret, delivery.Payload)); err != nil || queued {
		t.Fatalf("duplicate = %v, %v", queued, err)
	}
	for name, mutate := range map[string]func(*ProjectionEngine){
		"deliveries": func(e *ProjectionEngine) { e.Deliveries = nil },
		"reader":     func(e *ProjectionEngine) { e.Reader = nil },
		"writer":     func(e *ProjectionEngine) { e.Writer = nil },
		"refresh":    func(e *ProjectionEngine) { e.AuthoritativeReader = nil },
		"secret":     func(e *ProjectionEngine) { e.WebhookSecret = nil },
	} {
		t.Run(name, func(t *testing.T) {
			candidate, _, _ := newProjector()
			mutate(&candidate)
			if _, err := candidate.Process(context.Background(), delivery, ""); !errors.Is(err, ErrNoProjector) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestProjectionEngineProcessRejectsInvalidEnvelopeAndSignature(t *testing.T) {
	engine, _, _ := newProjector()
	for name, testCase := range map[string]struct {
		delivery  WebhookDelivery
		signature string
	}{
		"missing id":    {WebhookDelivery{Event: "push"}, ""},
		"missing event": {WebhookDelivery{ID: "id"}, ""},
		"bad signature": {WebhookDelivery{ID: "id", Event: "push"}, "sha256=00"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := engine.Process(context.Background(), testCase.delivery, testCase.signature); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestProjectionEngineProcessPropagatesPhaseErrors(t *testing.T) {
	delivery := WebhookDelivery{ID: "id", Event: "push", Payload: []byte("payload")}
	cases := map[string]func(*ProjectionEngine){
		"has":     func(e *ProjectionEngine) { e.Deliveries = &projectorDeliveryStore{err: errors.New("has")} },
		"claim":   func(e *ProjectionEngine) { e.Deliveries = &projectorDeliveryStore{claimErr: errors.New("claim")} },
		"refresh": func(e *ProjectionEngine) { e.AuthoritativeReader = projectorRefresh{err: errors.New("refresh")} },
		"read":    func(e *ProjectionEngine) { e.Reader = projectorReader{err: errors.New("read")} },
		"invalid": func(e *ProjectionEngine) {
			e.Reader = projectorReader{snapshot: ProjectionSnapshot{Repositories: []ProjectionDocument{{Scope: ScopeOrganization, ID: "id", DisplayName: "x", UpdatedAt: time.Unix(1, 0)}}}}
		},
		"write repo":   func(e *ProjectionEngine) { e.Writer = &projectorWriter{err: errors.New("write")} },
		"write org":    func(e *ProjectionEngine) { e.Writer = &projectorSelectiveWriter{orgErr: errors.New("org")} },
		"write merges": func(e *ProjectionEngine) { e.Writer = &projectorSelectiveWriter{mergeErr: errors.New("merge")} },
		"commit":       func(e *ProjectionEngine) { e.Deliveries = &projectorDeliveryStore{commitErr: errors.New("commit")} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			engine, _, _ := newProjector()
			mutate(&engine)
			if _, err := engine.Process(context.Background(), delivery, projectorSignature(engine.WebhookSecret, delivery.Payload)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

type projectorSelectiveWriter struct{ orgErr, mergeErr error }

func (w projectorSelectiveWriter) WriteRepositories(context.Context, string, []ProjectionDocument) error {
	return nil
}
func (w projectorSelectiveWriter) WriteOrganizations(context.Context, string, []ProjectionDocument) error {
	return w.orgErr
}
func (w projectorSelectiveWriter) WriteLatestMerges(context.Context, string, []LatestMerge) error {
	return w.mergeErr
}

func TestValidateProjectionSnapshotRejectsMismatchedRecords(t *testing.T) {
	cases := []ProjectionSnapshot{
		{Repositories: []ProjectionDocument{{Scope: ScopeOrganization, ID: "id", DisplayName: "x", UpdatedAt: time.Unix(1, 0)}}},
		{Repositories: []ProjectionDocument{{Scope: ScopeRepository}}},
		{Organizations: []ProjectionDocument{{Scope: ScopeRepository, ID: "id", DisplayName: "x", UpdatedAt: time.Unix(1, 0)}}},
		{Organizations: []ProjectionDocument{{Scope: ScopeOrganization}}},
		{LatestMerges: []LatestMerge{{Repository: "", PullRequest: 1}}},
		{LatestMerges: []LatestMerge{{Repository: "repo"}}},
	}
	for i, snapshot := range cases {
		if err := validateProjectionSnapshot(snapshot); err == nil || !strings.Contains(err.Error(), "invalid workbench projection snapshot") {
			t.Errorf("case %d err = %v", i, err)
		}
	}
}
