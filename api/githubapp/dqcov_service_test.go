package githubapp

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// dqCovWorktreeModel is a scriptable WorktreeReadModel.
type dqCovWorktreeModel struct {
	access Access[WorktreeTable]
	err    error
}

func (model dqCovWorktreeModel) Worktrees(context.Context, Viewer, WorktreeFilter) (Access[WorktreeTable], error) {
	return model.access, model.err
}

func TestDQCovServiceFailsClosedWithoutConfiguredModels(t *testing.T) {
	t.Parallel()
	service := Service{}
	ctx := context.Background()
	if _, err := service.Dashboard(ctx, Viewer{}); !errors.Is(err, ErrNoReadModel) {
		t.Errorf("dashboard err = %v", err)
	}
	if _, err := service.Stats(ctx, Viewer{}, ScopeRepository, "id"); !errors.Is(err, ErrNoReadModel) {
		t.Errorf("stats err = %v", err)
	}
	if _, err := service.Series(ctx, Viewer{}, ScopeRepository, "id", "metric"); !errors.Is(err, ErrNoReadModel) {
		t.Errorf("series err = %v", err)
	}
	if _, err := service.Leaderboard(ctx, Viewer{}, "metric"); !errors.Is(err, ErrNoReadModel) {
		t.Errorf("leaderboard err = %v", err)
	}
	if _, err := service.LatestMerges(ctx, Viewer{}, 5); !errors.Is(err, ErrNoReadModel) {
		t.Errorf("latest merges err = %v", err)
	}
	if _, err := service.WorktreeTable(ctx, Viewer{}, WorktreeFilter{}); !errors.Is(err, ErrNoReadModel) {
		t.Errorf("worktree table err = %v", err)
	}
	if _, _, err := service.EventStream(ctx, Viewer{}, EventFilter{}); !errors.Is(err, ErrNoReadModel) {
		t.Errorf("event stream err = %v", err)
	}
	if _, err := service.ProcessWebhook(ctx, WebhookDelivery{}, ""); !errors.Is(err, ErrNoWebhook) {
		t.Errorf("webhook err = %v", err)
	}
}

func TestDQCovServicePropagatesReadModelFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	boom := errors.New("read model down")
	service := Service{ReadModel: &dqCovReadModel{err: boom}}
	if _, err := service.Dashboard(ctx, Viewer{}); !errors.Is(err, boom) {
		t.Errorf("dashboard err = %v", err)
	}
	if _, err := service.Stats(ctx, Viewer{}, ScopeRepository, "id"); !errors.Is(err, boom) {
		t.Errorf("stats err = %v", err)
	}
	if _, err := service.Series(ctx, Viewer{}, ScopeRepository, "id", "metric"); !errors.Is(err, boom) {
		t.Errorf("series err = %v", err)
	}
	if _, err := service.Leaderboard(ctx, Viewer{}, "metric"); !errors.Is(err, boom) {
		t.Errorf("leaderboard err = %v", err)
	}
	if _, err := service.LatestMerges(ctx, Viewer{}, 5); !errors.Is(err, boom) {
		t.Errorf("latest merges err = %v", err)
	}

	worktrees := Service{Worktrees: dqCovWorktreeModel{err: boom}}
	if _, err := worktrees.WorktreeTable(ctx, Viewer{}, WorktreeFilter{}); !errors.Is(err, boom) {
		t.Errorf("worktree table err = %v", err)
	}
}

func TestDQCovServiceReturnsPublicAndPrivateValues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	member := Viewer{Authenticated: true, Member: true, UserID: "firebase-user"}

	public := Service{ReadModel: &dqCovReadModel{visibility: VisibilityPublic}}
	series, err := public.Series(ctx, member, ScopeRepository, "github.com/acme/app", "merged")
	if err != nil || series.Metric != "merged" || len(series.Points) != 1 {
		t.Fatalf("public series = %#v, %v", series, err)
	}
	board, err := public.Leaderboard(ctx, member, "landed")
	if err != nil || board.Metric != "landed" || len(board.Entries) != 1 {
		t.Fatalf("public leaderboard = %#v, %v", board, err)
	}
	merges, err := public.LatestMerges(ctx, member, 9)
	if err != nil || len(merges) != 1 || merges[0].Repository != "github.com/acme/app" {
		t.Fatalf("public merges = %#v, %v", merges, err)
	}
	if public.ReadModel.(*dqCovReadModel).limit != 9 {
		t.Fatalf("read-model limit = %d, want 9", public.ReadModel.(*dqCovReadModel).limit)
	}

	// Each call below builds its own Service/dqCovReadModel rather than
	// closing over one shared instance: the anonymous and member subtests
	// for the same case, and every other case, all run in parallel with
	// each other, and dqCovReadModel.LatestMerges writes its receiver's
	// limit field, so a shared instance raced under -race.
	newPrivate := func() Service { return Service{ReadModel: &dqCovReadModel{visibility: VisibilityPrivate}} }
	for name, call := range map[string]func(Viewer) error{
		"series": func(viewer Viewer) error {
			_, err := newPrivate().Series(ctx, viewer, ScopeRepository, "id", "metric")
			return err
		},
		"leaderboard": func(viewer Viewer) error {
			_, err := newPrivate().Leaderboard(ctx, viewer, "metric")
			return err
		},
		"latest merges": func(viewer Viewer) error {
			_, err := newPrivate().LatestMerges(ctx, viewer, 5)
			return err
		},
	} {
		t.Run(name+"/anonymous", func(t *testing.T) {
			t.Parallel()
			if err := call(Viewer{}); !errors.Is(err, ErrPrivateData) {
				t.Fatalf("anonymous err = %v, want ErrPrivateData", err)
			}
		})
		t.Run(name+"/member", func(t *testing.T) {
			t.Parallel()
			if err := call(member); err != nil {
				t.Fatalf("member err = %v", err)
			}
		})
	}

	privateWorktrees := Service{Worktrees: dqCovWorktreeModel{access: Access[WorktreeTable]{Visibility: VisibilityPrivate}}}
	if _, err := privateWorktrees.WorktreeTable(ctx, Viewer{}, WorktreeFilter{}); !errors.Is(err, ErrPrivateData) {
		t.Fatalf("anonymous worktree table err = %v", err)
	}
	if _, err := privateWorktrees.WorktreeTable(ctx, member, WorktreeFilter{}); err != nil {
		t.Fatalf("member worktree table err = %v", err)
	}
}

func TestDQCovEventStreamPropagatesSourceFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	if _, _, err := (Service{Events: &dqCovEventSource{replayErr: errors.New("replay down"), live: make(chan Event)}}).
		EventStream(ctx, Viewer{}, EventFilter{}); err == nil || !strings.Contains(err.Error(), "replay daemon events") {
		t.Fatalf("replay err = %v", err)
	}
	if _, _, err := (Service{Events: &dqCovEventSource{subscribeErr: errors.New("subscribe down"), live: make(chan Event)}}).
		EventStream(ctx, Viewer{}, EventFilter{}); err == nil || !strings.Contains(err.Error(), "subscribe daemon events") {
		t.Fatalf("subscribe err = %v", err)
	}
	nonMonotonic := &dqCovEventSource{replay: []Event{{ID: 5}, {ID: 2}}, live: make(chan Event)}
	if _, _, err := (Service{Events: nonMonotonic}).EventStream(ctx, Viewer{}, EventFilter{}); err == nil || !strings.Contains(err.Error(), "not monotonic") {
		t.Fatalf("non-monotonic err = %v", err)
	}
}

func TestDQCovEventStreamFiltersVisibleEventsAndAdvancesCursor(t *testing.T) {
	t.Parallel()
	since := time.Unix(100, 0)
	source := &dqCovEventSource{
		replay: []Event{
			{ID: 1, Repository: "github.com/acme/app", Task: "t", At: time.Unix(50, 0), Visibility: VisibilityPublic, Payload: []byte(`{}`)},
			{ID: 2, Repository: "github.com/other/app", Task: "t", At: time.Unix(200, 0), Visibility: VisibilityPublic, Payload: []byte(`{}`)},
			{ID: 3, Repository: "github.com/acme/app", Task: "t", Operation: "merge", Session: "s", Severity: "info", At: time.Unix(200, 0), Visibility: VisibilityPrivate, Payload: []byte(`{}`)},
			{ID: 4, Repository: "github.com/acme/app", Task: "t", At: time.Unix(200, 0), Visibility: VisibilityPublic, Payload: []byte(`{}`)},
		},
		live: dqCovClosedEvents(),
	}
	replay, updates, err := (Service{Events: source}).EventStream(context.Background(), Viewer{},
		EventFilter{Since: since, Repository: "github.com/acme/app", Task: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].ID != 4 {
		t.Fatalf("replay = %#v, want only the public matching event 4", replay)
	}
	if source.liveFilter.After != 4 {
		t.Fatalf("subscribe cursor = %d, want 4", source.liveFilter.After)
	}
	var delivered []Event
	for event := range updates {
		delivered = append(delivered, event)
	}
	if len(delivered) != 0 {
		t.Fatalf("unexpected live delivery = %#v", delivered)
	}
}

func TestDQCovEventStreamKeepsCursorForEmptyReplayAndSkipsStaleLiveEvents(t *testing.T) {
	t.Parallel()
	live := make(chan Event, 1)
	live <- Event{ID: 3, Visibility: VisibilityPublic, Payload: []byte(`{}`)}
	close(live)
	source := &dqCovEventSource{live: live}
	replay, updates, err := (Service{Events: source}).EventStream(context.Background(), Viewer{}, EventFilter{After: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 0 {
		t.Fatalf("replay = %#v, want empty", replay)
	}
	if source.liveFilter.After != 7 {
		t.Fatalf("empty replay cursor = %d, want the original 7", source.liveFilter.After)
	}
	var delivered []Event
	for event := range updates {
		delivered = append(delivered, event)
	}
	if len(delivered) != 0 {
		t.Fatalf("stale live event leaked: %#v", delivered)
	}
}

func TestDQCovEventStreamGoroutineStopsOnCancelledContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	source := &dqCovEventSource{live: make(chan Event)}
	_, updates, err := (Service{Events: source}).EventStream(ctx, Viewer{}, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	for range updates {
		t.Fatal("no live event was produced")
	}
}

func TestDQCovEventStreamDropsLiveEventWhenCancelledWhileBlocked(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	live := make(chan Event)
	source := &dqCovEventSource{live: live}
	_, updates, err := (Service{Events: source}).EventStream(ctx, Viewer{}, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	live <- Event{ID: 1, Visibility: VisibilityPublic, Payload: []byte(`{}`)}
	// Give the subscription goroutine time to reach its delivery select, which
	// is blocked because this test never reads the filtered channel.
	time.Sleep(50 * time.Millisecond)
	cancel()
	for range updates {
		t.Fatal("no live event should be delivered after cancellation")
	}
}

func TestDQCovProcessWebhookRejectsMalformedSignatureEncoding(t *testing.T) {
	t.Parallel()
	engine := ProjectionEngine{
		Deliveries: &testDeliveries{}, Reader: &testReader{}, Writer: &testProjectionWriter{},
		AuthoritativeReader: &testReader{}, WebhookSecret: []byte("secret"),
	}
	queued, err := engine.Process(context.Background(), WebhookDelivery{ID: "delivery-1", Event: "push", Payload: []byte(`{}`)}, "sha256=zz")
	if err == nil || queued || !strings.Contains(err.Error(), "signature is invalid") {
		t.Fatalf("queued/err = %v/%v", queued, err)
	}
}
