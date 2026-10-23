package orchestrate

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/githubobserver"
)

func TestAwaitLandablePullRequestDefaultPollCannotObserveSpentBudget(t *testing.T) {
	t.Parallel()
	for _, interval := range []time.Duration{0, -time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			t.Parallel()
			calls := 0
			ctx := githubobserver.WithReader(context.Background(), githubobserver.Reader{
				Execute: func(context.Context, string, ...string) githubobserver.CommandResponse {
					calls++
					t.Fatal("spent budget mutated provider")
					return githubobserver.CommandResponse{}
				},
				Get: func(context.Context, githubobserver.GetRequest) (githubobserver.Response, error) {
					calls++
					t.Fatal("spent budget observed provider")
					return githubobserver.Response{}, nil
				},
			})
			now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
			options := PullRequestLandOptions{Repository: "acme/app", NoAutoMerge: true, NoUpdateBranch: true, Slice: githubchecks.DefaultCheckPollInterval, CheckPollInterval: interval, Now: func() time.Time { return now }}
			var view githubchecks.PullRequestView
			view.Base.Ref = "main"
			view.Head.SHA = "pinned-head"
			evidence := map[string]string{"existing": "unchanged"}
			updated, waited, armed, merged, refusal, err := awaitLandablePullRequest(ctx, options, view, "7", "subject", "body", evidence)
			if err != nil || refusal != nil || armed || merged || calls != 0 || !reflect.DeepEqual(updated, view) {
				t.Fatalf("unexpected spent-budget result: view=%+v waited=%+v armed=%v merged=%v refusal=%+v err=%v calls=%d", updated, waited, armed, merged, refusal, err, calls)
			}
			if waited.Status != githubchecks.PullRequestWaitPending || waited.Repository != "acme/app" || waited.PullRequest != "7" || waited.Target != "main" || waited.Head != "pinned-head" || waited.Reason != "landing wait budget elapsed before checks settled; resume the same exact target identity in another foreground slice" {
				t.Fatalf("pending receipt = %+v", waited)
			}
			if !reflect.DeepEqual(evidence, map[string]string{"existing": "unchanged"}) {
				t.Fatalf("evidence changed: %v", evidence)
			}
		})
	}
}
