package lifecyclehooks

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestBackfillOptionsSkipFailuresAndSortStableExecutions(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var dispatched []Event
	deps := BackfillDependencies{
		Scan: func(root string) ([]discover.Repo, error) {
			if root != "root" {
				t.Fatal(root)
			}
			return []discover.Repo{{Org: "other", Name: "skip", Path: "skip"}, {Org: "ACME", Name: "bad-id", Path: "bad-id"}, {Org: "acme", Name: "bad-head", Path: "bad-head"}, {Org: "acme", Name: "ready", Path: "ready"}}, nil
		},
		Identity: func(path string) (string, error) {
			if path == "skip" {
				t.Fatal("filtered repository inspected")
			}
			if path == "bad-id" {
				return "", errors.New("identity failed")
			}
			return "github.com/acme/" + path, nil
		},
		Head: func(path string) (string, error) {
			if path == "bad-head" {
				return "", errors.New("head failed")
			}
			return "new-head", nil
		},
		Plan: func(events []Event) ([]Planned, Report, error) {
			if len(events) != 1 || events[0].Cause != "lifecycle-backfill" || events[0].Name != EventCheckoutUpdated || events[0].Checkout != "ready" || events[0].NewSHA != "new-head" {
				t.Fatal(events)
			}
			return []Planned{{Executor: "z", Event: Event{Repository: "z/repo"}}, {Executor: "z", Event: Event{Repository: "a/repo"}}, {Executor: "a", Event: Event{Repository: "a/repo"}}}, Report{}, nil
		},
		Dispatch: func(gotCtx context.Context, events []Event) (Report, error) {
			if gotCtx != ctx {
				t.Fatal("context changed")
			}
			dispatched = events
			return Report{Enqueued: 1}, nil
		},
	}
	plan, err := backfillWith(ctx, "root", "AcMe", true, deps)
	if err != nil || !plan.Apply || plan.Scanned != 4 || len(plan.Skipped) != 2 || plan.Enqueue.Enqueued != 1 || len(dispatched) != 1 {
		t.Fatalf("plan=%+v err=%v dispatched=%v", plan, err, dispatched)
	}
	var names []string
	for _, p := range plan.Executions {
		names = append(names, p.Event.Repository+"/"+p.Executor)
	}
	if !reflect.DeepEqual(names, []string{"a/repo/a", "a/repo/z", "z/repo/z"}) {
		t.Fatal(names)
	}
}
func TestBackfillScanPlanDispatchErrorsAndPreviewGating(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("operation failed")
	for _, mode := range []string{"scan", "plan", "dispatch", "preview", "empty"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			calls := 0
			deps := BackfillDependencies{Scan: func(string) ([]discover.Repo, error) {
				if mode == "scan" {
					return nil, sentinel
				}
				return nil, nil
			}, Plan: func([]Event) ([]Planned, Report, error) {
				if mode == "plan" {
					return nil, Report{}, sentinel
				}
				if mode == "empty" {
					return nil, Report{}, nil
				}
				return []Planned{{Executor: "actual"}}, Report{}, nil
			}, Dispatch: func(context.Context, []Event) (Report, error) { calls++; return Report{Enqueued: 1}, sentinel }}
			plan, err := backfillWith(context.Background(), "root", "", mode != "preview", deps)
			switch mode {
			case "scan", "plan", "dispatch":
				if !errors.Is(err, sentinel) {
					t.Fatal(err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode == "dispatch" {
				if calls != 1 || plan.Enqueue.Enqueued != 1 {
					t.Fatal(plan)
				}
			} else if calls != 0 {
				t.Fatal("dispatch occurred outside apply with executions")
			}
		})
	}
}
func TestBackfillPublicBoundaryReportsEmptySource(t *testing.T) {
	t.Parallel()
	plan, err := Backfill(context.Background(), t.TempDir(), "", Dispatcher{}, false)
	if err != nil || plan.Apply || plan.Scanned != 0 || len(plan.Executions) != 0 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}
