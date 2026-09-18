// Copyright 2026 Sneat Co.

package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/api/githubapp/machinesnapshot"
	"github.com/sneat-dev/wb/hub/narrate"
)

// TestRedeliveredWebhookDeduplicatesThroughTheRealHandler is the task's own
// requirement: a redelivery arrives at the exact same route, with the exact
// same X-GitHub-Delivery header, as the original delivery. It must need no
// dedup logic of its own — the store's existing per-event marker (keyed by
// the delivery-derived event ID) already refuses it. This runs the real
// hub.NewHandler over a real RepositoryEventStore, not a test double, so the
// deduplication it proves is the one GitHub's redelivery of a missed webhook
// will actually hit.
func TestRedeliveredWebhookDeduplicatesThroughTheRealHandler(t *testing.T) {
	secret := bytes.Repeat([]byte("w"), MinimumWebhookSecretBytes)
	backend := newFirestoreMemoryBackend()
	events, _ := NewRepositoryEventStore(backend)
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	snapshots := snapshotMemory{
		{IdentityID: "uid-a", MachineID: "machine-a", Snapshot: machinesnapshot.Snapshot{
			SchemaVersion: machinesnapshot.SchemaVersion, Login: "alex", Machine: "laptop", PublishedAt: at,
			Repositories: []string{"github.com/acme/app"}, Worktrees: []machinesnapshot.Worktree{},
		}, ReceivedAt: at, Digest: "a"},
	}
	var console bytes.Buffer
	service := &RepositoryEventService{
		Snapshots: snapshots, Entitlements: entitlementMemory{"uid-a": {987: true}}, Store: events,
		Narrate: narrate.Writer{Out: &console}.Write,
	}
	handler := NewHandler(HandlerOptions{RepositoryEvents: service, WebhookSecret: secret})

	payload := `{"ref":"refs/heads/main","after":"0123456789abcdef0123456789abcdef01234567","repository":{"id":987,"full_name":"acme/app","default_branch":"main"},"installation":{"id":123}}`
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(payload))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	send := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(payload))
		request.Header.Set("X-GitHub-Event", "push")
		request.Header.Set("X-GitHub-Delivery", "delivery-redelivered-guid")
		request.Header.Set("X-Hub-Signature-256", signature)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}

	first := send()
	if first.Code != http.StatusAccepted {
		t.Fatalf("first delivery status = %d body=%s", first.Code, first.Body.String())
	}
	if !strings.Contains(console.String(), "queued for 1 machines") {
		t.Fatalf("first delivery narration = %q", console.String())
	}

	// GitHub replays the identical delivery ID on redelivery: the same
	// X-GitHub-Delivery header, the same body.
	second := send()
	if second.Code != http.StatusAccepted {
		t.Fatalf("redelivered status = %d body=%s", second.Code, second.Body.String())
	}
	if !strings.Contains(console.String(), "duplicate delivery") {
		t.Fatalf("redelivered narration = %q", console.String())
	}

	// A "repository" webhook (rename/transfer) derives its event ID with a
	// different suffix (":renamed" instead of ":default"); a redelivery of
	// one of those must dedup exactly the same way.
	renamePayload := `{"action":"renamed","installation":{"id":123},"repository":{"id":987,"full_name":"acme/renamed-app","default_branch":"main"},"changes":{"repository":{"name":{"from":"app"}}}}`
	renameMAC := hmac.New(sha256.New, secret)
	_, _ = renameMAC.Write([]byte(renamePayload))
	renameSignature := "sha256=" + hex.EncodeToString(renameMAC.Sum(nil))
	sendRename := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(renamePayload))
		request.Header.Set("X-GitHub-Event", "repository")
		request.Header.Set("X-GitHub-Delivery", "delivery-redelivered-rename-guid")
		request.Header.Set("X-Hub-Signature-256", renameSignature)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	firstRename := sendRename()
	if firstRename.Code != http.StatusAccepted {
		t.Fatalf("first rename delivery status = %d body=%s", firstRename.Code, firstRename.Body.String())
	}
	secondRename := sendRename()
	if secondRename.Code != http.StatusAccepted {
		t.Fatalf("redelivered rename status = %d body=%s", secondRename.Code, secondRename.Body.String())
	}
	if !strings.Contains(console.String(), "duplicate delivery delivery-redelivered-rename-guid:renamed") {
		t.Fatalf("redelivered rename narration = %q", console.String())
	}

	response, err := service.Poll(context.Background(), Machine{ID: "machine-a", Name: "laptop", IdentityID: "uid-a", Scopes: []MachineScope{ScopeEventsPoll}}, "", 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	// One default-branch-updated event from the push above, plus one
	// repository-renamed event from the rename above — never four, which is
	// what no dedup at all would have produced.
	if len(response.Events) != 2 {
		t.Fatalf("queued events = %d, want exactly 2 despite four deliveries (two pairs of duplicates)", len(response.Events))
	}
}
