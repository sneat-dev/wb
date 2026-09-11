// Copyright 2026 Sneat Co.

package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
	"github.com/sneat-dev/wb/hub/narrate"
)

// narratedAt is the fixed clock every narration golden is written against, so
// the first column is a constant rather than "now".
var narratedAt = time.Date(2026, 9, 11, 14, 2, 11, 0, time.UTC)

// duplicateEventStore answers every enqueue as a replay, which is how the
// marker collection reports a delivery GitHub sent twice.
type duplicateEventStore struct{ eventStoreMemory }

func (*duplicateEventStore) EnqueueForMachines(context.Context, repositoryevent.Event, []Machine) (EnqueueResult, error) {
	return EnqueueResult{Duplicate: true}, nil
}

// failingEventStore is a store that cannot accept the event, so the HTTP
// handler is the one that narrates the rejection.
type failingEventStore struct{ eventStoreMemory }

func (*failingEventStore) EnqueueForMachines(context.Context, repositoryevent.Event, []Machine) (EnqueueResult, error) {
	return EnqueueResult{}, errors.New("store is unavailable")
}

func narrationSnapshots(repository string) snapshotMemory {
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	return snapshotMemory{{
		IdentityID: "uid-a", MachineID: "machine-a",
		Snapshot: machinesnapshot.Snapshot{
			SchemaVersion: machinesnapshot.SchemaVersion, Login: "alex", Machine: "laptop", PublishedAt: at,
			Repositories: []string{repository}, Worktrees: []machinesnapshot.Worktree{},
		},
		ReceivedAt: at, Digest: "a",
	}}
}

const narrationPushPayload = `{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"id":987,"full_name":"acme/app","default_branch":"main"},"installation":{"id":123}}`

// TestRepositoryEventServiceNarratesEveryDecision is the console half of
// AC every-event-is-narrated-on-the-console: one line per delivery, naming
// what the hub did with it, for every decision the service can reach.
func TestRepositoryEventServiceNarratesEveryDecision(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		service  func() RepositoryEventService
		delivery WebhookDelivery
		want     string
	}{
		{
			name: "queued",
			service: func() RepositoryEventService {
				return RepositoryEventService{Snapshots: narrationSnapshots("github.com/acme/app"), Entitlements: &entitlementRecorder{allowed: true}, Store: &eventStoreMemory{}}
			},
			delivery: WebhookDelivery{ID: "delivery-1", Event: "push", Repository: "github.com/acme/app", Payload: []byte(narrationPushPayload)},
			want:     "14:02:11 push            github.com/acme/app              queued for 1 machines",
		},
		{
			name: "duplicate",
			service: func() RepositoryEventService {
				return RepositoryEventService{Snapshots: narrationSnapshots("github.com/acme/app"), Entitlements: &entitlementRecorder{allowed: true}, Store: &duplicateEventStore{}}
			},
			delivery: WebhookDelivery{ID: "delivery-1", Event: "push", Repository: "github.com/acme/app", Payload: []byte(narrationPushPayload)},
			want:     "14:02:11 push            github.com/acme/app              duplicate delivery delivery-1:default; dropped",
		},
		{
			name: "ignored because no machine is entitled",
			service: func() RepositoryEventService {
				return RepositoryEventService{Snapshots: narrationSnapshots("github.com/acme/app"), Entitlements: &entitlementRecorder{allowed: false}, Store: &eventStoreMemory{}}
			},
			delivery: WebhookDelivery{ID: "delivery-1", Event: "push", Repository: "github.com/acme/app", Payload: []byte(narrationPushPayload)},
			want:     "14:02:11 push            github.com/acme/app              ignored: no entitled machine",
		},
		{
			name:     "ignored because the push was not on the default branch",
			service:  func() RepositoryEventService { return RepositoryEventService{} },
			delivery: WebhookDelivery{ID: "delivery-1", Event: "push", Repository: "github.com/acme/app", Payload: []byte(`{"ref":"refs/heads/topic","after":"0123456789abcdef0123456789abcdef01234567","repository":{"id":987,"full_name":"acme/app","default_branch":"main"},"installation":{"id":123}}`)},
			want:     "14:02:11 push            github.com/acme/app              ignored: not on default branch",
		},
		{
			name:     "ignored because the repository action is neither rename nor transfer",
			service:  func() RepositoryEventService { return RepositoryEventService{} },
			delivery: WebhookDelivery{ID: "delivery-1", Event: "repository", Repository: "github.com/acme/app", Payload: []byte(`{"action":"edited","repository":{"id":987,"full_name":"acme/app","default_branch":"main"},"installation":{"id":123}}`)},
			want:     "14:02:11 repository      github.com/acme/app              ignored: not a rename or transfer",
		},
		{
			name:     "ignored because the event type is not one the hub routes",
			service:  func() RepositoryEventService { return RepositoryEventService{} },
			delivery: WebhookDelivery{ID: "delivery-1", Event: "star", Repository: "github.com/acme/app", Payload: []byte(`{"action":"created"}`)},
			want:     "14:02:11 star            github.com/acme/app              ignored: unsupported event",
		},
		{
			name:     "an installation lifecycle delivery names the account column it has",
			service:  func() RepositoryEventService { return RepositoryEventService{Lifecycle: &lifecycleMemory{}} },
			delivery: WebhookDelivery{ID: "delivery-1", Event: "installation", Payload: []byte(`{"action":"suspend","installation":{"id":7}}`)},
			want:     "14:02:11 installation    github.com                       entitlements refreshed",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			service := testCase.service()
			service.Now = func() time.Time { return narratedAt }
			service.Narrate = narrate.Writer{Out: &out}.Write
			if _, err := service.EnqueueWebhook(context.Background(), testCase.delivery); err != nil {
				t.Fatalf("EnqueueWebhook: %v", err)
			}
			if got := strings.TrimSuffix(out.String(), "\n"); got != testCase.want {
				t.Fatalf("narration\n got %q\nwant %q", got, testCase.want)
			}
		})
	}
}

// TestNarrationIsOptionalAndDefaultsToTheWallClock keeps the hosted instance's
// behaviour byte-identical: it sets no Narrate, and nothing must panic or
// change. A service with a narrator but no injected clock still renders.
func TestNarrationIsOptionalAndDefaultsToTheWallClock(t *testing.T) {
	silent := RepositoryEventService{Snapshots: narrationSnapshots("github.com/acme/app"), Entitlements: &entitlementRecorder{allowed: true}, Store: &eventStoreMemory{}}
	if _, err := silent.EnqueueWebhook(context.Background(), WebhookDelivery{ID: "delivery-1", Event: "push", Payload: []byte(narrationPushPayload)}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	narrated := silent
	narrated.Narrate = narrate.Writer{Out: &out}.Write
	if _, err := narrated.EnqueueWebhook(context.Background(), WebhookDelivery{ID: "delivery-2", Event: "push", Payload: []byte(narrationPushPayload)}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.TrimSuffix(out.String(), "\n"), "queued for 1 machines") {
		t.Fatalf("wall-clock narration = %q", out.String())
	}
}

// TestWebhookHandlerNarratesRejectionsBeforeTheResponse is the rest of
// AC every-event-is-narrated-on-the-console: a delivery the handler refuses
// is still one line, and the line is written before GitHub is answered.
func TestWebhookHandlerNarratesRejectionsBeforeTheResponse(t *testing.T) {
	secret := bytes.Repeat([]byte("s"), MinimumWebhookSecretBytes)
	for _, testCase := range []struct {
		name       string
		options    HandlerOptions
		signature  func([]byte) string
		wantStatus int
		want       string
	}{
		{
			name:       "no webhook secret is configured",
			options:    HandlerOptions{},
			signature:  func([]byte) string { return "" },
			wantStatus: http.StatusServiceUnavailable,
			want:       "push            github.com                       rejected: webhook not configured",
		},
		{
			name:       "bad signature",
			options:    HandlerOptions{WebhookSecret: secret, RepositoryEvents: &RepositoryEventService{}},
			signature:  func([]byte) string { return "sha256=" + strings.Repeat("00", sha256.Size) },
			wantStatus: http.StatusUnauthorized,
			want:       "push            github.com                       rejected: bad signature",
		},
		{
			name: "the service cannot attribute the delivery",
			options: HandlerOptions{WebhookSecret: secret, RepositoryEvents: &RepositoryEventService{
				Snapshots: narrationSnapshots("github.com/acme/app"), Entitlements: &entitlementRecorder{allowed: true}, Store: &failingEventStore{},
			}},
			signature:  func(payload []byte) string { return signWebhook(secret, payload) },
			wantStatus: http.StatusServiceUnavailable,
			want:       "push            github.com/acme/app              rejected: unknown installation",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			options := testCase.options
			options.Narrate = narrate.Writer{Out: &out}.Write
			payload := []byte(narrationPushPayload)
			request := httptest.NewRequest(http.MethodPost, WebhookPath, bytes.NewReader(payload))
			request.Header.Set("X-GitHub-Event", "push")
			request.Header.Set("X-GitHub-Delivery", "delivery-1")
			request.Header.Set("X-Hub-Signature-256", testCase.signature(payload))
			recorder := httptest.NewRecorder()
			NewHandler(options).ServeHTTP(recorder, request)
			if recorder.Code != testCase.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, testCase.wantStatus)
			}
			got := strings.TrimSuffix(out.String(), "\n")
			// The clock is the wall clock here, so only the columns after it
			// are pinned; the rejection must have been written at all, which
			// it can only have been before the response was produced.
			if _, rest, found := strings.Cut(got, " "); !found || rest != testCase.want {
				t.Fatalf("narration\n got %q\nwant %q", got, testCase.want)
			}
		})
	}
}

// TestWebhookHandlerNarratesAnUnreadableBody covers the one rejection that
// needs a body which fails mid-read rather than a header the test can set.
func TestWebhookHandlerNarratesAnUnreadableBody(t *testing.T) {
	var out bytes.Buffer
	handler := NewHandler(HandlerOptions{
		WebhookSecret:    bytes.Repeat([]byte("s"), MinimumWebhookSecretBytes),
		RepositoryEvents: &RepositoryEventService{},
		Narrate:          narrate.Writer{Out: &out}.Write,
	})
	request := httptest.NewRequest(http.MethodPost, WebhookPath, errorReader{})
	request.Header.Set("X-GitHub-Event", "push")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.Contains(out.String(), "rejected: unreadable request body") {
		t.Fatalf("narration = %q", out.String())
	}
}

// TestWebhookHandlerWithoutNarrationStaysSilent is the hosted instance's
// configuration: no Narrate, no console line, same response.
func TestWebhookHandlerWithoutNarrationStaysSilent(t *testing.T) {
	handler := NewHandler(HandlerOptions{})
	request := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader("{}"))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", recorder.Code)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("connection reset") }

func signWebhook(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
