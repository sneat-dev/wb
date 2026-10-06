package lifecyclehooks

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/gitops"
)

type BackfillPlan struct {
	Apply      bool      `json:"apply"`
	Scanned    int       `json:"scanned"`
	Skipped    []string  `json:"skipped,omitempty"`
	Executions []Planned `json:"executions"`
	Enqueue    Report    `json:"enqueue"`
}

// BackfillDependencies are the concrete source and queue boundaries used by planning.
type BackfillDependencies struct {
	Scan     func(string) ([]discover.Repo, error)
	Identity func(string) (string, error)
	Head     func(string) (string, error)
	Plan     func([]Event) ([]Planned, Report, error)
	Dispatch func(context.Context, []Event) (Report, error)
}

// Backfill plans current canonical heads and optionally dispatches matching events.
func Backfill(ctx context.Context, root, filter string, dispatcher Dispatcher, apply bool) (BackfillPlan, error) {
	return backfillWith(ctx, root, filter, apply, BackfillDependencies{Scan: discover.ScanLocal, Identity: RepositoryIdentity, Head: gitops.HeadSHA, Plan: dispatcher.Plan, Dispatch: dispatcher.Dispatch})
}
func backfillWith(ctx context.Context, root, filter string, apply bool, deps BackfillDependencies) (BackfillPlan, error) {
	repositories, err := deps.Scan(root)
	if err != nil {
		return BackfillPlan{}, err
	}
	plan := BackfillPlan{Apply: apply, Scanned: len(repositories)}
	var events []Event
	for _, repository := range repositories {
		if filter != "" && !strings.Contains(strings.ToLower(repository.Slug()), strings.ToLower(filter)) {
			continue
		}
		identity, err := deps.Identity(repository.Path)
		if err != nil {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("identify %s: %v", repository.Path, err))
			continue
		}
		head, err := deps.Head(repository.Path)
		if err != nil {
			plan.Skipped = append(plan.Skipped, fmt.Sprintf("read HEAD for %s: %v", identity, err))
			continue
		}
		events = append(events, Event{Name: EventCheckoutUpdated, Repository: identity, Checkout: repository.Path, NewSHA: head, Cause: "lifecycle-backfill"})
	}
	plan.Executions, _, err = deps.Plan(events)
	if err != nil {
		return plan, err
	}
	sort.Slice(plan.Executions, func(i, j int) bool {
		left, right := plan.Executions[i], plan.Executions[j]
		if left.Event.Repository == right.Event.Repository {
			return left.Executor < right.Executor
		}
		return left.Event.Repository < right.Event.Repository
	})
	if apply && len(plan.Executions) != 0 {
		plan.Enqueue, err = deps.Dispatch(ctx, events)
	}
	return plan, err
}
