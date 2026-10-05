package worktreecollab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testService(t *testing.T) (Service, *string, *ObservedOwner) {
	t.Helper()
	caller := "owner"
	legacy := ObservedOwner{}
	sequence := 0
	return Service{Store: NewStore(privateTestHome(t)), Ports: ServicePorts{
		Resolve: func(_ context.Context, value string) (Checkout, error) {
			if value != "worktree" {
				return Checkout{}, fmt.Errorf("unknown checkout")
			}
			return testCheckout(), nil
		},
		Caller: func() (string, error) { return caller, nil },
		Live:   func(id string) (bool, error) { return id != "gone", nil },
		OwnerStatus: func(id string) (string, error) {
			if id == "gone" {
				return "inactive", nil
			}
			return "live", nil
		},
		ObserveLegacy:              func(Checkout) (ObservedOwner, error) { return legacy, nil },
		ObserveLegacyForInspection: func(Checkout) (ObservedOwner, error) { return legacy, nil },
		Now:                        func() time.Time { return testNow },
		NewMessageID: func() (string, error) {
			sequence++
			return fmt.Sprintf("message-%d", sequence), nil
		},
	}}, &caller, &legacy
}

func TestServiceNativeOwnerJoinMessageTransferAndAck(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, caller, _ := testService(t)
	view, err := service.Inspect(ctx, "worktree")
	if err != nil || view.Owner != NoOwner || view.OwnerStatus != "none" {
		t.Fatalf("uninitialized inspect = %+v, %v", view, err)
	}
	if _, err := service.Join(ctx, "worktree"); err == nil {
		t.Fatal("join initialized ownership")
	}
	for _, action := range []func() error{
		func() error { _, err := service.Leave(ctx, "worktree"); return err },
		func() error { _, err := service.Transfer(ctx, "worktree", "peer"); return err },
		func() error { _, _, err := service.Send(ctx, "worktree", "key", []string{"peer"}, "text"); return err },
		func() error { _, err := service.Inbox(ctx, "worktree"); return err },
		func() error { _, err := service.Ack(ctx, "worktree", "missing"); return err },
	} {
		if err := action(); err == nil || !strings.Contains(err.Error(), "not initialized") {
			t.Fatalf("pre-initialization action = %v", err)
		}
	}
	state, err := service.Take(ctx, "worktree", NoOwner, false, "")
	if err != nil || state.Owner != "owner" || len(state.Members) != 1 {
		t.Fatalf("initial owner = %+v, %v", state, err)
	}
	*caller = "peer"
	if state, err = service.Join(ctx, "worktree"); err != nil || state.Owner != "owner" || len(state.Members) != 2 {
		t.Fatalf("join = %+v, %v", state, err)
	}
	*caller = "owner"
	receipt, replay, err := service.Send(ctx, "worktree", "key", []string{"peer"}, "hello")
	if err != nil || replay || receipt.MessageID == "" {
		t.Fatalf("send = %+v, %t, %v", receipt, replay, err)
	}
	*caller = "peer"
	inbox, err := service.Inbox(ctx, "worktree")
	if err != nil || len(inbox.Messages) != 1 || inbox.Messages[0].Body != "hello" {
		t.Fatalf("inbox = %+v, %v", inbox, err)
	}
	if cursor, err := service.Ack(ctx, "worktree", receipt.MessageID); err != nil || cursor != 1 {
		t.Fatalf("ack = %d, %v", cursor, err)
	}
	*caller = "owner"
	if state, err = service.Transfer(ctx, "worktree", "peer"); err != nil || state.Owner != "peer" {
		t.Fatalf("transfer = %+v, %v", state, err)
	}
	if interim, err := service.Inspect(ctx, "worktree"); err != nil || len(interim.Joined) != 2 || interim.Joined[0].SessionID != "owner" {
		t.Fatalf("sorted joined participants = %+v, %v", interim, err)
	}
	service.Ports.NewMessageID = func() (string, error) { return "", errors.New("ID generator unavailable after acceptance") }
	replayed, replay, err := service.Send(ctx, "worktree", "key", []string{"peer"}, "hello")
	if err != nil || !replay || replayed.MessageID != receipt.MessageID {
		t.Fatalf("retry after transfer = %+v, %t, %v", replayed, replay, err)
	}
	if state, err = service.Leave(ctx, "worktree"); err != nil || len(state.Members) != 1 {
		t.Fatalf("previous owner leave = %+v, %v", state, err)
	}
	*caller = "peer"
	view, err = service.Inspect(ctx, "worktree")
	if err != nil || view.Owner != "peer" || view.OwnerStatus != "live" || len(view.Joined) != 1 {
		t.Fatalf("final inspect = %+v, %v", view, err)
	}
	inbox, err = service.Inbox(ctx, "worktree")
	if err != nil || inbox.Notice == nil || inbox.Notice.Current != "peer" || len(inbox.Messages) != 0 {
		t.Fatalf("independent owner notice = %+v, %v", inbox, err)
	}
}

func TestServiceLegacyTakeAndExactContenders(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, caller, legacy := testService(t)
	*legacy = ObservedOwner{Status: "unknown"}
	if _, err := service.Inspect(ctx, "worktree"); err == nil || !strings.Contains(err.Error(), "exact observation ID") {
		t.Fatalf("unidentified legacy observation displayed as unowned: %v", err)
	}
	*legacy = ObservedOwner{ID: "legacy-observation", SessionID: "prior", Status: "unknown"}
	view, err := service.Inspect(ctx, "worktree")
	if err != nil || view.Owner != legacy.ID || view.OwnerStatus != "unknown" {
		t.Fatalf("legacy inspect = %+v, %v", view, err)
	}
	if _, err := service.Take(ctx, "worktree", NoOwner, true, "reviewed"); !errors.Is(err, ErrConflict) {
		t.Fatalf("none bypassed legacy owner: %v", err)
	}
	if _, err := os.Stat(service.Store.Home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused take created coordination metadata: %v", err)
	}
	if _, err := service.Take(ctx, "worktree", legacy.ID, false, ""); err == nil {
		t.Fatal("unknown legacy owner taken without force")
	}
	if _, err := service.Take(ctx, "worktree", legacy.ID, true, "reviewed"); err != nil {
		t.Fatal(err)
	}
	*caller = "second"
	if _, err := service.Take(ctx, "worktree", legacy.ID, true, "stale"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expected owner accepted: %v", err)
	}
	service.Ports.OwnerStatus = func(string) (string, error) { return "unknown", nil }
	if _, err := service.Take(ctx, "worktree", "owner", false, ""); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("uncorroborated current owner taken without force: %v", err)
	}
	if state, err := service.Take(ctx, "worktree", "owner", true, "unreadable registration reviewed"); err != nil || state.Owner != "second" {
		t.Fatalf("explicit uncertain-owner takeover = %+v, %v", state, err)
	}
	service2, _, _ := testService(t)
	service2.Store = NewStore(privateTestHome(t))
	first := service2
	second := service2
	first.Ports.Caller = func() (string, error) { return "first", nil }
	second.Ports.Caller = func() (string, error) { return "second", nil }
	start := make(chan struct{})
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, contender := range []Service{first, second} {
		wait.Add(1)
		go func(candidate Service) {
			defer wait.Done()
			<-start
			_, err := candidate.Take(ctx, "worktree", NoOwner, false, "")
			results <- err
		}(contender)
	}
	close(start)
	wait.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("contender error: %v", err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatalf("CAS contenders: success=%d conflict=%d", success, conflicts)
	}
}

func TestServiceBoundaryRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	actions := []struct {
		name string
		run  func(Service, string) error
	}{
		{"join", func(s Service, path string) error { _, err := s.Join(ctx, path); return err }},
		{"leave", func(s Service, path string) error { _, err := s.Leave(ctx, path); return err }},
		{"take", func(s Service, path string) error { _, err := s.Take(ctx, path, NoOwner, false, ""); return err }},
		{"rebind", func(s Service, path string) error { _, err := s.Rebind(ctx, path, "/retired"); return err }},
		{"transfer", func(s Service, path string) error { _, err := s.Transfer(ctx, path, "peer"); return err }},
		{"send", func(s Service, path string) error {
			_, _, err := s.Send(ctx, path, "key", []string{"peer"}, "body")
			return err
		}},
		{"inbox", func(s Service, path string) error { _, err := s.Inbox(ctx, path); return err }},
		{"ack", func(s Service, path string) error { _, err := s.Ack(ctx, path, "message"); return err }},
	}
	for _, action := range actions {
		t.Run(action.name+" unknown checkout", func(t *testing.T) {
			t.Parallel()
			service, _, _ := testService(t)
			if err := action.run(service, "missing"); err == nil || !strings.Contains(err.Error(), "unknown checkout") {
				t.Fatalf("resolution error = %v", err)
			}
		})
		t.Run(action.name+" caller failure", func(t *testing.T) {
			t.Parallel()
			service, _, _ := testService(t)
			service.Ports.Caller = func() (string, error) { return "", errors.New("caller failure") }
			if err := action.run(service, "worktree"); err == nil || !strings.Contains(err.Error(), "caller failure") {
				t.Fatalf("caller error = %v", err)
			}
		})
	}
	service, _, _ := testService(t)
	service.Ports.Resolve = nil
	if _, err := service.Inspect(ctx, "worktree"); err == nil || !strings.Contains(err.Error(), "boundaries") {
		t.Fatalf("missing boundary = %v", err)
	}
	service, _, _ = testService(t)
	if _, err := service.Inspect(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "unknown checkout") {
		t.Fatalf("inspect resolution error = %v", err)
	}
	service, _, _ = testService(t)
	service.Ports.Caller = func() (string, error) { return "", nil }
	if _, err := service.Join(ctx, "worktree"); err == nil || !strings.Contains(err.Error(), "registered") {
		t.Fatalf("invalid caller = %v", err)
	}
}

func TestServiceRebindRepairsRetiredCheckoutRoot(t *testing.T) {
	t.Parallel()
	service, _, _ := testService(t)
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	oldCheckout := testCheckout()
	oldCheckout.Root = filepath.Join(base, "retired")
	currentCheckout := oldCheckout
	currentCheckout.Root = filepath.Join(base, "current")
	if err := os.Mkdir(currentCheckout.Root, 0o755); err != nil {
		t.Fatal(err)
	}
	service.Ports.Resolve = func(context.Context, string) (Checkout, error) { return currentCheckout, nil }
	if _, err := service.Store.WithLocked(context.Background(), oldCheckout, func(state *State, found bool) error {
		if found {
			t.Fatal("unexpected existing coordination state")
		}
		return state.Take(TakeRequest{Caller: "owner", ExpectedOwner: NoOwner, At: testNow})
	}); err != nil {
		t.Fatal(err)
	}
	state, err := service.Rebind(context.Background(), "worktree", oldCheckout.Root)
	if err != nil {
		t.Fatal(err)
	}
	if state.Checkout != currentCheckout || state.Revision != 2 || len(state.CheckoutRebinds) != 1 {
		t.Fatalf("service rebind state = %+v", state)
	}
}

func TestServiceOperationSpecificFaults(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	service, caller, _ := testService(t)
	originalOpen := service.Store.Ports.OpenHome
	service.Store.Ports.OpenHome = func(string, bool) (*os.File, error) { return nil, errors.New("read-only state unavailable") }
	if _, err := service.Take(ctx, "worktree", NoOwner, false, ""); err == nil || !strings.Contains(err.Error(), "read-only state unavailable") {
		t.Fatalf("take read-only failure = %v", err)
	}
	if _, err := service.Inspect(ctx, "worktree"); err == nil || !strings.Contains(err.Error(), "read-only state unavailable") {
		t.Fatalf("inspect read-only failure = %v", err)
	}
	service.Store.Ports.OpenHome = originalOpen
	service.Ports.ObserveLegacy = func(Checkout) (ObservedOwner, error) { return ObservedOwner{}, errors.New("legacy unavailable") }
	service.Ports.ObserveLegacyForInspection = service.Ports.ObserveLegacy
	if _, err := service.Take(ctx, "worktree", NoOwner, false, ""); err == nil || !strings.Contains(err.Error(), "legacy unavailable") {
		t.Fatalf("legacy error = %v", err)
	}
	if _, err := service.Inspect(ctx, "worktree"); err == nil || !strings.Contains(err.Error(), "legacy unavailable") {
		t.Fatalf("legacy inspection error = %v", err)
	}
	service.Ports.ObserveLegacy = func(Checkout) (ObservedOwner, error) { return ObservedOwner{}, nil }
	service.Ports.ObserveLegacyForInspection = service.Ports.ObserveLegacy
	service.Ports.ObserveLegacy = func(Checkout) (ObservedOwner, error) { return ObservedOwner{}, errors.New("locked legacy unavailable") }
	if _, err := service.Take(ctx, "worktree", NoOwner, false, ""); err == nil || !strings.Contains(err.Error(), "locked legacy unavailable") {
		t.Fatalf("locked legacy failure = %v", err)
	}
	service.Ports.ObserveLegacy = func(Checkout) (ObservedOwner, error) { return ObservedOwner{}, nil }
	if _, err := service.Take(ctx, "worktree", NoOwner, false, ""); err != nil {
		t.Fatal(err)
	}
	service.Ports.Live = func(string) (bool, error) { return false, errors.New("liveness unavailable") }
	service.Ports.OwnerStatus = func(string) (string, error) { return "unknown", errors.New("owner liveness unavailable") }
	if _, err := service.Take(ctx, "worktree", "owner", false, ""); err == nil || !strings.Contains(err.Error(), "owner liveness unavailable") {
		t.Fatalf("owner liveness error = %v", err)
	}
	if _, err := service.Inspect(ctx, "worktree"); err == nil || !strings.Contains(err.Error(), "owner liveness unavailable") {
		t.Fatalf("inspected owner liveness error = %v", err)
	}
	service.Ports.OwnerStatus = func(string) (string, error) { return "live", nil }
	service.Ports.OwnerStatus = func(string) (string, error) { return "untrusted", nil }
	if _, err := service.Take(ctx, "worktree", "owner", false, ""); err == nil || !strings.Contains(err.Error(), "not corroborated") {
		t.Fatalf("invalid owner status = %v", err)
	}
	if _, err := service.Inspect(ctx, "worktree"); err == nil || !strings.Contains(err.Error(), "not corroborated") {
		t.Fatalf("invalid inspected owner status = %v", err)
	}
	service.Ports.OwnerStatus = func(string) (string, error) { return "live", nil }
	if _, err := service.Transfer(ctx, "worktree", "peer"); err == nil || !strings.Contains(err.Error(), "liveness unavailable") {
		t.Fatalf("successor liveness error = %v", err)
	}
	if _, err := service.Inspect(ctx, "worktree"); err == nil || !strings.Contains(err.Error(), "liveness unavailable") {
		t.Fatalf("participant liveness error = %v", err)
	}
	service.Ports.Live = func(string) (bool, error) { return false, nil }
	service.Ports.OwnerStatus = func(string) (string, error) { return "inactive", nil }
	view, err := service.Inspect(ctx, "worktree")
	if err != nil || view.OwnerStatus != "inactive" {
		t.Fatalf("inactive owner = %+v, %v", view, err)
	}
	*caller = "outsider"
	if _, err := service.Transfer(ctx, "worktree", "peer"); err == nil || !strings.Contains(err.Error(), "current owner") {
		t.Fatalf("unauthorized transfer = %v", err)
	}
	if _, err := service.Inbox(ctx, "worktree"); err == nil || !strings.Contains(err.Error(), "not joined") {
		t.Fatalf("outsider inbox = %v", err)
	}
	if _, err := service.Ack(ctx, "worktree", "missing"); err == nil || !strings.Contains(err.Error(), "not joined") {
		t.Fatalf("outsider ack = %v", err)
	}
	*caller = "owner"
	if _, err := service.Ack(ctx, "worktree", "missing"); err == nil || !strings.Contains(err.Error(), "not in recipient inbox") {
		t.Fatalf("missing ack = %v", err)
	}
	service.Ports.NewMessageID = func() (string, error) { return "", errors.New("ID unavailable") }
	if _, _, err := service.Send(ctx, "worktree", "key", []string{"owner"}, "text"); err == nil || !strings.Contains(err.Error(), "ID unavailable") {
		t.Fatalf("message ID failure = %v", err)
	}
}
