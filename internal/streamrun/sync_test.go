package streamrun

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/streamsync"
)

func streamSyncCommandFixture(t *testing.T) (string, *streams.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := streams.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(streams.Stream{Name: "release-batch", Members: []streams.Member{
		{Repository: "acme/lib", Role: streams.RoleLibrary, Worktree: root + "/lib", Branch: "stream/release-batch", Base: "main", Lease: streams.Lease{RecordedHead: "old-lib"}},
		{Repository: "acme/app", Role: streams.RoleConsumer, Worktree: root + "/app", Branch: "stream/release-batch", Base: "develop", Lease: streams.Lease{RecordedHead: "old-app"}},
		{Repository: "acme/unavailable", Role: streams.RoleConsumer, Base: "main"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return root, store
}
func TestStreamSyncCommandPassesMemberSpecificOptionsAndPersistsRemoteHead(t *testing.T) {
	root, store := streamSyncCommandFixture(t)
	var calls []streamsync.Options
	runner := func(_ context.Context, options streamsync.Options) (streamsync.Result, error) {
		calls = append(calls, options)
		return streamsync.Result{
			Stream: options.Stream, Repository: options.Repository,
			StreamRebase:       streamsync.RebaseResult{Branch: options.Branch, Rebased: true},
			RecordedRemoteHead: "fetched-" + options.Repository,
		}, nil
	}
	service := isolatedService(t)
	service.sync = runner
	results, err := service.Sync(context.Background(), SyncRequest{ProjectsRoot: root, Name: "release-batch", Base: "release", Libraries: []streamsync.Library{{Name: "github.com/acme/lib", Target: "v1.2.3", Ecosystem: string(streams.EcosystemGo)}}, Verify: true, AllowMidReview: true, PushTrigger: streamsync.TriggerExplicit, PushReason: "checkpoint", Timeout: 2 * time.Second})
	raw, encodeErr := json.Marshal(results)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	stdout := string(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("sync calls = %+v, want library and consumer only", calls)
	}
	for i, repository := range []string{"acme/lib", "acme/app"} {
		got := calls[i]
		if got.Stream != "release-batch" || got.Repository != repository || got.Branch != "stream/release-batch" || got.Base != "release" ||
			!got.Verify || !got.AllowMidReview || got.PushTrigger != streamsync.TriggerExplicit || got.PushReason != "checkpoint" || got.Timeout != 2*time.Second {
			t.Errorf("options for %s = %+v", repository, got)
		}
	}
	if calls[0].Worktree != root+"/lib" || calls[0].RecordedRemoteHead != "old-lib" || len(calls[0].Libraries) != 0 {
		t.Errorf("library options = %+v", calls[0])
	}
	if calls[1].Worktree != root+"/app" || calls[1].RecordedRemoteHead != "old-app" ||
		!reflect.DeepEqual(calls[1].Libraries, []streamsync.Library{{Name: "github.com/acme/lib", Target: "v1.2.3", Ecosystem: string(streams.EcosystemGo)}}) {
		t.Errorf("consumer options = %+v", calls[1])
	}
	var decoded []streamsync.Result
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil || len(decoded) != 2 {
		t.Fatalf("JSON result = %q, decoded %+v, error %v", stdout, decoded, err)
	}
	persisted, err := store.Load("release-batch")
	if err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"acme/lib", "acme/app"} {
		member, ok := persisted.Member(repository)
		if !ok || member.Lease.RecordedHead != "fetched-"+repository {
			t.Errorf("persisted head for %s = %+v, found %t", repository, member, ok)
		}
	}
}
func TestStreamSyncCommandUsesMemberBaseAndReportsRefusal(t *testing.T) {
	root, _ := streamSyncCommandFixture(t)
	var calls []streamsync.Options
	runner := func(_ context.Context, options streamsync.Options) (streamsync.Result, error) {
		calls = append(calls, options)
		if options.Repository == "acme/app" {
			return streamsync.Result{}, &streamsync.Refusal{Code: "dirty-worktree", Message: "dirty checkout"}
		}
		return streamsync.Result{Repository: options.Repository, StreamRebase: streamsync.RebaseResult{Branch: options.Branch}}, nil
	}
	service := isolatedService(t)
	service.sync = runner
	_, err := service.Sync(context.Background(), SyncRequest{ProjectsRoot: root, Name: "release-batch"})
	var refusal *SyncRefusal
	if !errors.As(err, &refusal) || !strings.Contains(err.Error(), "acme/app: dirty checkout") {
		t.Fatalf("refusal = %v, qualified %v", err, refusal)
	}
	if len(calls) != 2 || calls[0].Base != "main" || calls[1].Base != "develop" || calls[0].PushTrigger != "" || calls[1].Verify {
		t.Fatalf("default member options = %+v", calls)
	}
}
func TestStreamSyncCommandPropagatesEngineError(t *testing.T) {
	root, _ := streamSyncCommandFixture(t)
	want := errors.New("injected sync failure")
	service := isolatedService(t)
	service.sync = func(context.Context, streamsync.Options) (streamsync.Result, error) { return streamsync.Result{}, want }
	_, err := service.Sync(context.Background(), SyncRequest{ProjectsRoot: root, Name: "release-batch"})
	if !errors.Is(err, want) {
		t.Fatalf("sync error = %v, want %v", err, want)
	}
}
func TestSyncStopsAfterPartialPersistenceAndNamesReceiptFailure(t *testing.T) {
	t.Parallel()
	root, store := streamSyncCommandFixture(t)
	service := isolatedService(t)
	failure := errors.New("later failure")
	calls := 0
	service.sync = func(_ context.Context, o streamsync.Options) (streamsync.Result, error) {
		calls++
		if calls == 2 {
			return streamsync.Result{}, failure
		}
		return streamsync.Result{RecordedRemoteHead: "first-fresh", StreamRebase: streamsync.RebaseResult{Branch: o.Branch}}, nil
	}
	result, err := service.Sync(context.Background(), SyncRequest{ProjectsRoot: root, Name: "release-batch"})
	if result != nil || !errors.Is(err, failure) || calls != 2 {
		t.Fatalf("results=%v err=%v calls%d", result, err, calls)
	}
	record, err := store.Load("release-batch")
	if err != nil {
		t.Fatal(err)
	}
	member, _ := record.Member("acme/lib")
	if member.Lease.RecordedHead != "first-fresh" {
		t.Fatal(member)
	}
	service.sync = func(_ context.Context, o streamsync.Options) (streamsync.Result, error) {
		if _, err := store.Update("release-batch", func(s *streams.Stream) error { s.Members = nil; return nil }); err != nil {
			t.Fatal(err)
		}
		return streamsync.Result{RecordedRemoteHead: "head", StreamRebase: streamsync.RebaseResult{Branch: o.Branch}}, nil
	}
	result, err = service.Sync(context.Background(), SyncRequest{ProjectsRoot: root, Name: "release-batch"})
	if result != nil || err == nil || !strings.Contains(err.Error(), "record fetched stream/release-batch head for acme/lib: stream member") {
		t.Fatalf("results=%v err=%v", result, err)
	}
}
func TestSyncOpeningAndMissingRecordFailuresDoNotRunEngine(t *testing.T) {
	t.Parallel()
	service := isolatedService(t)
	failure := errors.New("open")
	service.open = func(string) (*streams.Store, error) { return nil, failure }
	if _, err := service.Sync(context.Background(), SyncRequest{}); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	service.open = func(string) (*streams.Store, error) { return streams.OpenAt(t.TempDir()), nil }
	if _, err := service.Sync(context.Background(), SyncRequest{Name: "absent"}); !errors.Is(err, streams.ErrNotFound) {
		t.Fatal(err)
	}
}
func TestSyncDefaultEngineRejectsDirtyOrMissingNativeWorktree(t *testing.T) {
	t.Parallel()
	root, store := streamSyncCommandFixture(t)
	service := isolatedService(t)
	_, err := service.Sync(context.Background(), SyncRequest{ProjectsRoot: root, Name: "release-batch", Timeout: time.Second})
	var refusal *SyncRefusal
	if err == nil || !strings.Contains(err.Error(), "git status --porcelain") {
		t.Fatalf("native sync error=%v", err)
	}
	refusal = &SyncRefusal{Repository: "acme/lib", Refusal: &streamsync.Refusal{Message: "refused"}}
	if refusal.Unwrap() != refusal.Refusal || !strings.HasPrefix(refusal.Error(), "acme/lib: ") {
		t.Fatal(refusal)
	}
	if _, err := store.Update("release-batch", func(s *streams.Stream) error {
		for i := range s.Members {
			s.Members[i].Worktree = ""
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Sync(context.Background(), SyncRequest{ProjectsRoot: root, Name: "release-batch"})
	if err != nil || result == nil || len(result) != 0 {
		t.Fatalf("empty result=%v err=%v", result, err)
	}
}
