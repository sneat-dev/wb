package redeliver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/hub/narrate"
)

func background() context.Context { return context.Background() }

// testAppPrivateKeyPEM is generated once per test binary run. hub.BuildAppJWT
// parses and signs with a real RSA key, and the fake GitHub App API below
// never verifies the signature — it only has to look like an Authorization
// header GitHub would accept — so a fixed 2048-bit key generated at test time
// is exactly as good as a real App's and needs no fixture file.
var testAppPrivateKeyPEM = generateTestAppPrivateKeyPEM()

func generateTestAppPrivateKeyPEM() []byte {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// fakeStore is an in-memory hub.WebhookRedeliveryStore. hub's own store
// implementation is exercised against a real backend in
// hub/webhook_redelivery_store_test.go; this fake lets the sweep's own logic
// be tested without a document store.
type fakeStore struct {
	mu      sync.Mutex
	records map[string]hub.WebhookRedeliveryRecord

	loadErr, saveErr, listErr, deleteErr error
}

func newFakeStore() *fakeStore { return &fakeStore{records: map[string]hub.WebhookRedeliveryRecord{}} }

func (store *fakeStore) LoadWebhookRedelivery(_ context.Context, guid string) (hub.WebhookRedeliveryRecord, bool, error) {
	if store.loadErr != nil {
		return hub.WebhookRedeliveryRecord{}, false, store.loadErr
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.records[guid]
	return record, found, nil
}

func (store *fakeStore) SaveWebhookRedelivery(_ context.Context, record hub.WebhookRedeliveryRecord) error {
	if store.saveErr != nil {
		return store.saveErr
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.records[record.GUID] = record
	return nil
}

func (store *fakeStore) ListWebhookRedeliveries(context.Context) ([]hub.WebhookRedeliveryRecord, error) {
	if store.listErr != nil {
		return nil, store.listErr
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	records := make([]hub.WebhookRedeliveryRecord, 0, len(store.records))
	for _, record := range store.records {
		records = append(records, record)
	}
	return records, nil
}

func (store *fakeStore) DeleteWebhookRedeliveries(_ context.Context, guids []string) error {
	if store.deleteErr != nil {
		return store.deleteErr
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, guid := range guids {
		delete(store.records, guid)
	}
	return nil
}

func (store *fakeStore) get(guid string) (hub.WebhookRedeliveryRecord, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	record, found := store.records[guid]
	return record, found
}

func (store *fakeStore) count() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return len(store.records)
}

// fakeDelivery is one entry the fake GitHub App API lists.
type fakeDelivery struct {
	id             int64
	guid           string
	deliveredAt    time.Time
	redelivery     bool
	statusCode     int
	event          string
	installationID int64
	repositoryID   int64
}

func (delivery fakeDelivery) json() string {
	return fmt.Sprintf(`{"id":%d,"guid":%q,"delivered_at":%q,"redelivery":%t,"status_code":%d,"event":%q,"installation_id":%d,"repository_id":%d}`,
		delivery.id, delivery.guid, delivery.deliveredAt.UTC().Format(time.RFC3339), delivery.redelivery, delivery.statusCode, delivery.event, delivery.installationID, delivery.repositoryID)
}

// fakeGitHubAppAPI serves GET /app/hook/deliveries (paginated by page index,
// newest-first, matching GitHub's real ordering) and POST
// /app/hook/deliveries/{id}/attempts. Tests mutate deliveries and
// listStatus/redeliverStatus between calls to change what the next request
// sees.
type fakeGitHubAppAPI struct {
	mu sync.Mutex

	// pages holds one slice of deliveries per page, newest page first, so a
	// test can exercise cursor pagination explicitly by populating more than
	// one page.
	pages [][]fakeDelivery

	// listStatus, when non-zero, makes every GET answer with that status
	// instead of a page, to test transient-failure handling.
	listStatus int

	// redeliverStatus, when non-zero, makes every POST answer with that
	// status instead of 202.
	redeliverStatus int

	redeliverCalls   []int64
	listRequests     int
	sawAuthorization []string
}

func newFakeGitHubAppAPI() *fakeGitHubAppAPI { return &fakeGitHubAppAPI{} }

func (api *fakeGitHubAppAPI) setPages(pages ...[]fakeDelivery) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.pages = pages
}

func (api *fakeGitHubAppAPI) redeliveredIDs() []int64 {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]int64(nil), api.redeliverCalls...)
}

func (api *fakeGitHubAppAPI) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/hook/deliveries", func(writer http.ResponseWriter, request *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.listRequests++
		api.sawAuthorization = append(api.sawAuthorization, request.Header.Get("Authorization"))
		if api.listStatus != 0 {
			writer.WriteHeader(api.listStatus)
			return
		}
		page := 0
		if raw := request.URL.Query().Get("page"); raw != "" {
			page, _ = strconv.Atoi(raw)
		}
		if page < len(api.pages)-1 {
			writer.Header().Set("Link", fmt.Sprintf(`<%s/app/hook/deliveries?per_page=100&page=%d>; rel="next"`, "http://"+request.Host, page+1))
		}
		writer.Header().Set("Content-Type", "application/json")
		items := make([]string, 0)
		if page < len(api.pages) {
			for _, delivery := range api.pages[page] {
				items = append(items, delivery.json())
			}
		}
		_, _ = fmt.Fprintf(writer, "[%s]", strings.Join(items, ","))
	})
	mux.HandleFunc("/app/hook/deliveries/", func(writer http.ResponseWriter, request *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		if request.Method != http.MethodPost || !strings.HasSuffix(request.URL.Path, "/attempts") {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		idPart := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/app/hook/deliveries/"), "/attempts")
		id, _ := strconv.ParseInt(idPart, 10, 64)
		api.redeliverCalls = append(api.redeliverCalls, id)
		if api.redeliverStatus != 0 {
			writer.WriteHeader(api.redeliverStatus)
			return
		}
		writer.WriteHeader(http.StatusAccepted)
	})
	return httptest.NewServer(mux)
}

func recordingNarrate() (func(narrate.Line), *[]narrate.Line) {
	var lines []narrate.Line
	var mu sync.Mutex
	return func(line narrate.Line) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, line)
	}, &lines
}

func fixedClock(at time.Time) func() time.Time { return func() time.Time { return at } }

// TestSweepRedeliversExactlyTheFailedGUIDsWithinTheWindow is the feature's
// central acceptance criterion: against three failed GUIDs, one successful
// GUID, one delivery older than 72 hours, and one GUID that already has a
// successful redelivery attempt, the sweep redelivers exactly the three.
func TestSweepRedeliversExactlyTheFailedGUIDsWithinTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI()
	api.setPages([]fakeDelivery{
		{id: 1, guid: "guid-failed-1", deliveredAt: now.Add(-1 * time.Hour), statusCode: 500, event: "push", repositoryID: 1},
		{id: 2, guid: "guid-failed-2", deliveredAt: now.Add(-2 * time.Hour), statusCode: 503, event: "push", repositoryID: 2},
		{id: 3, guid: "guid-failed-3", deliveredAt: now.Add(-3 * time.Hour), statusCode: 400, event: "push", repositoryID: 3},
		{id: 4, guid: "guid-succeeded", deliveredAt: now.Add(-1 * time.Hour), statusCode: 200, event: "push", repositoryID: 4},
		// guid-recovered's latest attempt (newest, listed first) already
		// succeeded on a redelivery; its earlier failure must not surface.
		{id: 6, guid: "guid-recovered", deliveredAt: now.Add(-30 * time.Minute), redelivery: true, statusCode: 200, event: "push", repositoryID: 6},
		{id: 5, guid: "guid-recovered", deliveredAt: now.Add(-10 * time.Hour), statusCode: 500, event: "push", repositoryID: 6},
		// Older than the 72-hour window: must never be touched.
		{id: 7, guid: "guid-too-old", deliveredAt: now.Add(-80 * time.Hour), statusCode: 500, event: "push", repositoryID: 7},
	})
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: fixedClock(now), Narrate: narrateFn,
	})

	delay := sweeper.Sweep(background())
	if delay != DefaultInterval {
		t.Fatalf("delay = %s, want the configured interval on a clean sweep", delay)
	}

	redelivered := api.redeliveredIDs()
	if len(redelivered) != 3 {
		t.Fatalf("redelivered ids = %v, want exactly 3", redelivered)
	}
	for _, id := range []int64{1, 2, 3} {
		if !containsInt64(redelivered, id) {
			t.Fatalf("redelivered ids = %v, missing %d", redelivered, id)
		}
	}
	for _, id := range []int64{4, 5, 6, 7} {
		if containsInt64(redelivered, id) {
			t.Fatalf("redelivered ids = %v, must not include %d", redelivered, id)
		}
	}

	for _, guid := range []string{"guid-failed-1", "guid-failed-2", "guid-failed-3"} {
		record, found := store.get(guid)
		if !found || record.Attempts != 1 || record.Abandoned {
			t.Fatalf("record for %s = %+v, found=%t", guid, record, found)
		}
	}
	for _, guid := range []string{"guid-succeeded", "guid-recovered", "guid-too-old"} {
		if _, found := store.get(guid); found {
			t.Fatalf("%s must not have been touched", guid)
		}
	}

	status := sweeper.Status()
	if status.Redelivered != 3 || status.Abandoned != 0 || !status.LastSweepAt.Equal(now) {
		t.Fatalf("status = %+v", status)
	}

	redeliverLines := 0
	for _, line := range *lines {
		if line.Event != "redeliver" {
			t.Fatalf("narrated event column = %q, want %q", line.Event, "redeliver")
		}
		if strings.Contains(line.Action, "abandoned") {
			t.Fatalf("unexpected abandonment narrated: %+v", line)
		}
		redeliverLines++
	}
	if redeliverLines != 3 {
		t.Fatalf("narrated %d lines, want 3", redeliverLines)
	}
}

// TestSweepRetriesUpToThreeAttemptsThenAbandons walks four sweeps of one
// GUID that never stops failing: the first three each redeliver and record
// one more attempt, and the fourth abandons it without calling GitHub again.
// A fifth sweep touches it not at all.
func TestSweepRetriesUpToThreeAttemptsThenAbandons(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI()
	api.setPages([]fakeDelivery{
		{id: 9, guid: "guid-always-fails", deliveredAt: now.Add(-1 * time.Hour), statusCode: 500, event: "push", repositoryID: 9},
	})
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: fixedClock(now), Narrate: narrateFn,
	})

	for pass := 1; pass <= 3; pass++ {
		sweeper.Sweep(background())
		record, found := store.get("guid-always-fails")
		if !found || record.Attempts != pass || record.Abandoned {
			t.Fatalf("pass %d: record = %+v, found=%t", pass, record, found)
		}
	}
	if got := len(api.redeliveredIDs()); got != 3 {
		t.Fatalf("redeliver calls = %d, want 3 after three retryable sweeps", got)
	}

	// Fourth sweep: attempts already at MaxAttempts, so this one abandons
	// instead of asking GitHub again.
	sweeper.Sweep(background())
	if got := len(api.redeliveredIDs()); got != 3 {
		t.Fatalf("redeliver calls after abandonment = %d, want still 3", got)
	}
	record, found := store.get("guid-always-fails")
	if !found || !record.Abandoned || record.Attempts != MaxAttempts {
		t.Fatalf("record after abandonment = %+v, found=%t", record, found)
	}

	abandonLines := 0
	for _, line := range *lines {
		if strings.Contains(line.Action, "abandoned") {
			abandonLines++
			if !strings.Contains(line.Action, "3 attempts") {
				t.Fatalf("abandon line = %q", line.Action)
			}
		}
	}
	if abandonLines != 1 {
		t.Fatalf("abandon lines = %d, want exactly 1", abandonLines)
	}

	// Fifth sweep: abandoned GUIDs are never retried, and never narrated
	// again.
	linesBefore := len(*lines)
	sweeper.Sweep(background())
	if got := len(api.redeliveredIDs()); got != 3 {
		t.Fatalf("redeliver calls after a fifth sweep = %d, want still 3", got)
	}
	if len(*lines) != linesBefore {
		t.Fatalf("a fifth sweep narrated %d more lines for an abandoned GUID", len(*lines)-linesBefore)
	}
}

// TestSweepStopsOnceAGUIDSucceeds covers the case where a redelivery (or
// GitHub's own retry) fixes a delivery between sweeps: the next sweep leaves
// it alone.
func TestSweepStopsOnceAGUIDSucceeds(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI()
	api.setPages([]fakeDelivery{
		{id: 20, guid: "guid-recovers", deliveredAt: now.Add(-1 * time.Hour), statusCode: 500, event: "push", repositoryID: 20},
	})
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: fixedClock(now), Narrate: narrateFn,
	})

	sweeper.Sweep(background())
	if got := len(api.redeliveredIDs()); got != 1 {
		t.Fatalf("first sweep redeliver calls = %d, want 1", got)
	}

	// The redelivery worked: the next listing shows a newer, successful
	// attempt for the same GUID.
	api.setPages([]fakeDelivery{
		{id: 21, guid: "guid-recovers", deliveredAt: now.Add(-30 * time.Minute), redelivery: true, statusCode: 200, event: "push", repositoryID: 20},
		{id: 20, guid: "guid-recovers", deliveredAt: now.Add(-1 * time.Hour), statusCode: 500, event: "push", repositoryID: 20},
	})
	linesBefore := len(*lines)
	sweeper.Sweep(background())
	if got := len(api.redeliveredIDs()); got != 1 {
		t.Fatalf("second sweep redeliver calls = %d, want still 1", got)
	}
	if len(*lines) != linesBefore {
		t.Fatalf("a sweep on a succeeded GUID narrated %d more lines", len(*lines)-linesBefore)
	}
	status := sweeper.Status()
	if status.Redelivered != 0 {
		t.Fatalf("second sweep status = %+v, want 0 redelivered", status)
	}
}

// TestRetentionPruneRemovesRecordsOlderThanSevenDays proves the 7-day
// retention the store is documented to carry: a sweep with nothing to act on
// still prunes.
func TestRetentionPruneRemovesRecordsOlderThanSevenDays(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI()
	api.setPages(nil)
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	store.records["expired"] = hub.WebhookRedeliveryRecord{GUID: "expired", Attempts: 3, Abandoned: true, LastAttemptAt: now.Add(-8 * 24 * time.Hour)}
	store.records["fresh"] = hub.WebhookRedeliveryRecord{GUID: "fresh", Attempts: 1, LastAttemptAt: now.Add(-1 * 24 * time.Hour)}

	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: fixedClock(now),
	})
	sweeper.Sweep(background())

	if store.count() != 1 {
		t.Fatalf("store has %d records after prune, want 1", store.count())
	}
	if _, found := store.get("fresh"); !found {
		t.Fatal("prune removed the record that was still within retention")
	}
	if _, found := store.get("expired"); found {
		t.Fatal("prune left a record past its 7-day retention")
	}
}

// TestSweepIsInertWithoutFullAppConfiguration is the defensive counterpart of
// "there is no activity when hub.github.app is not configured": the wiring
// test for that lives in cmd/wb, where an unconfigured hub never even
// constructs a Sweeper. This proves the Sweeper itself never calls out when
// it is missing what it needs.
func TestSweepIsInertWithoutFullAppConfiguration(t *testing.T) {
	api := newFakeGitHubAppAPI()
	server := api.server()
	defer server.Close()

	for name, options := range map[string]Options{
		"no store":       {Client: server.Client(), APIBaseURL: server.URL, AppID: 1, PrivateKeyPEM: testAppPrivateKeyPEM},
		"no app id":      {Client: server.Client(), APIBaseURL: server.URL, PrivateKeyPEM: testAppPrivateKeyPEM, Store: newFakeStore()},
		"no private key": {Client: server.Client(), APIBaseURL: server.URL, AppID: 1, Store: newFakeStore()},
	} {
		t.Run(name, func(t *testing.T) {
			sweeper := New(options)
			sweeper.Sweep(background())
			if api.listRequests != 0 {
				t.Fatalf("%s: sweep called github %d times", name, api.listRequests)
			}
		})
	}
}

// TestSweepBacksOffAndNarratesOneLineOnFailure proves the sweep never
// crashes on a GitHub failure, narrates exactly one line for it, and backs
// off exponentially until a clean sweep resets it.
func TestSweepBacksOffAndNarratesOneLineOnFailure(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI()
	api.listStatus = http.StatusInternalServerError
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: fixedClock(now), Interval: time.Minute, Narrate: narrateFn,
	})

	first := sweeper.Sweep(background())
	if first != time.Minute {
		t.Fatalf("first backoff = %s, want the configured interval", first)
	}
	second := sweeper.Sweep(background())
	if second != 2*time.Minute {
		t.Fatalf("second backoff = %s, want double", second)
	}
	if len(*lines) != 2 {
		t.Fatalf("narrated %d lines for two failed sweeps, want 2", len(*lines))
	}
	for _, line := range *lines {
		if line.Event != "redeliver" || !strings.HasPrefix(line.Action, "sweep failed: ") {
			t.Fatalf("failure line = %+v", line)
		}
	}
	if store.count() != 0 {
		t.Fatal("a failed sweep must not have written anything")
	}

	api.listStatus = 0
	api.setPages(nil)
	clean := sweeper.Sweep(background())
	if clean != time.Minute {
		t.Fatalf("delay after a clean sweep = %s, want the interval (backoff reset)", clean)
	}
}

// TestNarrationCarriesNoSecrets is the golden check the common brief and the
// task both require: exact line shape, and no JWT, key material, or bearer
// token anywhere in a narrated line.
func TestNarrationCarriesNoSecrets(t *testing.T) {
	now := time.Date(2026, 9, 18, 14, 2, 11, 0, time.UTC)
	api := newFakeGitHubAppAPI()
	api.setPages([]fakeDelivery{
		{id: 30, guid: "guid-golden", deliveredAt: now.Add(-1 * time.Hour), statusCode: 500, event: "push", repositoryID: 42},
	})
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: fixedClock(now), Narrate: narrateFn,
	})
	sweeper.Sweep(background())

	if len(*lines) != 1 {
		t.Fatalf("lines = %+v, want exactly 1", *lines)
	}
	got := (*lines)[0].String()
	want := "14:02:11 redeliver       push repository:42               redelivered (attempt 1 of 3)"
	if got != want {
		t.Fatalf("narrated line = %q, want %q", got, want)
	}

	for _, authorization := range api.sawAuthorization {
		if !strings.HasPrefix(authorization, "Bearer ") {
			t.Fatalf("request carried no bearer token: %q", authorization)
		}
		token := strings.TrimPrefix(authorization, "Bearer ")
		if strings.Contains(got, token) {
			t.Fatal("the narrated line carried the App JWT")
		}
	}
	if strings.Contains(got, "BEGIN RSA PRIVATE KEY") {
		t.Fatal("the narrated line carried key material")
	}
}

// TestPaginationFollowsLinkHeaderAcrossPages proves cursor pagination is
// actually followed rather than only the first page being read.
func TestPaginationFollowsLinkHeaderAcrossPages(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI()
	api.setPages(
		[]fakeDelivery{{id: 40, guid: "guid-page-0", deliveredAt: now.Add(-1 * time.Hour), statusCode: 500, event: "push", repositoryID: 1}},
		[]fakeDelivery{
			{id: 41, guid: "guid-page-1", deliveredAt: now.Add(-2 * time.Hour), statusCode: 500, event: "push", repositoryID: 2},
			// Crosses the 72-hour boundary on the second page: pagination
			// must stop here rather than requesting a third page.
			{id: 42, guid: "guid-too-old", deliveredAt: now.Add(-80 * time.Hour), statusCode: 500, event: "push", repositoryID: 3},
		},
	)
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: fixedClock(now),
	})
	sweeper.Sweep(background())

	redelivered := api.redeliveredIDs()
	if len(redelivered) != 2 || !containsInt64(redelivered, 40) || !containsInt64(redelivered, 41) {
		t.Fatalf("redelivered ids = %v, want [40 41]", redelivered)
	}
	if api.listRequests != 2 {
		t.Fatalf("list requests = %d, want exactly 2 pages fetched", api.listRequests)
	}
}

func containsInt64(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
