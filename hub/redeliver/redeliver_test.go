package redeliver

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
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

// fakeDelivery is one attempt the fake GitHub App API remembers, seeded
// directly or created by a redeliver POST.
type fakeDelivery struct {
	id          int64
	guid        string
	deliveredAt time.Time
	redelivery  bool
	statusCode  int
	event       string
}

func (delivery fakeDelivery) json() string {
	return fmt.Sprintf(`{"id":%d,"guid":%q,"delivered_at":%q,"redelivery":%t,"status_code":%d,"event":%q}`,
		delivery.id, delivery.guid, delivery.deliveredAt.UTC().Format(time.RFC3339), delivery.redelivery, delivery.statusCode, delivery.event)
}

// fakeGitHubAppAPI serves GET /app/hook/deliveries and POST
// /app/hook/deliveries/{id}/attempts against a mutable, append-only set of
// attempts: a redeliver POST creates a genuinely new attempt (new id, new
// delivered_at, redelivery:true) rather than only recording that a call was
// made, so a second sweep's listing reflects it exactly as GitHub's real API
// would.
type fakeGitHubAppAPI struct {
	mu sync.Mutex

	now func() time.Time

	nextID   int64
	attempts []fakeDelivery

	perPage            int // 0 means the package's real perPage.
	forceRepeatingLink bool

	listStatus      int
	listHeader      http.Header
	redeliverStatus int // the POST response's own status; 0 means 202.
	// onListRequest, when set, is called with the 1-based count of this GET
	// request before it is answered. A test uses it to advance a shared
	// clock mid-pagination without any real sleep.
	onListRequest func(count int)
	// redeliverOutcome overrides the status_code the new attempt a redeliver
	// POST creates gets, keyed by GUID. Missing means "keep failing with the
	// same status the previous attempt had".
	redeliverOutcome map[string]int

	redeliverCalls   []int64
	listRequests     int
	sawAuthorization []string
	requestedURLs    []string
}

func newFakeGitHubAppAPI(now func() time.Time) *fakeGitHubAppAPI {
	return &fakeGitHubAppAPI{now: now, redeliverOutcome: map[string]int{}}
}

// seed adds one delivery attempt directly, as the App's own history would
// already contain before a sweep ever runs.
func (api *fakeGitHubAppAPI) seed(guid, event string, statusCode int, deliveredAt time.Time) int64 {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.nextID++
	id := api.nextID
	api.attempts = append(api.attempts, fakeDelivery{id: id, guid: guid, deliveredAt: deliveredAt, statusCode: statusCode, event: event})
	return id
}

func (api *fakeGitHubAppAPI) redeliveredIDs() []int64 {
	api.mu.Lock()
	defer api.mu.Unlock()
	return append([]int64(nil), api.redeliverCalls...)
}

func (api *fakeGitHubAppAPI) latestStatus(guid string) (int, bool) {
	api.mu.Lock()
	defer api.mu.Unlock()
	found := false
	var latest fakeDelivery
	for _, delivery := range api.attempts {
		if delivery.guid != guid {
			continue
		}
		if !found || delivery.deliveredAt.After(latest.deliveredAt) {
			latest = delivery
			found = true
		}
	}
	return latest.statusCode, found
}

func (api *fakeGitHubAppAPI) server() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/hook/deliveries", func(writer http.ResponseWriter, request *http.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.listRequests++
		api.sawAuthorization = append(api.sawAuthorization, request.Header.Get("Authorization"))
		api.requestedURLs = append(api.requestedURLs, request.URL.String())
		if api.onListRequest != nil {
			api.onListRequest(api.listRequests)
		}
		if api.listStatus != 0 {
			for key, values := range api.listHeader {
				for _, value := range values {
					writer.Header().Add(key, value)
				}
			}
			writer.WriteHeader(api.listStatus)
			return
		}
		perPage := api.perPage
		if perPage <= 0 {
			perPage = 100
		}
		page := 0
		if raw := request.URL.Query().Get("page"); raw != "" {
			page, _ = strconv.Atoi(raw)
		}
		all := make([]fakeDelivery, len(api.attempts))
		copy(all, api.attempts)
		sort.SliceStable(all, func(i, j int) bool { return all[i].deliveredAt.After(all[j].deliveredAt) })
		start := min(page*perPage, len(all))
		end := min(start+perPage, len(all))
		slice := all[start:end]
		if api.forceRepeatingLink {
			writer.Header().Set("Link", fmt.Sprintf(`<%s>; rel="next"`, "http://"+request.Host+request.URL.RequestURI()))
		} else if end < len(all) {
			writer.Header().Set("Link", fmt.Sprintf(`<%s/app/hook/deliveries?per_page=%d&page=%d>; rel="next"`, "http://"+request.Host, perPage, page+1))
		}
		items := make([]string, 0, len(slice))
		for _, delivery := range slice {
			items = append(items, delivery.json())
		}
		writer.Header().Set("Content-Type", "application/json")
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
		var guid, event string
		var prevStatus int
		for _, delivery := range api.attempts {
			if delivery.id == id {
				guid, event, prevStatus = delivery.guid, delivery.event, delivery.statusCode
			}
		}
		if guid != "" {
			result := prevStatus
			if override, ok := api.redeliverOutcome[guid]; ok {
				result = override
			}
			api.nextID++
			api.attempts = append(api.attempts, fakeDelivery{id: api.nextID, guid: guid, deliveredAt: api.now(), redelivery: true, statusCode: result, event: event})
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

// steppingClock is an injectable Now that a test advances explicitly,
// standing in for wall-clock time across a sequence of sweeps without any
// real sleep.
type steppingClock struct {
	mu  sync.Mutex
	now time.Time
}

func newSteppingClock(start time.Time) *steppingClock { return &steppingClock{now: start} }
func (clock *steppingClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}
func (clock *steppingClock) Advance(delta time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(delta)
}

// TestSweepRedeliversExactlyTheFailedGUIDsWithinTheWindow is the feature's
// central acceptance criterion: against three failed GUIDs, one successful
// GUID, one delivery older than 72 hours, and one GUID that already has a
// successful redelivery attempt, the sweep redelivers exactly the three —
// and, because the successful GUID's delivery is evidence the endpoint is
// reachable, counts all three as spent attempts.
func TestSweepRedeliversExactlyTheFailedGUIDsWithinTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.seed("guid-failed-1", "push", 500, now.Add(-1*time.Hour))
	api.seed("guid-failed-2", "push", 503, now.Add(-2*time.Hour))
	api.seed("guid-failed-3", "push", 400, now.Add(-3*time.Hour))
	api.seed("guid-succeeded", "push", 200, now.Add(-1*time.Hour))
	// guid-recovered's latest attempt (newest, listed first) already
	// succeeded on a redelivery; its earlier failure must not surface.
	api.seed("guid-recovered", "push", 500, now.Add(-10*time.Hour))
	api.seed("guid-recovered", "push", 200, now.Add(-30*time.Minute))
	// Older than the 72-hour window: must never be touched.
	api.seed("guid-too-old", "push", 500, now.Add(-80*time.Hour))
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: func() time.Time { return now }, Narrate: narrateFn,
	})

	delay := sweeper.Sweep(background())
	if delay != DefaultInterval {
		t.Fatalf("delay = %s, want the configured interval on a clean sweep", delay)
	}

	redelivered := api.redeliveredIDs()
	if len(redelivered) != 3 {
		t.Fatalf("redelivered ids = %v, want exactly 3", redelivered)
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
	if status.Redelivered != 3 || status.Abandoned != 0 || status.LastSweepAt == nil || !status.LastSweepAt.Equal(now) {
		t.Fatalf("status = %+v", status)
	}
	if status.LastFailureAt != nil || status.LastFailureClass != "" {
		t.Fatalf("a clean sweep must not record a failure: %+v", status)
	}

	redeliverLines := 0
	for _, line := range *lines {
		if line.Event != "redeliver" {
			t.Fatalf("narrated event column = %q, want %q", line.Event, "redeliver")
		}
		if strings.Contains(line.Action, "abandoned") || strings.Contains(line.Action, "not counted") {
			t.Fatalf("unexpected line: %+v", line)
		}
		redeliverLines++
	}
	if redeliverLines != 3 {
		t.Fatalf("narrated %d lines, want 3", redeliverLines)
	}
}

// TestMinimumGapSkipsAGUIDAttemptedWithinTheInterval is S1(a): a GUID
// redelivered less than Interval ago is left alone, which is what protects
// against a crash-loop restart hammering the redeliver endpoint.
func TestMinimumGapSkipsAGUIDAttemptedWithinTheInterval(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.seed("guid-evidence", "push", 200, now.Add(-1*time.Minute))
	api.seed("guid-gap", "push", 500, now.Add(-1*time.Hour))
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: func() time.Time { return now }, Interval: time.Hour,
	})

	sweeper.Sweep(background())
	if got := len(api.redeliveredIDs()); got != 1 {
		t.Fatalf("first sweep redeliver calls = %d, want 1", got)
	}

	// Immediately sweeping again (same clock, well within Interval) must
	// not call GitHub for this GUID a second time.
	sweeper.Sweep(background())
	if got := len(api.redeliveredIDs()); got != 1 {
		t.Fatalf("second sweep (within the gap) redeliver calls = %d, want still 1", got)
	}
}

// TestSweepRetriesUpToThreeAttemptsThenAbandons walks four sweeps of one
// GUID that never stops failing, spaced an Interval apart with fresh
// reachability evidence at each sweep (an unrelated delivery succeeding),
// the way a live repository with ongoing traffic would. The first three
// each redeliver and record one more attempt; the fourth abandons it
// without calling GitHub again. A fifth sweep touches it not at all.
func TestSweepRetriesUpToThreeAttemptsThenAbandons(t *testing.T) {
	start := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	clock := newSteppingClock(start)
	api := newFakeGitHubAppAPI(clock.Now)
	api.seed("guid-always-fails", "push", 500, start.Add(-1*time.Minute))
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: clock.Now, Interval: time.Hour, Narrate: narrateFn,
	})

	for pass := 1; pass <= 3; pass++ {
		// Fresh evidence of reachability for this pass: some other delivery
		// just succeeded.
		api.seed("guid-evidence", "push", 200, clock.Now())
		sweeper.Sweep(background())
		record, found := store.get("guid-always-fails")
		if !found || record.Attempts != pass || record.Abandoned {
			t.Fatalf("pass %d: record = %+v, found=%t", pass, record, found)
		}
		clock.Advance(time.Hour)
	}
	if got := len(api.redeliveredIDs()); got != 3 {
		t.Fatalf("redeliver calls = %d, want 3 after three retryable sweeps", got)
	}

	// Fourth sweep: attempts already at MaxAttempts, so this one abandons
	// instead of asking GitHub again.
	api.seed("guid-evidence", "push", 200, clock.Now())
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
	clock.Advance(time.Hour)
	linesBefore := len(*lines)
	sweeper.Sweep(background())
	if got := len(api.redeliveredIDs()); got != 3 {
		t.Fatalf("redeliver calls after a fifth sweep = %d, want still 3", got)
	}
	if len(*lines) != linesBefore {
		t.Fatalf("a fifth sweep narrated %d more lines for an abandoned GUID", len(*lines)-linesBefore)
	}
}

// TestSweepStopsOnceAGUIDSucceeds covers the case where a redelivery fixes a
// delivery: the fake's own redeliver handler creates the new, successful
// attempt, so the next sweep's listing shows it resolved on its own, exactly
// as GitHub's real API would.
func TestSweepStopsOnceAGUIDSucceeds(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.seed("guid-evidence", "push", 200, now.Add(-1*time.Minute))
	api.seed("guid-recovers", "push", 500, now.Add(-1*time.Hour))
	api.redeliverOutcome["guid-recovers"] = http.StatusOK
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: func() time.Time { return now }, Interval: time.Hour, Narrate: narrateFn,
	})

	sweeper.Sweep(background())
	if got := len(api.redeliveredIDs()); got != 1 {
		t.Fatalf("first sweep redeliver calls = %d, want 1", got)
	}
	if status, found := api.latestStatus("guid-recovers"); !found || status != http.StatusOK {
		t.Fatalf("the fake's own redeliver call must have recorded a successful new attempt: status=%d found=%t", status, found)
	}

	linesBefore := len(*lines)
	sweeper.Sweep(background())
	if got := len(api.redeliveredIDs()); got != 1 {
		t.Fatalf("second sweep redeliver calls = %d, want still 1", got)
	}
	if len(*lines) != linesBefore {
		t.Fatalf("a sweep on a succeeded GUID narrated %d more lines", len(*lines)-linesBefore)
	}
	if status := sweeper.Status(); status.Redelivered != 0 {
		t.Fatalf("second sweep status = %+v, want 0 redelivered", status)
	}
}

// TestNoEvidenceRedeliversWithoutSpendingAnAttempt is S1(b): with nothing in
// the 72-hour window ever succeeding, the sweep still asks GitHub to
// redeliver every sweep (the delivery may succeed) but must never spend a
// counted attempt, and must narrate "not counted" exactly once per sweep,
// not once per GUID.
func TestNoEvidenceRedeliversWithoutSpendingAnAttempt(t *testing.T) {
	start := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	clock := newSteppingClock(start)
	api := newFakeGitHubAppAPI(clock.Now)
	api.seed("guid-down-1", "push", 500, start.Add(-1*time.Minute))
	api.seed("guid-down-2", "push", 500, start.Add(-2*time.Minute))
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: clock.Now, Interval: time.Hour, Narrate: narrateFn,
	})

	// Four sweeps, an Interval apart: with no evidence ever appearing, every
	// sweep must redeliver both GUIDs again and never reach MaxAttempts.
	for pass := 1; pass <= 4; pass++ {
		sweeper.Sweep(background())
		for _, guid := range []string{"guid-down-1", "guid-down-2"} {
			record, found := store.get(guid)
			if !found || record.Attempts != 0 || record.Abandoned {
				t.Fatalf("pass %d: %s record = %+v, found=%t, want Attempts=0 and never abandoned", pass, guid, record, found)
			}
		}
		clock.Advance(time.Hour)
	}
	if got := len(api.redeliveredIDs()); got != 8 {
		t.Fatalf("redeliver calls = %d, want 8 (2 GUIDs x 4 sweeps)", got)
	}

	uncounted := 0
	for _, line := range *lines {
		if line.Action == "endpoint unreachable; not counted" {
			uncounted++
		}
		if strings.Contains(line.Action, "redelivered") || strings.Contains(line.Action, "abandoned") {
			t.Fatalf("an uncounted redeliver must not be narrated as a normal one: %+v", line)
		}
	}
	if uncounted != 4 {
		t.Fatalf("uncounted lines = %d, want exactly 1 per sweep (4)", uncounted)
	}
}

// TestPerDeliveryRejectionCountsAbandonsAndContinues is S2: GitHub itself
// rejecting one GUID's redeliver call (400/404/422) still counts as a spent
// attempt, abandons at the limit, is narrated, and does not stop the sweep
// from reaching the next GUID.
func TestPerDeliveryRejectionCountsAbandonsAndContinues(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.seed("guid-evidence", "push", 200, now.Add(-1*time.Minute))
	api.seed("guid-gone", "push", 500, now.Add(-1*time.Hour))
	api.seed("guid-other", "push", 500, now.Add(-2*time.Hour))
	api.redeliverStatus = http.StatusNotFound // GitHub says the delivery id is gone.
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: func() time.Time { return now }, Interval: time.Hour, Narrate: narrateFn,
	})

	delay := sweeper.Sweep(background())
	if delay != DefaultInterval {
		t.Fatalf("delay = %s, a per-delivery rejection must not abort the sweep", delay)
	}
	// Both GUIDs must have been reached: a per-delivery 404 must not stop
	// the loop from continuing to the next one.
	if len(api.redeliveredIDs()) != 2 {
		t.Fatalf("redeliver calls = %v, want both GUIDs reached", api.redeliveredIDs())
	}
	for _, guid := range []string{"guid-gone", "guid-other"} {
		record, found := store.get(guid)
		if !found || record.Attempts != 1 {
			t.Fatalf("%s record = %+v, found=%t, want one counted attempt", guid, record, found)
		}
	}
	rejected := 0
	for _, line := range *lines {
		if strings.Contains(line.Action, "rejected") {
			rejected++
		}
	}
	if rejected != 2 {
		t.Fatalf("narrated %d rejection lines, want 2", rejected)
	}
}

// TestSystemicFailureAbortsTheSweep is the other half of S2: a redeliver
// call answered with a systemic status (403) aborts the rest of the sweep,
// leaving the next GUID untouched for the next pass.
func TestSystemicFailureAbortsTheSweep(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.seed("guid-evidence", "push", 200, now.Add(-1*time.Minute))
	api.seed("guid-first", "push", 500, now.Add(-1*time.Hour))
	api.seed("guid-second", "push", 500, now.Add(-2*time.Hour))
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: func() time.Time { return now }, Interval: time.Hour,
	})
	// Fail every redeliver call from here on with a systemic status.
	api.redeliverStatus = http.StatusForbidden

	// The first failure's backoff legitimately starts at Interval (B1), the
	// same value a clean sweep returns, so the signal that this sweep
	// failed is the recorded failure class below, not the returned delay.
	sweeper.Sweep(background())
	redelivered := api.redeliveredIDs()
	if len(redelivered) != 1 {
		t.Fatalf("redeliver calls = %v, want exactly 1 before the abort", redelivered)
	}
	status := sweeper.Status()
	if status.LastFailureAt == nil || status.LastFailureClass != "auth" {
		t.Fatalf("status = %+v, want a recorded auth failure", status)
	}
	// The prune-and-record-status contract still ran despite the abort.
	if status.LastSweepAt == nil {
		t.Fatal("an aborted sweep must still record LastSweepAt")
	}
}

// TestAttemptIsSavedBeforeTheRedeliverCall is M2: a crash between the save
// and GitHub's answer must never be able to grant a fourth real attempt.
// This sweep's redeliver call is made to fail outright (a systemic error),
// and the store must already show the attempt spent regardless.
func TestAttemptIsSavedBeforeTheRedeliverCall(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.seed("guid-evidence", "push", 200, now.Add(-1*time.Minute))
	api.seed("guid-crash", "push", 500, now.Add(-1*time.Hour))
	api.redeliverStatus = http.StatusInternalServerError
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: func() time.Time { return now }, Interval: time.Hour,
	})
	sweeper.Sweep(background())

	record, found := store.get("guid-crash")
	if !found || record.Attempts != 1 {
		t.Fatalf("record = %+v, found=%t, want the attempt saved even though the call itself failed", record, found)
	}
}

// TestJWTIsReMintedAfterFiveMinutes is M3: a sweep spanning more than
// jwtRefreshAfter must sign a fresh Authorization bearer token rather than
// reusing one past its useful lifetime.
func TestJWTIsReMintedAfterFiveMinutes(t *testing.T) {
	start := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	clock := newSteppingClock(start)
	api := newFakeGitHubAppAPI(clock.Now)
	// Two pages, so the sweep makes two GET calls; the clock advances past
	// the refresh threshold between them.
	api.perPage = 1
	api.seed("guid-a", "push", 500, start.Add(-1*time.Minute))
	api.seed("guid-b", "push", 500, start.Add(-2*time.Minute))
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: clock.Now, Interval: time.Hour,
	})

	// Advance the shared clock past jwtRefreshAfter right after the first
	// page is served, so ensureToken re-mints before the second page's GET,
	// with no real sleep anywhere.
	api.onListRequest = func(count int) {
		if count == 1 {
			clock.Advance(6 * time.Minute)
		}
	}
	sweeper.Sweep(background())

	if len(api.sawAuthorization) < 2 {
		t.Fatalf("saw %d list requests, want at least 2 (two pages)", len(api.sawAuthorization))
	}
	first, second := api.sawAuthorization[0], api.sawAuthorization[1]
	if first == "" || second == "" {
		t.Fatal("every request must carry a bearer token")
	}
	if first == second {
		t.Fatal("a JWT minted more than 5 minutes before a later call must have been re-minted, producing a different token")
	}
}

// TestPageCapStopsAnUnboundedListing is M4's first half: pagination never
// asks for more than maxPages pages, whatever the Link header says.
func TestPageCapStopsAnUnboundedListing(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.perPage = 1
	for i := 0; i < maxPages+10; i++ {
		api.seed(fmt.Sprintf("guid-%d", i), "push", 200, now.Add(-time.Duration(i)*time.Second))
	}
	server := api.server()
	defer server.Close()

	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: newFakeStore(), Now: func() time.Time { return now },
	})
	sweeper.Sweep(background())

	if api.listRequests > maxPages {
		t.Fatalf("list requests = %d, want at most maxPages (%d)", api.listRequests, maxPages)
	}
}

// TestRepeatingNextURLStopsThePagination is M4's second half: a
// misbehaving Link header that always points at the same URL must not loop
// forever.
func TestRepeatingNextURLStopsThePagination(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.forceRepeatingLink = true
	api.seed("guid-a", "push", 500, now.Add(-1*time.Minute))
	server := api.server()
	defer server.Close()

	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: newFakeStore(), Now: func() time.Time { return now },
	})
	done := make(chan time.Duration, 1)
	go func() { done <- sweeper.Sweep(background()) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Sweep did not return; a repeating next URL must not loop forever")
	}
	if api.listRequests > 1 {
		t.Fatalf("list requests = %d, want exactly 1 (the repeat is detected before a second fetch)", api.listRequests)
	}
}

// TestRetentionPruneRemovesRecordsOlderThanSevenDays proves the 7-day
// retention the store is documented to carry: a sweep with nothing to act on
// still prunes.
func TestRetentionPruneRemovesRecordsOlderThanSevenDays(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	store.records["expired"] = hub.WebhookRedeliveryRecord{GUID: "expired", Attempts: 3, Abandoned: true, LastAttemptAt: now.Add(-8 * 24 * time.Hour)}
	store.records["fresh"] = hub.WebhookRedeliveryRecord{GUID: "fresh", Attempts: 1, LastAttemptAt: now.Add(-1 * 24 * time.Hour)}

	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: func() time.Time { return now },
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

// TestPruneRunsEvenAfterAnAbortedSweep is part of S2's "always run prune and
// update the status" requirement.
func TestPruneRunsEvenAfterAnAbortedSweep(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.listStatus = http.StatusInternalServerError
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	store.records["expired"] = hub.WebhookRedeliveryRecord{GUID: "expired", Attempts: 3, Abandoned: true, LastAttemptAt: now.Add(-8 * 24 * time.Hour)}

	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: func() time.Time { return now },
	})
	sweeper.Sweep(background())

	if _, found := store.get("expired"); found {
		t.Fatal("prune must still run when the listing call itself fails")
	}
	status := sweeper.Status()
	if status.LastSweepAt == nil || status.LastFailureAt == nil {
		t.Fatalf("status = %+v, want both set after an aborted sweep", status)
	}
}

// TestSweepIsInertWithoutFullAppConfiguration is the defensive counterpart of
// "there is no activity when hub.github.app is not configured": the wiring
// test for that lives in cmd/wb, where an unconfigured hub never even
// constructs a Sweeper. This proves the Sweeper itself never calls out when
// it is missing what it needs.
func TestSweepIsInertWithoutFullAppConfiguration(t *testing.T) {
	api := newFakeGitHubAppAPI(time.Now)
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

// TestSweepBacksOffAtTheProductionIntervalAndCapsAtSixHours is B1: the
// production Interval (1h) is what the backoff starts from and doubles from,
// capped at MaxBackoff (6h) rather than the shorter, unrelated MaxBackoff the
// bug had before. A test at a fake one-minute interval would have hidden
// exactly this bug, so this one runs at DefaultInterval.
func TestSweepBacksOffAtTheProductionIntervalAndCapsAtSixHours(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.listStatus = http.StatusInternalServerError
	server := api.server()
	defer server.Close()

	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: newFakeStore(), Now: func() time.Time { return now },
	})
	if sweeper.options.Interval != DefaultInterval {
		t.Fatalf("interval = %s, want the production DefaultInterval", sweeper.options.Interval)
	}

	want := []time.Duration{time.Hour, 2 * time.Hour, 4 * time.Hour, 6 * time.Hour, 6 * time.Hour}
	for index, expected := range want {
		if got := sweeper.Sweep(background()); got != expected {
			t.Fatalf("sweep %d backoff = %s, want %s", index+1, got, expected)
		}
	}

	api.listStatus = 0
	if got := sweeper.Sweep(background()); got != DefaultInterval {
		t.Fatalf("delay after a clean sweep = %s, want the interval (backoff reset)", got)
	}
}

// TestSweepHonorsRetryAfterOnRateLimit is B1's second half: a 429 carrying
// Retry-After overrides the exponential backoff with GitHub's own number.
func TestSweepHonorsRetryAfterOnRateLimit(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.listStatus = http.StatusTooManyRequests
	api.listHeader = http.Header{"Retry-After": []string{"120"}}
	server := api.server()
	defer server.Close()

	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: newFakeStore(), Now: func() time.Time { return now },
	})
	if got := sweeper.Sweep(background()); got != 120*time.Second {
		t.Fatalf("delay = %s, want the Retry-After value (120s)", got)
	}
}

// TestSweepHonorsRateLimitResetWithoutRetryAfter covers the other header
// GitHub may send instead: X-RateLimit-Reset.
func TestSweepHonorsRateLimitResetWithoutRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	reset := now.Add(90 * time.Second)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.listStatus = http.StatusForbidden
	api.listHeader = http.Header{"X-RateLimit-Remaining": []string{"0"}, "X-RateLimit-Reset": []string{strconv.FormatInt(reset.Unix(), 10)}}
	server := api.server()
	defer server.Close()

	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: newFakeStore(), Now: func() time.Time { return now },
	})
	got := sweeper.Sweep(background())
	if got <= 0 || got > 90*time.Second {
		t.Fatalf("delay = %s, want roughly 90s from X-RateLimit-Reset", got)
	}
}

// TestNarrationCarriesNoSecretsAndNoIdentifiers is the golden check the
// common brief, the task, and the review all require: exact line shape,
// no installation or repository identifier (M1), and no JWT, key material,
// or bearer token anywhere in a narrated line.
func TestNarrationCarriesNoSecretsAndNoIdentifiers(t *testing.T) {
	now := time.Date(2026, 9, 18, 14, 2, 11, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.seed("guid-evidence", "push", 200, now.Add(-1*time.Minute))
	api.seed("guid-golden", "push", 500, now.Add(-1*time.Hour))
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	narrateFn, lines := recordingNarrate()
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: func() time.Time { return now }, Interval: time.Hour, Narrate: narrateFn,
	})
	sweeper.Sweep(background())

	redeliverLine := ""
	for _, line := range *lines {
		if strings.Contains(line.Action, "redelivered") {
			redeliverLine = line.String()
		}
	}
	want := "14:02:11 redeliver       push github.com                  redelivered (attempt 1 of 3)"
	if redeliverLine != want {
		t.Fatalf("narrated line = %q, want %q", redeliverLine, want)
	}
	if strings.Contains(redeliverLine, "repository:") || strings.Contains(redeliverLine, "installation:") {
		t.Fatal("the narrated line carried an identifier")
	}

	for _, authorization := range api.sawAuthorization {
		if !strings.HasPrefix(authorization, "Bearer ") {
			t.Fatalf("request carried no bearer token: %q", authorization)
		}
		token := strings.TrimPrefix(authorization, "Bearer ")
		if strings.Contains(redeliverLine, token) {
			t.Fatal("the narrated line carried the App JWT")
		}
	}
	if strings.Contains(redeliverLine, "BEGIN RSA PRIVATE KEY") {
		t.Fatal("the narrated line carried key material")
	}
}

// TestDeliveryItemSubjectIsEventNamePlusGithubCom covers M1: never an
// identifier, always the event name (or "unknown"/absent) plus github.com.
func TestDeliveryItemSubjectIsEventNamePlusGithubCom(t *testing.T) {
	for _, testCase := range []struct {
		name string
		item deliveryItem
		want string
	}{
		{"named event", deliveryItem{Event: "push"}, "push github.com"},
		{"blank event", deliveryItem{}, "github.com"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.item.subject(); got != testCase.want {
				t.Fatalf("subject() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestRunSweepsOnStartAndStopsOnContextCancellation covers the actual Run
// loop with an injected clock and Sleep — never a real timer — the way M9
// asks: Sleep counts its calls and reports "cancelled" on the third one,
// simulating ctx ending without any wall-clock wait.
func TestRunSweepsOnStartAndStopsOnContextCancellation(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	server := api.server()
	defer server.Close()

	store := newFakeStore()
	sleeps := 0
	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: store, Now: func() time.Time { return now }, Interval: time.Hour,
		Sleep: func(context.Context, time.Duration) error {
			sleeps++
			if sleeps >= 3 {
				return context.Canceled
			}
			return nil
		},
	})

	if err := sweeper.Run(background()); err != nil {
		t.Fatalf("Run = %v, want nil once Sleep reports the loop should end", err)
	}
	if api.listRequests != 3 {
		t.Fatalf("list requests = %d, want exactly 3 (Run sweeps once per Sleep call including the first)", api.listRequests)
	}
	if sleeps != 3 {
		t.Fatalf("Sleep calls = %d, want exactly 3", sleeps)
	}
}

// TestRunReturnsImmediatelyOnAnAlreadyCancelledContext covers the other exit
// from Run: a context cancelled before the first sleep even starts.
func TestRunReturnsImmediatelyOnAnAlreadyCancelledContext(t *testing.T) {
	api := newFakeGitHubAppAPI(time.Now)
	server := api.server()
	defer server.Close()

	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: newFakeStore(), Interval: time.Hour,
		Sleep: func(context.Context, time.Duration) error { return context.Canceled },
	})
	if err := sweeper.Run(background()); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

// TestRunUsesTheDefaultSleepContextImplementation is the one test that does
// not inject Sleep, so sleepContext itself — both its zero-delay branch and
// its ctx-ends-first branch — is exercised rather than only the fakes above.
func TestRunUsesTheDefaultSleepContextImplementation(t *testing.T) {
	api := newFakeGitHubAppAPI(time.Now)
	server := api.server()
	defer server.Close()

	sweeper := New(Options{
		Client: server.Client(), APIBaseURL: server.URL,
		AppID: 1234, PrivateKeyPEM: testAppPrivateKeyPEM,
		Store: newFakeStore(), Interval: time.Hour,
	})
	if got := sleepContext(background(), 0); got != nil {
		t.Fatalf("sleepContext with a non-positive delay = %v, want nil on a live context", got)
	}
	ctx, cancel := context.WithCancel(background())
	cancel()
	if got := sleepContext(ctx, time.Hour); got == nil {
		t.Fatal("sleepContext must return the context's error once it is done")
	}

	ctx, cancel = context.WithTimeout(background(), 20*time.Millisecond)
	defer cancel()
	if err := sweeper.Run(ctx); err != nil {
		t.Fatalf("Run = %v, want nil once the real context deadline passes", err)
	}
}

// TestStoreFailuresAreClassifiedAndSurfaced covers every point actOn and
// prune read from or write to the store: each failure aborts the sweep,
// classifies as "store", and is still followed by a status update.
func TestStoreFailuresAreClassifiedAndSurfaced(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	fault := errors.New("store is down")

	t.Run("load", func(t *testing.T) {
		api := newFakeGitHubAppAPI(func() time.Time { return now })
		api.seed("guid-a", "push", 500, now.Add(-1*time.Minute))
		server := api.server()
		defer server.Close()
		store := newFakeStore()
		store.loadErr = fault
		sweeper := New(Options{Client: server.Client(), APIBaseURL: server.URL, AppID: 1, PrivateKeyPEM: testAppPrivateKeyPEM, Store: store, Now: func() time.Time { return now }})
		sweeper.Sweep(background())
		if status := sweeper.Status(); status.LastFailureClass != "store" {
			t.Fatalf("status = %+v", status)
		}
	})
	t.Run("save", func(t *testing.T) {
		api := newFakeGitHubAppAPI(func() time.Time { return now })
		api.seed("guid-evidence", "push", 200, now.Add(-1*time.Minute))
		api.seed("guid-a", "push", 500, now.Add(-2*time.Minute))
		server := api.server()
		defer server.Close()
		store := newFakeStore()
		store.saveErr = fault
		sweeper := New(Options{Client: server.Client(), APIBaseURL: server.URL, AppID: 1, PrivateKeyPEM: testAppPrivateKeyPEM, Store: store, Now: func() time.Time { return now }})
		sweeper.Sweep(background())
		if status := sweeper.Status(); status.LastFailureClass != "store" {
			t.Fatalf("status = %+v", status)
		}
	})
	t.Run("list (prune)", func(t *testing.T) {
		api := newFakeGitHubAppAPI(func() time.Time { return now })
		server := api.server()
		defer server.Close()
		store := newFakeStore()
		store.listErr = fault
		sweeper := New(Options{Client: server.Client(), APIBaseURL: server.URL, AppID: 1, PrivateKeyPEM: testAppPrivateKeyPEM, Store: store, Now: func() time.Time { return now }})
		sweeper.Sweep(background())
		if status := sweeper.Status(); status.LastFailureClass != "store" {
			t.Fatalf("status = %+v", status)
		}
	})
	t.Run("delete (prune)", func(t *testing.T) {
		api := newFakeGitHubAppAPI(func() time.Time { return now })
		server := api.server()
		defer server.Close()
		store := newFakeStore()
		store.records["expired"] = hub.WebhookRedeliveryRecord{GUID: "expired", Attempts: 1, LastAttemptAt: now.Add(-8 * 24 * time.Hour)}
		store.deleteErr = fault
		sweeper := New(Options{Client: server.Client(), APIBaseURL: server.URL, AppID: 1, PrivateKeyPEM: testAppPrivateKeyPEM, Store: store, Now: func() time.Time { return now }})
		sweeper.Sweep(background())
		if status := sweeper.Status(); status.LastFailureClass != "store" {
			t.Fatalf("status = %+v", status)
		}
	})
}

// TestClassifyFallsBackToUnknown covers an error this package never itself
// produced, which LastFailureClass must still render as something rather
// than panicking on a type assertion.
func TestClassifyFallsBackToUnknown(t *testing.T) {
	if got := classify(errors.New("some other package's error")); got != "unknown" {
		t.Fatalf("classify = %q, want %q", got, "unknown")
	}
	wrapped := &sweepError{class: "auth", err: errors.New("github responded 403")}
	if got := wrapped.Unwrap(); got == nil || got.Error() != "github responded 403" {
		t.Fatalf("Unwrap = %v", got)
	}
}
