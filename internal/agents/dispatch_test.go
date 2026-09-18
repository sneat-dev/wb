package agents

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/worktrees"
)

// dispatchFixture is a fully faked dispatch environment: the worktree service
// and the detached owner are replaced, so the orchestration above them can be
// checked without a live fleet.
type dispatchFixture struct {
	deps   DispatchDeps
	home   string
	record Record
	err    error

	createCalls    int
	createOptions  worktrees.CreateOptions
	createRepos    []string
	listCalls      int
	listOptions    worktrees.ListOptions
	beforeCalls    int
	beforeRepos    []string
	beforeErr      error
	afterCalls     int
	afterResults   []worktrees.CreateResult
	spawnedAgentID string
	createErr      error
	listErr        error
	listResults    []worktrees.ListResult
	originErr      error
	originCalls    int
	spawnErr       error
}

func newDispatchFixture(t *testing.T) *dispatchFixture {
	t.Helper()
	fixture := &dispatchFixture{home: t.TempDir()}
	t.Setenv("WB_PROJECTS_ROOT", t.TempDir())
	t.Setenv("DEEPSEEK_API_KEY", "test-credential")
	fixture.deps = DispatchDeps{
		ConfigPath:   "/tmp/wb.yaml",
		ProjectsRoot: t.TempDir(),
		Home:         fixture.home,
		LoadConfig: func() (Config, error) {
			config, err := LoadConfigFile(filepath.Join(t.TempDir(), "absent.yaml"))
			if err != nil {
				return Config{}, err
			}
			config.Profiles["cheap"] = Profile{Harness: HarnessCodex, Provider: "deepseek", Model: "deepseek-flash", Reasoning: "high"}
			return config, nil
		},
		CreateWorktree: func(_ context.Context, repositories []string, options worktrees.CreateOptions) ([]worktrees.CreateResult, error) {
			fixture.createCalls++
			fixture.createRepos = repositories
			fixture.createOptions = options
			if fixture.createErr != nil {
				return nil, fixture.createErr
			}
			return []worktrees.CreateResult{{
				Repository: "acme/app", WorktreeDir: "/fleet/.worktrees/task-one",
				Branch: "task-one", Base: "main", BaseSHA: "abc123", Action: "created",
				WorkLogPath: "/home/.wb/worklogs/claims/task-one/claim.json",
			}}, nil
		},
		ListWorktrees: func(_ context.Context, options worktrees.ListOptions) ([]worktrees.ListResult, error) {
			fixture.listCalls++
			fixture.listOptions = options
			if fixture.listErr != nil {
				return nil, fixture.listErr
			}
			return fixture.listResults, nil
		},
		OriginSlug: func(_ context.Context, path string) (string, error) {
			fixture.originCalls++
			if fixture.originErr != nil {
				return "", fixture.originErr
			}
			if path != "." {
				t.Errorf("origin must be derived from the invoking checkout, got %q", path)
			}
			return "acme/app", nil
		},
		BeforeCreate: func(repositories []string) error {
			fixture.beforeCalls++
			fixture.beforeRepos = repositories
			return fixture.beforeErr
		},
		AfterCreate: func(_ []string, results []worktrees.CreateResult) {
			fixture.afterCalls++
			fixture.afterResults = results
		},
		SpawnOwner: func(agentID string) (int, error) {
			fixture.spawnedAgentID = agentID
			if fixture.spawnErr != nil {
				return 0, fixture.spawnErr
			}
			return os.Getpid(), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC) },
	}
	return fixture
}

func (fixture *dispatchFixture) dispatch(t *testing.T, request DispatchRequest) (Record, error) {
	t.Helper()
	fixture.record, fixture.err = Dispatch(context.Background(), request, fixture.deps)
	return fixture.record, fixture.err
}

func newRequest() DispatchRequest {
	return DispatchRequest{
		Mode: ModeNew, Worktree: "task-one", Profile: "cheap",
		Task: "do the bounded thing", Timeout: 5 * time.Minute,
	}
}

func TestDispatchNewWorktreeRecordsTheResolvedRunAndStartsTheOwner(t *testing.T) {
	fixture := newDispatchFixture(t)
	record, err := fixture.dispatch(t, newRequest())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if fixture.createCalls != 1 || fixture.beforeCalls != 1 || fixture.afterCalls != 1 {
		t.Fatalf("worktree side effects: create=%d before=%d after=%d", fixture.createCalls, fixture.beforeCalls, fixture.afterCalls)
	}
	if fixture.listCalls != 0 {
		t.Fatal("--new-worktree must not consult the existing-worktree inventory")
	}
	if fixture.afterResults == nil {
		t.Fatal("the checkout marker must be written for the created checkout")
	}
	if len(fixture.beforeRepos) != 1 || fixture.beforeRepos[0] != "acme/app" {
		t.Fatalf("managed hooks must be refreshed for the resolved repository: %#v", fixture.beforeRepos)
	}

	// The creation service must be handed the inputs it actually requires.
	options := fixture.createOptions
	if options.Operation != "task-one" || options.ProjectsRoot != fixture.deps.ProjectsRoot {
		t.Fatalf("create options = %#v", options)
	}
	if !options.SessionRequired {
		t.Fatal("agent-mode creation must keep WB's live-session gate")
	}
	if options.WorkLog.Model != "deepseek-flash" || options.WorkLog.Provider != "deepseek" || options.WorkLog.AgentRuntime != HarnessCodex {
		t.Fatalf("resolved profile was not recorded in the Work Log claim: %#v", options.WorkLog)
	}
	if !options.WorkLog.RequireOriginalPrompt {
		t.Fatal("the private original-prompt archive must be required")
	}
	if options.WorkLog.RunID == "" {
		t.Fatal("the Work Log run id must be prepared")
	}
	if options.BranchChosen {
		t.Fatal("dispatch must not claim a branch choice it did not make")
	}

	if record.State != StateRunning {
		t.Fatalf("state = %s", record.State)
	}
	if record.RequestedProfile != "cheap" || record.Resolved.Model != "deepseek-flash" || record.Resolved.Reasoning != "high" {
		t.Fatalf("resolved snapshot = %#v", record.Resolved)
	}
	if record.Task != "do the bounded thing" || record.TaskSummary == "" {
		t.Fatalf("the private task and its summary must be persisted: %#v", record)
	}
	if record.Repository != "acme/app" || record.WorktreeDir != "/fleet/.worktrees/task-one" || record.BaseSHA != "abc123" {
		t.Fatalf("worktree facts = %#v", record)
	}
	if record.WorkLogClaimPath == "" || record.WorkLogRunID == "" {
		t.Fatal("the run must reference the Work Log claim the creation service published")
	}
	if record.OwnerPID != os.Getpid() {
		t.Fatalf("the returned record must carry the owner PID: %d", record.OwnerPID)
	}
	if record.TimeoutMS != (5 * time.Minute).Milliseconds() {
		t.Fatalf("timeout = %d", record.TimeoutMS)
	}
	if record.StartedAt.IsZero() || !record.StartedAt.Equal(fixture.deps.Now()) {
		t.Fatalf("start time = %v", record.StartedAt)
	}

	// The persisted record is the durable contract, and it has exactly one
	// writer per phase: the dispatcher admits the run, and the owner records
	// process identity from then on. The dispatcher must not write again after
	// spawning, or it could clobber the worker PID the owner records moments
	// later.
	store := NewStore(fixture.home)
	persisted, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatalf("the run must be durable before Dispatch returns: %v", err)
	}
	if persisted.Task != record.Task || persisted.RequestedProfile != "cheap" {
		t.Fatalf("the admitted record lost its immutable facts: %#v", persisted)
	}
	if persisted.OwnerPID != 0 || persisted.WorkerPID != 0 {
		t.Fatalf("process identity belongs to the owner, not the dispatcher: %#v", persisted)
	}
	if fixture.spawnedAgentID != record.AgentID {
		t.Fatalf("owner spawned for %q, want %q", fixture.spawnedAgentID, record.AgentID)
	}
}

// TestDispatchDoesNotClobberWhatTheOwnerRecordsWhileSpawning is the regression
// guard for the two-writer race: the owner starts as soon as SpawnOwner
// returns and records its own PID and then the worker PID, so a dispatcher
// write afterwards would erase them.
func TestDispatchDoesNotClobberWhatTheOwnerRecordsWhileSpawning(t *testing.T) {
	fixture := newDispatchFixture(t)
	store := NewStore(fixture.home)
	ownerpid := os.Getpid()
	fixture.deps.SpawnOwner = func(agentID string) (int, error) {
		fixture.spawnedAgentID = agentID
		// Simulate an owner that has already raced ahead to record process
		// identity before Dispatch returns.
		raced, err := store.Load(agentID)
		if err != nil {
			return 0, err
		}
		raced.OwnerPID = ownerpid
		raced.WorkerPID = ownerpid
		if err := store.Save(raced); err != nil {
			return 0, err
		}
		return ownerpid, nil
	}

	record, err := fixture.dispatch(t, newRequest())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	persisted, err := store.Load(record.AgentID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.WorkerPID != ownerpid {
		t.Fatalf("the dispatcher erased the worker PID the owner recorded: %#v", persisted)
	}
	if persisted.OwnerPID != ownerpid {
		t.Fatalf("the dispatcher erased the owner PID: %#v", persisted)
	}
}

func TestDispatchRecordsABranchAndBaseOnlyWhenTheCallerAsked(t *testing.T) {
	fixture := newDispatchFixture(t)
	request := newRequest()
	request.Branch = "feature/exact"
	request.Base = "release"
	if _, err := fixture.dispatch(t, request); err != nil {
		t.Fatal(err)
	}
	if fixture.createOptions.Branch != "feature/exact" || !fixture.createOptions.BranchChosen {
		t.Fatalf("explicit branch was not passed through: %#v", fixture.createOptions)
	}
	if fixture.createOptions.Base != "release" {
		t.Fatalf("explicit base was not passed through: %#v", fixture.createOptions)
	}
}

func TestDispatchDerivesTheRepositoryFromOriginWhenUnset(t *testing.T) {
	fixture := newDispatchFixture(t)
	if _, err := fixture.dispatch(t, newRequest()); err != nil {
		t.Fatal(err)
	}
	if fixture.originCalls != 1 {
		t.Fatalf("origin lookups = %d", fixture.originCalls)
	}

	fixture = newDispatchFixture(t)
	fixture.originErr = errors.New("no origin remote")
	request := newRequest()
	_, err := fixture.dispatch(t, request)
	if err == nil || !strings.Contains(err.Error(), "--repo") {
		t.Fatalf("a missing origin must tell the caller to pass --repo: %v", err)
	}
	if fixture.createCalls != 0 {
		t.Fatal("nothing may be created when the repository cannot be resolved")
	}

	fixture = newDispatchFixture(t)
	request.Repository = "other/repo"
	if _, err := fixture.dispatch(t, request); err != nil {
		t.Fatal(err)
	}
	if fixture.originCalls != 0 {
		t.Fatal("an explicit --repo must not consult the checkout")
	}
	if fixture.createRepos[0] != "other/repo" {
		t.Fatalf("repository = %#v", fixture.createRepos)
	}
}

func TestDispatchExistingWorktreeResolvesWithoutCreating(t *testing.T) {
	fixture := newDispatchFixture(t)
	fixture.listResults = []worktrees.ListResult{{
		Task: "task-one", Repository: "acme/app", WorktreeDir: "/fleet/.worktrees/task-one",
		Branch: "task-one", RecordedBase: "main",
	}}
	request := newRequest()
	request.Mode = ModeExisting
	record, err := fixture.dispatch(t, request)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if fixture.createCalls != 0 || fixture.beforeCalls != 0 || fixture.afterCalls != 0 {
		t.Fatal("--use-worktree must never create, refresh hooks, or write a marker")
	}
	if fixture.listCalls != 1 || len(fixture.listOptions.Tasks) != 1 || fixture.listOptions.Tasks[0] != "task-one" {
		t.Fatalf("inventory selection = %#v", fixture.listOptions)
	}
	if record.WorktreeMode != ModeExisting || record.WorktreeDir != "/fleet/.worktrees/task-one" || record.Branch != "task-one" {
		t.Fatalf("record = %#v", record)
	}
	if record.BaseSHA != "" {
		t.Fatalf("an existing worktree has no dispatch-time base revision: %q", record.BaseSHA)
	}
}

func TestDispatchExistingWorktreeRefusesZeroAndManyMatches(t *testing.T) {
	fixture := newDispatchFixture(t)
	request := newRequest()
	request.Mode = ModeExisting
	_, err := fixture.dispatch(t, request)
	if err == nil || !strings.Contains(err.Error(), "no WB-managed worktree named") {
		t.Fatalf("an unresolvable worktree must fail naming what was searched: %v", err)
	}
	if !strings.Contains(err.Error(), fixture.deps.ProjectsRoot) {
		t.Fatalf("the message must name the searched root: %v", err)
	}

	fixture = newDispatchFixture(t)
	fixture.listResults = []worktrees.ListResult{
		{Repository: "acme/one", WorktreeDir: "/one"},
		{Repository: "acme/two", WorktreeDir: "/two"},
	}
	_, err = fixture.dispatch(t, request)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("an ambiguous name must be refused, not guessed: %v", err)
	}
	for _, expected := range []string{"acme/one", "acme/two", "--repo"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("ambiguity message is missing %q: %v", expected, err)
		}
	}

	// With --repo the same ambiguity resolves cleanly.
	fixture = newDispatchFixture(t)
	fixture.listResults = []worktrees.ListResult{
		{Repository: "acme/one", WorktreeDir: "/one"},
		{Repository: "acme/two", WorktreeDir: "/two"},
	}
	request.Repository = "acme/two"
	record, err := fixture.dispatch(t, request)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if record.WorktreeDir != "/two" {
		t.Fatalf("wrong candidate selected: %#v", record)
	}

	// A candidate with no directory is not a usable worktree.
	fixture = newDispatchFixture(t)
	fixture.listResults = []worktrees.ListResult{{Repository: "acme/one", WorktreeDir: ""}}
	if _, err := fixture.dispatch(t, request); err == nil {
		t.Fatal("a candidate with no checkout must not be selected")
	}
}

func TestDispatchPropagatesWorktreeFailuresBeforeStartingAnything(t *testing.T) {
	fixture := newDispatchFixture(t)
	fixture.createErr = errors.New("worktree already exists: /fleet/.worktrees/task-one")
	_, err := fixture.dispatch(t, newRequest())
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("the creation error must reach the caller unchanged: %v", err)
	}
	if fixture.spawnedAgentID != "" {
		t.Fatal("no owner may be started when creation failed")
	}
	records, listErr := NewStore(fixture.home).List()
	if listErr != nil || len(records) != 0 {
		t.Fatalf("a failed creation must not leave a run record: %v %v", records, listErr)
	}

	fixture = newDispatchFixture(t)
	fixture.listErr = errors.New("inventory unreadable")
	request := newRequest()
	request.Mode = ModeExisting
	if _, err := fixture.dispatch(t, request); err == nil || !strings.Contains(err.Error(), "inventory unreadable") {
		t.Fatalf("inventory failure = %v", err)
	}
}

func TestDispatchRecordsALaunchFailureInsteadOfHidingIt(t *testing.T) {
	fixture := newDispatchFixture(t)
	fixture.spawnErr = errors.New("exec format error")
	record, err := fixture.dispatch(t, newRequest())
	if err == nil || !strings.Contains(err.Error(), "run owner") {
		t.Fatalf("a launch failure must be reported: %v", err)
	}
	if record.State != StateFailed || !strings.Contains(record.Failure, "exec format error") {
		t.Fatalf("a failed launch must still leave a diagnosable record: %#v", record)
	}
	persisted, loadErr := NewStore(fixture.home).Load(record.AgentID)
	if loadErr != nil || persisted.State != StateFailed {
		t.Fatalf("persisted record = %#v %v", persisted, loadErr)
	}
}

func TestDispatchRefusesBadRequestsAsRequestErrors(t *testing.T) {
	t.Parallel()
	cases := map[string]func(*DispatchRequest){
		"no worktree mode": func(r *DispatchRequest) { r.Worktree = "  " },
		"no task":          func(r *DispatchRequest) { r.Task = "" },
		"negative timeout": func(r *DispatchRequest) { r.Timeout = -time.Second },
		"unknown mode":     func(r *DispatchRequest) { r.Mode = "sideways" },
		"unknown profile":  func(r *DispatchRequest) { r.Profile = "nope" },
		"missing profile":  func(r *DispatchRequest) { r.Profile = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newDispatchFixture(t)
			request := newRequest()
			mutate(&request)
			_, err := fixture.dispatch(t, request)
			if err == nil {
				t.Fatalf("Dispatch accepted %s", name)
			}
			if name != "unknown mode" && !IsRequestError(err) {
				t.Fatalf("%s must be refused as a request error, got %T", name, err)
			}
			if fixture.createCalls != 0 || fixture.spawnedAgentID != "" {
				t.Fatalf("%s must be refused before any mutation or launch", name)
			}
		})
	}
}

func TestDispatchRefusesAMissingProviderCredential(t *testing.T) {
	fixture := newDispatchFixture(t)
	t.Setenv("DEEPSEEK_API_KEY", "")
	_, err := fixture.dispatch(t, newRequest())
	if err == nil {
		t.Fatal("a missing credential must fail before any worktree is created")
	}
	if !strings.Contains(err.Error(), "DEEPSEEK_API_KEY") {
		t.Fatalf("the failure must name the exact variable: %v", err)
	}
	if fixture.createCalls != 0 {
		t.Fatal("no worktree may be created without a usable credential")
	}
}

func TestDispatchReportsConfigurationAndHomeFailures(t *testing.T) {
	fixture := newDispatchFixture(t)
	fixture.deps.LoadConfig = func() (Config, error) { return Config{}, errors.New("config unreadable") }
	if _, err := fixture.dispatch(t, newRequest()); err == nil || !strings.Contains(err.Error(), "config unreadable") {
		t.Fatalf("configuration failure = %v", err)
	}

	fixture = newDispatchFixture(t)
	fixture.deps.LoadConfig = nil
	if _, err := fixture.dispatch(t, newRequest()); err == nil {
		t.Fatal("dispatch must refuse to run without a configuration loader")
	}
}

func TestDispatchUsesTheRealWorktreeServiceByDefault(t *testing.T) {
	// The production seams are WB's own worktree service, not a parallel
	// implementation: a nil seam must be filled from internal/worktrees.
	fixture := newDispatchFixture(t)
	deps := fixture.deps
	deps.CreateWorktree = nil
	deps.ListWorktrees = nil
	deps.OriginSlug = nil
	request := newRequest()
	request.Profile = "cheap"
	request.Repository = "acme/app"
	// No canonical clone exists under the projects root, so the real service
	// must refuse with its own actionable message rather than panicking on a
	// nil seam.
	_, err := Dispatch(context.Background(), request, deps)
	if err == nil {
		t.Fatal("a missing canonical clone must fail through the real worktree service")
	}
	if strings.Contains(err.Error(), "nil pointer") {
		t.Fatalf("the default seam was not installed: %v", err)
	}
}

func TestDispatchRefusesAnUnusableRepositoryOrOperationName(t *testing.T) {
	fixture := newDispatchFixture(t)
	fixture.deps.BeforeCreate = nil
	request := newRequest()
	request.Repository = "not-a-valid-repository-slug"
	if _, err := fixture.dispatch(t, request); err == nil {
		t.Fatal("an unusable repository coordinate must be refused")
	}
	if fixture.createCalls != 0 {
		t.Fatal("nothing may be created for an unusable repository")
	}

	// The operation name becomes a path segment and a Work Log effort id, so a
	// name that is not one safe segment must fail before creation.
	fixture = newDispatchFixture(t)
	request = newRequest()
	request.Worktree = "bad/name"
	if _, err := fixture.dispatch(t, request); err == nil {
		t.Fatal("a worktree name that is not one safe segment must be refused")
	}
	if fixture.createCalls != 0 {
		t.Fatal("nothing may be created for an unsafe operation name")
	}
}

func TestDispatchPropagatesAHookRefreshFailure(t *testing.T) {
	fixture := newDispatchFixture(t)
	fixture.beforeErr = errors.New("managed hooks cannot be refreshed")
	_, err := fixture.dispatch(t, newRequest())
	if err == nil || !strings.Contains(err.Error(), "managed hooks") {
		t.Fatalf("a hook refresh failure must stop creation: %v", err)
	}
	if fixture.createCalls != 0 {
		t.Fatal("creation must not proceed after a hook refresh failure")
	}
}

func TestDispatchReportsARunDirectoryItCannotCreate(t *testing.T) {
	fixture := newDispatchFixture(t)
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.deps.Home = blocked
	_, err := fixture.dispatch(t, newRequest())
	if err == nil || !strings.Contains(err.Error(), "create agent run directory") {
		t.Fatalf("an unwritable run directory must be reported: %v", err)
	}
	if fixture.spawnedAgentID != "" {
		t.Fatal("no owner may start when the run cannot be persisted")
	}
}

func TestDispatchRecordsASummaryForEveryTaskItAccepts(t *testing.T) {
	fixture := newDispatchFixture(t)
	request := newRequest()
	request.Task = "line one\nline two"
	record, err := fixture.dispatch(t, request)
	if err != nil {
		t.Fatal(err)
	}
	if record.Task != "line one\nline two" {
		t.Fatalf("the exact task bytes must be persisted: %q", record.Task)
	}
	if record.TaskSummary != "line one" {
		t.Fatalf("the summary must be the first line: %q", record.TaskSummary)
	}
}
