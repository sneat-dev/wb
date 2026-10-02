package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/wbconfig"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const (
	testHost      = "127.0.0.1:8766"
	hostedOrigin  = "https://hosted.example"
	testMachine   = "laptop"
	sentinel      = "SENTINEL-"
	testVersion   = "v1.2.3"
	remotePublish = "2026-09-30T10:00:00Z"
)

// fakeSources is every collector, with the calls it received counted so a test
// can prove a request ran none. Its fields are read under mu.
type fakeSources struct {
	mu           sync.Mutex
	repos        []discover.Repo
	worktrees    map[string][]LinkedWorktree
	branches     map[string][]BranchRef
	records      map[string]WorktreeRecord
	bindings     []worktrees.RegisteredPullRequestBinding
	sessions     []session.View
	runs         []agents.Result
	remote       []remotestate.Entry
	activity     ActivityCollector
	branch       string
	readme       []byte
	readmeErr    error
	readmeBranch string

	panicIn string

	repoErr, worktreeErr, branchErr, bindingErr, sessionErr, runErr, remoteErr error

	calls         atomic.Int64
	worktreeCalls atomic.Int64
	recordCalls   atomic.Int64
}

func (f *fakeSources) collectors() Collectors {
	return Collectors{Repositories: f, Worktrees: f, Branches: f, Readme: f, Records: f, PullRequests: f, Sessions: f, Runs: f, Remote: f, Activity: f.activity}
}

func (f *fakeSources) Repositories(context.Context) ([]discover.Repo, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maybePanic("repositories")
	return append([]discover.Repo(nil), f.repos...), f.repoErr
}

func (f *fakeSources) Worktrees(_ context.Context, repo discover.Repo) ([]LinkedWorktree, error) {
	f.calls.Add(1)
	f.worktreeCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maybePanic("worktrees")
	return append([]LinkedWorktree(nil), f.worktrees[repo.Slug()]...), f.worktreeErr
}

func (f *fakeSources) Branches(_ context.Context, repo discover.Repo) ([]BranchRef, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maybePanic("branches")
	return append([]BranchRef(nil), f.branches[repo.Slug()]...), f.branchErr
}

func (f *fakeSources) DefaultBranch(context.Context, discover.Repo) string {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maybePanic("default-branch")
	return f.branch
}

func (f *fakeSources) Readme(_ context.Context, _ discover.Repo, branch string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.readmeBranch = branch
	return f.readme, f.readmeErr
}

func (f *fakeSources) Record(path string) (WorktreeRecord, bool) {
	f.calls.Add(1)
	f.recordCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maybePanic("record")
	record, ok := f.records[path]
	return record, ok
}

func (f *fakeSources) PullRequests(context.Context) ([]worktrees.RegisteredPullRequestBinding, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maybePanic("pull-requests")
	return append([]worktrees.RegisteredPullRequestBinding(nil), f.bindings...), f.bindingErr
}

func (f *fakeSources) Sessions(context.Context) ([]session.View, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maybePanic("sessions")
	return append([]session.View(nil), f.sessions...), f.sessionErr
}

func (f *fakeSources) Runs(context.Context) ([]agents.Result, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maybePanic("runs")
	return append([]agents.Result(nil), f.runs...), f.runErr
}

func (f *fakeSources) Machines(context.Context) ([]remotestate.Entry, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.maybePanic("machines")
	return append([]remotestate.Entry(nil), f.remote...), f.remoteErr
}

// maybePanic panics when the test asked this source to; the caller holds mu.
func (f *fakeSources) maybePanic(source string) {
	if f.panicIn == source {
		panic("a collector panicked")
	}
}

// change applies edit to the fake's data under its lock.
func (f *fakeSources) change(edit func(*fakeSources)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f)
}

// forbidden is a set of collectors that fail the test when any is called.
type forbidden struct{ t *testing.T }

func (f forbidden) collectors() Collectors {
	return Collectors{Repositories: f, Worktrees: f, Branches: f, Readme: f, Records: f, PullRequests: f, Sessions: f, Runs: f, Remote: f}
}

func (f forbidden) fail() { f.t.Error("a collector ran during a request") }

func (f forbidden) Repositories(context.Context) ([]discover.Repo, error) { f.fail(); return nil, nil }
func (f forbidden) Worktrees(context.Context, discover.Repo) ([]LinkedWorktree, error) {
	f.fail()
	return nil, nil
}
func (f forbidden) Branches(context.Context, discover.Repo) ([]BranchRef, error) {
	f.fail()
	return nil, nil
}
func (f forbidden) DefaultBranch(context.Context, discover.Repo) string { f.fail(); return "" }
func (f forbidden) Readme(context.Context, discover.Repo, string) ([]byte, error) {
	f.fail()
	return nil, nil
}
func (f forbidden) Record(string) (WorktreeRecord, bool) { f.fail(); return WorktreeRecord{}, false }
func (f forbidden) PullRequests(context.Context) ([]worktrees.RegisteredPullRequestBinding, error) {
	f.fail()
	return nil, nil
}
func (f forbidden) Sessions(context.Context) ([]session.View, error) { f.fail(); return nil, nil }
func (f forbidden) Runs(context.Context) ([]agents.Result, error)    { f.fail(); return nil, nil }
func (f forbidden) Machines(context.Context) ([]remotestate.Entry, error) {
	f.fail()
	return nil, nil
}

var errBoom = errors.New("boom")

// manualClock is a clock a test advances.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *manualClock {
	return &manualClock{now: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// constFingerprint is a fingerprint the test controls.
type constFingerprint struct {
	value atomic.Value
	calls atomic.Int64
}

func newConstFingerprint(value string) *constFingerprint {
	f := &constFingerprint{}
	f.value.Store(value)
	return f
}

func (f *constFingerprint) get(string) (string, error) {
	f.calls.Add(1)
	return f.value.Load().(string), nil
}

// passCalls is how many collector calls one pass over oneRepoSources makes
// when the repository's Git state is read: the scan, the worktrees, the
// branches, the default branch, two records, the pull-request records, the sessions, the runs and
// the other machines.
const passCalls = 10

// oneRepoSources is a fake with one repository holding two worktrees (the
// first with a live owner process, the second with a gone one), their branches, a pull request recorded for
// the first, a session and a second machine's snapshot.
func oneRepoSources(path string) *fakeSources {
	repo := discover.Repo{Host: "github.com", Org: "acme", Name: "widgets", Path: path}
	published, _ := time.Parse(time.RFC3339, remotePublish)
	now := newClock().Now()
	return &fakeSources{
		repos:  []discover.Repo{repo},
		branch: "main",
		readme: []byte("# Widgets\n"),
		worktrees: map[string][]LinkedWorktree{"acme/widgets": {
			{Path: "/wt/task-a", Branch: "feature/a"}, {Path: "/wt/task-b", Branch: "feature/b"},
		}},
		records: map[string]WorktreeRecord{
			"/wt/task-a": {Task: "task-a", Branch: "feature/a", CreatedAt: now.Add(-72 * time.Hour), HeartbeatAt: now.Add(-time.Hour), Owner: worktrees.OwnerLive},
			"/wt/task-b": {Task: "task-b", Branch: "feature/b", CreatedAt: now.Add(-72 * time.Hour), HeartbeatAt: now.Add(-48 * time.Hour), Owner: worktrees.OwnerGone},
		},
		branches: map[string][]BranchRef{"acme/widgets": {
			{Name: "feature/a", Scope: BranchLocal, Upstream: "origin/feature/a", Ahead: 2},
			{Name: "feature/b", Scope: BranchLocal},
			{Name: "origin/feature/c", Scope: BranchRemote},
		}},
		bindings: []worktrees.RegisteredPullRequestBinding{{Task: "task-a", Repository: "acme/widgets", PullRequest: 7, URL: "https://github.com/acme/widgets/pull/7"}},
		sessions: []session.View{{Record: session.Record{WBSessionID: "wbs-1", Runtime: "claude", Model: "opus"}, State: session.StateLive}},
		remote: []remotestate.Entry{{Snapshot: remotestate.Snapshot{
			Login: "someone", Machine: "desktop", PublishedAt: published, WBVersion: "v0.9.0",
			KnownRepositories: []string{"acme/widgets", "acme/gadgets"},
			Worktrees: []remotestate.WorktreeState{{
				Task: "task-x", Repository: "acme/gadgets", Branch: "feature/x", Lifecycle: "active", OwnerState: "active",
				PullRequest: &remotestate.PullRequestState{Number: 3, State: "OPEN", URL: "https://github.com/acme/gadgets/pull/3"},
			}},
		}}},
	}
}

// refreshAndSettle runs one pass and waits for the read of the other machines
// it started, which the pass does not wait for.
func refreshAndSettle(t *testing.T, snapshotter *Snapshotter) {
	t.Helper()
	if err := snapshotter.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshotter.side.Wait()
}

// newSnapshotter builds a snapshotter over sources with a manual clock.
func newSnapshotter(sources Collectors, change func(*Options)) (*Snapshotter, *manualClock) {
	clock := newClock()
	options := Options{Machine: testMachine, Version: testVersion, Collectors: sources, Now: clock.Now}
	if change != nil {
		change(&options)
	}
	return New(options), clock
}

// cockpitServer is a real Cockpit server with the fleet routes registered and
// its API mount, plus a way to log in as the owner.
type cockpitServer struct {
	t      *testing.T
	server *cockpit.Server
	api    http.Handler
	page   http.Handler
	// session is the owner session owner() logged in with, kept for the test.
	session *http.Cookie
}

// owner returns one owner session cookie for the whole test: a read with it is
// an owner's, the only reader that is demand for the SSH transport.
func (c *cockpitServer) owner() *http.Cookie {
	c.t.Helper()
	if c.session == nil {
		c.session = c.login()
	}
	return c.session
}

func newCockpitServer(t *testing.T, snapshotter *Snapshotter) *cockpitServer {
	t.Helper()
	server := cockpit.New(cockpit.Options{
		CanonicalHost: "127.0.0.1",
		Config:        wbconfig.CockpitConfig{HostedURL: hostedOrigin + "/wb/cockpit/", AnonymousMetadata: true},
	})
	Register(server, snapshotter)
	mounts := server.Mounts()
	return &cockpitServer{t: t, server: server, api: mounts[cockpit.APIPrefix], page: mounts[cockpit.PagePrefix]}
}

// get requests target on the API mount with the given cookie and headers.
func (c *cockpitServer) get(target string, cookie *http.Cookie, headers ...string) *httptest.ResponseRecorder {
	c.t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.Host = testHost
	for i := 0; i < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	c.api.ServeHTTP(recorder, request)
	return recorder
}

// login returns an owner session cookie.
func (c *cockpitServer) login() *http.Cookie {
	c.t.Helper()
	issued, err := c.server.MintLoginCode()
	if err != nil {
		c.t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, cockpit.LoginPath+"?code="+issued.Code, nil)
	request.Host = testHost
	recorder := httptest.NewRecorder()
	c.page.ServeHTTP(recorder, request)
	cookies := recorder.Result().Cookies()
	if recorder.Code != http.StatusSeeOther || len(cookies) != 1 {
		c.t.Fatalf("login = %d with %d cookies", recorder.Code, len(cookies))
	}
	return cookies[0]
}

// fleet requests the fleet read model as anonymous-local and decodes it.
func (c *cockpitServer) fleet() Document {
	c.t.Helper()
	recorder := c.get(cockpit.APIPrefix+FleetRoute, nil)
	if recorder.Code != http.StatusOK {
		c.t.Fatalf("fleet = %d %s", recorder.Code, recorder.Body.String())
	}
	var document Document
	if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
		c.t.Fatal(err)
	}
	return document
}

// sentinelNumber is the number every settable numeric field of a source value
// is given, so a number that reaches the document by a leak is as visible as a
// string.
const sentinelNumber = 7351957

// fillValue sets every settable string in value to the sentinel plus its field
// path and every settable number to sentinelNumber, allocates nil pointers,
// gives a nil or empty slice one filled element and a nil map one filled
// entry, and recurses into nested structs, pointers, slices and maps. A test
// then overrides the few fields the document may carry, so what remains is
// everything the mapping must drop, including any field a source type gains
// later.
func fillValue(value reflect.Value, label string, depth int) {
	if depth > 8 {
		return
	}
	switch value.Kind() {
	case reflect.String:
		if value.CanSet() {
			value.SetString(sentinel + label)
		}
	case reflect.Int, reflect.Int32, reflect.Int64:
		if value.CanSet() {
			value.SetInt(sentinelNumber)
		}
	case reflect.Uint, reflect.Uint32, reflect.Uint64:
		if value.CanSet() {
			value.SetUint(sentinelNumber)
		}
	case reflect.Float32, reflect.Float64:
		if value.CanSet() {
			value.SetFloat(sentinelNumber)
		}
	case reflect.Struct:
		for index := range value.NumField() {
			if field := value.Field(index); field.CanSet() {
				fillValue(field, label+"."+value.Type().Field(index).Name, depth+1)
			}
		}
	case reflect.Pointer:
		if value.IsNil() && value.CanSet() {
			value.Set(reflect.New(value.Type().Elem()))
		}
		if !value.IsNil() {
			fillValue(value.Elem(), label, depth+1)
		}
	case reflect.Slice:
		if value.CanSet() {
			value.Set(reflect.MakeSlice(value.Type(), 1, 1))
			fillValue(value.Index(0), label, depth+1)
		}
	case reflect.Map:
		if value.CanSet() {
			key, element := reflect.New(value.Type().Key()).Elem(), reflect.New(value.Type().Elem()).Elem()
			fillValue(key, label+".key", depth+1)
			fillValue(element, label, depth+1)
			value.Set(reflect.MakeMap(value.Type()))
			value.SetMapIndex(key, element)
		}
	}
}

// filled returns a T with every field filled by fillValue.
func filled[T any]() T {
	var value T
	fillValue(reflect.ValueOf(&value).Elem(), reflect.TypeFor[T]().Name(), 0)
	return value
}

// jsonFields lists the JSON field names of a struct type, embedded structs
// flattened as encoding/json does.
func jsonFields(value any) []string {
	var names []string
	var walk func(reflect.Type)
	walk = func(kind reflect.Type) {
		for index := range kind.NumField() {
			field := kind.Field(index)
			if field.Anonymous {
				walk(field.Type)
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name != "" {
				names = append(names, name)
			}
		}
	}
	walk(reflect.TypeOf(value))
	return names
}

func sameSet(left, right []string) bool {
	left, right = slices.Clone(left), slices.Clone(right)
	slices.Sort(left)
	slices.Sort(right)
	return slices.Equal(left, right)
}

// jsonString marshals value to the string a request would return.
func jsonString(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

// remotePublishedAt is the publish time the fixtures give a remote snapshot.
func remotePublishedAt() time.Time {
	published, _ := time.Parse(time.RFC3339, remotePublish)
	return published
}

// Checkout is the checkout of the repository with id; a test-only view of what
// the daemon keeps internally.
func (s *Snapshotter) Checkout(id string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, found := s.repos[id]
	if !found {
		return "", false
	}
	return state.repo.Path, true
}

// Document is the last published document, decoded form; a test-only view, as
// the daemon serves Body.
func (s *Snapshotter) Document() Document {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.doc
}

// fakeGate is a Git gate with a fixed answer that counts its asks.
type fakeGate struct {
	usable bool
	panics bool
	asked  atomic.Int64
}

func (g *fakeGate) GitUsable(context.Context) bool {
	g.asked.Add(1)
	if g.panics {
		panic("a gate panicked")
	}
	return g.usable
}

// Body is the last published document as marshalled JSON, and its strong ETag,
// as a request without Accept-Encoding receives them; a test-only view.
func (s *Snapshotter) Body() (body []byte, etag string) {
	recorder := httptest.NewRecorder()
	cockpit.ServePayload(recorder, httptest.NewRequest(http.MethodGet, "/", nil), s.Payload())
	return recorder.Body.Bytes(), recorder.Header().Get("ETag")
}

// allBranches is every branch the daemon holds, in the order the branches route
// would list them within a repository and by repository id across them; a
// test-only view, as the document no longer carries them.
func (s *Snapshotter) allBranches() []Branch {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for id := range s.repos {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var branches []Branch
	for _, id := range ids {
		branches = append(branches, s.repos[id].entries.branches...)
	}
	return branches
}

// mapRemoteForTest is mapRemote for this machine and the fixtures' clock.
func mapRemoteForTest(login, projectsRoot string, entries []remotestate.Entry) remoteView {
	return mapRemote(testMachine, login, projectsRoot, entries, newClock().Now())
}
